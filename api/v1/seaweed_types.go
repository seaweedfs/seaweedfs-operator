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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// SeaweedSpec defines the desired state of Seaweed
type SeaweedSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// MetricsAddress is Prometheus gateway address
	MetricsAddress string `json:"metricsAddress,omitempty"`

	// Image
	Image string `json:"image,omitempty"`

	// VolumeServerCount is the number of volume servers, default to 1
	VolumeServerCount int32 `json:"volumeServerCount,omitempty"`
	VolumeServerDiskCount int32 `json:"volumeServerDiskCount,omitempty"`
	VolumeServerDiskSizeInGiB int32 `json:"volumeServerDiskSizeInGiB,omitempty"`

	// FilerCount is the number of filers, default to 1
	FilerCount int32 `json:"filerCount,omitempty"`

	// ingress
	Hosts []string `json:"hosts"`

}

// SeaweedStatus defines the observed state of Seaweed
type SeaweedStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Seaweed is the Schema for the seaweeds API
type Seaweed struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SeaweedSpec   `json:"spec,omitempty"`
	Status SeaweedStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SeaweedList contains a list of Seaweed
type SeaweedList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Seaweed `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Seaweed{}, &SeaweedList{})
}
