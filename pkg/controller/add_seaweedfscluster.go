package controller

import (
	"github.com/seaweedfs/seaweedfs-operator/pkg/controller/seaweedfscluster"
)

func init() {
	// AddToManagerFuncs is a list of functions to create controllers and add them to a manager.
	AddToManagerFuncs = append(AddToManagerFuncs, seaweedfscluster.Add)
}
