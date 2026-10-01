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
	"context"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	storagev1alpha1 "github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/internal"
	"github.com/deckhouse/sds-common-lib/conditions"
	"github.com/deckhouse/sds-common-lib/logger"
)

const conflictTestClusterID = "cluster"

func conflictTestConnection(name, userID, subvolumeGroup string) storagev1alpha1.CephClusterConnection {
	connection := storagev1alpha1.CephClusterConnection{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: storagev1alpha1.CephClusterConnectionSpec{
			ClusterID: conflictTestClusterID,
			Monitors:  []string{"mon1"},
			UserID:    userID,
			UserKey:   userID + "-key",
		},
	}
	if subvolumeGroup != "" {
		connection.Spec.CephFS = &storagev1alpha1.CephClusterConnectionSpecCephFS{SubvolumeGroup: subvolumeGroup}
	}
	return connection
}

// conflictTestPV returns a volume that reads config.json: an RBD one without a
// controller-publish secret, or a CephFS one lying under subvolumePath.
func conflictTestPV(name, driver, connectionName, subvolumePath string) corev1.PersistentVolume {
	attributes := map[string]string{"clusterID": conflictTestClusterID}
	if subvolumePath != "" {
		attributes["subvolumePath"] = subvolumePath
	}
	return corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
			Driver:             driver,
			VolumeAttributes:   attributes,
			NodeStageSecretRef: &corev1.SecretReference{Name: internal.CephClusterConnectionSecretPrefix + connectionName, Namespace: testSecretNamespace},
		}}},
	}
}

func TestSharedClusterConfigConflicts(t *testing.T) {
	tests := []struct {
		name        string
		connections []storagev1alpha1.CephClusterConnection
		pvs         []corev1.PersistentVolume
		// want maps every connection to what its message must contain, or
		// to "" when it must have no conflict.
		want map[string]string
	}{
		{
			name:        "RBD volumes of two users",
			connections: []storagev1alpha1.CephClusterConnection{conflictTestConnection("a", "user-a", ""), conflictTestConnection("b", "user-b", "")},
			pvs: []corev1.PersistentVolume{
				conflictTestPV("rbd-a", CephStorageClassRBDProvisioner, "a", ""),
				conflictTestPV("rbd-b", CephStorageClassRBDProvisioner, "b", ""),
			},
			want: map[string]string{"a": "", "b": "1 RBD PersistentVolumes [rbd-b] need another user than the one of the CephClusterConnection a"},
		},
		{
			name:        "RBD volumes of two connections with one user",
			connections: []storagev1alpha1.CephClusterConnection{conflictTestConnection("a", "user", ""), conflictTestConnection("b", "user", "")},
			pvs: []corev1.PersistentVolume{
				conflictTestPV("rbd-a", CephStorageClassRBDProvisioner, "a", ""),
				conflictTestPV("rbd-b", CephStorageClassRBDProvisioner, "b", ""),
			},
			want: map[string]string{"a": "", "b": ""},
		},
		{
			name:        "CephFS volume of one connection lying in the group of the other",
			connections: []storagev1alpha1.CephClusterConnection{conflictTestConnection("a", "user-a", "group-a"), conflictTestConnection("b", "user-b", "group-b")},
			pvs: []corev1.PersistentVolume{
				conflictTestPV("fs-b", CephStorageClassCephFSProvisioner, "b", "/volumes/group-a/csi-vol-1/0b6e1c1e"),
			},
			want: map[string]string{"a": "", "b": ""},
		},
		{
			name:        "CephFS volumes in two groups",
			connections: []storagev1alpha1.CephClusterConnection{conflictTestConnection("a", "user-a", "group-a"), conflictTestConnection("b", "user-b", "group-b")},
			pvs: []corev1.PersistentVolume{
				conflictTestPV("fs-a", CephStorageClassCephFSProvisioner, "a", "/volumes/group-a/csi-vol-1/0b6e1c1e"),
				conflictTestPV("fs-b", CephStorageClassCephFSProvisioner, "b", "/volumes/group-b/csi-vol-2/0b6e1c1e"),
				conflictTestPV("fs-b-no-path", CephStorageClassCephFSProvisioner, "b", ""),
			},
			want: map[string]string{"a": "", "b": "2 CephFS PersistentVolumes [fs-b fs-b-no-path] need another subvolume group than group-a of the CephClusterConnection a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, conflicts := generateSharedClusterConfig(logger.NewNop(), &tt.connections[0], tt.connections, nil, nil, volumeUsage{pvs: tt.pvs}, testSecretNamespace)

			if len(conflicts) != len(tt.want) {
				t.Fatalf("conflicts = %v, want one message per connection %v", conflicts, tt.want)
			}
			for name, want := range tt.want {
				got, ok := conflicts[name]
				switch {
				case !ok:
					t.Errorf("no message for the connection %s", name)
				case want == "" && got != "":
					t.Errorf("connection %s: got the conflict %q, want none", name, got)
				case want != "" && !strings.Contains(got, want):
					t.Errorf("connection %s: message %q should contain %q", name, got, want)
				}
			}
		})
	}
}

func TestSharedClusterConfigConflictListsAtMostTenVolumes(t *testing.T) {
	connections := []storagev1alpha1.CephClusterConnection{conflictTestConnection("a", "user-a", ""), conflictTestConnection("b", "user-b", "")}
	pvs := []corev1.PersistentVolume{conflictTestPV("rbd-a", CephStorageClassRBDProvisioner, "a", "")}
	for i := range maxListedNames + 1 {
		pvs = append(pvs, conflictTestPV(fmt.Sprintf("rbd-b-%02d", i), CephStorageClassRBDProvisioner, "b", ""))
	}

	_, conflicts := generateSharedClusterConfig(logger.NewNop(), &connections[0], connections, nil, nil, volumeUsage{pvs: pvs}, testSecretNamespace)

	if !strings.Contains(conflicts["b"], "11 RBD PersistentVolumes") || !strings.Contains(conflicts["b"], "rbd-b-09") || strings.Contains(conflicts["b"], "rbd-b-10") {
		t.Errorf("message %q should count 11 PersistentVolumes and name the first %d", conflicts["b"], maxListedNames)
	}
}

func TestSharedClusterConfigIgnoresKeptVolumesAttachedNowhere(t *testing.T) {
	connections := []storagev1alpha1.CephClusterConnection{conflictTestConnection("a", "user-a", ""), conflictTestConnection("b", "user-b", "")}
	kept := conflictTestPV("rbd-a-kept", CephStorageClassRBDProvisioner, "a", "")
	kept.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
	kept.Status.Phase = corev1.VolumeReleased
	keptAttached := conflictTestPV("rbd-a-kept-attached", CephStorageClassRBDProvisioner, "a", "")
	keptAttached.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
	keptAttached.Status.Phase = corev1.VolumeReleased
	bound := conflictTestPV("rbd-b", CephStorageClassRBDProvisioner, "b", "")
	bound.Status.Phase = corev1.VolumeBound

	entry, conflicts := generateSharedClusterConfig(logger.NewNop(), &connections[0], connections, nil, nil, volumeUsage{pvs: []corev1.PersistentVolume{kept, bound}}, testSecretNamespace)
	if got, want := entry.RBD.ControllerPublishSecretRef.Name, internal.CephClusterConnectionSecretPrefix+"b"; got != want {
		t.Errorf("RBD secret = %s, want %s: a kept volume attached nowhere makes no CSI call", got, want)
	}
	if conflicts["a"] != "" || conflicts["b"] != "" {
		t.Errorf("conflicts = %v, want none", conflicts)
	}

	usage := volumeUsage{pvs: []corev1.PersistentVolume{keptAttached, bound}, attached: map[string]bool{keptAttached.Name: true}}
	entry, conflicts = generateSharedClusterConfig(logger.NewNop(), &connections[0], connections, nil, nil, usage, testSecretNamespace)
	if got, want := entry.RBD.ControllerPublishSecretRef.Name, internal.CephClusterConnectionSecretPrefix+"a"; got != want {
		t.Errorf("RBD secret = %s, want %s: a kept volume still attached is detached with the entry", got, want)
	}
	if !strings.Contains(conflicts["b"], "[rbd-b]") {
		t.Errorf("conflict of b = %q, want it to name rbd-b", conflicts["b"])
	}
}

func TestPublishClusterConfigConflict(t *testing.T) {
	ctx := context.Background()
	connection := conflictTestConnection("b", "user-b", "")
	connection.Generation = 3
	cl := newStatusTestClient(t, &connection)

	read := func() *metav1.Condition {
		t.Helper()
		got := &storagev1alpha1.CephClusterConnection{}
		if err := cl.Get(ctx, client.ObjectKey{Name: "b"}, got); err != nil {
			t.Fatalf("reading back: %v", err)
		}
		if got.Status == nil {
			t.Fatal("no status written")
		}
		return conditions.Get(got.Status.Conditions, storagev1alpha1.ConditionTypeClusterConfigConflict)
	}

	if err := publishClusterConfigConflict(ctx, cl, "b", "config.json has one entry for the cluster cluster: it"); err != nil {
		t.Fatalf("publishing the conflict: %v", err)
	}
	cond := read()
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != storagev1alpha1.ReasonVolumesNeedOtherSettings || cond.ObservedGeneration != 3 {
		t.Fatalf("condition = %+v, want True with %s at generation 3", cond, storagev1alpha1.ReasonVolumesNeedOtherSettings)
	}

	if err := publishClusterConfigConflict(ctx, cl, "b", ""); err != nil {
		t.Fatalf("clearing the conflict: %v", err)
	}
	cond = read()
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != storagev1alpha1.ReasonServedByClusterConfig {
		t.Fatalf("condition = %+v, want False with %s", cond, storagev1alpha1.ReasonServedByClusterConfig)
	}
}

func TestSharedClusterConfigKeepsTheConnectionTheEntryHolds(t *testing.T) {
	connections := []storagev1alpha1.CephClusterConnection{conflictTestConnection("a", "user-a", ""), conflictTestConnection("b", "user-b", "")}
	pvs := []corev1.PersistentVolume{
		conflictTestPV("rbd-a", CephStorageClassRBDProvisioner, "a", ""),
		conflictTestPV("rbd-b", CephStorageClassRBDProvisioner, "b", ""),
	}
	entryOf := func(name string) *storagev1alpha1.ClusterConfig {
		entry := generateClusterConfig(&storagev1alpha1.CephClusterConnection{ObjectMeta: metav1.ObjectMeta{Name: name}}, testSecretNamespace)
		return &entry
	}

	tests := []struct {
		name    string
		current *storagev1alpha1.ClusterConfig
		want    string
	}{
		{name: "no entry yet", current: nil, want: "a"},
		{name: "the entry holds a connection volumes depend on", current: entryOf("b"), want: "b"},
		{name: "the entry holds a connection no volume depends on", current: entryOf("c"), want: "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry, conflicts := generateSharedClusterConfig(logger.NewNop(), &connections[0], connections, nil, tt.current, volumeUsage{pvs: pvs}, testSecretNamespace)
			if got := entry.RBD.ControllerPublishSecretRef.Name; got != "csi-ceph-secret-for-"+tt.want {
				t.Fatalf("the RBD entry holds %s, want the secret of %s", got, tt.want)
			}
			other := "a"
			if tt.want == "a" {
				other = "b"
			}
			if conflicts[tt.want] != "" || conflicts[other] == "" {
				t.Errorf("conflicts = %v, want one for %s only", conflicts, other)
			}
		})
	}
}
