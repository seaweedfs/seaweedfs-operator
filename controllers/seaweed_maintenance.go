package controllers

import (
	"io/ioutil"
	"os"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/controllers/swadmin"
	ctrl "sigs.k8s.io/controller-runtime"
)

func (r *SeaweedReconciler) maintenance(m *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {

	masters := getMasterPeersString(m.Name, m.Spec.Master.Replicas)

	r.Log.V(0).Info("wait to connect to masters", "masters", masters)

	return ReconcileResult(nil)

	// this step blocks since the operator can not access the masters when running from outside of the k8s cluster
	sa := swadmin.NewSeaweedAdmin(masters, ioutil.Discard)

	// For now this is an example of the admin commands
	// master by default has some maintenance commands already.
	r.Log.V(0).Info("volume.list")
	sa.Output = os.Stdout
	if err := sa.ProcessCommand("volume.list"); err != nil {
		r.Log.V(0).Info("volume.list", "error", err)
	}

	sa.ProcessCommand("lock")
	if err := sa.ProcessCommand("volume.balance -force"); err != nil {
		r.Log.V(0).Info("volume.balance", "error", err)
	}
	sa.ProcessCommand("unlock")

	return ReconcileResult(nil)

}
