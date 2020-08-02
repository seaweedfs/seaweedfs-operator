package controllers

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"

	objectstorev100 "github.com/seaweedfs/seaweedfs-operator/apis/objectstore/v100"
)

func (r *MasterReconciler) findMasterCustomResourceInstance(ctx context.Context, log logr.Logger, req ctrl.Request) (*objectstorev100.Master, bool, ctrl.Result, error) {
	// fetch the master instance
	master := &objectstorev100.Master{}
	err := r.Get(ctx, req.NamespacedName, master)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			log.Info("Master resource not found. Ignoring since object must be deleted")
			return nil, true, ctrl.Result{RequeueAfter: time.Second * 5}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get Master")
		return nil, true, ctrl.Result{}, err
	}
	log.Info("Get master " + master.Name)
	return master, false, ctrl.Result{}, nil
}

func (r *MasterReconciler) ensureMasterStatefulSet(ctx context.Context, log logr.Logger, master *objectstorev100.Master) (bool, ctrl.Result, error) {

	// fetch the master instance
	masterCluster := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: master.Name, Namespace: master.Namespace}, masterCluster)
	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		dep := r.createMasterStatefulSet(master)
		log.Info("Creating a new master cluster statefulset", "Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
		err = r.Create(ctx, dep)
		if err != nil {
			log.Error(err, "Failed to create new Deployment", "Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
			return true, ctrl.Result{}, err
		}
		// Deployment created successfully - return and requeue
		return true, ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get Deployment")
		return true, ctrl.Result{}, err
	}
	log.Info("Get master cluster " + masterCluster.Name)
	return false, ctrl.Result{}, nil
}

// deploymentForMaster returns a memcached Deployment object
func (r *MasterReconciler) createMasterStatefulSet(m *objectstorev100.Master) *appsv1.StatefulSet {
	labels := lablesForMaster(m.Name)
	replicas := int32(3)
	enableServiceLinks := false

	dep := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Volumes:        nil,
					InitContainers: nil,
					Containers: []corev1.Container{{
						Image: "chrislusf/seaweedfs:latest",
						Name:  "master",
						Env: []corev1.EnvVar{
							{
								Name: "POD_IP",
								ValueFrom: &corev1.EnvVarSource{
									FieldRef: &corev1.ObjectFieldSelector{
										FieldPath: "status.podIP",
									},
								},
							},
							{
								Name: "POD_NAME",
								ValueFrom: &corev1.EnvVarSource{
									FieldRef: &corev1.ObjectFieldSelector{
										FieldPath: "metadata.name",
									},
								},
							},
							{
								Name: "NAMESPACE",
								ValueFrom: &corev1.EnvVarSource{
									FieldRef: &corev1.ObjectFieldSelector{
										FieldPath: "metadata.namespace",
									},
								},
							},
						},
						Command: []string{"weed", "master", "-volumeSizeLimitMB=1000", "-ip=${POD_NAME}"},
						Ports: []corev1.ContainerPort{
							{
								ContainerPort: 9333,
								Name:          "swfs-master",
							},
							{
								ContainerPort: 19333,
							},
						},
					}},
					EnableServiceLinks:            &enableServiceLinks,
				},
			},
		},
	}
	// Set master instance as the owner and controller
	// ctrl.SetControllerReference(m, dep, r.Scheme)
	return dep
}

func (r *MasterReconciler) ensureMasterService(ctx context.Context, log logr.Logger, master *objectstorev100.Master) (bool, ctrl.Result, error) {

	// fetch the master instance
	masterService := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: master.Name, Namespace: master.Namespace}, masterService)
	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		dep := r.createMasterService(master)
		log.Info("Creating a new master cluster statefulset", "Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
		err = r.Create(ctx, dep)
		if err != nil {
			log.Error(err, "Failed to create new Deployment", "Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
			return true, ctrl.Result{}, err
		}
		// Deployment created successfully - return and requeue
		return true, ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get Deployment")
		return true, ctrl.Result{}, err
	}
	log.Info("Get master cluster " + masterService.Name)
	return false, ctrl.Result{}, nil
}

// deploymentForMaster returns a memcached Deployment object
func (r *MasterReconciler) createMasterService(m *objectstorev100.Master) *corev1.Service {
	labels := lablesForMaster(m.Name)

	dep := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Ports: []corev1.ServicePort{
				{
					Name:     "swfs-master",
					Protocol: corev1.Protocol("TCP"),
					Port:     9333,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 9333,
					},
				},
				{
					Name:     "swfs-master-grpc",
					Protocol: corev1.Protocol("TCP"),
					Port:     19333,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 19333,
					},
				},
			},
			Selector: labels,
		},
	}
	// Set master instance as the owner and controller
	// ctrl.SetControllerReference(m, dep, r.Scheme)
	return dep
}
