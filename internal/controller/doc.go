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

// Package controller holds the kubebuilder RBAC markers shared by the
// manager role (config/rbac/role.yaml), including the coordination.k8s.io
// leases grant the manager needs for leader election. It is a separate
// package from internal/controller/csirbac so that controller-gen's default
// pass (DEFAULT_RBAC_PATHS) can include it while the CSI-only pass
// (CSI_RBAC_PATH) cannot accidentally pull the CSI cluster-wide grants into
// the default role.
package controller

// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
