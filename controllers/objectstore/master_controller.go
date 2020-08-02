/*
Copyright 2020 SeaweedFS.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	objectstorev100 "github.com/seaweedfs/seaweedfs-operator/apis/objectstore/v100"
)

// MasterReconciler reconciles a Master object
type MasterReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=objectstore.seaweedfs.com,resources=masters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=objectstore.seaweedfs.com,resources=masters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;
func (r *MasterReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("master", req.NamespacedName)

	// your logic here
	log.Info("start Reconcile ...")

	master, done, result, err := r.findMasterCustomResourceInstance(ctx, log, req)
	if done {
		return result, err
	}

	if done, result, err = r.ensureMasterStatefulSet(ctx, log, master); done {
		return result, err
	}
	if done, result, err = r.ensureMasterService(ctx, log, master); done {
		return result, err
	}

	// Update the Memcached status with the pod names
	// List the pods for this memcached's deployment
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(master.Namespace),
		client.MatchingLabels(lablesForMaster(master.Name)),
	}
	if err = r.List(ctx, podList, listOpts...); err != nil {
		log.Error(err, "Failed to list pods", "Memcached.Namespace", master.Namespace, "Memcached.Name", master.Name)
		return ctrl.Result{}, err
	}

	log.Info("pods", "count", len(podList.Items))

	for _, pod := range podList.Items {
		log.Info("pod", "name", pod.Name, "podIP", pod.Status.PodIP)
	}

	return ctrl.Result{RequeueAfter: time.Second * 5}, nil
}

// deploymentForMaster returns a memcached Deployment object
func (r *MasterReconciler) deploymentForMaster(m *objectstorev100.Master) *appsv1.Deployment {
	ls := lablesForMaster(m.Name)
	replicas := int32(0)

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: ls,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: ls,
				},
				Spec: corev1.PodSpec{
					Hostname: "mastername",
					Containers: []corev1.Container{{
						Image:   "chrislusf/seaweedfs:latest",
						Name:    "master",
						Command: []string{"weed", "master"},
						Ports: []corev1.ContainerPort{{
							ContainerPort: 9333,
							Name:          "master",
						}},
					}},
				},
			},
		},
	}
	// Set Memcached instance as the owner and controller
	ctrl.SetControllerReference(m, dep, r.Scheme)
	return dep
}

// lablesForMaster returns the labels for selecting the resources
// belonging to the given memcached CR name.
func lablesForMaster(name string) map[string]string {
	return map[string]string{"app": "seaweedfs", "role": "master", "name": name}
}

// getPodNames returns the pod names of the array of pods passed in
func getPodNames(pods []corev1.Pod) []string {
	var podNames []string
	for _, pod := range pods {
		podNames = append(podNames, pod.Name)
	}
	return podNames
}

func (r *MasterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&objectstorev100.Master{}).
		Complete(r)
}
