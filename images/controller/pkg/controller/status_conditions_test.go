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

package controller

import (
	"context"
	"errors"
	"testing"

	v1 "k8s.io/api/storage/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	storagev1alpha1 "github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/internal"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/logger"
	"github.com/deckhouse/sds-common-lib/conditions"
)

func newStatusTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()

	scheme := apiruntime.NewScheme()
	if err := storagev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding scheme: %v", err)
	}

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(
			&storagev1alpha1.CephStorageClass{},
			&storagev1alpha1.CephClusterConnection{},
			&storagev1alpha1.CephClusterAuthentication{},
		).
		WithObjects(objs...).
		Build()
}

// Each kind declares the condition types it publishes; these check that the
// writers deliver exactly those.
//
// A condition type that is declared but never written is worse than one that
// does not exist: an absent condition is indistinguishable from "not yet
// evaluated", so an operator waits for a verdict that never comes and an alert
// on it never fires. The tests below drive the real writers, so a declared type
// nobody sets, or a type set but never declared, both fail.
func TestEveryDeclaredConditionTypeIsWritten(t *testing.T) {
	ctx := context.Background()

	t.Run("CephStorageClass", func(t *testing.T) {
		sc := &storagev1alpha1.CephStorageClass{}
		sc.Name = "sc"
		cl := newStatusTestClient(t, sc)

		if err := updateCephStorageClassStatus(ctx, cl, sc, nil, "created"); err != nil {
			t.Fatalf("updating status: %v", err)
		}

		got := &storagev1alpha1.CephStorageClass{}
		if err := cl.Get(ctx, client.ObjectKey{Name: "sc"}, got); err != nil {
			t.Fatalf("reading back: %v", err)
		}
		assertConditionTypes(t, "CephStorageClass",
			storagev1alpha1.CephStorageClassConditionTypes, got.Status.Conditions)
	})

	t.Run("CephClusterConnection", func(t *testing.T) {
		cc := &storagev1alpha1.CephClusterConnection{}
		cc.Name = "cc"
		cl := newStatusTestClient(t, cc)

		if err := updateCephClusterConnectionStatus(ctx, cl, cc, nil, "created"); err != nil {
			t.Fatalf("updating status: %v", err)
		}

		got := &storagev1alpha1.CephClusterConnection{}
		if err := cl.Get(ctx, client.ObjectKey{Name: "cc"}, got); err != nil {
			t.Fatalf("reading back: %v", err)
		}
		assertConditionTypes(t, "CephClusterConnection",
			storagev1alpha1.CephClusterConnectionConditionTypes, got.Status.Conditions)
	})

	t.Run("CephClusterAuthentication", func(t *testing.T) {
		cca := &storagev1alpha1.CephClusterAuthentication{}
		cca.Name = "cca"
		cl := newStatusTestClient(t, cca)

		if err := publishDeprecatedCondition(ctx, cl, cca); err != nil {
			t.Fatalf("publishing the condition: %v", err)
		}

		got := &storagev1alpha1.CephClusterAuthentication{}
		if err := cl.Get(ctx, client.ObjectKey{Name: "cca"}, got); err != nil {
			t.Fatalf("reading back: %v", err)
		}
		assertConditionTypes(t, "CephClusterAuthentication",
			storagev1alpha1.CephClusterAuthenticationConditionTypes, got.Status.Conditions)
	})
}

// CephClusterAuthentication publishes no Ready, and that is deliberate: the
// controller does not reconcile it towards a desired state, it only marks it as
// superseded. A Ready condition here would be a verdict about nothing, and a
// False one would look like a fault on a resource that is working exactly as
// intended.
func TestCephClusterAuthenticationPublishesNoReady(t *testing.T) {
	for _, declared := range storagev1alpha1.CephClusterAuthenticationConditionTypes {
		if declared == storagev1alpha1.ConditionTypeReady {
			t.Fatal("CephClusterAuthentication must not declare Ready")
		}
	}

	ctx := context.Background()
	cca := &storagev1alpha1.CephClusterAuthentication{}
	cca.Name = "cca"
	cl := newStatusTestClient(t, cca)

	if err := publishDeprecatedCondition(ctx, cl, cca); err != nil {
		t.Fatalf("publishing the condition: %v", err)
	}

	got := &storagev1alpha1.CephClusterAuthentication{}
	if err := cl.Get(ctx, client.ObjectKey{Name: "cca"}, got); err != nil {
		t.Fatalf("reading back: %v", err)
	}
	for _, c := range got.Status.Conditions {
		if c.Type == storagev1alpha1.ConditionTypeReady {
			t.Error("a Ready condition was published on CephClusterAuthentication")
		}
	}
}

// assertConditionTypes reports any difference between what a kind declares and
// what its writer actually produced.
func assertConditionTypes(t *testing.T, kind string, declared []string, written []metav1.Condition) {
	t.Helper()

	present := map[string]bool{}
	for _, c := range written {
		present[c.Type] = true

		if c.Reason == "" {
			t.Errorf("%s: condition %s needs a machine-readable reason", kind, c.Type)
		}
		if c.Message == "" {
			t.Errorf("%s: condition %s needs a message", kind, c.Type)
		}
		if c.Status == "" {
			t.Errorf("%s: condition %s needs a status", kind, c.Type)
		}
	}

	for _, condType := range declared {
		if !present[condType] {
			t.Errorf("%s declares %s but the writer did not set it", kind, condType)
		}
		delete(present, condType)
	}
	for stray := range present {
		t.Errorf("%s writes %s, which it does not declare", kind, stray)
	}
}

func TestPhaseFromReady(t *testing.T) {
	for _, tc := range []struct {
		name  string
		conds []metav1.Condition
		want  string
	}{
		{
			name:  "ready",
			conds: []metav1.Condition{{Type: storagev1alpha1.ConditionTypeReady, Status: metav1.ConditionTrue}},
			want:  internal.PhaseCreated,
		},
		{
			name:  "not ready",
			conds: []metav1.Condition{{Type: storagev1alpha1.ConditionTypeReady, Status: metav1.ConditionFalse}},
			want:  internal.PhaseFailed,
		},
		{
			name:  "unknown",
			conds: []metav1.Condition{{Type: storagev1alpha1.ConditionTypeReady, Status: metav1.ConditionUnknown}},
			want:  internal.PhaseFailed,
		},
		{
			// Matches the behaviour before conditions existed: the phase was
			// Created only when the reconcile pass returned no error.
			name:  "no conditions at all",
			conds: nil,
			want:  internal.PhaseFailed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := phaseFromReady(tc.conds); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUpdateCephStorageClassStatus_Success(t *testing.T) {
	sc := &storagev1alpha1.CephStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: "sc-1", Generation: 3},
	}
	cl := newStatusTestClient(t, sc)

	if err := updateCephStorageClassStatus(context.Background(), cl, sc, nil, "Successfully reconciled"); err != nil {
		t.Fatalf("updateCephStorageClassStatus: %v", err)
	}

	got := &storagev1alpha1.CephStorageClass{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "sc-1"}, got); err != nil {
		t.Fatalf("reading back: %v", err)
	}

	ready := conditions.Get(got.Status.Conditions, storagev1alpha1.ConditionTypeReady)
	if ready == nil {
		t.Fatal("Ready condition was not published")
	}
	if ready.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %q, want True", ready.Status)
	}
	if ready.Reason != conditions.ReasonReconciled {
		t.Errorf("reason = %q, want %q", ready.Reason, conditions.ReasonReconciled)
	}
	if ready.Message != "Successfully reconciled" {
		t.Errorf("message = %q", ready.Message)
	}
	if got.Status.Phase != internal.PhaseCreated {
		t.Errorf("phase = %q, want %q", got.Status.Phase, internal.PhaseCreated)
	}
	if got.Status.ObservedGeneration != 3 {
		t.Errorf("observedGeneration = %d, want 3", got.Status.ObservedGeneration)
	}
}

func TestUpdateCephStorageClassStatus_Failure(t *testing.T) {
	sc := &storagev1alpha1.CephStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: "sc-1", Generation: 1},
	}
	cl := newStatusTestClient(t, sc)

	reconcileErr := errors.New("unable to create the StorageClass")
	if err := updateCephStorageClassStatus(context.Background(), cl, sc, reconcileErr, "some message"); err != nil {
		t.Fatalf("updateCephStorageClassStatus: %v", err)
	}

	got := &storagev1alpha1.CephStorageClass{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "sc-1"}, got); err != nil {
		t.Fatalf("reading back: %v", err)
	}

	ready := conditions.Get(got.Status.Conditions, storagev1alpha1.ConditionTypeReady)
	if ready == nil || ready.Status != metav1.ConditionFalse {
		t.Fatalf("expected Ready=False, got %+v", ready)
	}
	if ready.Reason != conditions.ReasonReconcileFailed {
		t.Errorf("reason = %q, want %q", ready.Reason, conditions.ReasonReconcileFailed)
	}
	// The error text wins over the pass message: it is the more specific
	// explanation of why the resource is not ready.
	if ready.Message != reconcileErr.Error() {
		t.Errorf("message = %q, want the error text", ready.Message)
	}
	if got.Status.Phase != internal.PhaseFailed {
		t.Errorf("phase = %q, want %q", got.Status.Phase, internal.PhaseFailed)
	}
}

// observedGeneration must name the generation that was actually reconciled. If
// the spec changes while a pass is in flight, reporting the newer generation
// would claim the controller had acted on a spec it never saw — which is the
// one thing a bare status value cannot express and the whole reason the field
// exists.
func TestUpdateCephStorageClassStatus_ObservedGenerationIsTheReconciledOne(t *testing.T) {
	const (
		reconciledGeneration = 4
		currentGeneration    = 5
	)

	// On the API server the spec has already moved on to generation 5.
	// The fake client does not maintain metadata.generation itself, so both
	// generations are set explicitly rather than produced by an update.
	sc := &storagev1alpha1.CephStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: "sc-1", Generation: currentGeneration},
	}
	cl := newStatusTestClient(t, sc)

	// What the reconcile pass read and acted on.
	reconciled := sc.DeepCopy()
	reconciled.Generation = reconciledGeneration

	if err := updateCephStorageClassStatus(context.Background(), cl, reconciled, nil, "ok"); err != nil {
		t.Fatalf("updateCephStorageClassStatus: %v", err)
	}

	got := &storagev1alpha1.CephStorageClass{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "sc-1"}, got); err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if got.Status.ObservedGeneration != reconciledGeneration {
		t.Errorf("observedGeneration = %d, want the reconciled generation %d",
			got.Status.ObservedGeneration, reconciledGeneration)
	}
	ready := conditions.Get(got.Status.Conditions, storagev1alpha1.ConditionTypeReady)
	if ready == nil {
		t.Fatal("Ready condition was not published")
	}
	if ready.ObservedGeneration != reconciledGeneration {
		t.Errorf("condition observedGeneration = %d, want %d",
			ready.ObservedGeneration, reconciledGeneration)
	}

	// And the consumer-facing consequence: a reader can tell that the verdict
	// is about an older spec than the one currently stored.
	if !conditions.IsStale(got.Status.Conditions, storagev1alpha1.ConditionTypeReady, currentGeneration) {
		t.Error("the Ready condition should read as stale against the current generation")
	}
}

func TestUpdateCephStorageClassStatus_SkipsWriteWhenNothingChanges(t *testing.T) {
	sc := &storagev1alpha1.CephStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: "sc-1", Generation: 1},
	}
	cl := newStatusTestClient(t, sc)

	if err := updateCephStorageClassStatus(context.Background(), cl, sc, nil, "ok"); err != nil {
		t.Fatalf("first update: %v", err)
	}

	first := &storagev1alpha1.CephStorageClass{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "sc-1"}, first); err != nil {
		t.Fatalf("reading: %v", err)
	}

	if err := updateCephStorageClassStatus(context.Background(), cl, sc, nil, "ok"); err != nil {
		t.Fatalf("second update: %v", err)
	}

	second := &storagev1alpha1.CephStorageClass{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "sc-1"}, second); err != nil {
		t.Fatalf("reading: %v", err)
	}

	// A periodic resync that changes nothing must not write: otherwise every
	// requeue produces an etcd write and a watch event for every object.
	if second.ResourceVersion != first.ResourceVersion {
		t.Fatalf("expected no write, resourceVersion moved %s -> %s",
			first.ResourceVersion, second.ResourceVersion)
	}
}

func TestUpdateCephClusterConnectionStatus(t *testing.T) {
	cc := &storagev1alpha1.CephClusterConnection{
		ObjectMeta: metav1.ObjectMeta{Name: "cc-1", Generation: 2},
	}
	cl := newStatusTestClient(t, cc)

	if err := updateCephClusterConnectionStatus(context.Background(), cl, cc, nil, "Successfully reconciled"); err != nil {
		t.Fatalf("updateCephClusterConnectionStatus: %v", err)
	}

	got := &storagev1alpha1.CephClusterConnection{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "cc-1"}, got); err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if !conditions.IsTrue(got.Status.Conditions, storagev1alpha1.ConditionTypeReady) {
		t.Errorf("expected Ready=True, got %+v", got.Status.Conditions)
	}
	if got.Status.Phase != internal.PhaseCreated {
		t.Errorf("phase = %q, want %q", got.Status.Phase, internal.PhaseCreated)
	}
	if got.Status.ObservedGeneration != 2 {
		t.Errorf("observedGeneration = %d, want 2", got.Status.ObservedGeneration)
	}
}

// The deprecation used to be visible only as a label, which neither
// `kubectl get` nor `kubectl describe` surfaces by default.
func TestPublishDeprecatedCondition(t *testing.T) {
	cca := &storagev1alpha1.CephClusterAuthentication{
		ObjectMeta: metav1.ObjectMeta{Name: "cca-1", Generation: 1},
	}
	cl := newStatusTestClient(t, cca)

	if err := publishDeprecatedCondition(context.Background(), cl, cca); err != nil {
		t.Fatalf("publishDeprecatedCondition: %v", err)
	}

	got := &storagev1alpha1.CephClusterAuthentication{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "cca-1"}, got); err != nil {
		t.Fatalf("reading back: %v", err)
	}

	dep := conditions.Get(got.Status.Conditions, storagev1alpha1.ConditionTypeDeprecated)
	if dep == nil || dep.Status != metav1.ConditionTrue {
		t.Fatalf("expected Deprecated=True, got %+v", dep)
	}
	if dep.Reason != storagev1alpha1.ReasonSupersededByCephClusterConnection {
		t.Errorf("reason = %q", dep.Reason)
	}
	if got.Status.Reason == "" {
		t.Error("status.reason should carry the deprecation message for the printer column")
	}

	// This kind is not reconciled towards a desired state, so claiming
	// readiness either way would be meaningless.
	if conditions.Get(got.Status.Conditions, storagev1alpha1.ConditionTypeReady) != nil {
		t.Error("CephClusterAuthentication must not publish a Ready condition")
	}
}

// A pass over a live object always publishes. A pass over an object being
// deleted publishes only when it failed — see shouldPublishStatus.
func TestShouldPublishStatus(t *testing.T) {
	deleting := metav1.Now()

	for _, tc := range []struct {
		name          string
		deletedAt     *metav1.Time
		reconcileErr  error
		shouldRequeue bool
		want          bool
	}{
		{"live object, successful pass", nil, nil, false, true},
		{"live object, failed pass", nil, errors.New("boom"), false, true},
		{"live object, requeue asked for", nil, nil, true, true},

		// The finalizer has just been dropped, so there is nothing left to
		// write to.
		{"deletion succeeded", &deleting, nil, false, false},

		// The finalizer is still held and the object is alive. Its status is
		// the only in-cluster signal of what is blocking the teardown.
		{"deletion failed", &deleting, errors.New("cannot delete the StorageClass"), true, true},
		{"deletion asked for a requeue", &deleting, nil, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := &storagev1alpha1.CephStorageClass{
				ObjectMeta: metav1.ObjectMeta{Name: "sc-1", DeletionTimestamp: tc.deletedAt},
			}

			if got := shouldPublishStatus(obj, tc.reconcileErr, tc.shouldRequeue); got != tc.want {
				t.Errorf("shouldPublishStatus = %t, want %t", got, tc.want)
			}
		})
	}
}

// Both watchers tolerate a NotFound from the status write instead of skipping
// the write altogether, which only works if the write actually reports NotFound
// when the object is gone. This pins that for both writers.
func TestUpdateStatus_ReportsNotFoundForAGoneObject(t *testing.T) {
	ctx := context.Background()
	cl := newStatusTestClient(t)

	t.Run("CephStorageClass", func(t *testing.T) {
		gone := &storagev1alpha1.CephStorageClass{ObjectMeta: metav1.ObjectMeta{Name: "sc-1"}}
		if err := updateCephStorageClassStatus(ctx, cl, gone, nil, "ok"); !k8serr.IsNotFound(err) {
			t.Fatalf("err = %v, want a NotFound", err)
		}
	})

	t.Run("CephClusterConnection", func(t *testing.T) {
		gone := &storagev1alpha1.CephClusterConnection{ObjectMeta: metav1.ObjectMeta{Name: "cc-1"}}
		if err := updateCephClusterConnectionStatus(ctx, cl, gone, nil, "ok"); !k8serr.IsNotFound(err) {
			t.Fatalf("err = %v, want a NotFound", err)
		}
	})
}

// The status-driven retry must not be reachable for an object whose reconcile
// never failed.
//
// The caller turns UpdateReconcile into recreateStorageClass — a delete followed
// by a create. An object written before this module published conditions carries
// phase=Created and no condition at all; reading that as "not ready" would delete
// and recreate every correct StorageClass in the cluster on upgrade.
func TestIdentifyReconcileFuncForStorageClass_StatusDrivenRetry(t *testing.T) {
	const (
		controllerNamespace = "d8-csi-ceph"
		clusterID           = "cluster-id"
	)

	log, err := logger.NewLogger("0")
	if err != nil {
		t.Fatalf("building the logger: %v", err)
	}

	readyStatus := func(status metav1.ConditionStatus, phase string) *storagev1alpha1.CephStorageClassStatus {
		return &storagev1alpha1.CephStorageClassStatus{
			Phase: phase,
			Conditions: []metav1.Condition{{
				Type:   storagev1alpha1.ConditionTypeReady,
				Status: status,
				Reason: conditions.ReasonReconciled,
			}},
		}
	}

	for _, tc := range []struct {
		name   string
		status *storagev1alpha1.CephStorageClassStatus
		want   string
	}{
		{"upgraded from a controller that predates conditions",
			&storagev1alpha1.CephStorageClassStatus{Phase: internal.PhaseCreated}, ""},
		{"failed before the upgrade, still no conditions",
			&storagev1alpha1.CephStorageClassStatus{Phase: internal.PhaseFailed}, internal.UpdateReconcile},
		{"ready", readyStatus(metav1.ConditionTrue, internal.PhaseCreated), ""},
		{"not ready", readyStatus(metav1.ConditionFalse, internal.PhaseFailed), internal.UpdateReconcile},
		{"no status at all", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cephSC := &storagev1alpha1.CephStorageClass{
				ObjectMeta: metav1.ObjectMeta{Name: "sc-1"},
				Spec: storagev1alpha1.CephStorageClassSpec{
					ClusterConnectionName: "cc-1",
					ReclaimPolicy:         "Delete",
					Type:                  storagev1alpha1.CephStorageClassTypeRBD,
					RBD: &storagev1alpha1.CephStorageClassRBD{
						DefaultFSType: "ext4",
						Pool:          "pool",
					},
				},
				Status: tc.status,
			}

			// The StorageClass exactly as a successful reconcile pass would
			// have left it, so nothing but the status can drive the verdict.
			existing := ConfigureStorageClass(cephSC, controllerNamespace, clusterID, nil)
			scList := &v1.StorageClassList{Items: []v1.StorageClass{*existing}}

			got, err := IdentifyReconcileFuncForStorageClass(*log, scList, cephSC, controllerNamespace, clusterID, nil)
			if err != nil {
				t.Fatalf("identifying the reconcile func: %v", err)
			}
			if got != tc.want {
				t.Errorf("reconcile type = %q, want %q", got, tc.want)
			}
		})
	}
}
