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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	storagev1alpha1 "github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/internal"
	"github.com/deckhouse/sds-common-lib/conditions"
)

// phaseFromReady maps the Ready condition onto this module's coarse phase
// vocabulary. CephStorageClass and CephClusterConnection share it: their phase
// vocabularies are identical.
//
// It is a pure function of the conditions and never consults the previous
// phase, so a resource cannot get stranded in a phase the way it can with a
// phase-to-phase state machine. Anything other than Ready=True is Failed, which
// matches the behaviour before conditions existed: the phase was Created only
// when the reconcile pass returned no error.
func phaseFromReady(conds []metav1.Condition) string {
	if conditions.IsTrue(conds, storagev1alpha1.ConditionTypeReady) {
		return internal.PhaseCreated
	}
	return internal.PhaseFailed
}

// shouldPublishStatus reports whether a reconcile pass should record its outcome
// in the resource status.
//
// Every pass over a live object publishes, including a successful one that had
// nothing to say: a Ready condition that is never written is indistinguishable
// from one that was never evaluated.
//
// The single exception is a pass that succeeded on an object being deleted. That
// pass has just dropped the finalizer, so there is no object left to write to. A
// pass that failed during deletion left the finalizer in place, and the status is
// then the only in-cluster signal of what is blocking the teardown — which is why
// the caller writes it, and why it tolerates a NotFound from the write rather
// than skipping it.
func shouldPublishStatus(obj metav1.Object, reconcileErr error, shouldRequeue bool) bool {
	if obj.GetDeletionTimestamp() == nil {
		return true
	}
	return reconcileErr != nil || shouldRequeue
}

// shouldRetryFailedStorageClass reports whether the last reconcile pass of a
// CephStorageClass is known to have failed, and so deserves another attempt even
// when the StorageClass itself needs no change.
//
// "Known to have failed" is the operative part. The caller turns a true here
// into internal.UpdateReconcile, whose only implementation is
// recreateStorageClass — a delete followed by a create. An absent Ready
// condition is not a failure: it is what every object written before this module
// published conditions looks like, and treating it as one would delete and
// recreate a StorageClass that is already correct on every upgrade.
//
// The phase is consulted only for those pre-conditions objects, so that ones
// that did fail before the upgrade are still picked up. Once the condition
// exists it is the only thing that counts.
func shouldRetryFailedStorageClass(status *storagev1alpha1.CephStorageClassStatus) bool {
	if status == nil {
		return false
	}
	if conditions.Get(status.Conditions, storagev1alpha1.ConditionTypeReady) == nil {
		return status.Phase == internal.PhaseFailed
	}
	return conditions.IsFalse(status.Conditions, storagev1alpha1.ConditionTypeReady)
}
