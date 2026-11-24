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

// Package csirbac declares the kubebuilder RBAC markers for the
// SeaweedCSIDriver controller.
//
// They live in their own package, and not next to the controller in
// internal/controller, because controller-gen cannot exclude a single marker
// from a pass: `make manifests` generates the default manager role from every
// package except this one (config/rbac/role.yaml) and generates the CSI-only
// role from this package alone (config/csi/role.yaml). cmd/main.go registers
// SeaweedCSIDriverReconciler only when ENABLE_CSI_DRIVER=true, so a default
// install never runs the controller these grants exist for; holding them
// cluster-wide, including bind/escalate on roles, would turn an operator-pod
// compromise into cluster-admin.
//
// Keep these in step with the objects the controller writes in
// seaweedcsidriver_controller.go and seaweedcsidriver_objects.go.
// test/helm/rbac_drift_test.go fails if the Helm chart, the default role and
// this package disagree.
package csirbac

// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=seaweedcsidrivers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=seaweedcsidrivers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=seaweedcsidrivers/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments;daemonsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=storage.k8s.io,resources=csidrivers;storageclasses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings;roles;rolebindings,verbs=get;list;watch;create;update;patch;delete;bind;escalate
// The driver ServiceAccounts run with the permissions below; the operator must
// hold them (plus bind/escalate above) to create the backing roles without
// privilege escalation.
// +kubebuilder:rbac:groups=core,resources=persistentvolumes;persistentvolumeclaims;nodes;pods;secrets;events,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=storage.k8s.io,resources=volumeattachments;csinodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshots;volumesnapshotcontents,verbs=get;list
