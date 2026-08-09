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

package v1alpha1

// Condition types published in status.conditions.
//
// These follow the Kubernetes API conventions for typical status properties:
// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties
const (
	// ConditionTypeReady reports whether the controller has fully reconciled
	// the resource:
	//
	//   - True  every object the resource owns is in place;
	//   - False a reconcile pass failed, see the condition message.
	//
	// What "every object" covers is per kind and is documented in the CRD: the
	// StorageClass and the VolumeSnapshotClass for CephStorageClass, the secret
	// and the CSI ConfigMap entry for CephClusterConnection.
	//
	// The condition is absent until the controller has produced its first
	// verdict. That is deliberately not the same as Unknown: no writer emits
	// Unknown, so a consumer waiting for a verdict must treat "no Ready
	// condition" — not "Ready=Unknown" — as the not-yet-observed state.
	//
	// It matches the aggregate Ready condition used across the storage
	// modules; the shared helpers live in
	// github.com/deckhouse/sds-common-lib/conditions.
	ConditionTypeReady = "Ready"

	// ConditionTypeDeprecated reports that the resource kind is superseded and
	// should be migrated away from. It is only published on
	// CephClusterAuthentication, which CephClusterConnection replaced.
	ConditionTypeDeprecated = "Deprecated"
)

// The condition types each kind publishes.
//
// A condition type that is declared but never written is worse than one that
// does not exist: an absent condition is indistinguishable from "not yet
// evaluated", so an operator waits for a verdict that never comes and an alert
// on it never fires. Keeping each set in one list is what lets a test hold the
// controller to it.
var (
	CephClusterConnectionConditionTypes = []string{
		ConditionTypeReady,
	}
	CephStorageClassConditionTypes = []string{
		ConditionTypeReady,
	}
	// CephClusterAuthenticationConditionTypes deliberately omits Ready. The
	// controller does not reconcile this kind towards a desired state — it only
	// marks it as superseded — so there is no readiness to report, and a Ready
	// condition here would be a verdict about nothing.
	CephClusterAuthenticationConditionTypes = []string{
		ConditionTypeDeprecated,
	}
)

// Condition reasons specific to this module. Reasons shared with the other
// storage modules — Reconciled, ReconcileFailed, Pending — come from
// github.com/deckhouse/sds-common-lib/conditions.
const (
	// ReasonSupersededByCephClusterConnection is set on Deprecated=True.
	ReasonSupersededByCephClusterConnection = "SupersededByCephClusterConnection"
)
