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
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	storagekube "github.com/deckhouse/storage-e2e/pkg/kubernetes"
	"github.com/deckhouse/storage-e2e/pkg/testkit"
)

type matrixExpectation int

const (
	expectBind matrixExpectation = iota
	expectNoBind
)

type cephProtocol string

const (
	protocolRBD    cephProtocol = "rbd"
	protocolCephFS cephProtocol = "cephfs"
)

var _ = Describe("msCrcData matrix", func() {
	// The server=on/client=off quadrant is intentionally omitted: the regression
	// under test is the asymmetric default-client-on/server-off mismatch. The
	// remaining two cells prove both explicitly-matching states still provision.
	matrix := []struct {
		name      string
		serverCRC bool
		clientCRC bool
		expect    matrixExpectation
	}{
		{
			name:      "server=off client=off -> Bound",
			serverCRC: false,
			clientCRC: false,
			expect:    expectBind,
		},
		{
			name:      "server=off client=on -> NotBound",
			serverCRC: false,
			clientCRC: true,
			expect:    expectNoBind,
		},
		{
			name:      "server=on client=on -> Bound",
			serverCRC: true,
			clientCRC: true,
			expect:    expectBind,
		},
	}

	for _, p := range []cephProtocol{protocolRBD, protocolCephFS} {
		proto := p
		// No Ordered: after the FS-snapshot rework applyMatrixCell is
		// idempotent (sets server/client to the cell target unconditionally,
		// then asserts via real ceph.conf delta — not via residual state),
		// so cells and protocols are safe to interleave. Combined with
		// RandomizeAllSpecs in TestCsiCeph this gives a fully randomized
		// matrix per run; the seed is logged at suite start and can be
		// pinned with -ginkgo.seed=<N> for reproduction.
		Context("protocol="+string(proto), func() {
			for i := range matrix {
				tc := matrix[i]
				It(tc.name, func() {
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
					defer cancel()

					runMatrixCase(ctx, proto, tc.serverCRC, tc.clientCRC, tc.expect)
				})
			}
		})
	}
})

func runMatrixCase(ctx context.Context, proto cephProtocol, serverCRC, clientCRC bool, expect matrixExpectation) {
	GinkgoHelper()
	applyMatrixCell(ctx, serverCRC, clientCRC)
	runPVCScenario(ctx, proto, serverCRC, clientCRC, expect)
}

// applyMatrixCell flips server- and client-side CRC settings and asserts
// that csi-ceph reacts correctly. The reaction depends on whether the
// flip actually changes the rendered ceph.conf body (it doesn't if a
// previous cell already left the same target value behind):
//
//   - changed: all four CSI workloads must auto-rollout — checksum/ceph-config
//     pod-template annotation flips and the rolling update completes.
//   - unchanged: all four CSI workloads must stay steady — no spurious
//     checksum bump, no spurious rollout.
//
// The "did the body change?" signal is taken directly from the pod
// filesystem (/etc/ceph/ceph.conf inside the csi-controller container)
// via an injected ephemeral container — the previous annotation-only
// signal was unreliable both ways: it lied about no-op flips when the
// chart re-rendered identical bytes, and it never proved the new value
// actually reached the running pod's filesystem.
func applyMatrixCell(ctx context.Context, serverCRC, clientCRC bool) {
	GinkgoHelper()

	By("Snapshotting checksum/ceph-config on all four CSI workloads before the flip")
	rbdCtrlSnap, err := getDeploymentPodTemplateAnnotation(ctx, suiteK8s, csiCephNamespace, rbdControllerDeployment, cephConfigChecksumAnnotation)
	Expect(err).NotTo(HaveOccurred(), "snapshot checksum on %s", rbdControllerDeployment)
	rbdNodeSnap, err := getDaemonSetPodTemplateAnnotation(ctx, suiteK8s, csiCephNamespace, rbdNodeDaemonSet, cephConfigChecksumAnnotation)
	Expect(err).NotTo(HaveOccurred(), "snapshot checksum on %s", rbdNodeDaemonSet)
	fsCtrlSnap, err := getDeploymentPodTemplateAnnotation(ctx, suiteK8s, csiCephNamespace, cephfsControllerDeployment, cephConfigChecksumAnnotation)
	Expect(err).NotTo(HaveOccurred(), "snapshot checksum on %s", cephfsControllerDeployment)
	fsNodeSnap, err := getDaemonSetPodTemplateAnnotation(ctx, suiteK8s, csiCephNamespace, cephfsNodeDaemonSet, cephConfigChecksumAnnotation)
	Expect(err).NotTo(HaveOccurred(), "snapshot checksum on %s", cephfsNodeDaemonSet)

	By("Opening distroless readers and snapshotting /etc/ceph/ceph.conf inside the csi-controller-{rbd,cephfs} pods")
	rbdCtrlPod, err := latestReadyPod(ctx, suiteK8s, csiCephNamespace, rbdControllerDeployment)
	Expect(err).NotTo(HaveOccurred(), "find Ready pod for %s", rbdControllerDeployment)
	rbdCtrlContainer := findContainerMountingPath(rbdCtrlPod, cephConfigMountDir)
	Expect(rbdCtrlContainer).NotTo(BeEmpty(),
		"no container in %s/%s mounts %s", csiCephNamespace, rbdCtrlPod.Name, cephConfigMountDir)

	fsCtrlPod, err := latestReadyPod(ctx, suiteK8s, csiCephNamespace, cephfsControllerDeployment)
	Expect(err).NotTo(HaveOccurred(), "find Ready pod for %s", cephfsControllerDeployment)
	fsCtrlContainer := findContainerMountingPath(fsCtrlPod, cephConfigMountDir)
	Expect(fsCtrlContainer).NotTo(BeEmpty(),
		"no container in %s/%s mounts %s", csiCephNamespace, fsCtrlPod.Name, cephConfigMountDir)

	// One ephemeral container per pod for the whole cell. Open them
	// before the flip and reuse for both the pre-flip snapshot and the
	// post-flip eventually-poll: re-injecting per poll burned ~20s of
	// kubelet container-start latency per iteration, which dominated
	// matrix runtime. See storagekube.DistrolessReader.
	openCtx, openCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer openCancel()

	rbdReader, err := storagekube.OpenDistrolessReader(
		openCtx, suiteRestCfg, csiCephNamespace, rbdCtrlPod.Name, rbdCtrlContainer,
		storagekube.ReadFileOptions{},
	)
	Expect(err).NotTo(HaveOccurred(), "open distroless reader on %s/%s[%s]",
		csiCephNamespace, rbdCtrlPod.Name, rbdCtrlContainer)

	fsReader, err := storagekube.OpenDistrolessReader(
		openCtx, suiteRestCfg, csiCephNamespace, fsCtrlPod.Name, fsCtrlContainer,
		storagekube.ReadFileOptions{},
	)
	Expect(err).NotTo(HaveOccurred(), "open distroless reader on %s/%s[%s]",
		csiCephNamespace, fsCtrlPod.Name, fsCtrlContainer)

	rbdCtrlOldBody, err := rbdReader.ReadFile(openCtx, cephConfigPodPath)
	Expect(err).NotTo(HaveOccurred(), "snapshot %s in %s/%s[%s]",
		cephConfigPodPath, csiCephNamespace, rbdCtrlPod.Name, rbdCtrlContainer)

	fsCtrlOldBody, err := fsReader.ReadFile(openCtx, cephConfigPodPath)
	Expect(err).NotTo(HaveOccurred(), "snapshot %s in %s/%s[%s]",
		cephConfigPodPath, csiCephNamespace, fsCtrlPod.Name, fsCtrlContainer)

	if serverCRC {
		By("Enabling server-side CRC on the Ceph cluster")
		Expect(testkit.EnableServerCRC(ctx, suiteRestCfg, suiteCfg.rook.Namespace)).To(Succeed())
	} else {
		By("Disabling server-side CRC on the Ceph cluster")
		Expect(testkit.DisableServerCRC(ctx, suiteRestCfg, suiteCfg.rook.Namespace)).To(Succeed())
	}

	By(fmt.Sprintf("Setting client-side msCrcData=%v", clientCRC))
	Expect(setModuleConfigSetting(ctx, suiteDyn, moduleConfigName, "msCrcData", clientCRC, false)).To(Succeed())

	if clientCRC {
		Expect(waitCephConfigExcludes(ctx, suiteK8s, "ms_crc_data = false", moduleReconcileTimeout)).To(Succeed())
	} else {
		Expect(waitCephConfigContains(ctx, suiteK8s, "ms_crc_data = false", moduleReconcileTimeout)).To(Succeed())
	}

	predicate := func(body string) bool {
		if clientCRC {
			return !strings.Contains(body, "ms_crc_data = false")
		}
		return strings.Contains(body, "ms_crc_data = false")
	}

	By("Waiting for /etc/ceph/ceph.conf in csi-controller-rbd to reflect the target ms_crc_data value")
	rbdCtrlNewBody, err := eventuallyPodFileMatches(
		ctx, suiteRestCfg, suiteK8s,
		csiCephNamespace, rbdControllerDeployment, cephConfigMountDir, cephConfigPodPath,
		rbdReader,
		predicate, kubeletSyncTimeout,
	)
	Expect(err).NotTo(HaveOccurred(),
		"FS-level verification on csi-controller-rbd; last body=%q", rbdCtrlNewBody)

	By("Waiting for /etc/ceph/ceph.conf in csi-controller-cephfs to reflect the target ms_crc_data value")
	fsCtrlNewBody, err := eventuallyPodFileMatches(
		ctx, suiteRestCfg, suiteK8s,
		csiCephNamespace, cephfsControllerDeployment, cephConfigMountDir, cephConfigPodPath,
		fsReader,
		predicate, kubeletSyncTimeout,
	)
	Expect(err).NotTo(HaveOccurred(),
		"FS-level verification on csi-controller-cephfs; last body=%q", fsCtrlNewBody)

	configChanged := rbdCtrlOldBody != rbdCtrlNewBody || fsCtrlOldBody != fsCtrlNewBody

	if configChanged {
		By("ceph.conf body changed → asserting auto-rollout on all four CSI workloads (in parallel)")
		Expect(runInParallel(
			func() error {
				return waitDeploymentAutoRollout(ctx, suiteK8s, csiCephNamespace, rbdControllerDeployment, rbdCtrlSnap, autoRolloutTimeout)
			},
			func() error {
				return waitDaemonSetAutoRollout(ctx, suiteK8s, csiCephNamespace, rbdNodeDaemonSet, rbdNodeSnap, autoRolloutTimeout)
			},
			func() error {
				return waitDeploymentAutoRollout(ctx, suiteK8s, csiCephNamespace, cephfsControllerDeployment, fsCtrlSnap, autoRolloutTimeout)
			},
			func() error {
				return waitDaemonSetAutoRollout(ctx, suiteK8s, csiCephNamespace, cephfsNodeDaemonSet, fsNodeSnap, autoRolloutTimeout)
			},
		)).To(Succeed())
		return
	}

	By("ceph.conf body unchanged → asserting all four CSI workloads stay steady at the snapshot annotation (no spurious rollout) — 4×steadyWindow observed in parallel")
	Expect(runInParallel(
		func() error {
			return assertDeploymentSteadyAtAnnotation(ctx, suiteK8s, csiCephNamespace, rbdControllerDeployment, rbdCtrlSnap, steadyWindow)
		},
		func() error {
			return assertDaemonSetSteadyAtAnnotation(ctx, suiteK8s, csiCephNamespace, rbdNodeDaemonSet, rbdNodeSnap, steadyWindow)
		},
		func() error {
			return assertDeploymentSteadyAtAnnotation(ctx, suiteK8s, csiCephNamespace, cephfsControllerDeployment, fsCtrlSnap, steadyWindow)
		},
		func() error {
			return assertDaemonSetSteadyAtAnnotation(ctx, suiteK8s, csiCephNamespace, cephfsNodeDaemonSet, fsNodeSnap, steadyWindow)
		},
	)).To(Succeed())
}

// runInParallel runs each fn in its own goroutine and returns the
// joined error after all have finished. Used to fan out the four
// per-workload steady/rollout assertions inside applyMatrixCell so
// that the four 15s observation windows (or four rollout waits)
// elapse concurrently instead of summing up. Joined error preserves
// every failure verbatim — handy for diagnosing which workload
// drifted.
func runInParallel(fns ...func() error) error {
	errs := make([]error, len(fns))
	var wg sync.WaitGroup
	wg.Add(len(fns))
	for i, fn := range fns {
		i, fn := i, fn
		go func() {
			defer wg.Done()
			errs[i] = fn()
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

func runPVCScenario(ctx context.Context, proto cephProtocol, serverCRC, clientCRC bool, expect matrixExpectation) {
	GinkgoHelper()

	stamp := time.Now().UnixNano()
	pvcName := fmt.Sprintf("pvc-matrix-%s-s%v-c%v-%d", proto, serverCRC, clientCRC, stamp)
	podName := fmt.Sprintf("pod-matrix-%s-s%v-c%v-%d", proto, serverCRC, clientCRC, stamp)
	testContent := fmt.Sprintf("ceph-matrix protocol=%s server=%v client=%v ts=%d", proto, serverCRC, clientCRC, stamp)

	size, err := resource.ParseQuantity(suiteCfg.pvcSize)
	Expect(err).NotTo(HaveOccurred(), "parse E2E_PVC_SIZE=%q", suiteCfg.pvcSize)

	sc := suiteCfg.cephStorageClass
	if proto == protocolCephFS {
		sc = suiteCfg.cephFSStorageClass
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: suiteCfg.namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "csi-ceph-e2e",
				"ceph.matrix.protocol":         string(proto),
				"ceph.matrix.server-crc":       fmt.Sprintf("%v", serverCRC),
				"ceph.matrix.client-crc":       fmt.Sprintf("%v", clientCRC),
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: &sc,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: size},
			},
		},
	}

	By(fmt.Sprintf("Creating PVC %s/%s on StorageClass %s", pvc.Namespace, pvc.Name, sc))
	Expect(suiteK8s.Create(ctx, pvc)).To(Succeed())

	// Cleanup runs in Ginkgo's cleanup phase (LIFO). Registered FIRST so
	// it runs LAST — the diagnostics dump below needs to see the PVC and
	// Pod still alive in the cluster.
	DeferCleanup(func(cleanupCtx SpecContext) {
		_ = suiteK8s.Delete(cleanupCtx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: suiteCfg.namespace}})
		_ = suiteK8s.Delete(cleanupCtx, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: pvcName, Namespace: suiteCfg.namespace}})
	}, NodeTimeout(2*time.Minute))

	// Diagnostics on failure. We use DeferCleanup (not a raw `defer`) on
	// purpose: CurrentSpecReport().Failed() is only guaranteed to reflect
	// the final spec state in Ginkgo's cleanup phase; inside a raw defer
	// running during runtime.Goexit unwinding it can return false, which
	// would silently skip the dump (this is exactly what bit us before —
	// the test was failing without ever printing diagnostics).
	//
	// Registered AFTER the cleanup callback, so by Ginkgo's LIFO ordering
	// this runs FIRST — i.e. while the PVC / Pod are still alive in the
	// cluster — and emits its own header line as soon as it enters, so a
	// passing-but-still-running spec produces zero noise but a failed spec
	// makes the diagnostic block immediately greppable.
	DeferCleanup(func(diagCtx SpecContext) {
		report := CurrentSpecReport()
		fmt.Fprintf(GinkgoWriter,
			"\ndiag: cleanup entered (spec=%q, failed=%v, state=%s)\n",
			report.LeafNodeText, report.Failed(), report.State)
		if !report.Failed() {
			return
		}
		dumpFailedSpecDiagnostics(diagCtx, proto, pvcName)
	}, NodeTimeout(2*time.Minute))

	switch expect {
	case expectBind:
		By("Waiting for the PVC to become Bound")
		Eventually(func(g Gomega) {
			var current corev1.PersistentVolumeClaim
			g.Expect(suiteK8s.Get(ctx, client.ObjectKeyFromObject(pvc), &current)).To(Succeed())
			g.Expect(current.Status.Phase).To(Equal(corev1.ClaimBound))
		}, pvcBindTimeout, pollInterval).Should(Succeed())

		By("Verifying the volume with a pod read/write round-trip")
		createProbePod(ctx, suiteCfg.namespace, podName, pvcName, testContent)
		waitForPodRunning(ctx, suiteCfg.namespace, podName)
		verifyProbeFile(ctx, suiteCfg.namespace, podName, testContent)

	case expectNoBind:
		By("Checking that the PVC stays not Bound")
		lastPhase := corev1.ClaimPending
		Consistently(func(g Gomega) {
			var current corev1.PersistentVolumeClaim
			g.Expect(suiteK8s.Get(ctx, client.ObjectKeyFromObject(pvc), &current)).To(Succeed())
			lastPhase = current.Status.Phase
			g.Expect(current.Status.Phase).NotTo(Equal(corev1.ClaimBound))
		}, pvcBindTimeout, 15*time.Second).Should(Succeed())
		GinkgoWriter.Printf("PVC stayed in phase=%s for the full observation window\n", lastPhase)
	}
}

func createProbePod(ctx context.Context, namespace, podName, pvcName, content string) {
	GinkgoHelper()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "csi-ceph-e2e",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:  "writer",
				Image: "busybox:1.36",
				Command: []string{
					"/bin/sh", "-c",
					fmt.Sprintf(
						"set -eux; echo %q > /data/probe.txt && sync && test \"$(cat /data/probe.txt)\" = %q && sleep 3600",
						content, content,
					),
				},
				VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
			}},
			Volumes: []corev1.Volume{{
				Name: "data",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcName,
					},
				},
			}},
		},
	}
	Expect(suiteK8s.Create(ctx, pod)).To(Succeed())
}

func waitForPodRunning(ctx context.Context, namespace, name string) {
	GinkgoHelper()

	Eventually(func(g Gomega) {
		var p corev1.Pod
		g.Expect(suiteK8s.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &p)).To(Succeed())
		g.Expect(p.Status.Phase).To(Equal(corev1.PodRunning))
	}, podReadyTimeout, pollInterval).Should(Succeed())
}

func verifyProbeFile(ctx context.Context, namespace, podName, expected string) {
	GinkgoHelper()

	execCtx, execCancel := context.WithTimeout(ctx, 30*time.Second)
	defer execCancel()

	out, err := storagekube.ReadFileFromPod(execCtx, suiteRestCfg, namespace, podName, "writer", "/data/probe.txt")
	Expect(err).NotTo(HaveOccurred(), "read /data/probe.txt: %s", out)
	Expect(strings.TrimSpace(out)).To(Equal(expected))
}
