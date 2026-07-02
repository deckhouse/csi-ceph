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

package tests

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	storagekube "github.com/deckhouse/storage-e2e/pkg/kubernetes"
)

const moduleNamespace = "d8-csi-ceph"

// dumpFailedSpecDiagnostics emits a best-effort, self-contained dump describing
// why a spec failed: the ElasticStorageClass conditions, the csi-ceph CRs it is
// responsible for (CephClusterConnection / CephStorageClass), the csi-ceph module
// pods, and recent Warning events in the module and test namespaces. Framed with
// markers so it can be sliced out of the run log. Callers invoke this only when
// CurrentSpecReport().Failed(); this function does NOT re-check that condition.
func dumpFailedSpecDiagnostics(ctx context.Context) {
	GinkgoWriter.Printf("\n========== csi-ceph e2e failure diagnostics ==========\n")

	dumpESCConditions(ctx, escRBDName())
	dumpESCConditions(ctx, escCephFSName())

	dumpCR(ctx, cephClusterConnectionGVR, "CephClusterConnection")
	dumpCR(ctx, cephStorageClassGVR, "CephStorageClass")

	dumpModulePods(ctx)
	dumpWarningEvents(ctx, moduleNamespace)
	dumpWarningEvents(ctx, suiteCfg.namespace)

	GinkgoWriter.Printf("======================================================\n")
}

func dumpESCConditions(ctx context.Context, escName string) {
	GinkgoWriter.Printf("--- ElasticStorageClass %s conditions ---\n", escName)
	for _, cond := range []string{
		storagekube.ElasticStorageClassConditionPoolReady,
		storagekube.ElasticStorageClassConditionCsiStorageClassReady,
	} {
		status, reason, message, found, err := storagekube.GetElasticStorageClassCondition(ctx, suiteRestCfg, escName, cond)
		if err != nil {
			GinkgoWriter.Printf("  %s: <error: %v>\n", cond, err)
			continue
		}
		if !found {
			GinkgoWriter.Printf("  %s: <absent>\n", cond)
			continue
		}
		GinkgoWriter.Printf("  %s: status=%s reason=%s message=%q\n", cond, status, reason, message)
	}
}

func dumpCR(ctx context.Context, gvr schema.GroupVersionResource, kind string) {
	GinkgoWriter.Printf("--- %s (%s) ---\n", kind, gvr.Resource)
	list, err := suiteDyn.Resource(gvr).List(ctx, metav1.ListOptions{})
	if err != nil {
		GinkgoWriter.Printf("  <error listing: %v>\n", err)
		return
	}
	if len(list.Items) == 0 {
		GinkgoWriter.Printf("  <none>\n")
		return
	}
	for i := range list.Items {
		item := &list.Items[i]
		phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")
		GinkgoWriter.Printf("  %s (phase=%q)\n", item.GetName(), phase)
	}
}

func dumpModulePods(ctx context.Context) {
	GinkgoWriter.Printf("--- pods in %s ---\n", moduleNamespace)
	var pods corev1.PodList
	if err := suiteK8s.List(ctx, &pods, client.InNamespace(moduleNamespace)); err != nil {
		GinkgoWriter.Printf("  <error listing pods: %v>\n", err)
		return
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		GinkgoWriter.Printf("  %s: phase=%s ready=%v\n", p.Name, p.Status.Phase, isPodReady(p))
	}
}

func dumpWarningEvents(ctx context.Context, namespace string) {
	GinkgoWriter.Printf("--- recent Warning events in %s ---\n", namespace)
	var events corev1.EventList
	if err := suiteK8s.List(ctx, &events, client.InNamespace(namespace)); err != nil {
		GinkgoWriter.Printf("  <error listing events: %v>\n", err)
		return
	}
	count := 0
	for i := range events.Items {
		e := &events.Items[i]
		if e.Type != corev1.EventTypeWarning {
			continue
		}
		GinkgoWriter.Printf("  %s\n", fmt.Sprintf("%s/%s: %s: %s", e.InvolvedObject.Kind, e.InvolvedObject.Name, e.Reason, e.Message))
		count++
		if count >= 30 {
			GinkgoWriter.Printf("  ... (truncated)\n")
			break
		}
	}
	if count == 0 {
		GinkgoWriter.Printf("  <none>\n")
	}
}
