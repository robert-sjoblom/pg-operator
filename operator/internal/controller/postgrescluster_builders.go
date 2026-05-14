package controller

import (
	"fmt"

	dbv1alpha1 "github.com/robert-sjoblom/pg-operator/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// This function only builds the Service, if it did SetControllerReference
// we'd have to return (*corev1.Service, error)
func Service(pg *dbv1alpha1.PostgresCluster) *corev1.Service {
	v := &corev1.Service{}
	v.Name = fmt.Sprintf("%s-svc", pg.Name)
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
			Name:      fmt.Sprintf("%s-creds", pg.Name),
			Namespace: pg.Namespace,
			Labels:    labelsForPg(pg),
		},
	}
}

func labelsForPg(pg *dbv1alpha1.PostgresCluster) map[string]string {
	return map[string]string{"app.kubernetes.io/instance": pg.Name, "app.kubernetes.io/managed-by": "pg-operator"}

}

/*
apiVersion: v1
kind: Service
metadata:
  name: minimal-cr # this is the same name as our cr's
  namespace: default # always the same as the CR's
  labels:
  	app.kubernetes.io/instance: minimal-cr
  	app.kubernetes.io/managed-by: pg-operator
spec:
  selector:
	app.kubernetes.io/instance: minimal-cr
	app.kubernetes.io/managed-by: pg-operator
  clusterIP: None
  ports:
    - protocol: TCP
      port: 5432
      targetPort: 5432
	  name: postgres
*/
