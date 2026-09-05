/*
Copyright 2026 Flant JSC

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

package controller_test

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	storagev1alpha1 "github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/controller"
	"github.com/deckhouse/sds-common-lib/fsreserve"
)

// The share of the filesystem reserved for the superuser is an annotation on
// the CephStorageClass, and a CSI driver only ever sees StorageClass.parameters.
// ceph-csi copies those into the volume context, where our patch to
// mountVolumeToStagePath reads them.
func TestGetStoragecClassParams_ReservedPercent(t *testing.T) {
	newRBDSC := func(annotations map[string]string) *storagev1alpha1.CephStorageClass {
		return &storagev1alpha1.CephStorageClass{
			ObjectMeta: metav1.ObjectMeta{Name: "ceph-sc", Annotations: annotations},
			Spec: storagev1alpha1.CephStorageClassSpec{
				Type:                  storagev1alpha1.CephStorageClassTypeRBD,
				ClusterConnectionName: "conn",
				RBD:                   &storagev1alpha1.CephStorageClassRBD{DefaultFSType: "ext4", Pool: "pool"},
			},
		}
	}

	t.Run("no annotation leaves the parameter out", func(t *testing.T) {
		params, err := controller.GetStoragecClassParams(newRBDSC(nil), "d8-csi-ceph", "cluster-1")
		if err != nil {
			t.Fatalf("building the parameters: %v", err)
		}
		if value, ok := params[fsreserve.ParameterKey]; ok {
			t.Errorf("parameter %s = %q, want it absent", fsreserve.ParameterKey, value)
		}
	})

	t.Run("an explicit zero also leaves the parameter out", func(t *testing.T) {
		params, err := controller.GetStoragecClassParams(newRBDSC(map[string]string{fsreserve.AnnotationKey: "0"}), "d8-csi-ceph", "cluster-1")
		if err != nil {
			t.Fatalf("building the parameters: %v", err)
		}
		if value, ok := params[fsreserve.ParameterKey]; ok {
			t.Errorf("parameter %s = %q, want it absent", fsreserve.ParameterKey, value)
		}
	})

	t.Run("a percentage reaches the parameters", func(t *testing.T) {
		params, err := controller.GetStoragecClassParams(newRBDSC(map[string]string{fsreserve.AnnotationKey: "5"}), "d8-csi-ceph", "cluster-1")
		if err != nil {
			t.Fatalf("building the parameters: %v", err)
		}
		if got := params[fsreserve.ParameterKey]; got != "5" {
			t.Errorf("parameter %s = %q, want %q", fsreserve.ParameterKey, got, "5")
		}
	})

	t.Run("an invalid value fails the StorageClass instead of every volume", func(t *testing.T) {
		if _, err := controller.GetStoragecClassParams(newRBDSC(map[string]string{fsreserve.AnnotationKey: "300"}), "d8-csi-ceph", "cluster-1"); err == nil {
			t.Error("a percentage above the bound was accepted")
		}
	})

	t.Run("CephFS has no mkfs of its own, so the annotation is not carried", func(t *testing.T) {
		cephFSSC := &storagev1alpha1.CephStorageClass{
			ObjectMeta: metav1.ObjectMeta{Name: "cephfs-sc", Annotations: map[string]string{fsreserve.AnnotationKey: "5"}},
			Spec: storagev1alpha1.CephStorageClassSpec{
				Type:                  storagev1alpha1.CephStorageClassTypeCephFS,
				ClusterConnectionName: "conn",
				CephFS:                &storagev1alpha1.CephStorageClassCephFS{FSName: "cephfs"},
			},
		}

		params, err := controller.GetStoragecClassParams(cephFSSC, "d8-csi-ceph", "cluster-1")
		if err != nil {
			t.Fatalf("building the parameters: %v", err)
		}
		if _, ok := params[fsreserve.ParameterKey]; ok {
			t.Error("the reserve parameter was carried onto a CephFS class")
		}
	})
}
