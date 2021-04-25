package controllers

import (
	"context"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/controllers/label"
)

func (r *SeaweedReconciler) ensureMaster(seaweedCR *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {
	_ = context.Background()
	_ = r.Log.WithValues("seaweed", seaweedCR.Name)

	if done, result, err = r.ensureMasterPeerService(seaweedCR); done {
		return
	}

	if done, result, err = r.ensureMasterService(seaweedCR); done {
		return
	}

	if done, result, err = r.ensureMasterConfigMap(seaweedCR); done {
		return
	}

	if done, result, err = r.ensureMasterStatefulSet(seaweedCR); done {
		return
	}

	if seaweedCR.Spec.Master.ConcurrentStart == nil || !*seaweedCR.Spec.Master.ConcurrentStart {
		if done, result, err = r.waitForMasterStatefulSet(seaweedCR); done {
			return
		}
	}

	return
}

func (r *SeaweedReconciler) waitForMasterStatefulSet(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-master-statefulset", seaweedCR.Name)

	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(seaweedCR.Namespace),
		client.MatchingLabels(labelsForMaster(seaweedCR.Name)),
	}
	if err := r.List(context.Background(), podList, listOpts...); err != nil {
		log.Error(err, "Failed to list master pods", "namespace", seaweedCR.Namespace, "name", seaweedCR.Name)
		return true, ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}

	log.Info("pods", "count", len(podList.Items))
	runningCounter := 0
	for _, pod := range podList.Items {
		if pod.Status.Phase == corev1.PodRunning {
			for _, containerStatus := range pod.Status.ContainerStatuses {
				if containerStatus.Ready {
					runningCounter++
				}
				log.Info("pod", "name", pod.Name, "containerStatus", containerStatus)
			}
		} else {
			log.Info("pod", "name", pod.Name, "status", pod.Status)
		}
	}

	if runningCounter < int(seaweedCR.Spec.Master.Replicas)/2+1 {
		log.Info("some masters are not ready", "missing", int(seaweedCR.Spec.Master.Replicas)-runningCounter)
		return true, ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}

	log.Info("masters are ready")
	return ReconcileResult(nil)

}

func (r *SeaweedReconciler) ensureMasterStatefulSet(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-master-statefulset", seaweedCR.Name)

	masterStatefulSet := r.createMasterStatefulSet(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, masterStatefulSet, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdate(masterStatefulSet, func(existing, desired runtime.Object) error {
		existingStatefulSet := existing.(*appsv1.StatefulSet)
		desiredStatefulSet := desired.(*appsv1.StatefulSet)

		existingStatefulSet.Spec.Template.Spec = desiredStatefulSet.Spec.Template.Spec
		return nil
	})
	log.Info("ensure master stateful set " + masterStatefulSet.Name)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureMasterConfigMap(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-master-configmap", seaweedCR.Name)

	masterConfigMap := r.createMasterConfigMap(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, masterConfigMap, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateConfigMap(masterConfigMap)

	log.Info("Get master ConfigMap " + masterConfigMap.Name)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureMasterService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-master-service", seaweedCR.Name)

	masterService := r.createMasterService(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, masterService, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateService(masterService)

	log.Info("Get master service " + masterService.Name)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureMasterPeerService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-master-peer-service", seaweedCR.Name)

	masterPeerService := r.createMasterPeerService(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, masterPeerService, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateService(masterPeerService)

	log.Info("Get master peer service " + masterPeerService.Name)
	return ReconcileResult(err)

}

func labelsForMaster(name string) map[string]string {
	return map[string]string{
		label.ManagedByLabelKey: "seaweedfs-operator",
		label.NameLabelKey:      "seaweedfs",
		label.ComponentLabelKey: "master",
		label.InstanceLabelKey:  name,
	}
}
