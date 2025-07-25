package controller

import (
	"context"
	"io"
	"os"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/internal/controller/swadmin"
	ctrl "sigs.k8s.io/controller-runtime"
)

func (r *SeaweedReconciler) maintenance(ctx context.Context, m *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {

	masters := getMasterPeersString(m)

	r.Log.Debugw("wait to connect to masters", "masters", masters)

	// this step blocks since the operator can not access the masters when running from outside of the k8s cluster
	sa := swadmin.NewSeaweedAdmin(ctx, masters, io.Discard)

	// For now this is an example of the admin commands
	// master by default has some maintenance commands already.
	r.Log.Debugw("volume.list")
	sa.Output = os.Stdout
	if err := sa.ProcessCommand("volume.list"); err != nil {
		r.Log.Debugw("volume.list", "error", err)
	}

	sa.ProcessCommand("lock")
	if err := sa.ProcessCommand("volume.balance -force"); err != nil {
		r.Log.Debugw("volume.balance", "error", err)
	}
	sa.ProcessCommand("unlock")

	return ReconcileResult(nil)

}
