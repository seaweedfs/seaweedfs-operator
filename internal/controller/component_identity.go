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
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// componentServiceAccountDeniedCondition records that a component's
// spec.serviceAccountName was refused, so the reason survives the next status
// refresh instead of only appearing as a transient Ready=False.
const componentServiceAccountDeniedCondition = "ServiceAccountDenied"

// componentServiceAccount pairs each component with the ServiceAccount its
// pods would run as.
type componentServiceAccount struct {
	component string
	name      string
}

// componentServiceAccounts lists every ServiceAccount the CR's components
// select. Volume topology groups carry their own ComponentSpec and do not
// inherit spec.volume's, so a retired flat spec.volume selects none.
func componentServiceAccounts(cr *seaweedv1.Seaweed) []componentServiceAccount {
	var selected []componentServiceAccount
	add := func(component string, accessor seaweedv1.ComponentAccessor) {
		if name := accessor.ServiceAccountName(); name != "" {
			selected = append(selected, componentServiceAccount{component: component, name: name})
		}
	}

	if cr.Spec.Master != nil {
		add(ComponentMaster, cr.BaseMasterSpec())
	}
	if cr.Spec.Volume != nil && !flatVolumeRetired(cr) {
		add(ComponentVolume, cr.BaseVolumeSpec())
	}
	// Sorted so the denial message is stable across reconciles.
	topologies := make([]string, 0, len(cr.Spec.VolumeTopology))
	for name := range cr.Spec.VolumeTopology {
		topologies = append(topologies, name)
	}
	sort.Strings(topologies)
	for _, name := range topologies {
		topology := cr.Spec.VolumeTopology[name]
		if topology == nil || topology.ServiceAccountName == nil || *topology.ServiceAccountName == "" {
			continue
		}
		selected = append(selected, componentServiceAccount{
			component: ComponentVolume + "/" + name,
			name:      *topology.ServiceAccountName,
		})
	}
	if cr.Spec.Filer != nil {
		add(ComponentFiler, cr.BaseFilerSpec())
	}
	if cr.Spec.Admin != nil {
		add(ComponentAdmin, cr.BaseAdminSpec())
	}
	if cr.Spec.Worker != nil {
		add(ComponentWorker, cr.BaseWorkerSpec())
	}
	if cr.Spec.S3 != nil {
		add("s3", cr.BaseS3Spec())
	}
	if cr.Spec.SFTP != nil {
		add("sftp", cr.BaseSFTPSpec())
	}
	if cr.Spec.Vector != nil {
		add(ComponentVector, cr.BaseVectorSpec())
	}
	return selected
}

// ensureComponentServiceAccounts refuses to render component pods under a
// ServiceAccount the platform did not allow. A namespace tenant can edit a
// Seaweed CR; without this gate they could pick any identity in the namespace —
// including one holding permissions the tenant does not have — and the
// component pods would run as it, with the cluster's TLS material mounted.
//
// The check runs before any workload is applied, so a denied identity never
// reaches a pod spec; it is repeated every reconcile because the ServiceAccount
// may be annotated or un-annotated later. It is the reconcile-time twin of the
// CRD-level rule, and holds where admission webhooks are disabled.
//
// It returns done=true when the reconcile must stop: either the identity was
// denied (status updated, requeue) or reading a ServiceAccount failed.
func (r *SeaweedReconciler) ensureComponentServiceAccounts(ctx context.Context, cr *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	if !cr.DeletionTimestamp.IsZero() {
		return false, ctrl.Result{}, nil
	}
	selected := componentServiceAccounts(cr)
	if len(selected) == 0 {
		return false, ctrl.Result{}, nil
	}

	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}

	var denied []string
	for _, entry := range selected {
		sa := &corev1.ServiceAccount{}
		if err := reader.Get(ctx, client.ObjectKey{Namespace: cr.Namespace, Name: entry.name}, sa); err != nil {
			if apierrors.IsNotFound(err) {
				denied = append(denied, fmt.Sprintf("%s: serviceAccountName %q does not exist in namespace %q", entry.component, entry.name, cr.Namespace))
				continue
			}
			return true, ctrl.Result{}, fmt.Errorf("get ServiceAccount %q for %s: %w", entry.name, entry.component, err)
		}
		if err := seaweedv1.ServiceAccountAllowed(sa); err != nil {
			denied = append(denied, fmt.Sprintf("%s: %v", entry.component, err))
		}
	}
	if len(denied) == 0 {
		return false, ctrl.Result{}, nil
	}

	message := "refusing to run components under a ServiceAccount that is not permitted: " + strings.Join(denied, "; ")
	if r.Recorder != nil {
		r.Recorder.Eventf(cr, corev1.EventTypeWarning, "ServiceAccountDenied", "%s", message)
	}
	meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
		Type:               componentServiceAccountDeniedCondition,
		Status:             metav1.ConditionTrue,
		Reason:             "ServiceAccountDenied",
		Message:            message,
		ObservedGeneration: cr.Generation,
	})
	meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "ServiceAccountDenied",
		Message:            message,
		ObservedGeneration: cr.Generation,
	})
	if err := r.Status().Update(ctx, cr); err != nil {
		// A conflict only means another writer touched the CR; the next
		// reconcile writes the same condition again.
		if apierrors.IsConflict(err) {
			return true, ctrl.Result{RequeueAfter: requeueWhileReconciling}, nil
		}
		return true, ctrl.Result{}, err
	}
	return true, ctrl.Result{RequeueAfter: requeueWhileReconciling}, nil
}
