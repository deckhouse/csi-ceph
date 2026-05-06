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
	"io"
	"sort"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubernetes "k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// diagControllerLogTail is the per-container log tail used in the on-failure
// diagnostic dump. 200 lines covers the most recent reconcile cycle of any of
// the csi-controller sidecars (provisioner / attacher / resizer / snapshotter /
// livenessprobe / controller / rbdplugin) while keeping the spec output
// readable. We do NOT dump csi-node-* DaemonSets: in this suite the volume is
// never mounted (failures happen at provision time, before any node attach).
const diagControllerLogTail = int64(200)

// dumpFailedSpecDiagnostics emits a self-contained, best-effort dump describing
// why a spec failed: the offending PVC, its events, per-container logs of the
// matching csi-controller-{rbd,cephfs} pods, and the three sources of truth for
// ms_crc_data — server-side rook-config-override, client-side ceph-config CM
// and ModuleConfig.spec.settings.msCrcData.
//
// Output is framed with `========== diagnostics on failure ==========` /
// `========== /diagnostics ==========` so it can be sliced out of the run log
// with a single grep. All probes are best-effort: any individual failure is
// logged inline as a warning line and does not abort the rest of the dump.
//
// The caller is expected to invoke this only when CurrentSpecReport().Failed()
// is true; this function does NOT re-check that condition.
func dumpFailedSpecDiagnostics(ctx context.Context, proto cephProtocol, pvcName string) {
	cs, err := kubernetes.NewForConfig(suiteRestCfg)
	if err != nil {
		fmt.Fprintf(GinkgoWriter, "diag: build clientset: %v\n", err)
		return
	}

	fmt.Fprintln(GinkgoWriter, "\n========== diagnostics on failure ==========")
	fmt.Fprintf(GinkgoWriter, "spec      : %s\n", CurrentSpecReport().FullText())
	fmt.Fprintf(GinkgoWriter, "protocol  : %s\n", proto)
	fmt.Fprintf(GinkgoWriter, "pvc       : %s/%s\n", suiteCfg.namespace, pvcName)

	dumpPVCAndEvents(ctx, cs, suiteCfg.namespace, pvcName)
	dumpRelevantControllerLogs(ctx, cs, proto, diagControllerLogTail)
	dumpCephStackStatus(ctx)
	dumpMsCrcDataState(ctx, cs)

	fmt.Fprintln(GinkgoWriter, "========== /diagnostics ==========")
}

// dumpPVCAndEvents prints the PVC YAML (or NotFound) followed by the Events
// involving that PVC. This is the same data `kubectl describe pvc` would
// surface, sliced to what's actionable: spec / status / events.
func dumpPVCAndEvents(ctx context.Context, cs kubernetes.Interface, namespace, name string) {
	fmt.Fprintf(GinkgoWriter, "\n--- PVC %s/%s ---\n", namespace, name)

	pvc := &corev1.PersistentVolumeClaim{}
	if err := suiteK8s.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, pvc); err != nil {
		if apierrors.IsNotFound(err) {
			fmt.Fprintln(GinkgoWriter, "  PVC not found (already gone)")
		} else {
			fmt.Fprintf(GinkgoWriter, "  get pvc: %v\n", err)
		}
	} else {
		fmt.Fprintf(GinkgoWriter, "  phase=%s\n", pvc.Status.Phase)
		// Strip noisy server-managed fields so the YAML stays readable.
		pvc.ManagedFields = nil
		if y, err := yaml.Marshal(pvc); err == nil {
			GinkgoWriter.Write(y)
		} else {
			fmt.Fprintf(GinkgoWriter, "  marshal pvc: %v\n", err)
		}
	}

	evts, err := cs.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.kind=PersistentVolumeClaim,involvedObject.name=%s", name),
	})
	if err != nil {
		fmt.Fprintf(GinkgoWriter, "  list events: %v\n", err)
		return
	}

	// Sort by event time so the latest message is at the bottom and easy to
	// eyeball without scrolling. Falls back to FirstTimestamp when the
	// LastTimestamp is zero (some controllers only set one).
	items := evts.Items
	sort.SliceStable(items, func(i, j int) bool {
		return eventTime(items[i]).Before(eventTime(items[j]))
	})

	fmt.Fprintf(GinkgoWriter, "--- PVC events (%d) ---\n", len(items))
	for _, e := range items {
		fmt.Fprintf(GinkgoWriter, "  %s [%s/%s] %s: %s\n",
			eventTime(e).Format(time.RFC3339),
			e.Type, e.Reason, e.Source.Component, strings.TrimSpace(e.Message))
	}
}

func eventTime(e corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.FirstTimestamp.IsZero() {
		return e.FirstTimestamp.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.CreationTimestamp.Time
}

// dumpRelevantControllerLogs prints, per pod and per container, the last
// `tail` lines of logs for the csi-controller Deployment that matches the
// failing PVC's protocol. Containers are not hard-coded — we iterate
// pod.Spec.Containers so any change in the chart (e.g. a new sidecar) is
// reflected automatically.
func dumpRelevantControllerLogs(ctx context.Context, cs kubernetes.Interface, proto cephProtocol, tail int64) {
	deployName := rbdControllerDeployment
	if proto == protocolCephFS {
		deployName = cephfsControllerDeployment
	}

	pods, err := cs.CoreV1().Pods(csiCephNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=" + deployName,
	})
	if err != nil {
		fmt.Fprintf(GinkgoWriter, "\nlist pods app=%s: %v\n", deployName, err)
		return
	}
	if len(pods.Items) == 0 {
		fmt.Fprintf(GinkgoWriter, "\nno pods matched app=%s in namespace %s\n", deployName, csiCephNamespace)
		return
	}

	for i := range pods.Items {
		pod := &pods.Items[i]
		fmt.Fprintf(GinkgoWriter, "\n--- pod %s/%s on node %s phase=%s ---\n",
			pod.Namespace, pod.Name, pod.Spec.NodeName, pod.Status.Phase)
		for _, c := range pod.Spec.Containers {
			dumpContainerLogs(ctx, cs, pod.Namespace, pod.Name, c.Name, tail)
		}
	}
}

func dumpContainerLogs(ctx context.Context, cs kubernetes.Interface, namespace, podName, container string, tail int64) {
	req := cs.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: container,
		TailLines: &tail,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		fmt.Fprintf(GinkgoWriter, "logs %s/%s [%s]: %v\n", namespace, podName, container, err)
		return
	}
	defer stream.Close()

	fmt.Fprintf(GinkgoWriter, "\n--- logs %s/%s container=%s tail=%d ---\n", namespace, podName, container, tail)
	if _, err := io.Copy(GinkgoWriter, stream); err != nil {
		fmt.Fprintf(GinkgoWriter, "\n  copy logs: %v\n", err)
	}
}

// dumpCephStackStatus emits status sub-objects of the Rook-managed
// CephCluster, CephFilesystem and CephBlockPool CRs in the suite's Rook
// namespace. The status is the fastest signal for diagnosing why CSI
// provisioning hangs (e.g. CephFilesystem.status.phase != Ready typically
// means MDS is laggy or replaying after a config change).
func dumpCephStackStatus(ctx context.Context) {
	gvrs := []schema.GroupVersionResource{
		{Group: "ceph.rook.io", Version: "v1", Resource: "cephclusters"},
		{Group: "ceph.rook.io", Version: "v1", Resource: "cephfilesystems"},
		{Group: "ceph.rook.io", Version: "v1", Resource: "cephblockpools"},
	}
	for _, gvr := range gvrs {
		list, err := suiteDyn.Resource(gvr).Namespace(suiteCfg.rook.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			fmt.Fprintf(GinkgoWriter, "\nlist %s in %s: %v\n", gvr.Resource, suiteCfg.rook.Namespace, err)
			continue
		}
		for i := range list.Items {
			obj := list.Items[i]
			status, _, _ := unstructured.NestedMap(obj.Object, "status")
			fmt.Fprintf(GinkgoWriter, "\n--- %s %s/%s status ---\n", gvr.Resource, obj.GetNamespace(), obj.GetName())
			if status == nil {
				fmt.Fprintln(GinkgoWriter, "  <empty>")
				continue
			}
			if y, err := yaml.Marshal(status); err == nil {
				GinkgoWriter.Write(y)
			} else {
				fmt.Fprintf(GinkgoWriter, "  marshal status: %v\n", err)
			}
		}
	}
}

// dumpMsCrcDataState writes out all three sources of truth for the
// ms_crc_data setting that this matrix is exercising. A divergence between
// any two of these three is itself actionable diagnostic information.
func dumpMsCrcDataState(ctx context.Context, cs kubernetes.Interface) {
	dumpConfigMapData(ctx, cs, suiteCfg.rook.Namespace, "rook-config-override", "server-side")
	dumpConfigMapData(ctx, cs, csiCephNamespace, cephConfigMap, "client-side")

	val, found, err := getModuleConfigSetting(ctx, suiteDyn, moduleConfigName, "msCrcData")
	switch {
	case err != nil:
		fmt.Fprintf(GinkgoWriter, "\nget ModuleConfig.msCrcData: %v\n", err)
	case !found:
		fmt.Fprintf(GinkgoWriter, "\nModuleConfig %s: msCrcData=<unset>\n", moduleConfigName)
	default:
		fmt.Fprintf(GinkgoWriter, "\nModuleConfig %s: msCrcData=%v\n", moduleConfigName, val)
	}
}

func dumpConfigMapData(ctx context.Context, cs kubernetes.Interface, namespace, name, role string) {
	cm, err := cs.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		fmt.Fprintf(GinkgoWriter, "\nget ConfigMap %s/%s (%s): %v\n", namespace, name, role, err)
		return
	}

	keys := make([]string, 0, len(cm.Data))
	for k := range cm.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Fprintf(GinkgoWriter, "\n--- ConfigMap %s/%s (%s) ---\n", namespace, name, role)
	if len(keys) == 0 {
		fmt.Fprintln(GinkgoWriter, "  <no data>")
		return
	}
	for _, k := range keys {
		fmt.Fprintf(GinkgoWriter, "[%s]\n%s\n", k, cm.Data[k])
	}
}
