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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/deckhouse/storage-e2e/pkg/testkit"
)

type matrixExpectation int

const (
	expectBind matrixExpectation = iota
	expectNoBind
)

var _ = Describe("msCrcData matrix", Ordered, func() {
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

	for i := range matrix {
		tc := matrix[i]
		It(tc.name, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			defer cancel()

			runMatrixCase(ctx, tc.serverCRC, tc.clientCRC, tc.expect)
		})
	}
})

func runMatrixCase(ctx context.Context, serverCRC, clientCRC bool, expect matrixExpectation) {
	GinkgoHelper()
	applyMatrixCell(ctx, serverCRC, clientCRC)
	runPVCScenario(ctx, serverCRC, clientCRC, expect)
}

func applyMatrixCell(ctx context.Context, serverCRC, clientCRC bool) {
	GinkgoHelper()

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

	By("Restarting csi-controller-rbd and csi-node-rbd")
	Expect(rolloutRestartDeployment(ctx, suiteK8s, csiCephNamespace, rbdControllerDeployment)).To(Succeed())
	Expect(rolloutRestartDaemonSet(ctx, suiteK8s, csiCephNamespace, rbdNodeDaemonSet)).To(Succeed())
	Expect(waitDeploymentReady(ctx, suiteK8s, csiCephNamespace, rbdControllerDeployment, deploymentReadyTimeout)).To(Succeed())
	Expect(waitDaemonSetReady(ctx, suiteK8s, csiCephNamespace, rbdNodeDaemonSet, deploymentReadyTimeout)).To(Succeed())
}

func runPVCScenario(ctx context.Context, serverCRC, clientCRC bool, expect matrixExpectation) {
	GinkgoHelper()

	stamp := time.Now().UnixNano()
	pvcName := fmt.Sprintf("pvc-matrix-s%v-c%v-%d", serverCRC, clientCRC, stamp)
	podName := fmt.Sprintf("pod-matrix-s%v-c%v-%d", serverCRC, clientCRC, stamp)
	testContent := fmt.Sprintf("ceph-matrix server=%v client=%v ts=%d", serverCRC, clientCRC, stamp)

	size, err := resource.ParseQuantity(suiteCfg.pvcSize)
	Expect(err).NotTo(HaveOccurred(), "parse E2E_PVC_SIZE=%q", suiteCfg.pvcSize)

	sc := suiteCfg.cephStorageClass
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: suiteCfg.namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "csi-ceph-e2e",
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

	By(fmt.Sprintf("Creating PVC %s/%s", pvc.Namespace, pvc.Name))
	Expect(suiteK8s.Create(ctx, pvc)).To(Succeed())

	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		_ = suiteK8s.Delete(cleanupCtx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: suiteCfg.namespace}})
		_ = suiteK8s.Delete(cleanupCtx, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: pvcName, Namespace: suiteCfg.namespace}})
	}()

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

	out, err := execInPod(execCtx, suiteRestCfg, namespace, podName, "writer", []string{"cat", "/data/probe.txt"})
	Expect(err).NotTo(HaveOccurred(), "exec cat /data/probe.txt: %s", out)
	Expect(strings.TrimSpace(out)).To(Equal(expected))
}
