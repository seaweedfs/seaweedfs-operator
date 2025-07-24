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

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// SeaweedReconciler reconciles a Seaweed object
type SeaweedReconciler struct {
	client.Client
	Log    *zap.SugaredLogger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=seaweeds,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=seaweeds/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=extensions,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;watch
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=get;list;watch;create;update;patch;delete

// Reconcile implements the reconciliation logic
func (r *SeaweedReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.With("seaweed", req.NamespacedName)

	log.Debug("start Reconcile ...")

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

	if seaweedCR.Spec.FilerBackup != nil {
		if done, result, err = r.ensureFilerBackupServers(ctx, seaweedCR); done {
			return result, err
		}
	}

	if seaweedCR.Spec.Admin != nil {
		if done, result, err = r.ensureAdminServers(seaweedCR); done {
			return result, err
		}
	}

	if done, result, err = r.ensureSeaweedIngress(seaweedCR); done {
		return result, err
	}

	if false {
		if done, result, err = r.maintenance(ctx, seaweedCR); done {
			return result, err
		}
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *SeaweedReconciler) findSeaweedCustomResourceInstance(ctx context.Context, log *zap.SugaredLogger, req ctrl.Request) (*seaweedv1.Seaweed, bool, ctrl.Result, error) {
	// fetch the master instance
	seaweedCR := &seaweedv1.Seaweed{}
	err := r.Get(ctx, req.NamespacedName, seaweedCR)

	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			log.Info("seaweed CR not found. ignoring since object must be deleted")
			return nil, true, ctrl.Result{RequeueAfter: time.Second * 5}, nil
		}
		// Error reading the object - requeue the request.
		log.Errorw("failed to get SeaweedCR", "error", err)
		return nil, true, ctrl.Result{}, err
	}

	log.Debug("get master " + seaweedCR.Name)
	return seaweedCR, false, ctrl.Result{}, nil
}

func (r *SeaweedReconciler) getSecret(ctx context.Context, secretName string, namespace string) (map[string]string, error) {
	log := r.Log.With("get-secret", secretName)

	secret := &corev1.Secret{}
	err := r.Get(ctx, client.ObjectKey{
		Namespace: namespace,
		Name:      secretName,
	}, secret)

	if err != nil {
		log.Errorw("failed to get secret", "secret", secretName, "error", err)
		return nil, err
	}

	decodedData := make(map[string]string)

	for key, value := range secret.Data {
		decodedData[key] = string(value)
	}

	return decodedData, nil
}

func (r *SeaweedReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&seaweedv1.Seaweed{}).
		Complete(r)
}
