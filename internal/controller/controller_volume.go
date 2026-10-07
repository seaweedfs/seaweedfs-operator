package controller

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	monitorv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	label "github.com/seaweedfs/seaweedfs-operator/internal/controller/label"
)

func (r *SeaweedReconciler) ensureVolumeServers(ctx context.Context, seaweedCR *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {
	_ = r.Log.WithValues("seaweed", seaweedCR.Name)

	// Check if using topology-aware volume deployment
	if len(seaweedCR.Spec.VolumeTopology) > 0 {
		if done, result, err = r.ensureVolumeServersWithTopology(ctx, seaweedCR); done {
			return
		}
		// Topology groups take over volume duty entirely; retire a flat
		// <name>-volume workload left over from before the migration so it
		// does not keep running untracked.
		return r.retireFlatVolumeServers(ctx, seaweedCR)
	}

	// Fallback to single volume server group (legacy behavior).
	// DaemonSet mode ignores Replicas, so don't short-circuit on Replicas == 0.
	vol := seaweedCR.Spec.Volume
	if vol == nil || (!vol.IsDaemonSet() && vol.Replicas == 0) {
		return // No volume servers to deploy
	}

	if done, result, err = r.ensureVolumeServerPeerService(seaweedCR); done {
		return
	}

	if vol.IsDaemonSet() {
		// Defense in depth for installs without the validating webhook: a
		// DaemonSet without hostPath would fall back to PVC disks it can't use.
		if len(vol.HostPath) == 0 {
			return true, ctrl.Result{}, errors.New("spec.volume.kind=DaemonSet requires spec.volume.hostPath to be set")
		}
		// DaemonSet pods have no ordinal identity; the per-replica Services
		// don't apply, so the headless peer Service covers discovery.
		if done, result, err = r.ensureVolumeServerDaemonSet(ctx, seaweedCR); done {
			return
		}
	} else {
		if done, result, err = r.ensureVolumeServerServices(seaweedCR); done {
			return
		}

		if done, result, err = r.ensureVolumeServerStatefulSet(ctx, seaweedCR); done {
			return
		}
	}

	if done, result, err = r.pruneStaleVolumeServices(ctx, seaweedCR); done {
		return
	}

	if vol.MetricsPort != nil {
		if done, result, err = r.ensureVolumeServerServiceMonitor(seaweedCR); done {
			return
		}
	}

	return
}

func (r *SeaweedReconciler) ensureVolumeServerStatefulSet(ctx context.Context, seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-volume-statefulset", seaweedCR.Name)

	// Remove a DaemonSet left from a previous kind=DaemonSet config, and wait
	// for it to clear before creating the StatefulSet so they don't overlap.
	staleDaemonSet := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: seaweedCR.Name + "-volume", Namespace: seaweedCR.Namespace}}
	if existed, err := r.deleteIfExists(ctx, staleDaemonSet); err != nil {
		return ReconcileResult(err)
	} else if existed {
		log.Info("waiting for prior volume DaemonSet deletion before creating StatefulSet")
		return true, ctrl.Result{Requeue: true}, nil
	}

	volumeServerStatefulSet := r.createVolumeServerStatefulSet(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, volumeServerStatefulSet, r.Scheme); err != nil {
		return ReconcileResult(err)
	}

	// Gate scale-down on evacuation: cap the replica count so a volume server
	// pod is removed only after its data has drained to the other servers.
	allowed, err := r.allowedVolumeServerReplicas(ctx, seaweedCR, volumeServerStatefulSet.Name, seaweedCR.Spec.Volume.Replicas,
		func(ord int32) string { return volumeServerNodeAddress(seaweedCR, ord) })
	if err != nil {
		return ReconcileResult(err)
	}
	volumeServerStatefulSet.Spec.Replicas = &allowed

	_, err = r.CreateOrUpdate(volumeServerStatefulSet, func(existing, desired runtime.Object) error {
		existingStatefulSet := existing.(*appsv1.StatefulSet)
		desiredStatefulSet := desired.(*appsv1.StatefulSet)

		existingStatefulSet.Spec.Replicas = desiredStatefulSet.Spec.Replicas
		mergePodTemplateMetadata(existingStatefulSet, &existingStatefulSet.Spec.Template, &desiredStatefulSet.Spec.Template)
		existingStatefulSet.Spec.Template.Spec = desiredStatefulSet.Spec.Template.Spec
		existingStatefulSet.Spec.PersistentVolumeClaimRetentionPolicy = desiredStatefulSet.Spec.PersistentVolumeClaimRetentionPolicy

		return r.reconcileVolumeClaimTemplates(ctx, seaweedCR, existingStatefulSet, desiredStatefulSet)
	})
	if errors.Is(err, ErrStatefulSetDeleted) {
		log.Info("volume StatefulSet deleted for VolumeClaimTemplates update, requeueing")
		return true, ctrl.Result{Requeue: true}, nil
	}

	log.Info("ensure volume stateful set " + volumeServerStatefulSet.Name)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureVolumeServerDaemonSet(ctx context.Context, seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-volume-daemonset", seaweedCR.Name)

	// Remove a StatefulSet left from a previous kind=StatefulSet config, and
	// wait for it to clear before creating the DaemonSet so they don't overlap.
	staleStatefulSet := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: seaweedCR.Name + "-volume", Namespace: seaweedCR.Namespace}}
	if existed, err := r.deleteIfExists(ctx, staleStatefulSet); err != nil {
		return ReconcileResult(err)
	} else if existed {
		log.Info("waiting for prior volume StatefulSet deletion before creating DaemonSet")
		return true, ctrl.Result{Requeue: true}, nil
	}

	volumeServerDaemonSet := r.createVolumeServerDaemonSet(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, volumeServerDaemonSet, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdate(volumeServerDaemonSet, func(existing, desired runtime.Object) error {
		existingDaemonSet := existing.(*appsv1.DaemonSet)
		desiredDaemonSet := desired.(*appsv1.DaemonSet)

		mergePodTemplateMetadata(existingDaemonSet, &existingDaemonSet.Spec.Template, &desiredDaemonSet.Spec.Template)
		existingDaemonSet.Spec.Template.Spec = desiredDaemonSet.Spec.Template.Spec
		existingDaemonSet.Spec.UpdateStrategy = desiredDaemonSet.Spec.UpdateStrategy

		return nil
	})

	log.Info("ensure volume daemon set " + volumeServerDaemonSet.Name)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureVolumeServerPeerService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {

	log := r.Log.WithValues("sw-volume-peer-service", seaweedCR.Name)

	volumeServerPeerService := r.createVolumeServerPeerService(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, volumeServerPeerService, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateService(volumeServerPeerService)

	log.Info("ensure volume peer service " + volumeServerPeerService.Name)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureVolumeServerServices(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {

	for i := 0; i < int(seaweedCR.Spec.Volume.Replicas); i++ {
		done, result, err := r.ensureVolumeServerService(seaweedCR, i)
		if done {
			return done, result, err
		}
	}

	return ReconcileResult(nil)
}

func (r *SeaweedReconciler) ensureVolumeServerService(seaweedCR *seaweedv1.Seaweed, i int) (bool, ctrl.Result, error) {

	log := r.Log.WithValues("sw-volume-service", seaweedCR.Name, "index", i)

	volumeServerService := r.createVolumeServerService(seaweedCR, i)
	if err := controllerutil.SetControllerReference(seaweedCR, volumeServerService, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateService(volumeServerService)

	log.Info("ensure volume service "+volumeServerService.Name, "index", i)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureVolumeServerServiceMonitor(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-volume-servicemonitor", seaweedCR.Name)

	volumeServiceMonitor := r.createVolumeServerServiceMonitor(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, volumeServiceMonitor, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateServiceMonitor(volumeServiceMonitor)

	log.Info("Get volume service monitor " + volumeServiceMonitor.Name)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureVolumeServersWithTopology(ctx context.Context, seaweedCR *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {
	log := r.Log.WithValues("seaweed-topology", seaweedCR.Name)

	for topologyName, topologySpec := range seaweedCR.Spec.VolumeTopology {
		log.Info("ensuring volume servers for topology", "topology", topologyName, "datacenter", topologySpec.DataCenter, "rack", topologySpec.Rack)

		if done, result, err = r.ensureVolumeServerTopologyPeerService(seaweedCR, topologyName); done {
			return
		}

		if done, result, err = r.ensureVolumeServerTopologyServices(seaweedCR, topologyName, topologySpec); done {
			return
		}

		if done, result, err = r.ensureVolumeServerTopologyStatefulSet(ctx, seaweedCR, topologyName, topologySpec); done {
			return
		}

		if topologySpec.MetricsPort != nil {
			if done, result, err = r.ensureVolumeServerTopologyServiceMonitor(seaweedCR, topologyName, topologySpec); done {
				return
			}
		}
	}

	if done, result, err = r.pruneStaleVolumeServices(ctx, seaweedCR); done {
		return
	}

	return
}

// retireFlatVolumeServers drains and removes the legacy <name>-volume
// workload once spec.volumeTopology has taken over. The flat branch of
// ensureVolumeServers is skipped whenever topology groups exist, so without
// this the old StatefulSet and its Services keep running orphaned — and
// getVolumeStatus no longer counts them once spec.volume is dropped.
//
// Removal is gated twice. First on replacement capacity: the topology groups
// must have all their replicas ready, or an already-empty flat set would be
// deleted into a window with no volume server serving. Then on data: the
// StatefulSet scales down through the same evacuation gate used for ordinary
// scale-ins, and a DaemonSet is held until every one of its pods registers
// zero volumes — DaemonSet pods have no ordinals, so each is evacuated
// individually. The flat Services stay in place until the workload is gone
// because the pods register their Service names with the master — pulling
// them mid-drain would strand the evacuation traffic.
//
// Objects sharing the generated name but not owned by this CR are left
// alone throughout.
func (r *SeaweedReconciler) retireFlatVolumeServers(ctx context.Context, seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-volume-retire", seaweedCR.Name)
	name := seaweedCR.Name + "-volume"
	key := types.NamespacedName{Namespace: seaweedCR.Namespace, Name: name}

	ds := &appsv1.DaemonSet{}
	dsErr := r.Get(ctx, key, ds)
	if dsErr != nil && !apierrors.IsNotFound(dsErr) {
		return ReconcileResult(dsErr)
	}
	dsOwned := dsErr == nil && isOwnedBy(ds.OwnerReferences, seaweedCR.UID)

	sts := &appsv1.StatefulSet{}
	stsErr := r.Get(ctx, key, sts)
	if stsErr != nil && !apierrors.IsNotFound(stsErr) {
		return ReconcileResult(stsErr)
	}
	stsOwned := stsErr == nil && isOwnedBy(sts.OwnerReferences, seaweedCR.UID)

	if dsOwned || stsOwned {
		ready, err := r.topologyVolumeServersReady(ctx, seaweedCR)
		if err != nil {
			return ReconcileResult(err)
		}
		if !ready {
			log.Info("waiting for topology volume servers to serve before retiring flat workload")
			return true, ctrl.Result{Requeue: true}, nil
		}
	}

	if dsOwned {
		drained, err := r.flatDaemonSetDrained(ctx, seaweedCR, ds)
		if err != nil {
			return ReconcileResult(err)
		}
		if !drained {
			log.Info("waiting for flat volume DaemonSet servers to drain", "daemonset", name)
			return true, ctrl.Result{Requeue: true}, nil
		}
		if err := r.Delete(ctx, ds); err != nil {
			return ReconcileResult(client.IgnoreNotFound(err))
		}
		log.Info("deleted flat volume DaemonSet superseded by volumeTopology", "daemonset", name)
		return true, ctrl.Result{Requeue: true}, nil
	}

	if stsOwned {
		// desired=0 shrinks the set only as servers drain; the gate starts
		// background evacuations for any still-populated server.
		allowed, err := r.allowedVolumeServerReplicas(ctx, seaweedCR, name, 0,
			func(ord int32) string { return volumeServerNodeAddress(seaweedCR, ord) })
		if err != nil {
			return ReconcileResult(err)
		}
		if allowed > 0 {
			if ptr.Deref(sts.Spec.Replicas, 0) != allowed {
				sts.Spec.Replicas = ptr.To(allowed)
				if err := r.Update(ctx, sts); err != nil {
					return ReconcileResult(err)
				}
			}
			log.Info("waiting for flat volume StatefulSet to drain before removal", "statefulset", name, "allowed", allowed)
			return true, ctrl.Result{Requeue: true}, nil
		}
		if err := r.Delete(ctx, sts); err != nil {
			return ReconcileResult(client.IgnoreNotFound(err))
		}
		log.Info("deleted flat volume StatefulSet superseded by volumeTopology", "statefulset", name)
		return true, ctrl.Result{Requeue: true}, nil
	}

	// The workload is gone: remove its peer and per-replica Services plus the
	// flat ServiceMonitor. Topology resources carry the seaweedfs/topology
	// label, so under these volume labels everything without it is stale.
	services := &corev1.ServiceList{}
	if err := r.List(ctx, services,
		client.InNamespace(seaweedCR.Namespace),
		client.MatchingLabels{
			label.ManagedByLabelKey: "seaweedfs-operator",
			label.ComponentLabelKey: "volume",
			label.InstanceLabelKey:  seaweedCR.Name,
		},
	); err != nil {
		return ReconcileResult(err)
	}
	for i := range services.Items {
		svc := &services.Items[i]
		if _, isTopology := svc.Labels["seaweedfs/topology"]; isTopology {
			continue
		}
		if !isOwnedBy(svc.OwnerReferences, seaweedCR.UID) {
			continue
		}
		log.Info("deleting flat volume service superseded by volumeTopology", "service", svc.Name)
		if err := r.Delete(ctx, svc); err != nil && !apierrors.IsNotFound(err) {
			return ReconcileResult(err)
		}
	}

	monitor := &monitorv1.ServiceMonitor{}
	if err := r.Get(ctx, key, monitor); err == nil {
		if isOwnedBy(monitor.OwnerReferences, seaweedCR.UID) {
			if err := r.Delete(ctx, monitor); err != nil && !apierrors.IsNotFound(err) {
				return ReconcileResult(err)
			}
		}
	} else if !apierrors.IsNotFound(err) {
		// Clusters without the Prometheus Operator CRD cannot hold a stale
		// ServiceMonitor — treat the missing kind like a missing object.
		if !meta.IsNoMatchError(err) && !runtime.IsNotRegisteredError(err) {
			return ReconcileResult(err)
		}
	}

	return ReconcileResult(nil)
}

// pruneStaleVolumeServices removes per-replica volume Services whose ordinal
// no live workload wants anymore — e.g. test-volume-2 after a flat scale-in,
// or test-volume-dc1-2 after a topology group shrank past index 2. The cutoff
// is each workload's live StatefulSet spec.replicas, not the CR's desired
// count: while evacuation holds a scale-down the pod is still up, still
// registered under its Service DNS name, and still needs the Service.
// Services whose workload StatefulSet is missing or not owned by this CR are
// left alone — an orphaned group workload keeps serving on them.
func (r *SeaweedReconciler) pruneStaleVolumeServices(ctx context.Context, m *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	services := &corev1.ServiceList{}
	if err := r.List(ctx, services,
		client.InNamespace(m.Namespace),
		client.MatchingLabels{
			label.ManagedByLabelKey: "seaweedfs-operator",
			label.ComponentLabelKey: "volume",
			label.InstanceLabelKey:  m.Name,
		},
	); err != nil {
		return ReconcileResult(err)
	}

	prefix := m.Name + "-volume-"
	for i := range services.Items {
		svc := &services.Items[i]
		if !isOwnedBy(svc.OwnerReferences, m.UID) {
			continue
		}
		rest, ok := strings.CutPrefix(svc.Name, prefix)
		if !ok || strings.HasSuffix(rest, "-peer") {
			continue
		}
		// Per-replica names are <name>-volume-<i> (flat) and
		// <name>-volume-<topology>-<i>. The ordinal is the last dash
		// segment; a leading stem, when present, is the topology name —
		// which may itself contain dashes.
		workload := m.Name + "-volume"
		ordStr := rest
		if cut := strings.LastIndex(rest, "-"); cut >= 0 {
			workload = fmt.Sprintf("%s-volume-%s", m.Name, rest[:cut])
			ordStr = rest[cut+1:]
		}
		ord, err := strconv.Atoi(ordStr)
		if err != nil {
			continue
		}

		sts := &appsv1.StatefulSet{}
		err = r.Get(ctx, types.NamespacedName{Namespace: m.Namespace, Name: workload}, sts)
		if apierrors.IsNotFound(err) || (err == nil && !isOwnedBy(sts.OwnerReferences, m.UID)) {
			continue
		}
		if err != nil {
			return ReconcileResult(err)
		}
		if int32(ord) < ptr.Deref(sts.Spec.Replicas, 0) {
			continue
		}
		r.Log.Info("deleting stale per-replica volume service", "service", svc.Name, "workload", workload, "replicas", ptr.Deref(sts.Spec.Replicas, 0))
		if err := r.Delete(ctx, svc); err != nil && !apierrors.IsNotFound(err) {
			return ReconcileResult(err)
		}
	}
	return ReconcileResult(nil)
}

// topologyVolumeServersReady reports whether every topology group has all of
// its replicas ready. Retiring the flat workload before the replacements can
// serve would leave the cluster without volume capacity — the StatefulSets
// being created does not establish readiness, since pods can still be
// Pending on PVC binding or scheduling.
func (r *SeaweedReconciler) topologyVolumeServersReady(ctx context.Context, m *seaweedv1.Seaweed) (bool, error) {
	for topologyName, topologySpec := range m.Spec.VolumeTopology {
		if topologySpec == nil || topologySpec.Replicas == 0 {
			continue
		}
		sts := &appsv1.StatefulSet{}
		err := r.Get(ctx, types.NamespacedName{
			Namespace: m.Namespace,
			Name:      fmt.Sprintf("%s-volume-%s", m.Name, topologyName),
		}, sts)
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		// ReadyReplicas alone can still describe the pre-update revision
		// while a template change is rolling out; UpdatedReplicas only
		// reaches the desired count once every pod runs the new spec.
		if sts.Status.UpdatedReplicas < topologySpec.Replicas || sts.Status.ReadyReplicas < topologySpec.Replicas {
			return false, nil
		}
	}
	return true, nil
}

// flatDaemonSetDrained reports whether every pod the flat volume DaemonSet
// still runs registers zero volumes with the master. DaemonSet pods register
// as <podIP>:<volumePort> (they start with -ip=$(POD_IP)), so each is checked
// individually; populated servers are evacuated in the background while a
// server missing from the master's view is held rather than assumed empty.
func (r *SeaweedReconciler) flatDaemonSetDrained(ctx context.Context, m *seaweedv1.Seaweed, ds *appsv1.DaemonSet) (bool, error) {
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods,
		client.InNamespace(m.Namespace),
		client.MatchingLabels(labelsForVolumeServer(m.Name)),
	); err != nil {
		return false, err
	}

	var nodes []string
	unknown := false
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !metav1.IsControlledBy(pod, ds) {
			continue
		}
		if pod.Status.PodIP == "" {
			// A pod without an IP cannot be confirmed empty by the master.
			// Only one finished for good and not being deleted is safe to
			// ignore — a live or terminating pod may still hold populated
			// hostPath data.
			if pod.DeletionTimestamp != nil ||
				(pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed) {
				unknown = true
			}
			continue
		}
		nodes = append(nodes, fmt.Sprintf("%s:%d", pod.Status.PodIP, seaweedv1.VolumeHTTPPort))
	}
	if len(nodes) == 0 && !unknown {
		return true, nil
	}

	// Without the master admin there is no drain signal to read; proceed the
	// way the StatefulSet gate does when it is unwired.
	if r.VolumeAdminFactory == nil || r.evac == nil {
		return true, nil
	}

	counts, err := r.volumeServerVolumeCounts(ctx, m)
	if err != nil {
		r.Log.Error(err, "cannot read volume topology; holding flat DaemonSet removal", "daemonset", ds.Name)
		return false, nil
	}

	drained := !unknown
	for _, node := range nodes {
		n, known := counts[node]
		if !known {
			// Not registered with the master — cannot confirm it is empty.
			drained = false
			continue
		}
		if n > 0 {
			drained = false
			r.startVolumeServerEvacuation(ctx, m, node)
		}
	}
	return drained, nil
}

func labelsForVolumeServer(name string) map[string]string {
	return map[string]string{
		label.ManagedByLabelKey: "seaweedfs-operator",
		label.NameLabelKey:      "seaweedfs",
		label.ComponentLabelKey: "volume",
		label.InstanceLabelKey:  name,
	}
}

func labelsForVolumeServerTopology(name, topology string) map[string]string {
	return map[string]string{
		label.ManagedByLabelKey: "seaweedfs-operator",
		label.NameLabelKey:      "seaweedfs",
		label.ComponentLabelKey: "volume",
		label.InstanceLabelKey:  name,
		"seaweedfs/topology":    topology,
	}
}

func (r *SeaweedReconciler) ensureVolumeServerTopologyStatefulSet(ctx context.Context, seaweedCR *seaweedv1.Seaweed, topologyName string, topologySpec *seaweedv1.VolumeTopologySpec) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-volume-topology-statefulset", seaweedCR.Name, "topology", topologyName)

	volumeServerStatefulSet := r.createVolumeServerTopologyStatefulSet(seaweedCR, topologyName, topologySpec)
	if err := controllerutil.SetControllerReference(seaweedCR, volumeServerStatefulSet, r.Scheme); err != nil {
		return ReconcileResult(err)
	}

	// Gate scale-down on evacuation, as for the flat volume StatefulSet.
	allowed, err := r.allowedVolumeServerReplicas(ctx, seaweedCR, volumeServerStatefulSet.Name, topologySpec.Replicas,
		func(ord int32) string { return volumeServerTopologyNodeAddress(seaweedCR, topologyName, ord) })
	if err != nil {
		return ReconcileResult(err)
	}
	volumeServerStatefulSet.Spec.Replicas = &allowed

	_, err = r.CreateOrUpdate(volumeServerStatefulSet, func(existing, desired runtime.Object) error {
		existingStatefulSet := existing.(*appsv1.StatefulSet)
		desiredStatefulSet := desired.(*appsv1.StatefulSet)

		existingStatefulSet.Spec.Replicas = desiredStatefulSet.Spec.Replicas
		mergePodTemplateMetadata(existingStatefulSet, &existingStatefulSet.Spec.Template, &desiredStatefulSet.Spec.Template)
		existingStatefulSet.Spec.Template.Spec = desiredStatefulSet.Spec.Template.Spec
		existingStatefulSet.Spec.PersistentVolumeClaimRetentionPolicy = desiredStatefulSet.Spec.PersistentVolumeClaimRetentionPolicy

		return r.reconcileVolumeClaimTemplates(ctx, seaweedCR, existingStatefulSet, desiredStatefulSet)
	})
	if errors.Is(err, ErrStatefulSetDeleted) {
		log.Info("volume topology StatefulSet deleted for VolumeClaimTemplates update, requeueing", "topology", topologyName)
		return true, ctrl.Result{Requeue: true}, nil
	}

	log.Info("ensure volume topology stateful set " + volumeServerStatefulSet.Name)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureVolumeServerTopologyPeerService(seaweedCR *seaweedv1.Seaweed, topologyName string) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-volume-topology-peer-service", seaweedCR.Name, "topology", topologyName)

	volumeServerPeerService := r.createVolumeServerTopologyPeerService(seaweedCR, topologyName)
	if err := controllerutil.SetControllerReference(seaweedCR, volumeServerPeerService, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateService(volumeServerPeerService)

	log.Info("ensure volume topology peer service " + volumeServerPeerService.Name)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureVolumeServerTopologyServices(seaweedCR *seaweedv1.Seaweed, topologyName string, topologySpec *seaweedv1.VolumeTopologySpec) (bool, ctrl.Result, error) {
	for i := 0; i < int(topologySpec.Replicas); i++ {
		done, result, err := r.ensureVolumeServerTopologyService(seaweedCR, topologyName, i)
		if done {
			return done, result, err
		}
	}

	return ReconcileResult(nil)
}

func (r *SeaweedReconciler) ensureVolumeServerTopologyService(seaweedCR *seaweedv1.Seaweed, topologyName string, i int) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-volume-topology-service", seaweedCR.Name, "topology", topologyName, "index", i)

	volumeServerService := r.createVolumeServerTopologyService(seaweedCR, topologyName, i)
	if err := controllerutil.SetControllerReference(seaweedCR, volumeServerService, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateService(volumeServerService)

	log.Info("ensure volume topology service "+volumeServerService.Name, "topology", topologyName, "index", i)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureVolumeServerTopologyServiceMonitor(seaweedCR *seaweedv1.Seaweed, topologyName string, topologySpec *seaweedv1.VolumeTopologySpec) (bool, ctrl.Result, error) {
	log := r.Log.WithValues("sw-volume-topology-servicemonitor", seaweedCR.Name, "topology", topologyName)

	volumeServiceMonitor := r.createVolumeServerTopologyServiceMonitor(seaweedCR, topologyName, topologySpec)
	if err := controllerutil.SetControllerReference(seaweedCR, volumeServiceMonitor, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateServiceMonitor(volumeServiceMonitor)

	log.Info("Get volume topology service monitor " + volumeServiceMonitor.Name)
	return ReconcileResult(err)
}
