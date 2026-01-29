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
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

const (
	ComponentMaster = "master"
	ComponentVolume = "volume"
	ComponentFiler  = "filer"
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
	masterStatus, err := r.getComponentStatus(ctx, seaweedCR, ComponentMaster)
	if err != nil {
		log.Error(err, "Failed to get master status")
		return err
	}

	// Get volume statefulset status
	volumeStatus, err := r.getComponentStatus(ctx, seaweedCR, ComponentVolume)
	if err != nil {
		log.Error(err, "Failed to get volume status")
		return err
	}

	// Get filer statefulset status (if enabled)
	var filerStatus seaweedv1.ComponentStatus
	if seaweedCR.Spec.Filer != nil {
		filerStatus, err = r.getComponentStatus(ctx, seaweedCR, ComponentFiler)
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

	// Build informative status message
	parts := []string{
		fmt.Sprintf("Master: %d/%d ready", masterStatus.ReadyReplicas, masterStatus.Replicas),
		fmt.Sprintf("Volume: %d/%d ready", volumeStatus.ReadyReplicas, volumeStatus.Replicas),
	}
	if seaweedCR.Spec.Filer != nil {
		parts = append(parts, fmt.Sprintf("Filer: %d/%d ready", filerStatus.ReadyReplicas, filerStatus.Replicas))
	}
	readyMessage := strings.Join(parts, ", ")

	// Update conditions
	readyCondition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		ObservedGeneration: seaweedCR.Generation,
		Reason:             "NotReady",
		Message:            readyMessage,
	}

	if isReady {
		readyCondition.Status = metav1.ConditionTrue
		readyCondition.Reason = "Ready"
		readyCondition.Message = readyMessage
	}

	// Use idiomatic Kubernetes helper to manage conditions
	meta.SetStatusCondition(&seaweedCR.Status.Conditions, readyCondition)

	// Update the status, handling conflicts gracefully
	if err := r.Status().Update(ctx, seaweedCR); err != nil {
		// Handle conflicts gracefully: they often occur due to concurrent status updates.
		if errors.IsConflict(err) {
			log.V(2).Info("Conflict while updating Seaweed status; will retry on next reconciliation")
			// Do not treat conflict as a hard error to avoid unnecessary requeues.
			return nil
		}
		return err
	}

	log.Info("Updated Seaweed status", "ready", isReady)
	return nil
}

func (r *SeaweedReconciler) getComponentStatus(ctx context.Context, seaweedCR *seaweedv1.Seaweed, component string) (seaweedv1.ComponentStatus, error) {
	switch component {
	case ComponentMaster:
		if seaweedCR.Spec.Master != nil {
			return r.getStatefulSetStatus(ctx, seaweedCR.Namespace, seaweedCR.Name+"-master", seaweedCR.Spec.Master.Replicas)
		}
	case ComponentVolume:
		return r.getVolumeStatus(ctx, seaweedCR)
	case ComponentFiler:
		if seaweedCR.Spec.Filer != nil {
			return r.getStatefulSetStatus(ctx, seaweedCR.Namespace, seaweedCR.Name+"-filer", seaweedCR.Spec.Filer.Replicas)
		}
	}
	return seaweedv1.ComponentStatus{}, nil
}

func (r *SeaweedReconciler) getStatefulSetStatus(ctx context.Context, namespace, name string, desiredReplicas int32) (seaweedv1.ComponentStatus, error) {
	status := seaweedv1.ComponentStatus{
		Replicas: desiredReplicas,
	}

	// Get the StatefulSet
	statefulSet := &appsv1.StatefulSet{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, statefulSet); err != nil {
		if errors.IsNotFound(err) {
			// StatefulSet not yet created
			return status, nil
		}
		return status, err
	}

	// Use StatefulSet's ready replica count
	status.ReadyReplicas = statefulSet.Status.ReadyReplicas
	return status, nil
}

func (r *SeaweedReconciler) getVolumeStatus(ctx context.Context, seaweedCR *seaweedv1.Seaweed) (seaweedv1.ComponentStatus, error) {
	status := seaweedv1.ComponentStatus{}

	// Aggregate volume status from base spec and topology groups
	totalDesiredReplicas := int32(0)
	totalReadyReplicas := int32(0)

	// Check base volume spec
	if seaweedCR.Spec.Volume != nil {
		baseStatus, err := r.getStatefulSetStatus(ctx, seaweedCR.Namespace, seaweedCR.Name+"-volume", seaweedCR.Spec.Volume.Replicas)
		if err != nil {
			return status, err
		}
		totalDesiredReplicas += baseStatus.Replicas
		totalReadyReplicas += baseStatus.ReadyReplicas
	}

	// Check volume topology groups
	for topologyName, topologySpec := range seaweedCR.Spec.VolumeTopology {
		if topologySpec == nil {
			continue
		}
		statefulSetName := fmt.Sprintf("%s-volume-%s", seaweedCR.Name, topologyName)
		topologyStatus, err := r.getStatefulSetStatus(ctx, seaweedCR.Namespace, statefulSetName, topologySpec.Replicas)
		if err != nil {
			return status, err
		}
		totalDesiredReplicas += topologyStatus.Replicas
		totalReadyReplicas += topologyStatus.ReadyReplicas
	}

	status.Replicas = totalDesiredReplicas
	status.ReadyReplicas = totalReadyReplicas
	return status, nil
}
