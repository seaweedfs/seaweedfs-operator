package controllers

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime"

	"github.com/seaweedfs/seaweedfs-operator/apis/objectstore/v100"
)

func (r *MasterReconciler) findMasterInstance(req controllerruntime.Request, ctx context.Context, log logr.Logger) (*v100.Master, bool, controllerruntime.Result, error) {
	// fetch the master instance
	master := &v100.Master{}
	err := r.Get(ctx, req.NamespacedName, master)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			log.Info("Master resource not found. Ignoring since object must be deleted")
			return nil, true, controllerruntime.Result{RequeueAfter: time.Second * 5}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get Master")
		return nil, true, controllerruntime.Result{}, err
	}
	log.Info("Get master " + master.Name)
	return master, false, controllerruntime.Result{}, nil
}
