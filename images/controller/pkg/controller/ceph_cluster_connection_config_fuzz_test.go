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

package controller

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"

	storagev1alpha1 "github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/sds-common-lib/logger"
)

func cloneClusterConfigs(configs []storagev1alpha1.ClusterConfig) []storagev1alpha1.ClusterConfig {
	if configs == nil {
		return nil
	}
	out := make([]storagev1alpha1.ClusterConfig, len(configs))
	for i := range configs {
		configs[i].DeepCopyInto(&out[i])
	}
	return out
}

func FuzzUpdateClusterConfigsIfNeeded(f *testing.F) {
	entry := `{"cephFS":{"subvolumeGroup":"csi","controllerPublishSecretRef":{"name":"csi-ceph-secret-for-a","namespace":"d8-csi-ceph"}},"rbd":{"controllerPublishSecretRef":{"name":"csi-ceph-secret-for-a","namespace":"d8-csi-ceph"}},"clusterID":"%s","monitors":["10.0.0.1:6789","10.0.0.2:6789"]}`
	for _, seed := range []struct {
		configJSON, clusterID, monitors string
		remove                          bool
	}{
		{"[]", "cluster", "10.0.0.1:6789,10.0.0.2:6789", false},
		{"[" + strings.ReplaceAll(entry, "%s", "cluster") + "]", "cluster", "10.0.0.1:6789,10.0.0.2:6789", false},
		{"[" + strings.ReplaceAll(entry, "%s", "cluster") + "]", "cluster", "10.0.0.3:6789", false},
		{"[" + strings.ReplaceAll(entry, "%s", "a") + "," + strings.ReplaceAll(entry, "%s", "b") + "]", "a", "", true},
		{"[" + strings.ReplaceAll(entry, "%s", "a") + "," + strings.ReplaceAll(entry, "%s", "a") + "]", "a", "10.0.0.1:6789", false},
		{"null", "cluster", "", true},
		{`[{"clusterID":"x","monitors":null}]`, "x", "", false},
		{"not json", "cluster", "10.0.0.1:6789", false},
	} {
		f.Add(seed.configJSON, seed.clusterID, seed.monitors, seed.remove)
	}

	f.Fuzz(func(t *testing.T, configJSON, clusterID, monitors string, remove bool) {
		if !utf8.ValidString(clusterID) || !utf8.ValidString(monitors) {
			return
		}
		configMap := &corev1.ConfigMap{Data: map[string]string{"config.json": configJSON}}
		configs, err := getClusterConfigsFromConfigMap(configMap)
		if err != nil {
			configs = []storagev1alpha1.ClusterConfig{}
		}

		var desired *storagev1alpha1.ClusterConfig
		if !remove {
			desired = &storagev1alpha1.ClusterConfig{ClusterID: clusterID, Monitors: strings.Split(monitors, ",")}
			desired.CephFS.SubvolumeGroup = "csi"
			desired.CephFS.ControllerPublishSecretRef = storagev1alpha1.SecretReference{Name: "csi-ceph-secret-for-" + clusterID, Namespace: "d8-csi-ceph"}
			desired.RBD.ControllerPublishSecretRef = desired.CephFS.ControllerPublishSecretRef
		}

		before := cloneClusterConfigs(configs)
		want := cloneClusterConfigs(configs)
		i := slices.IndexFunc(want, func(c storagev1alpha1.ClusterConfig) bool { return c.ClusterID == clusterID })
		switch {
		case desired == nil && i >= 0:
			want = slices.Delete(want, i, i+1)
		case desired != nil && i >= 0:
			want[i] = *desired
		case desired != nil:
			want = append(want, *desired)
		}

		got, updated := updateClusterConfigsIfNeeded(logger.NewNop(), configs, clusterID, desired)
		if !reflect.DeepEqual(got, want) && (len(got) != 0 || len(want) != 0) {
			t.Fatalf("updating %+v for the cluster %q to %+v gave %+v, want %+v", before, clusterID, desired, got, want)
		}
		if wantUpdated := !reflect.DeepEqual(want, before) && (len(want) != 0 || len(before) != 0); updated != wantUpdated {
			t.Fatalf("updating %+v for the cluster %q to %+v reported updated = %t, want %t", before, clusterID, desired, updated, wantUpdated)
		}

		written, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshaling %+v: %v", got, err)
		}
		configMap.Data["config.json"] = string(written)
		reread, err := getClusterConfigsFromConfigMap(configMap)
		if err != nil {
			t.Fatalf("the config.json written, %s, does not parse: %v", written, err)
		}
		again, updated := updateClusterConfigsIfNeeded(logger.NewNop(), cloneClusterConfigs(reread), clusterID, desired)
		if updated {
			t.Fatalf("the config.json written for the cluster %q, %s, is updated again to %+v", clusterID, written, again)
		}
		if n := slices.IndexFunc(again, func(c storagev1alpha1.ClusterConfig) bool { return c.ClusterID == clusterID }); (n >= 0) != (desired != nil) {
			t.Fatalf("the config.json written for the cluster %q, %s, has its entry = %t, want %t", clusterID, written, n >= 0, desired != nil)
		}
	})
}

func FuzzSubvolumeGroupOfPath(f *testing.F) {
	for _, seed := range [][3]string{
		{"/volumes/csi/csi-vol-1/0b6e1c1e", "csi", "csi-vol-1"},
		{"/volumes/_nogroup/sub/0b6e1c1e", "_nogroup", "sub"},
		{"volumes/csi/csi-vol-1", "csi", ""},
		{"/volumes/csi", "g", "s"},
		{"/volumes//csi-vol-1/0b6e1c1e", "", "s"},
		{"/other/csi/csi-vol-1/0b6e1c1e", "csi", "s"},
		{"", "", ""},
		{"//volumes/csi/s", "a/b", "s/t"},
	} {
		f.Add(seed[0], seed[1], seed[2])
	}

	f.Fuzz(func(t *testing.T, path, group, subvolume string) {
		if got, ok := subvolumeGroupOfPath(path); ok {
			if got == "" || strings.Contains(got, "/") {
				t.Fatalf("subvolumeGroupOfPath(%q) = %q, not a group name", path, got)
			}
			if rest := strings.TrimPrefix(path, "/"); !strings.HasPrefix(rest, "volumes/"+got+"/") {
				t.Fatalf("subvolumeGroupOfPath(%q) = %q, which is not the directory under /volumes", path, got)
			}
		} else if got != "" {
			t.Fatalf("subvolumeGroupOfPath(%q) = %q together with false", path, got)
		}

		if group == "" || strings.Contains(group, "/") {
			return
		}
		for _, p := range []string{
			"/volumes/" + group + "/" + subvolume + "/0b6e1c1e-2c4f-4b8a-9d5e-7f3a6c1d2e4b",
			"volumes/" + group + "/" + subvolume,
		} {
			if got, ok := subvolumeGroupOfPath(p); !ok || got != group {
				t.Fatalf("subvolumeGroupOfPath(%q) = %q, %t; want %q, true", p, got, ok, group)
			}
		}
	})
}
