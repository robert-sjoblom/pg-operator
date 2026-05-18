package controller

import (
	"fmt"
	"strings"

	dbv1alpha1 "github.com/robert-sjoblom/pg-operator/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

const (
	PostgresImage string = "postgres:18.4"
)

// This function only builds the Service, if it did SetControllerReference
// we'd have to return (*corev1.Service, error)
func Service(pg *dbv1alpha1.PostgresCluster) *corev1.Service {
	v := &corev1.Service{}
	v.Name = serviceName(pg)
	v.Namespace = pg.Namespace

	labels := labelsForPg(pg)
	v.Labels = labels
	v.Spec.Selector = labels
	v.Spec.ClusterIP = "None"
	v.Spec.Ports = []corev1.ServicePort{{Name: "postgres", Protocol: corev1.ProtocolTCP, Port: 5432, TargetPort: intstr.FromInt32(5432)}}

	return v
}

func Secret(pg *dbv1alpha1.PostgresCluster) *corev1.Secret {
	return &corev1.Secret{
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"POSTGRES_PASSWORD": "password",
		},
		ObjectMeta: metav1.ObjectMeta{
			// suffixing from the start to make kubectl get all more readable
			Name:      secretName(pg),
			Namespace: pg.Namespace,
			Labels:    labelsForPg(pg),
		},
	}
}

func InstancePVC(pg *dbv1alpha1.PostgresCluster, idx int) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName(pg, idx, "data"),
			Namespace: pg.Namespace,
			Labels:    labelsForPg(pg),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
			StorageClassName: nil, // default StorageClass = local-path
			VolumeMode:       nil, // defaults to FileSystem
		},
	}
}

func InstancePod(pg *dbv1alpha1.PostgresCluster, idx int, role string) *corev1.Pod {
	labels := labelsForPod(pg, role)

	env := []corev1.EnvVar{
		{Name: "PGDATA", Value: "/var/lib/postgresql/data/pgdata"},
		{Name: "POSTGRES_HOST_AUTH_METHOD", Value: "trust"},
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName(pg, idx),
			Namespace: pg.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			Hostname:  podName(pg, idx),
			Subdomain: serviceName(pg),
			Volumes: []corev1.Volume{
				{
					Name: "data",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: pvcName(pg, idx, "data"),
						},
					},
				},
				{
					Name: "init-scripts",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: configMapName(pg),
							},
							DefaultMode: ptr.To(int32(0o755)),
						},
					},
				}},
			InitContainers: initContainersFor(pg, role, env),
			Containers: []corev1.Container{{
				Name:  "postgres",
				Image: PostgresImage,
				Ports: []corev1.ContainerPort{{
					Name:          "postgres",
					ContainerPort: 5432,
					Protocol:      corev1.ProtocolTCP,
				}},
				EnvFrom: []corev1.EnvFromSource{
					{
						SecretRef: &corev1.SecretEnvSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: secretName(pg)},
						},
					},
				},
				Env: env,
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "data",
						MountPath: "/var/lib/postgresql/data",
					},
					{
						Name:      "init-scripts",
						MountPath: "/docker-entrypoint-initdb.d",
						ReadOnly:  true,
					},
				},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						Exec: &corev1.ExecAction{Command: []string{"pg_isready", "-U", "postgres"}},
					},
					InitialDelaySeconds: 5,
					PeriodSeconds:       5,
				},
			}},
		},
	}
}

func BootstrapConfigMap(pg *dbv1alpha1.PostgresCluster) *corev1.ConfigMap {
	names := make([]string, 0, pg.Spec.Replicas)

	for i := 1; i <= int(pg.Spec.Replicas); i++ {
		// names might contain -
		names = append(names, fmt.Sprintf(`\"%s\"`, podName(pg, i)))
	}

	syncRepScript := fmt.Sprintf(
		`echo "synchronous_standby_names = 'ANY 1 (%s)'" >> "$PGDATA/postgresql.auto.conf"`, strings.Join(names, ", "),
	)

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName(pg),
			Namespace: pg.Namespace,
			Labels:    labelsForPg(pg),
		},
		Data: map[string]string{
			"00-replication-hba.sh": `echo "host replication all all trust" >> "$PGDATA/pg_hba.conf"`,
			"01-sync-rep.sh":        syncRepScript,
		},
	}
}

func initContainersFor(pg *dbv1alpha1.PostgresCluster, role string, env []corev1.EnvVar) []corev1.Container {
	var initContainers []corev1.Container
	if role == "replica" {
		// TODO: this assumes that idx 0 is always the primary. This is not true
		// after a failover. We'll deal with that later.

		primaryHost := hostName(pg, 0)
		script := fmt.Sprintf(`
	set -e
	until pg_isready -h %s -U postgres; do
    echo "waiting for primary..."
    sleep 2
	done
	if [ ! -s "$PGDATA/PG_VERSION" ]; then
	pg_basebackup -d "host=%s user=postgres application_name=$HOSTNAME" \
	-D "$PGDATA" -X stream -R -P
	fi
	`, primaryHost, primaryHost)

		initContainers = append(initContainers, corev1.Container{
			Name:    "bootstrap",
			Image:   PostgresImage,
			Command: []string{"sh", "-c", script},
			Env:     env,
			VolumeMounts: []corev1.VolumeMount{{
				Name:      "data",
				MountPath: "/var/lib/postgresql/data",
			}},
		})
	}

	return initContainers
}

func labelsForPg(pg *dbv1alpha1.PostgresCluster) map[string]string {
	return map[string]string{"app.kubernetes.io/instance": pg.Name, "app.kubernetes.io/managed-by": "pg-operator"}
}

func labelsForPod(pg *dbv1alpha1.PostgresCluster, role string) map[string]string {
	labels := labelsForPg(pg)
	labels["role"] = role
	return labels
}

func secretName(pg *dbv1alpha1.PostgresCluster) string {
	return fmt.Sprintf("%s-creds", pg.Name)
}

func serviceName(pg *dbv1alpha1.PostgresCluster) string {
	return fmt.Sprintf("%s-svc", pg.Name)
}

func podName(pg *dbv1alpha1.PostgresCluster, idx int) string {
	return fmt.Sprintf("%s-%d", pg.Name, idx)
}

func pvcName(pg *dbv1alpha1.PostgresCluster, idx int, prefix string) string {
	return fmt.Sprintf("%s-%s-%d", prefix, pg.Name, idx)
}

func hostName(pg *dbv1alpha1.PostgresCluster, idx int) string {
	return fmt.Sprintf("%s.%s", podName(pg, idx), serviceName(pg))
}

func configMapName(pg *dbv1alpha1.PostgresCluster) string {
	return fmt.Sprintf("%s-init-scripts", pg.Name)
}
