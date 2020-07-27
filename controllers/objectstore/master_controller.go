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
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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

func (r *MasterReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("master", req.NamespacedName)

	// your logic here
	log.Info("start Reconcile ...")

	// fetch the master instance
	master := &objectstorev100.Master{}
	err := r.Get(ctx, req.NamespacedName, master)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			log.Info("Master resource not found. Ignoring since object must be deleted")
			return ctrl.Result{RequeueAfter: time.Second * 5}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get Master")
		return ctrl.Result{}, err
	}
	log.Info("Get master " + master.Name)

	// Check if the deployment already exists, if not create a new one
	found := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: master.Name, Namespace: master.Namespace}, found)
	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		dep := r.deploymentForMaster(master)
		log.Info("Creating a new Deployment", "Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
		err = r.Create(ctx, dep)
		if err != nil {
			log.Error(err, "Failed to create new Deployment", "Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
			return ctrl.Result{}, err
		}
		// Deployment created successfully - return and requeue
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get Deployment")
		return ctrl.Result{}, err
	}
	log.Info("Get deployment" + found.Name)

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

	return ctrl.Result{RequeueAfter: time.Second * 5}, nil
}

// deploymentForMaster returns a memcached Deployment object
func (r *MasterReconciler) deploymentForMaster(m *objectstorev100.Master) *appsv1.Deployment {
	ls := lablesForMaster(m.Name)
	replicas := int32(3)

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
					Containers: []corev1.Container{{
						Image:   "chrislusf/seaweedfs:latest",
						Name:    "master",
						Command: []string{"master", "-ip=xxxxx"},
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
	return map[string]string{"app": "seaweedfs", "memcached_cr": name}
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
		Owns(&appsv1.Deployment{}).
		Complete(r)
}
