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
	"errors"
	"strings"
	"testing"

	v1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	storagev1alpha1 "github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/internal"
	"github.com/deckhouse/sds-common-lib/conditions"
	"github.com/deckhouse/sds-common-lib/logger"
)

func cephConnectionWithReady(generation int64, ready *metav1.Condition) *storagev1alpha1.CephClusterConnection {
	cc := &storagev1alpha1.CephClusterConnection{
		ObjectMeta: metav1.ObjectMeta{Name: "cc-1", Generation: generation},
		Spec: storagev1alpha1.CephClusterConnectionSpec{
			ClusterID: "cluster-id",
			Monitors:  []string{"mon1"},
		},
	}
	if ready != nil {
		cc.Status = &storagev1alpha1.CephClusterConnectionStatus{Conditions: []metav1.Condition{*ready}}
	}
	return cc
}

func TestConnectionNotReady(t *testing.T) {
	for _, tc := range []struct {
		name string
		cc   *storagev1alpha1.CephClusterConnection
		want string
	}{
		{
			name: "no verdict yet",
			cc:   cephConnectionWithReady(1, nil),
			want: `CephClusterConnection "cc-1" is not Ready (Pending)`,
		},
		{
			name: "not ready",
			cc: cephConnectionWithReady(1, &metav1.Condition{
				Type: storagev1alpha1.ConditionTypeReady, Status: metav1.ConditionFalse,
				Reason: conditions.ReasonReconcileFailed, Message: "unable to write the secret", ObservedGeneration: 1,
			}),
			want: `CephClusterConnection "cc-1" is not Ready (ReconcileFailed): unable to write the secret`,
		},
		{
			name: "ready for an older generation",
			cc: cephConnectionWithReady(2, &metav1.Condition{
				Type: storagev1alpha1.ConditionTypeReady, Status: metav1.ConditionTrue,
				Reason: conditions.ReasonReconciled, ObservedGeneration: 1,
			}),
			want: `CephClusterConnection "cc-1" has not been reconciled since its last change`,
		},
		{
			name: "ready",
			cc: cephConnectionWithReady(2, &metav1.Condition{
				Type: storagev1alpha1.ConditionTypeReady, Status: metav1.ConditionTrue,
				Reason: conditions.ReasonReconciled, ObservedGeneration: 2,
			}),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := connectionNotReady(tc.cc)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("got %v, want nil", err)
				}
				return
			}
			var waitErr *waitingForConnectionError
			if !errors.As(err, &waitErr) {
				t.Fatalf("got %v, want a waitingForConnectionError", err)
			}
			if err.Error() != tc.want {
				t.Errorf("message = %q, want %q", err.Error(), tc.want)
			}
		})
	}
}

func newCephClassConnectionTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()

	scheme := apiruntime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("adding scheme: %v", err)
	}
	if err := storagev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding scheme: %v", err)
	}

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&storagev1alpha1.CephStorageClass{}, &storagev1alpha1.CephClusterConnection{}).
		WithObjects(objs...).
		Build()
}

func cephClassForConnectionTest() *storagev1alpha1.CephStorageClass {
	return &storagev1alpha1.CephStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: "sc-1", Generation: 1},
		Spec: storagev1alpha1.CephStorageClassSpec{
			ClusterConnectionName: "cc-1",
			ReclaimPolicy:         "Delete",
			Type:                  storagev1alpha1.CephStorageClassTypeRBD,
			RBD: &storagev1alpha1.CephStorageClassRBD{
				DefaultFSType: "ext4",
				Pool:          "pool",
			},
		},
	}
}

func reconcileCephClassAndPublish(t *testing.T, cl client.Client, cephSC *storagev1alpha1.CephStorageClass) (shouldRequeue bool, ready *metav1.Condition, got *storagev1alpha1.CephStorageClass, reconcileErr error) {
	t.Helper()
	ctx := context.Background()

	scList := &v1.StorageClassList{}
	if err := cl.List(ctx, scList); err != nil {
		t.Fatalf("listing StorageClasses: %v", err)
	}
	shouldRequeue, msg, reconcileErr := RunStorageClassEventReconcile(ctx, cl, logger.NewNop(), scList, nil, cephSC, "d8-csi-ceph", nil)
	if err := updateCephStorageClassStatus(ctx, cl, nil, cephSC, reconcileErr, msg); err != nil {
		t.Fatalf("updating status: %v", err)
	}
	got = &storagev1alpha1.CephStorageClass{}
	if err := cl.Get(ctx, client.ObjectKey{Name: cephSC.Name}, got); err != nil {
		t.Fatalf("reading back: %v", err)
	}
	return shouldRequeue, conditions.Get(got.Status.Conditions, storagev1alpha1.ConditionTypeReady), got, reconcileErr
}

func TestCephStorageClassWaitsForAMissingConnection(t *testing.T) {
	cephSC := cephClassForConnectionTest()
	cl := newCephClassConnectionTestClient(t, cephSC)

	_, ready, got, reconcileErr := reconcileCephClassAndPublish(t, cl, cephSC)
	if reconcileErr == nil {
		t.Fatal("a class without its connection must not be Ready")
	}
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != conditions.ReasonWaitingForDependency {
		t.Fatalf("Ready = %+v, want False/%s", ready, conditions.ReasonWaitingForDependency)
	}
	if want := `CephClusterConnection "cc-1" does not exist`; ready.Message != want {
		t.Errorf("message = %q, want %q", ready.Message, want)
	}
	if got.Status.Phase != internal.PhaseFailed {
		t.Errorf("phase = %q, want %q", got.Status.Phase, internal.PhaseFailed)
	}
	if got.Status.Reason != ready.Message {
		t.Errorf("status.reason = %q, want it to repeat the Ready message %q", got.Status.Reason, ready.Message)
	}
}

func TestCephStorageClassIsReconciledButWaitsForAConnectionThatIsNotReady(t *testing.T) {
	cephSC := cephClassForConnectionTest()
	cc := cephConnectionWithReady(1, &metav1.Condition{
		Type: storagev1alpha1.ConditionTypeReady, Status: metav1.ConditionFalse,
		Reason: storagev1alpha1.ReasonPersistentVolumesExist, Message: "PersistentVolumes still need the secret: [pv]", ObservedGeneration: 1,
	})
	cl := newCephClassConnectionTestClient(t, cephSC, cc)

	shouldRequeue, ready, got, _ := reconcileCephClassAndPublish(t, cl, cephSC)
	if shouldRequeue {
		t.Error("the connection watch brings the class back, a requeue is not needed")
	}
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != conditions.ReasonWaitingForDependency {
		t.Fatalf("Ready = %+v, want False/%s", ready, conditions.ReasonWaitingForDependency)
	}
	if !strings.Contains(ready.Message, "(PersistentVolumesExist): PersistentVolumes still need the secret: [pv]") {
		t.Errorf("message = %q, want it to carry the reason and message of the connection", ready.Message)
	}

	sc := &v1.StorageClass{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: cephSC.Name}, sc); err != nil {
		t.Fatalf("the StorageClass must be created while the connection is not Ready: %v", err)
	}
	if shouldRetryFailedStorageClass(got.Status) {
		t.Error("WaitingForDependency must not recreate the StorageClass")
	}
}

func TestCephStorageClassTurnsReadyOnceTheConnectionIs(t *testing.T) {
	cephSC := cephClassForConnectionTest()
	cc := cephConnectionWithReady(1, &metav1.Condition{
		Type: storagev1alpha1.ConditionTypeReady, Status: metav1.ConditionTrue,
		Reason: conditions.ReasonReconciled, ObservedGeneration: 1,
	})
	cl := newCephClassConnectionTestClient(t, cephSC, cc)

	_, ready, got, reconcileErr := reconcileCephClassAndPublish(t, cl, cephSC)
	if reconcileErr != nil {
		t.Fatalf("reconcile: %v", reconcileErr)
	}
	if ready == nil || ready.Status != metav1.ConditionTrue || ready.Reason != conditions.ReasonReconciled {
		t.Fatalf("Ready = %+v, want True/%s", ready, conditions.ReasonReconciled)
	}
	if got.Status.Phase != internal.PhaseCreated {
		t.Errorf("phase = %q, want %q", got.Status.Phase, internal.PhaseCreated)
	}
}

func TestCephClusterConnectionClassHandler(t *testing.T) {
	mine := cephClassForConnectionTest()
	other := cephClassForConnectionTest()
	other.Name = "sc-other"
	other.Spec.ClusterConnectionName = "cc-other"
	cl := newCephClassConnectionTestClient(t, mine, other)
	h := cephClusterConnectionClassHandler(cl, logger.NewNop())

	notReady := cephConnectionWithReady(1, &metav1.Condition{
		Type: storagev1alpha1.ConditionTypeReady, Status: metav1.ConditionFalse, Reason: conditions.ReasonReconcileFailed, ObservedGeneration: 1,
	})
	ready := cephConnectionWithReady(1, &metav1.Condition{
		Type: storagev1alpha1.ConditionTypeReady, Status: metav1.ConditionTrue, Reason: conditions.ReasonReconciled, ObservedGeneration: 1,
	})
	relabelled := ready.DeepCopy()
	relabelled.Labels = map[string]string{"a": "b"}

	for _, tc := range []struct {
		name string
		fire func(q workqueue.TypedRateLimitingInterface[reconcile.Request])
		want int
	}{
		{"created", func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			h.Create(context.Background(), event.TypedCreateEvent[*storagev1alpha1.CephClusterConnection]{Object: ready}, q)
		}, 1},
		{"Ready changed", func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			h.Update(context.Background(), event.TypedUpdateEvent[*storagev1alpha1.CephClusterConnection]{ObjectOld: notReady, ObjectNew: ready}, q)
		}, 1},
		{"Ready unchanged", func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			h.Update(context.Background(), event.TypedUpdateEvent[*storagev1alpha1.CephClusterConnection]{ObjectOld: ready, ObjectNew: relabelled}, q)
		}, 0},
		{"deleted", func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			h.Delete(context.Background(), event.TypedDeleteEvent[*storagev1alpha1.CephClusterConnection]{Object: ready}, q)
		}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			defer q.ShutDown()
			tc.fire(q)
			if q.Len() != tc.want {
				t.Fatalf("queued %d requests, want %d", q.Len(), tc.want)
			}
			if tc.want == 1 {
				item, _ := q.Get()
				if item.Name != mine.Name {
					t.Errorf("queued %q, want %q", item.Name, mine.Name)
				}
			}
		})
	}
}
