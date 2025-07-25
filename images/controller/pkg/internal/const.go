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

package internal

const (
	CephStorageClassVolumeSnapshotClassAnnotationKey = "storage.deckhouse.io/volumesnapshotclass"
	CephClusterConnectionSecretPrefix                = "csi-ceph-secret-for-"
	CSICephConfigMapName                             = "ceph-csi-config"
	CreateReconcile                                  = "Create"
	StorageManagedLabelKey                           = "storage.deckhouse.io/managed-by"
	UpdateReconcile                                  = "Update"
	DeleteReconcile                                  = "Delete"

	PhaseFailed  = "Failed"
	PhaseCreated = "Created"

	UpdateConfigMapActionUpdate = "update"
	UpdateConfigMapActionDelete = "delete"

	CSISnapshotterSecretNameKey      = "csi.storage.k8s.io/snapshotter-secret-name"
	CSISnapshotterSecretNamespaceKey = "csi.storage.k8s.io/snapshotter-secret-namespace"
)
