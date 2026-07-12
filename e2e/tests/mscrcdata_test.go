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
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	storagekube "github.com/deckhouse/storage-e2e/pkg/kubernetes"
	"github.com/deckhouse/storage-e2e/pkg/testkit"
)

// moduleConfigGVR addresses the cluster-scoped Deckhouse ModuleConfig the suite
// patches to flip the csi-ceph `msCrcData` config value (the client side of the
// CRC knob).
var moduleConfigGVR = schema.GroupVersionResource{
	Group: "deckhouse.io", Version: "v1alpha1", Resource: "moduleconfigs",
}

// cephConfigMapName is the ConfigMap csi-ceph renders `ceph.conf` into; its
// `ms_crc_data` line is driven directly by `.Values.csiCeph.msCrcData`.
const cephConfigMapName = "ceph-config"

// cephConfigRolloutTimeout bounds how long we wait for the csi-ceph CSI
// controller/node workloads to roll after the ceph-config checksum changes.
const cephConfigRolloutTimeout = 10 * time.Minute

// csiWorkload identifies one csi-ceph CSI workload that carries the
// `checksum/ceph-config` pod annotation and therefore rolls when the
// ceph-config ConfigMap changes.
type csiWorkload struct {
	kind string // "Deployment" | "DaemonSet"
	name string
}

// csiCephWorkloads are the RBD and CephFS CSI controller Deployments and node
// DaemonSets. Both drivers are enabled by default (rbdEnabled/cephfsEnabled), so
// all four carry the checksum annotation and must roll on a ceph.conf change.
var csiCephWorkloads = []csiWorkload{
	{kind: "Deployment", name: "csi-controller-rbd"},
	{kind: "DaemonSet", name: "csi-node-rbd"},
	{kind: "Deployment", name: "csi-controller-cephfs"},
	{kind: "DaemonSet", name: "csi-node-cephfs"},
}

const cephConfigChecksumAnnotation = "checksum/ceph-config"

// msCrcDataSpecs registers a self-contained, label-gated ("msCrcData") suite that
// verifies the openapi `msCrcData` option end to end on a cluster that is *born*
// CRC-off — never live-reconfigured.
//
// Why label-gated and born-off, not a live flip on the shared cluster:
// `ms_crc_data` is NOT a per-frame-tolerated setting. A client/server mismatch is
// fatal — the moment the csi-ceph client (and the rook-operator, which reads the
// same rook-config-override) runs ms_crc_data=false while the Ceph daemons still
// run the default true, every connection to the mons times out (operator
// HEALTH_ERR, provisioning hangs). And flipping a *running* cluster is
// destructive: blindly rollout-restarting the PVC-backed OSDs
// (Rook storageClassDeviceSets) races Rook's OSD lifecycle, so the new OSD pod
// points at a deleted data PVC ("persistentvolumeclaim set1-data-... not found")
// and the OSDs stay Pending; and changing rook-config-override on a live cluster
// deadlocks (the operator can't drive the mon reconfigure through the very
// mismatch it just created). Both were observed on the live e2e cluster.
//
// So this suite sets BOTH sides to false BEFORE the ElasticCluster is created:
// the csi-ceph client (ModuleConfig) and the sds-elastic rook-config-override.
// Every Ceph daemon + the operator + the csi-ceph client then start matched at
// ms_crc_data=false, the cluster comes up healthy, and the data path is exercised
// against a coherent CRC-off cluster (a residual mismatch would simply hang the
// EC-Ready wait, surfacing as a clear failure rather than a wedged cluster).
//
// It runs ONLY under the `e2e/label:msCrcData` PR label; the default e2e run uses
// label_filter '!msCrcData' and skips it. The shared-suite EC/ESC/lifecycle specs
// are unlabeled, so they are filtered out under that label — hence this block
// stands up its own EC/ESCs. It reuses the shared EC/ESC names so the root
// AfterAll (teardownFixtures) reclaims them.
func msCrcDataSpecs() {
	Describe("msCrcData CRC-off suite", Ordered, Label("msCrcData"), func() {
		It("configures CRC off on the csi-ceph client and the sds-elastic server before Ceph bringup", func() {
			ctx, cancel := context.WithTimeout(context.Background(), cephConfigRolloutTimeout+5*time.Minute)
			defer cancel()

			By("asserting ceph.conf starts at the ms_crc_data=true default")
			got, err := cephConfigMsCrcData(ctx)
			Expect(err).NotTo(HaveOccurred(), "read %s/%s", moduleNamespace, cephConfigMapName)
			Expect(got).To(Equal("true"), "ceph.conf ms_crc_data should default to true (openapi default)")

			By("recording the current ceph-config checksum on each CSI workload")
			before, err := csiWorkloadChecksums(ctx)
			Expect(err).NotTo(HaveOccurred(), "read ceph-config checksums")

			By("setting msCrcData=false on the csi-ceph ModuleConfig (client side)")
			Expect(setModuleMsCrcData(ctx, false)).To(Succeed(), "patch csi-ceph ModuleConfig")

			By("waiting for ceph.conf to render ms_crc_data = false and the CSI pods to roll")
			Expect(waitCephConfigMsCrcData(ctx, "false", 3*time.Minute)).
				To(Succeed(), "ceph.conf should show ms_crc_data=false")
			Expect(waitCsiWorkloadsRolled(ctx, before, cephConfigRolloutTimeout)).
				To(Succeed(), "csi-ceph CSI pods should restart on the ceph-config change")

			By("writing ms_crc_data=false into the sds-elastic rook-config-override (server side) before the EC exists")
			enabled := false
			Expect(setServerMsCrcData(ctx, &enabled)).
				To(Succeed(), "configure server-side ms_crc_data in %s", suiteCfg.sdsElasticNamespace)
		})

		It("brings up a born-CRC-off ElasticCluster and waits for Ready", func() {
			ctx, cancel := context.WithTimeout(context.Background(), suiteCfg.ecReadyTimeout+10*time.Minute)
			defer cancel()

			By("applying the ElasticCluster (its Ceph daemons start with ms_crc_data=false) and waiting for Ready")
			_, err := testkit.EnsureElasticCluster(ctx, suiteRestCfg, testkit.ElasticClusterConfig{
				Name:                           suiteCfg.ecName,
				NodeSelectorMatchLabels:        ecNodeSelector(),
				BlockDeviceSelectorMatchLabels: ecBDSelector(),
				NetworkPublic:                  suiteCfg.networkPublic,
				NetworkCluster:                 suiteCfg.networkCluster,
				ReadyTimeout:                   suiteCfg.ecReadyTimeout,
			})
			Expect(err).NotTo(HaveOccurred(),
				"ElasticCluster %s did not reach Ready (a residual client/server ms_crc_data mismatch would hang it here)", suiteCfg.ecName)
		})

		It("declares an RBD ElasticStorageClass and materialises the csi-ceph objects", func() {
			ctx, cancel := context.WithTimeout(context.Background(), suiteCfg.escReadyTimeout+5*time.Minute)
			defer cancel()

			_, err := testkit.EnsureElasticStorageClass(ctx, suiteRestCfg, testkit.ElasticStorageClassConfig{
				Name:         escRBDName(),
				ClusterRef:   suiteCfg.ecName,
				Type:         testkit.ElasticStorageClassTypeRBD,
				Replication:  testkit.ElasticReplicationConsistencyAndAvailability,
				ReadyTimeout: suiteCfg.escReadyTimeout,
			})
			Expect(err).NotTo(HaveOccurred(), "RBD ElasticStorageClass %s did not reach Ready", escRBDName())
			assertCsiCephWired(ctx, escRBDName())
		})

		It("declares a CephFS ElasticStorageClass and materialises the csi-ceph objects", func() {
			ctx, cancel := context.WithTimeout(context.Background(), suiteCfg.escReadyTimeout+5*time.Minute)
			defer cancel()

			_, err := testkit.EnsureElasticStorageClass(ctx, suiteRestCfg, testkit.ElasticStorageClassConfig{
				Name:         escCephFSName(),
				ClusterRef:   suiteCfg.ecName,
				Type:         testkit.ElasticStorageClassTypeCephFS,
				Replication:  testkit.ElasticReplicationConsistencyAndAvailability,
				ReadyTimeout: suiteCfg.escReadyTimeout,
			})
			Expect(err).NotTo(HaveOccurred(), "CephFS ElasticStorageClass %s did not reach Ready", escCephFSName())
			assertCsiCephWired(ctx, escCephFSName())
		})

		It("round-trips RBD and CephFS data with CRC disabled on both client and server", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*(pvcBindTimeout+podReadyTimeout)+5*time.Minute)
			defer cancel()

			msCrcDataRoundTrip(ctx, "rbd", escRBDName(), corev1.ReadWriteOnce)
			msCrcDataRoundTrip(ctx, "cephfs", escCephFSName(), corev1.ReadWriteMany)
		})
	})
}

// msCrcDataRoundTrip provisions a fresh PVC on sc, mounts it in a Pod that writes
// a marker, reads the marker back through the mount, then reclaims the PVC/Pod.
// It reuses the lifecycle builders/helpers so the assertions match the rest of
// the suite. The write+read proves the csi-ceph data path (provision + node
// rbd-map/CephFS-mount) is intact with CRC disabled on both the client and the
// (born-CRC-off) Ceph daemons.
func msCrcDataRoundTrip(ctx context.Context, driver, sc string, mode corev1.PersistentVolumeAccessMode) {
	GinkgoHelper()

	pvcName := "mscrc-" + driver
	podName := "mscrc-" + driver
	marker := "csi-ceph-mscrc-" + driver

	DeferCleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), pvcGoneTimeout+2*time.Minute)
		defer ccancel()
		_ = deletePodBestEffort(cctx, podName)
		_ = deletePVCWaitGone(cctx, pvcName, pvcGoneTimeout)
	})

	By(fmt.Sprintf("provisioning a %s PVC and round-tripping a marker with client CRC off", driver))
	pvc := buildLifecyclePVC(pvcName, sc, suiteCfg.pvcSize, mode, nil)
	pod := buildLifecyclePod(podName, pvcName, "", marker, true)
	Expect(applyPVCAndPod(ctx, pvc, pod)).To(Succeed(), "%s PVC+Pod should bind and become Ready", driver)
	Expect(verifyProbeFile(ctx, podName, marker)).To(Succeed(), "%s marker should read back with CRC disabled", driver)
}

// setServerMsCrcData writes the sds-elastic rook-config-override so ms_crc_data
// equals *enabled (nil clears the key, falling back to Ceph's compile-time
// default). It is called BEFORE the ElasticCluster is created, so Rook renders
// the daemons' ceph.conf from it at bootstrap and they come up born CRC-off — no
// live daemon restart is needed (or safe; see the NOTE on msCrcDataSpecs).
func setServerMsCrcData(ctx context.Context, enabled *bool) error {
	ns := suiteCfg.sdsElasticNamespace

	var globals map[string]string
	if enabled != nil {
		globals = map[string]string{"ms_crc_data": strconv.FormatBool(*enabled)}
	}
	if err := storagekube.SetRookConfigOverride(ctx, suiteRestCfg, ns, globals); err != nil {
		return fmt.Errorf("set rook-config-override in %s: %w", ns, err)
	}
	return nil
}

// --- helpers ---------------------------------------------------------------

// cephConfigMsCrcData reads the ceph-config ConfigMap and extracts the value of
// the `ms_crc_data` key from the rendered ceph.conf (e.g. "true"/"false").
func cephConfigMsCrcData(ctx context.Context) (string, error) {
	var cm corev1.ConfigMap
	if err := suiteK8s.Get(ctx, client.ObjectKey{Namespace: moduleNamespace, Name: cephConfigMapName}, &cm); err != nil {
		return "", fmt.Errorf("get ConfigMap %s/%s: %w", moduleNamespace, cephConfigMapName, err)
	}
	conf, ok := cm.Data["ceph.conf"]
	if !ok {
		return "", fmt.Errorf("ConfigMap %s/%s has no ceph.conf key", moduleNamespace, cephConfigMapName)
	}
	val, ok := parseCephConfValue(conf, "ms_crc_data")
	if !ok {
		return "", fmt.Errorf("ceph.conf in %s/%s has no ms_crc_data key", moduleNamespace, cephConfigMapName)
	}
	return val, nil
}

// parseCephConfValue returns the value of `key = value` from a ceph.conf blob,
// trimming surrounding whitespace. ok is false when the key is absent.
func parseCephConfValue(conf, key string) (string, bool) {
	for _, line := range strings.Split(conf, "\n") {
		line = strings.TrimSpace(line)
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		if strings.TrimSpace(k) == key {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

// waitCephConfigMsCrcData polls the ceph-config ConfigMap until ms_crc_data
// equals want or the timeout elapses.
func waitCephConfigMsCrcData(ctx context.Context, want string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last string
	for {
		got, err := cephConfigMsCrcData(ctx)
		if err == nil && got == want {
			return nil
		}
		last = fmt.Sprintf("got=%q err=%v", got, err)
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for ceph.conf ms_crc_data=%s; last: %s", want, last)
		}
		if !sleepCtx(ctx, pollInterval) {
			return ctx.Err()
		}
	}
}

// setModuleMsCrcData patches the csi-ceph ModuleConfig, merging
// spec.settings.msCrcData=enabled. A JSON merge patch leaves spec.version,
// spec.enabled and any sibling settings untouched.
func setModuleMsCrcData(ctx context.Context, enabled bool) error {
	patch := []byte(fmt.Sprintf(`{"spec":{"settings":{"msCrcData":%t}}}`, enabled))
	if _, err := suiteDyn.Resource(moduleConfigGVR).Patch(
		ctx, moduleName, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("patch ModuleConfig %s settings.msCrcData=%t: %w", moduleName, enabled, err)
	}
	return nil
}

// csiWorkloadChecksums returns the current `checksum/ceph-config` pod-template
// annotation for each csi-ceph CSI workload, keyed by "Kind/name".
func csiWorkloadChecksums(ctx context.Context) (map[string]string, error) {
	out := make(map[string]string, len(csiCephWorkloads))
	for _, w := range csiCephWorkloads {
		sum, err := workloadCephConfigChecksum(ctx, w)
		if err != nil {
			return nil, err
		}
		out[w.kind+"/"+w.name] = sum
	}
	return out, nil
}

// workloadCephConfigChecksum reads the pod-template `checksum/ceph-config`
// annotation of a single CSI workload.
func workloadCephConfigChecksum(ctx context.Context, w csiWorkload) (string, error) {
	key := client.ObjectKey{Namespace: moduleNamespace, Name: w.name}
	switch w.kind {
	case "Deployment":
		var d appsv1.Deployment
		if err := suiteK8s.Get(ctx, key, &d); err != nil {
			return "", fmt.Errorf("get Deployment %s/%s: %w", moduleNamespace, w.name, err)
		}
		return d.Spec.Template.Annotations[cephConfigChecksumAnnotation], nil
	case "DaemonSet":
		var ds appsv1.DaemonSet
		if err := suiteK8s.Get(ctx, key, &ds); err != nil {
			return "", fmt.Errorf("get DaemonSet %s/%s: %w", moduleNamespace, w.name, err)
		}
		return ds.Spec.Template.Annotations[cephConfigChecksumAnnotation], nil
	default:
		return "", fmt.Errorf("unknown workload kind %q", w.kind)
	}
}

// waitCsiWorkloadsRolled waits until every CSI workload has a
// `checksum/ceph-config` annotation different from its pre-flip value in `before`
// AND has finished rolling out (observedGeneration current, updated replicas
// available). This proves the ceph-config change actually restarted the pods.
func waitCsiWorkloadsRolled(ctx context.Context, before map[string]string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last string
	for {
		allRolled := true
		last = ""
		for _, w := range csiCephWorkloads {
			rolled, detail, err := workloadRolled(ctx, w, before[w.kind+"/"+w.name])
			if err != nil {
				allRolled = false
				last = fmt.Sprintf("%s/%s: %v", w.kind, w.name, err)
				break
			}
			if !rolled {
				allRolled = false
				last = fmt.Sprintf("%s/%s: %s", w.kind, w.name, detail)
			}
		}
		if allRolled {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for CSI workloads to roll on ceph-config change; last: %s", last)
		}
		if !sleepCtx(ctx, pollInterval) {
			return ctx.Err()
		}
	}
}

// workloadRolled reports whether a single CSI workload has picked up a new
// ceph-config checksum (!= prev) and completed its rollout. detail carries a
// human-readable reason when it has not.
func workloadRolled(ctx context.Context, w csiWorkload, prev string) (bool, string, error) {
	key := client.ObjectKey{Namespace: moduleNamespace, Name: w.name}
	switch w.kind {
	case "Deployment":
		var d appsv1.Deployment
		if err := suiteK8s.Get(ctx, key, &d); err != nil {
			return false, "", fmt.Errorf("get Deployment %s/%s: %w", moduleNamespace, w.name, err)
		}
		if sum := d.Spec.Template.Annotations[cephConfigChecksumAnnotation]; sum == prev {
			return false, "checksum unchanged", nil
		}
		desired := int32(1)
		if d.Spec.Replicas != nil {
			desired = *d.Spec.Replicas
		}
		if d.Status.ObservedGeneration >= d.Generation &&
			d.Status.UpdatedReplicas >= desired && d.Status.AvailableReplicas >= desired {
			return true, "", nil
		}
		return false, fmt.Sprintf("rollout in progress (updated=%d available=%d desired=%d)",
			d.Status.UpdatedReplicas, d.Status.AvailableReplicas, desired), nil
	case "DaemonSet":
		var ds appsv1.DaemonSet
		if err := suiteK8s.Get(ctx, key, &ds); err != nil {
			return false, "", fmt.Errorf("get DaemonSet %s/%s: %w", moduleNamespace, w.name, err)
		}
		if sum := ds.Spec.Template.Annotations[cephConfigChecksumAnnotation]; sum == prev {
			return false, "checksum unchanged", nil
		}
		desired := ds.Status.DesiredNumberScheduled
		if ds.Status.ObservedGeneration >= ds.Generation &&
			ds.Status.UpdatedNumberScheduled >= desired && ds.Status.NumberAvailable >= desired {
			return true, "", nil
		}
		return false, fmt.Sprintf("rollout in progress (updated=%d available=%d desired=%d)",
			ds.Status.UpdatedNumberScheduled, ds.Status.NumberAvailable, desired), nil
	default:
		return false, "", fmt.Errorf("unknown workload kind %q", w.kind)
	}
}
