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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	storagev1alpha1 "github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/internal"
	"github.com/deckhouse/sds-common-lib/conditions"
)

// phaseFromReady maps the Ready condition onto the deprecated status.phase of
// CephStorageClass and CephClusterConnection: Created for True, Failed for
// False, and no phase while there is no verdict.
func phaseFromReady(conds []metav1.Condition) string {
	switch {
	case conditions.IsTrue(conds, storagev1alpha1.ConditionTypeReady):
		return internal.PhaseCreated
	case conditions.IsFalse(conds, storagev1alpha1.ConditionTypeReady):
		return internal.PhaseFailed
	default:
		return ""
	}
}

// cephStorageClassConditions and cephClusterConnectionConditions are how
// conditions.WithEvents reads the conditions of the two kinds. Both run on an
// object as read from the API server, whose status may still be nil.
func cephStorageClassConditions(sc *storagev1alpha1.CephStorageClass) []metav1.Condition {
	if sc.Status == nil {
		return nil
	}
	return sc.Status.Conditions
}

func cephClusterConnectionConditions(cc *storagev1alpha1.CephClusterConnection) []metav1.Condition {
	if cc.Status == nil {
		return nil
	}
	return cc.Status.Conditions
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
// into internal.UpdateReconcile, which recreates the StorageClass — a delete
// followed by a create — whenever its spec differs. An absent Ready condition is
// not a failure: it is what every object written before this module published
// conditions looks like, and treating it as one would send every StorageClass
// through that path on every upgrade.
//
// The phase is consulted only for those pre-conditions objects, so that ones
// that did fail before the upgrade are still picked up. Once the condition
// exists it is the only thing that counts. WaitingForDependency is not a
// failure of the class: the connection watch brings it back.
func shouldRetryFailedStorageClass(status *storagev1alpha1.CephStorageClassStatus) bool {
	if status == nil {
		return false
	}
	if conditions.Get(status.Conditions, storagev1alpha1.ConditionTypeReady) == nil {
		return status.Phase == internal.PhaseFailed
	}
	ready := conditions.Get(status.Conditions, storagev1alpha1.ConditionTypeReady)
	return ready.Status == metav1.ConditionFalse && ready.Reason != conditions.ReasonWaitingForDependency
}

// waitingForConnectionError is the verdict of a CephStorageClass pass that did
// its own part but whose CephClusterConnection is missing or not Ready.
type waitingForConnectionError struct {
	msg string
}

func (e *waitingForConnectionError) Error() string { return e.msg }

func connectionMissing(name string) error {
	return &waitingForConnectionError{msg: fmt.Sprintf("CephClusterConnection %q does not exist", name)}
}

// connectionNotReady returns a waitingForConnectionError unless the Ready
// condition of cc is True for its current generation.
func connectionNotReady(cc *storagev1alpha1.CephClusterConnection) error {
	ready := conditions.Get(cephClusterConnectionConditions(cc), storagev1alpha1.ConditionTypeReady)
	switch {
	case ready == nil:
		return &waitingForConnectionError{msg: fmt.Sprintf("CephClusterConnection %q is not Ready (%s)", cc.Name, conditions.ReasonPending)}
	case ready.Status != metav1.ConditionTrue:
		msg := fmt.Sprintf("CephClusterConnection %q is not Ready (%s)", cc.Name, ready.Reason)
		if ready.Message != "" {
			msg += ": " + ready.Message
		}
		return &waitingForConnectionError{msg: msg}
	case ready.ObservedGeneration < cc.Generation:
		return &waitingForConnectionError{msg: fmt.Sprintf("CephClusterConnection %q has not been reconciled since its last change", cc.Name)}
	}
	return nil
}
