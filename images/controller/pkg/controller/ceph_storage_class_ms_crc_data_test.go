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
	"github.com/deckhouse/csi-ceph/images/controller/pkg/config"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/controller"
)

// TestGetStoragecClassParamsMsCrcData verifies that the ms_mode=legacy override is
// added to the CSI map/mount options only when msCrcData is disabled
// (MS_CRC_DATA=false) — pinning the kernel clients to msgr1 so a CRC-off cluster
// stays mountable — and is absent for the default (CRC-on) case.
func TestGetStoragecClassParamsMsCrcData(t *testing.T) {
	rbdSC := &storagev1alpha1.CephStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: "rbd-sc"},
		Spec: storagev1alpha1.CephStorageClassSpec{
			ClusterConnectionName: "conn",
			Type:                  storagev1alpha1.CephStorageClassTypeRBD,
			RBD:                   &storagev1alpha1.CephStorageClassRBD{Pool: "pool1", DefaultFSType: "ext4"},
		},
	}
	cephFSSC := &storagev1alpha1.CephStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: "fs-sc"},
		Spec: storagev1alpha1.CephStorageClassSpec{
			ClusterConnectionName: "conn",
			Type:                  storagev1alpha1.CephStorageClassTypeCephFS,
			CephFS:                &storagev1alpha1.CephStorageClassCephFS{FSName: "cephfs"},
		},
	}

	cases := []struct {
		name          string
		msCrcDataEnv  string // "" = unset
		setEnv        bool
		wantRBDMap    string // expected params["mapOptions"] ("" = key absent)
		wantCephFSOpt string // expected params["kernelMountOptions"] ("" = key absent)
	}{
		{name: "default_unset_crc_on", setEnv: false, wantRBDMap: "", wantCephFSOpt: ""},
		{name: "explicit_true_crc_on", setEnv: true, msCrcDataEnv: "true", wantRBDMap: "", wantCephFSOpt: ""},
		{name: "false_crc_off_forces_msgr1", setEnv: true, msCrcDataEnv: "false", wantRBDMap: "ms_mode=legacy", wantCephFSOpt: "ms_mode=legacy"},
		{name: "false_case_insensitive", setEnv: true, msCrcDataEnv: "False", wantRBDMap: "ms_mode=legacy", wantCephFSOpt: "ms_mode=legacy"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setEnv {
				t.Setenv(config.MsCrcDataEnvName, tc.msCrcDataEnv)
			} else {
				// t.Setenv restores after the test; explicitly clear to be safe.
				t.Setenv(config.MsCrcDataEnvName, "")
			}

			rbdParams := controller.GetStoragecClassParams(rbdSC, "d8-csi-ceph", "cluster-1")
			if got := rbdParams["mapOptions"]; got != tc.wantRBDMap {
				t.Errorf("RBD mapOptions = %q, want %q", got, tc.wantRBDMap)
			}
			if _, ok := rbdParams["kernelMountOptions"]; ok {
				t.Errorf("RBD params should never carry kernelMountOptions, got %q", rbdParams["kernelMountOptions"])
			}

			fsParams := controller.GetStoragecClassParams(cephFSSC, "d8-csi-ceph", "cluster-1")
			if got := fsParams["kernelMountOptions"]; got != tc.wantCephFSOpt {
				t.Errorf("CephFS kernelMountOptions = %q, want %q", got, tc.wantCephFSOpt)
			}
			if _, ok := fsParams["mapOptions"]; ok {
				t.Errorf("CephFS params should never carry mapOptions, got %q", fsParams["mapOptions"])
			}
		})
	}
}
