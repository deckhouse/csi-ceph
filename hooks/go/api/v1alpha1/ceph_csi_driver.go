/*
Copyright 2025 Flant JSC

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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CephCSIDriverSpec defines the desired state of CephCSIDriver.
// +k8s:deepcopy-gen=true
type CephCSIDriverSpec struct {
	ClusterID string        `json:"clusterID"` // Ceph cluster FSID/UUID.
	UserID    string        `json:"userID"`    // Ceph username (without `client.` prefix).
	UserKey   string        `json:"userKey"`   // Ceph auth key corresponding to the `userID`.
	Monitors  []string      `json:"monitors"`  // List of ceph-mon IPs (e.g., `10.0.0.10:6789`).
	RBD       *RBDConfig    `json:"rbd,omitempty"`
	CephFS    *CephFSConfig `json:"cephfs,omitempty"`
}

// RBDConfig represents configuration for RBD (Rados Block Device).
// +k8s:deepcopy-gen=true
type RBDConfig struct {
	StorageClasses []RBDStorageClass `json:"storageClasses,omitempty"` // RBD StorageClasses configuration.
}

// RBDStorageClass represents an RBD StorageClass configuration.
// +k8s:deepcopy-gen=true
type RBDStorageClass struct {
	NamePostfix          string   `json:"namePostfix"`                    // Part of the StorageClass name after `-`.
	Pool                 string   `json:"pool,omitempty"`                 // Ceph pool into which the RBD image is created.
	ReclaimPolicy        string   `json:"reclaimPolicy,omitempty"`        // Reclaim policy (Delete/Retain).
	AllowVolumeExpansion *bool    `json:"allowVolumeExpansion,omitempty"` // Allow users to resize the volume.
	MountOptions         []string `json:"mountOptions,omitempty"`         // List of mount options.
	DefaultFSType        string   `json:"defaultFSType,omitempty"`        // Default filesystem type (ext4/xfs).
}

// CephFSConfig represents configuration for CephFS.
// +k8s:deepcopy-gen=true
type CephFSConfig struct {
	SubvolumeGroup string               `json:"subvolumeGroup,omitempty"` // CephFS subvolume group.
	StorageClasses []CephFSStorageClass `json:"storageClasses,omitempty"` // CephFS StorageClasses configuration.
}

// CephFSStorageClass represents a CephFS StorageClass configuration.
// +k8s:deepcopy-gen=true
type CephFSStorageClass struct {
	NamePostfix          string   `json:"namePostfix"`                    // Part of the StorageClass name after `-`.
	Pool                 string   `json:"pool,omitempty"`                 // Ceph pool name to store volume data.
	ReclaimPolicy        string   `json:"reclaimPolicy,omitempty"`        // Reclaim policy (Delete/Retain).
	AllowVolumeExpansion *bool    `json:"allowVolumeExpansion,omitempty"` // Allow users to resize the volume.
	MountOptions         []string `json:"mountOptions,omitempty"`         // List of mount options.
	FsName               string   `json:"fsName"`                         // CephFS filesystem name.
}

// CephCSIDriver is the Schema for the cephcsidrivers API.
// +k8s:deepcopy-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type CephCSIDriver struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CephCSIDriverSpec `json:"spec"`
}

// CephCSIDriverList contains a list of CephCSIDriver.
// +k8s:deepcopy-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type CephCSIDriverList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CephCSIDriver `json:"items"`
}
