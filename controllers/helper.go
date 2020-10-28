package controllers

import (
	"fmt"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	masterPeerAddressPattern = "%s-master-%d.%s-master:9333"
)

func ReconcileResult(err error) (bool, ctrl.Result, error) {
	if err != nil {
		return true, ctrl.Result{}, err
	}
	return false, ctrl.Result{}, nil
}

func getMasterAddresses(name string, replicas int32) []string {
	peersAddresses := make([]string, 0, replicas)
	for i := int32(0); i < replicas; i++ {
		peersAddresses = append(peersAddresses, fmt.Sprintf(masterPeerAddressPattern, name, i, name))
	}
	return peersAddresses
}

func getMasterPeersString(name string, replicas int32) string {
	return strings.Join(getMasterAddresses(name, replicas), ",")
}
