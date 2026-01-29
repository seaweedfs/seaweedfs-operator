/*


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

package controller

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// SeaweedReconciler reconciles a Seaweed object
type SeaweedReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=seaweeds,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=seaweeds/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=extensions,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;

// Reconcile implements the reconciliation logic
func (r *SeaweedReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("seaweed", req.NamespacedName)

	log.Info("start Reconcile ...")

	seaweedCR, done, result, err := r.findSeaweedCustomResourceInstance(ctx, log, req)
	if done {
		return result, err
	}

	if done, result, err = r.ensureMaster(seaweedCR); done {
		return result, err
	}

	if done, result, err = r.ensureVolumeServers(seaweedCR); done {
		return result, err
	}

	if seaweedCR.Spec.Filer != nil {
		if done, result, err = r.ensureFilerServers(seaweedCR); done {
			return result, err
		}
	}

	// Note: Standalone IAM has been removed. IAM is now embedded in S3 by default.
	// Use filer.s3.enabled=true to enable S3 with embedded IAM.

	if done, result, err = r.ensureSeaweedIngress(seaweedCR); done {
		return result, err
	}

	if false {
		if done, result, err = r.maintenance(seaweedCR); done {
			return result, err
		}
	}

	// Update status
	if err := r.updateStatus(ctx, seaweedCR); err != nil {
		log.Error(err, "Failed to update Seaweed status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *SeaweedReconciler) findSeaweedCustomResourceInstance(ctx context.Context, log logr.Logger, req ctrl.Request) (*seaweedv1.Seaweed, bool, ctrl.Result, error) {
	// fetch the master instance
	seaweedCR := &seaweedv1.Seaweed{}
	err := r.Get(ctx, req.NamespacedName, seaweedCR)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			log.Info("Seaweed CR not found. Ignoring since object must be deleted")
			return nil, true, ctrl.Result{RequeueAfter: time.Second * 5}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get SeaweedCR")
		return nil, true, ctrl.Result{}, err
	}
	log.Info("Get master " + seaweedCR.Name)
	return seaweedCR, false, ctrl.Result{}, nil
}

func (r *SeaweedReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&seaweedv1.Seaweed{}).
		Complete(r)
}

func (r *SeaweedReconciler) updateStatus(ctx context.Context, seaweedCR *seaweedv1.Seaweed) error {
	log := r.Log.WithValues("seaweed", seaweedCR.Name)

	// Get master statefulset status
	masterStatus, err := r.getComponentStatus(ctx, seaweedCR, "master")
	if err != nil {
		log.Error(err, "Failed to get master status")
		return err
	}

	// Get volume statefulset status
	volumeStatus, err := r.getComponentStatus(ctx, seaweedCR, "volume")
	if err != nil {
		log.Error(err, "Failed to get volume status")
		return err
	}

	// Get filer statefulset status (if enabled)
	var filerStatus seaweedv1.ComponentStatus
	if seaweedCR.Spec.Filer != nil {
		filerStatus, err = r.getComponentStatus(ctx, seaweedCR, "filer")
		if err != nil {
			log.Error(err, "Failed to get filer status")
			return err
		}
	}

	// Determine if cluster is ready
	isReady := masterStatus.ReadyReplicas == masterStatus.Replicas &&
		volumeStatus.ReadyReplicas == volumeStatus.Replicas &&
		masterStatus.Replicas > 0 && volumeStatus.Replicas > 0

	if seaweedCR.Spec.Filer != nil {
		isReady = isReady && filerStatus.ReadyReplicas == filerStatus.Replicas && filerStatus.Replicas > 0
	}

	// Update status
	seaweedCR.Status.Master = masterStatus
	seaweedCR.Status.Volume = volumeStatus
	if seaweedCR.Spec.Filer != nil {
		seaweedCR.Status.Filer = filerStatus
	} else {
		// Clear stale filer status when filer is disabled
		seaweedCR.Status.Filer = seaweedv1.ComponentStatus{}
	}

	// Update conditions
	readyCondition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		ObservedGeneration: seaweedCR.Generation,
		Reason:             "NotReady",
		Message:            "Seaweed cluster is not ready",
	}

	if isReady {
		readyCondition.Status = metav1.ConditionTrue
		readyCondition.Reason = "Ready"
		readyCondition.Message = "Seaweed cluster is ready"
	}

	// Use idiomatic Kubernetes helper to manage conditions
	meta.SetStatusCondition(&seaweedCR.Status.Conditions, readyCondition)

	// Update the status
	if err := r.Status().Update(ctx, seaweedCR); err != nil {
		return err
	}

	log.Info("Updated Seaweed status", "ready", isReady)
	return nil
}

func (r *SeaweedReconciler) getComponentStatus(ctx context.Context, seaweedCR *seaweedv1.Seaweed, component string) (seaweedv1.ComponentStatus, error) {
	var status seaweedv1.ComponentStatus
	var labels map[string]string
	var desiredReplicas int32

	switch component {
	case "master":
		labels = labelsForMaster(seaweedCR.Name)
		desiredReplicas = seaweedCR.Spec.Master.Replicas
	case "volume":
		labels = labelsForVolumeServer(seaweedCR.Name)
		if seaweedCR.Spec.Volume != nil {
			desiredReplicas = seaweedCR.Spec.Volume.Replicas
		}
	case "filer":
		labels = labelsForFiler(seaweedCR.Name)
		if seaweedCR.Spec.Filer != nil {
			desiredReplicas = seaweedCR.Spec.Filer.Replicas
		}
	}

	status.Replicas = desiredReplicas

	// Get pods for this component
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(seaweedCR.Namespace),
		client.MatchingLabels(labels),
	}
	if err := r.List(ctx, podList, listOpts...); err != nil {
		return status, err
	}

	// Count ready pods by checking the Ready condition
	readyCount := int32(0)
	for _, pod := range podList.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				readyCount++
				break
			}
		}
	}

	status.ReadyReplicas = readyCount
	return status, nil
}
