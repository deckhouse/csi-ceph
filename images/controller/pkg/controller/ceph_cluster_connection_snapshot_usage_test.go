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
	"strings"
	"testing"

	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	storagev1alpha1 "github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/internal"
	"github.com/deckhouse/sds-common-lib/conditions"
	"github.com/deckhouse/sds-common-lib/logger"
)

func newSnapshotContent(name, secretName, secretNamespace string, policy snapshotv1.DeletionPolicy) snapshotv1.VolumeSnapshotContent {
	return snapshotv1.VolumeSnapshotContent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{
			snapshotDeletionSecretNameAnnotation:      secretName,
			snapshotDeletionSecretNamespaceAnnotation: secretNamespace,
		}},
		Spec: snapshotv1.VolumeSnapshotContentSpec{DeletionPolicy: policy},
	}
}

func TestSnapshotContentsNeedingSecret(t *testing.T) {
	tests := []struct {
		name    string
		content snapshotv1.VolumeSnapshotContent
		want    bool
	}{
		{name: "deleted with its snapshot", content: newSnapshotContent("vsc", testSecretName, testSecretNamespace, snapshotv1.VolumeSnapshotContentDelete), want: true},
		{name: "snapshot kept", content: newSnapshotContent("vsc", testSecretName, testSecretNamespace, snapshotv1.VolumeSnapshotContentRetain)},
		{name: "same name in another namespace", content: newSnapshotContent("vsc", testSecretName, "other", snapshotv1.VolumeSnapshotContentDelete)},
		{name: "another secret", content: newSnapshotContent("vsc", "csi-ceph-secret-for-other", testSecretNamespace, snapshotv1.VolumeSnapshotContentDelete)},
		{name: "no deletion secret", content: snapshotv1.VolumeSnapshotContent{
			ObjectMeta: metav1.ObjectMeta{Name: "vsc"},
			Spec:       snapshotv1.VolumeSnapshotContentSpec{DeletionPolicy: snapshotv1.VolumeSnapshotContentDelete},
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := volumeUsage{snapshotContents: []snapshotv1.VolumeSnapshotContent{tt.content}}
			if got := len(usage.snapshotContentsNeedingSecret(testSecretNamespace, testSecretName)) > 0; got != tt.want {
				t.Errorf("snapshotContentsNeedingSecret() lists the content: %t, want %t", got, tt.want)
			}
			if got := usage.secretNeeded(testSecretNamespace, testSecretName); got != tt.want {
				t.Errorf("secretNeeded() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestReadyReasonOfConnectionHeldBySnapshots(t *testing.T) {
	ctx := context.Background()
	now := metav1.Now()
	pvHolding := corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv"},
		Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
			NodeStageSecretRef: &corev1.SecretReference{Name: testSecretName, Namespace: testSecretNamespace},
		}}},
	}
	contents := []snapshotv1.VolumeSnapshotContent{newSnapshotContent("vsc", testSecretName, testSecretNamespace, snapshotv1.VolumeSnapshotContentDelete)}

	tests := []struct {
		name       string
		usage      volumeUsage
		wantReason string
		wantInMsg  []string
	}{
		{
			name:       "snapshots only",
			usage:      volumeUsage{snapshotContents: contents},
			wantReason: storagev1alpha1.ReasonVolumeSnapshotContentsExist,
			wantInMsg:  []string{"used by 1 VolumeSnapshotContents [vsc];"},
		},
		{
			name:       "volumes and snapshots",
			usage:      volumeUsage{pvs: []corev1.PersistentVolume{pvHolding}, snapshotContents: contents},
			wantReason: storagev1alpha1.ReasonPersistentVolumesExist,
			wantInMsg:  []string{"used by 1 PersistentVolumes [pv] and 1 VolumeSnapshotContents [vsc];"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			connection := &storagev1alpha1.CephClusterConnection{ObjectMeta: metav1.ObjectMeta{Name: "conn", DeletionTimestamp: &now, Finalizers: []string{CephClusterConnectionControllerFinalizerName}}}
			cl := newStatusTestClient(t, connection)

			_, msg, err := reconcileSecret(ctx, nil, logger.NewNop(), &corev1.SecretList{}, connection, tt.usage, testSecretNamespace, testSecretName)
			if err == nil {
				t.Fatal("reconcileSecret() should hold the connection")
			}
			if err := updateCephClusterConnectionStatus(ctx, cl, connection, err, msg); err != nil {
				t.Fatalf("updating status: %v", err)
			}

			got := &storagev1alpha1.CephClusterConnection{}
			if err := cl.Get(ctx, client.ObjectKey{Name: "conn"}, got); err != nil {
				t.Fatalf("reading back: %v", err)
			}
			cond := conditions.Get(got.Status.Conditions, storagev1alpha1.ConditionTypeReady)
			if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != tt.wantReason {
				t.Fatalf("Ready = %+v, want False with %s", cond, tt.wantReason)
			}
			for _, want := range tt.wantInMsg {
				if !strings.Contains(cond.Message, want) {
					t.Errorf("message %q should contain %q", cond.Message, want)
				}
			}
		})
	}
}

func TestConnectionHeldBySnapshotsKeepsTheEntry(t *testing.T) {
	now := metav1.Now()
	connection := &storagev1alpha1.CephClusterConnection{
		ObjectMeta: metav1.ObjectMeta{Name: "conn", DeletionTimestamp: &now},
		Spec:       storagev1alpha1.CephClusterConnectionSpec{ClusterID: "cluster", Monitors: []string{"mon"}, UserID: "user", UserKey: "key"},
	}
	usage := volumeUsage{snapshotContents: []snapshotv1.VolumeSnapshotContent{
		newSnapshotContent("vsc", internal.CephClusterConnectionSecretPrefix+"conn", testSecretNamespace, snapshotv1.VolumeSnapshotContentDelete),
	}}

	entry, _ := generateSharedClusterConfig(logger.NewNop(), connection, nil, nil, nil, usage, testSecretNamespace)
	if entry == nil || entry.RBD.ControllerPublishSecretRef.Name != internal.CephClusterConnectionSecretPrefix+"conn" {
		t.Fatalf("entry = %+v, want the entry of cluster kept for the snapshot, whose deletion needs its monitors", entry)
	}
}

func snapshotUsageScheme(t *testing.T) *apiruntime.Scheme {
	t.Helper()
	scheme := apiruntime.NewScheme()
	for _, add := range []func(*apiruntime.Scheme) error{clientgoscheme.AddToScheme, storagev1alpha1.AddToScheme, snapshotv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatalf("adding scheme: %v", err)
		}
	}
	return scheme
}

func TestDeletionReadsSnapshotContentsPastTheCache(t *testing.T) {
	ctx := context.Background()
	scheme := snapshotUsageScheme(t)

	now := metav1.Now()
	objects := func() []client.Object {
		return []client.Object{
			&storagev1alpha1.CephClusterConnection{ObjectMeta: metav1.ObjectMeta{
				Name:              "conn",
				DeletionTimestamp: &now,
				Finalizers:        []string{CephClusterConnectionControllerFinalizerName},
			}},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{
				Name:       testSecretName,
				Namespace:  testSecretNamespace,
				Finalizers: []string{CephClusterConnectionControllerFinalizerName},
			}},
			&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: internal.CSICephConfigMapName, Namespace: testSecretNamespace},
				Data:       map[string]string{"config.json": "[]"},
			},
		}
	}
	content := newSnapshotContent("vsc-new", testSecretName, testSecretNamespace, snapshotv1.VolumeSnapshotContentDelete)
	cached := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects()...).Build()
	live := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objects(), &content)...).Build()

	connection := &storagev1alpha1.CephClusterConnection{}
	if err := cached.Get(ctx, client.ObjectKey{Name: "conn"}, connection); err != nil {
		t.Fatalf("reading the connection: %v", err)
	}

	shouldRequeue, _, _, err := reconcileCephClusterConnection(ctx, cached, live, logger.NewNop(), connection, testSecretNamespace)
	if !shouldRequeue || err == nil || !strings.Contains(err.Error(), "used by 1 VolumeSnapshotContents [vsc-new]") {
		t.Fatalf("reconcile error = %v, want the secret held by vsc-new, which only the live reader sees", err)
	}
	if err := cached.Get(ctx, client.ObjectKey{Namespace: testSecretNamespace, Name: testSecretName}, &corev1.Secret{}); err != nil {
		t.Errorf("the secret must stay while vsc-new needs it: %v", err)
	}
}

func TestListVolumeUsageWithoutSnapshotCRD(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(snapshotUsageScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*snapshotv1.VolumeSnapshotContentList); ok {
				return &meta.NoKindMatchError{GroupKind: snapshotv1.SchemeGroupVersion.WithKind("VolumeSnapshotContent").GroupKind()}
			}
			return cl.List(ctx, list, opts...)
		},
	}).Build()

	usage, err := listVolumeUsage(context.Background(), cl)
	if err != nil {
		t.Fatalf("listVolumeUsage() = %v, want no error when the VolumeSnapshotContent CRD is not installed", err)
	}
	if len(usage.snapshotContents) != 0 {
		t.Errorf("snapshot contents = %v, want none", usage.snapshotContents)
	}
}
