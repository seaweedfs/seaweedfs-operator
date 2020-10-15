package controllers

import ctrl "sigs.k8s.io/controller-runtime"

func ReconcileResult(err error) (bool, ctrl.Result, error) {
	if err != nil {
		return true, ctrl.Result{}, err
	}
	return false, ctrl.Result{}, nil
}
