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
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	storagekube "github.com/deckhouse/storage-e2e/pkg/kubernetes"
)

// The upstream ceph-csi provisioner names csi-ceph registers (used for the
// VolumeSnapshotClass driver field).
const (
	rbdProvisioner    = "rbd.csi.ceph.com"
	cephfsProvisioner = "cephfs.csi.ceph.com"

	// lifecycleResizedSize is the target for the expansion spec; restore/clone
	// PVCs use it too so they are always >= the (resized) source volume.
	lifecycleResizedSize = "2Gi"

	snapshotReadyTimeout = 10 * time.Minute
	pvcGoneTimeout       = 5 * time.Minute
)

var (
	volumeSnapshotGVR = schema.GroupVersionResource{
		Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshots",
	}
	volumeSnapshotClassGVR = schema.GroupVersionResource{
		Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshotclasses",
	}
)

// blockDevicePath is where the Block-volumeMode spec exposes the raw RBD device
// inside the probe Pod.
const blockDevicePath = "/dev/csi-block"

// driverCase parametrises the shared lifecycle suite for one csi-ceph driver.
type driverCase struct {
	name        string // short label: "rbd" / "cephfs"
	provisioner string // rbd.csi.ceph.com / cephfs.csi.ceph.com
	scName      func() string
	accessMode  corev1.PersistentVolumeAccessMode
	// supportsRWX runs the extra "same volume mounted RW on two nodes at once"
	// spec (CephFS). supportsBlock runs the raw Block-volumeMode spec (RBD).
	supportsRWX   bool
	supportsBlock bool
}

// lifecycleSpecs registers the full volume-lifecycle coverage for one driver on
// the already-Ready ElasticStorageClass: create, expand, pod migration to
// another node, snapshot + restore, clone (PVC dataSource — ceph-csi implements
// it via an internal snapshot), and delete. Ordered: the specs share one base
// PVC and the marker written at creation must survive every step.
func lifecycleSpecs(dc driverCase) {
	Describe(dc.name+" volume lifecycle", Ordered, func() {
		var (
			basePVC    = dc.name + "-base"
			basePod    = dc.name + "-base"
			readerPod  = dc.name + "-reader"
			restorePVC = dc.name + "-restore"
			restorePod = dc.name + "-restore"
			clonePVC   = dc.name + "-clone"
			clonePod   = dc.name + "-clone"
			snapName   = dc.name + "-snap"
			marker     = "csi-ceph-" + dc.name + "-lifecycle"

			snapClass string
			baseNode  string
		)

		It("creates a volume: PVC binds, Pod writes and reads back data", func() {
			ctx, cancel := context.WithTimeout(context.Background(), pvcBindTimeout+podReadyTimeout+2*time.Minute)
			defer cancel()

			pvc := buildLifecyclePVC(basePVC, dc.scName(), suiteCfg.pvcSize, dc.accessMode, nil)
			pod := buildLifecyclePod(basePod, basePVC, "", marker, true)
			Expect(applyPVCAndPod(ctx, pvc, pod)).To(Succeed(), "base PVC+Pod should bind and become Ready")
			Expect(verifyProbeFile(ctx, basePod, marker)).To(Succeed(), "written marker should read back")

			var err error
			baseNode, err = podNodeName(ctx, basePod)
			Expect(err).NotTo(HaveOccurred())
			Expect(baseNode).NotTo(BeEmpty())
		})

		It("expands the volume: PVC resize is honoured and data is intact", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
			defer cancel()

			Expect(resizePVCAndWait(ctx, basePVC, lifecycleResizedSize, 10*time.Minute)).
				To(Succeed(), "PVC %s should reach %s", basePVC, lifecycleResizedSize)
			Expect(verifyProbeFile(ctx, basePod, marker)).To(Succeed(), "data should survive the resize")
		})

		It("migrates the Pod to another node: volume re-attaches, data is intact", func() {
			ctx, cancel := context.WithTimeout(context.Background(), podReadyTimeout+5*time.Minute)
			defer cancel()

			other, err := otherWorkerNode(ctx, baseNode)
			Expect(err).NotTo(HaveOccurred())
			if other == "" {
				Skip("need >=2 schedulable worker nodes to migrate the Pod")
			}

			By("deleting the original Pod so the volume detaches from " + baseNode)
			Expect(deletePodWait(ctx, basePod)).To(Succeed())

			By("scheduling a new Pod on " + other + " against the same PVC")
			pod := buildLifecyclePod(readerPod, basePVC, other, marker, false)
			Expect(applyPodOnly(ctx, pod)).To(Succeed(), "migrated Pod should become Ready on %s", other)

			landed, err := podNodeName(ctx, readerPod)
			Expect(err).NotTo(HaveOccurred())
			Expect(landed).To(Equal(other), "migrated Pod should run on the other node")
			Expect(verifyProbeFile(ctx, readerPod, marker)).To(Succeed(), "data should survive migration")
		})

		It("creates a snapshot and restores it to a new volume", func() {
			ctx, cancel := context.WithTimeout(context.Background(), snapshotReadyTimeout+pvcBindTimeout+podReadyTimeout+2*time.Minute)
			defer cancel()

			var err error
			snapClass, err = ensureVolumeSnapshotClass(ctx, dc.scName(), dc.provisioner)
			Expect(err).NotTo(HaveOccurred(), "VolumeSnapshotClass for %s", dc.scName())

			By("creating VolumeSnapshot " + snapName + " and waiting for readyToUse")
			Expect(createSnapshotWait(ctx, snapName, basePVC, snapClass, snapshotReadyTimeout)).
				To(Succeed(), "snapshot %s should become readyToUse", snapName)

			By("restoring a new PVC from the snapshot")
			pvc := buildLifecyclePVC(restorePVC, dc.scName(), lifecycleResizedSize, dc.accessMode, snapshotDataSource(snapName))
			pod := buildLifecyclePod(restorePod, restorePVC, "", marker, false)
			Expect(applyPVCAndPod(ctx, pvc, pod)).To(Succeed(), "restored PVC+Pod should bind and become Ready")
			Expect(verifyProbeFile(ctx, restorePod, marker)).To(Succeed(), "restored volume should carry the original data")
		})

		It("clones the volume via a snapshot (PVC dataSource)", func() {
			ctx, cancel := context.WithTimeout(context.Background(), pvcBindTimeout+podReadyTimeout+5*time.Minute)
			defer cancel()

			pvc := buildLifecyclePVC(clonePVC, dc.scName(), lifecycleResizedSize, dc.accessMode, pvcDataSource(basePVC))
			pod := buildLifecyclePod(clonePod, clonePVC, "", marker, false)
			Expect(applyPVCAndPod(ctx, pvc, pod)).To(Succeed(), "cloned PVC+Pod should bind and become Ready")
			Expect(verifyProbeFile(ctx, clonePod, marker)).To(Succeed(), "cloned volume should carry the source data")
		})

		if dc.supportsRWX {
			It("serves the same volume RW from Pods on two nodes at once (RWX)", func() {
				ctx, cancel := context.WithTimeout(context.Background(), pvcBindTimeout+2*podReadyTimeout+3*time.Minute)
				defer cancel()

				n1, n2, err := twoSchedulableWorkers(ctx)
				Expect(err).NotTo(HaveOccurred())
				if n1 == "" {
					Skip("need >=2 schedulable worker nodes for the RWX multi-node spec")
				}

				rwxPVC := dc.name + "-rwx"
				podA := dc.name + "-rwx-a"
				podB := dc.name + "-rwx-b"
				rwxMarker := "csi-ceph-" + dc.name + "-rwx"
				DeferCleanup(func() {
					cctx, ccancel := context.WithTimeout(context.Background(), pvcGoneTimeout+2*time.Minute)
					defer ccancel()
					_ = deletePodBestEffort(cctx, podA)
					_ = deletePodBestEffort(cctx, podB)
					_ = deletePVCWaitGone(cctx, rwxPVC, pvcGoneTimeout)
				})

				By("mounting an RWX PVC on " + n1 + " (writer) and " + n2 + " (reader) simultaneously")
				pvc := buildLifecyclePVC(rwxPVC, dc.scName(), suiteCfg.pvcSize, corev1.ReadWriteMany, nil)
				writer := buildLifecyclePod(podA, rwxPVC, n1, rwxMarker, true)
				Expect(applyPVCAndPod(ctx, pvc, writer)).To(Succeed(), "writer Pod on %s should become Ready", n1)

				reader := buildLifecyclePod(podB, rwxPVC, n2, rwxMarker, false)
				Expect(applyPodOnly(ctx, reader)).To(Succeed(), "reader Pod on %s should mount the same RWX volume", n2)

				Expect(verifyProbeFile(ctx, podB, rwxMarker)).
					To(Succeed(), "the node-%s Pod should read the node-%s Pod's data while both are mounted", n2, n1)
			})
		}

		if dc.supportsBlock {
			It("provisions a raw Block-volumeMode PVC and round-trips through the block device", func() {
				ctx, cancel := context.WithTimeout(context.Background(), pvcBindTimeout+podReadyTimeout+3*time.Minute)
				defer cancel()

				blockPVC := dc.name + "-block"
				blockPod := dc.name + "-block"
				blockMarker := "csi-ceph-" + dc.name + "-block"
				DeferCleanup(func() {
					cctx, ccancel := context.WithTimeout(context.Background(), pvcGoneTimeout+2*time.Minute)
					defer ccancel()
					_ = deletePodBestEffort(cctx, blockPod)
					_ = deletePVCWaitGone(cctx, blockPVC, pvcGoneTimeout)
				})

				By("creating a Block-mode PVC and a Pod with a raw volumeDevice")
				pvc := buildBlockPVC(blockPVC, dc.scName(), suiteCfg.pvcSize, dc.accessMode)
				pod := buildBlockPod(blockPod, blockPVC, blockMarker)
				Expect(applyPVCAndPod(ctx, pvc, pod)).To(Succeed(), "Block-mode PVC+Pod should bind and become Ready")

				Expect(verifyBlockData(ctx, blockPod, blockMarker)).
					To(Succeed(), "raw block device should return the written marker")
			})
		}

		It("deletes the volumes and snapshot: resources are reclaimed", func() {
			ctx, cancel := context.WithTimeout(context.Background(), resourceGoneTimeout+5*time.Minute)
			defer cancel()

			By("deleting the Pods")
			for _, p := range []string{readerPod, restorePod, clonePod, basePod} {
				Expect(deletePodBestEffort(ctx, p)).To(Succeed())
			}

			By("deleting the PVCs and waiting for them to be reclaimed")
			for _, p := range []string{restorePVC, clonePVC, basePVC} {
				Expect(deletePVCWaitGone(ctx, p, pvcGoneTimeout)).To(Succeed(), "PVC %s should be deleted", p)
			}

			By("deleting the VolumeSnapshot")
			_ = suiteDyn.Resource(volumeSnapshotGVR).Namespace(suiteCfg.namespace).Delete(ctx, snapName, metav1.DeleteOptions{})
			Expect(waitResourceGone(ctx, volumeSnapshotGVR, suiteCfg.namespace, snapName, pvcGoneTimeout)).
				To(Succeed(), "VolumeSnapshot %s should be deleted", snapName)
		})
	})
}

// --- builders --------------------------------------------------------------

func buildLifecyclePVC(name, sc, size string, mode corev1.PersistentVolumeAccessMode, dataSource *corev1.TypedLocalObjectReference) *corev1.PersistentVolumeClaim {
	scp := sc
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: suiteCfg.namespace, Name: name},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{mode},
			StorageClassName: &scp,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)},
			},
		},
	}
	if dataSource != nil {
		pvc.Spec.DataSource = dataSource
	}
	return pvc
}

// buildLifecyclePod mounts pvc at /data. When write is true it writes marker to
// /data/probe.txt at boot; otherwise it only mounts and idles (readers assert on
// data that already lives on the volume). nodeName pins the Pod when non-empty.
func buildLifecyclePod(name, pvc, nodeName, marker string, write bool) *corev1.Pod {
	script := `sleep 360000`
	if write {
		script = fmt.Sprintf(`echo -n "$MARKER" > %s && sync && cat %s && sleep 360000`, probeFilePath, probeFilePath)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: suiteCfg.namespace,
			Name:      name,
			Labels:    map[string]string{"app": "csi-ceph-e2e-lifecycle"},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:         probeContainerName,
				Image:        suiteCfg.probeImage,
				Command:      []string{"sh", "-c", script},
				Env:          []corev1.EnvVar{{Name: "MARKER", Value: marker}},
				VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: probeMountPath}},
			}},
			Volumes: []corev1.Volume{{
				Name: "data",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvc},
				},
			}},
		},
	}
	if nodeName != "" {
		pod.Spec.NodeName = nodeName
	}
	return pod
}

func snapshotDataSource(snapName string) *corev1.TypedLocalObjectReference {
	group := "snapshot.storage.k8s.io"
	return &corev1.TypedLocalObjectReference{APIGroup: &group, Kind: "VolumeSnapshot", Name: snapName}
}

func pvcDataSource(pvcName string) *corev1.TypedLocalObjectReference {
	return &corev1.TypedLocalObjectReference{Kind: "PersistentVolumeClaim", Name: pvcName}
}

// --- apply / wait helpers --------------------------------------------------

func applyPVCAndPod(ctx context.Context, pvc *corev1.PersistentVolumeClaim, pod *corev1.Pod) error {
	if err := suiteK8s.Create(ctx, pvc); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create pvc %s/%s: %w", suiteCfg.namespace, pvc.Name, err)
	}
	if err := suiteK8s.Create(ctx, pod); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create pod %s/%s: %w", suiteCfg.namespace, pod.Name, err)
	}
	if err := waitPVCBound(ctx, pvc.Name, pvcBindTimeout); err != nil {
		return err
	}
	return waitPodReady(ctx, pod.Name, podReadyTimeout)
}

func applyPodOnly(ctx context.Context, pod *corev1.Pod) error {
	if err := suiteK8s.Create(ctx, pod); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create pod %s/%s: %w", suiteCfg.namespace, pod.Name, err)
	}
	return waitPodReady(ctx, pod.Name, podReadyTimeout)
}

func podNodeName(ctx context.Context, podName string) (string, error) {
	var pod corev1.Pod
	if err := suiteK8s.Get(ctx, client.ObjectKey{Namespace: suiteCfg.namespace, Name: podName}, &pod); err != nil {
		return "", err
	}
	return pod.Spec.NodeName, nil
}

// otherWorkerNode returns a schedulable worker node name distinct from exclude,
// or "" if there is none.
func otherWorkerNode(ctx context.Context, exclude string) (string, error) {
	workers, err := storagekube.GetWorkerNodes(ctx, suiteRestCfg)
	if err != nil {
		return "", err
	}
	for i := range workers {
		n := &workers[i]
		if n.Name == exclude {
			continue
		}
		if nodeSchedulable(n) {
			return n.Name, nil
		}
	}
	return "", nil
}

func nodeSchedulable(n *corev1.Node) bool {
	if n.Spec.Unschedulable {
		return false
	}
	for _, t := range n.Spec.Taints {
		if t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute {
			return false
		}
	}
	return true
}

// twoSchedulableWorkers returns two distinct schedulable worker node names, or
// ("","") if there are fewer than two.
func twoSchedulableWorkers(ctx context.Context) (string, string, error) {
	workers, err := storagekube.GetWorkerNodes(ctx, suiteRestCfg)
	if err != nil {
		return "", "", err
	}
	var names []string
	for i := range workers {
		if nodeSchedulable(&workers[i]) {
			names = append(names, workers[i].Name)
		}
	}
	if len(names) < 2 {
		return "", "", nil
	}
	return names[0], names[1], nil
}

// buildBlockPVC is buildLifecyclePVC with volumeMode: Block (raw device).
func buildBlockPVC(name, sc, size string, mode corev1.PersistentVolumeAccessMode) *corev1.PersistentVolumeClaim {
	pvc := buildLifecyclePVC(name, sc, size, mode, nil)
	block := corev1.PersistentVolumeBlock
	pvc.Spec.VolumeMode = &block
	return pvc
}

// buildBlockPod attaches pvc as a raw block device at blockDevicePath and writes
// marker to the start of the device at boot (via dd), then idles.
func buildBlockPod(name, pvc, marker string) *corev1.Pod {
	script := fmt.Sprintf(`printf '%%s' "$MARKER" | dd of=%s 2>/dev/null; sync; sleep 360000`, blockDevicePath)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: suiteCfg.namespace,
			Name:      name,
			Labels:    map[string]string{"app": "csi-ceph-e2e-block"},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:          probeContainerName,
				Image:         suiteCfg.probeImage,
				Command:       []string{"sh", "-c", script},
				Env:           []corev1.EnvVar{{Name: "MARKER", Value: marker}},
				VolumeDevices: []corev1.VolumeDevice{{Name: "data", DevicePath: blockDevicePath}},
			}},
			Volumes: []corev1.Volume{{
				Name: "data",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvc},
				},
			}},
		},
	}
}

// verifyBlockData reads len(want) bytes from the raw block device and asserts
// they equal want.
func verifyBlockData(ctx context.Context, podName, want string) error {
	out, stderr, err := storagekube.ExecInPod(ctx, suiteRestCfg, suiteCfg.namespace, podName, probeContainerName,
		[]string{"dd", "if=" + blockDevicePath, "bs=1", fmt.Sprintf("count=%d", len(want))})
	if err != nil {
		return fmt.Errorf("read block device in %s/%s: %w (stderr: %s)", suiteCfg.namespace, podName, err, stderr)
	}
	if strings.TrimSpace(out) != want {
		return fmt.Errorf("block data mismatch in %s/%s: want %q, got %q", suiteCfg.namespace, podName, want, strings.TrimSpace(out))
	}
	return nil
}

// resizePVCAndWait patches the PVC request up to newSize and waits for
// status.capacity to reflect it (controller + node expansion complete).
func resizePVCAndWait(ctx context.Context, name, newSize string, timeout time.Duration) error {
	want := resource.MustParse(newSize)

	var pvc corev1.PersistentVolumeClaim
	if err := suiteK8s.Get(ctx, client.ObjectKey{Namespace: suiteCfg.namespace, Name: name}, &pvc); err != nil {
		return fmt.Errorf("get pvc %s: %w", name, err)
	}
	pvc.Spec.Resources.Requests[corev1.ResourceStorage] = want
	if err := suiteK8s.Update(ctx, &pvc); err != nil {
		return fmt.Errorf("resize pvc %s: %w", name, err)
	}

	deadline := time.Now().Add(timeout)
	for {
		var cur corev1.PersistentVolumeClaim
		err := suiteK8s.Get(ctx, client.ObjectKey{Namespace: suiteCfg.namespace, Name: name}, &cur)
		if err == nil {
			if cap, ok := cur.Status.Capacity[corev1.ResourceStorage]; ok && cap.Cmp(want) >= 0 {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for pvc %s to reach %s (status.capacity=%v)", name, newSize, cur.Status.Capacity)
		}
		if !sleepCtx(ctx, pollInterval) {
			return ctx.Err()
		}
	}
}

// ensureVolumeSnapshotClass derives a ceph-csi VolumeSnapshotClass from the
// StorageClass csi-ceph created (clusterID + the provisioner secret, reused as
// the snapshotter secret). Idempotent.
func ensureVolumeSnapshotClass(ctx context.Context, scName, driver string) (string, error) {
	var sc storagev1.StorageClass
	if err := suiteK8s.Get(ctx, client.ObjectKey{Name: scName}, &sc); err != nil {
		return "", fmt.Errorf("get StorageClass %s: %w", scName, err)
	}
	clusterID := sc.Parameters["clusterID"]
	secretName := sc.Parameters["csi.storage.k8s.io/provisioner-secret-name"]
	secretNS := sc.Parameters["csi.storage.k8s.io/provisioner-secret-namespace"]
	if clusterID == "" || secretName == "" || secretNS == "" {
		return "", fmt.Errorf("StorageClass %s is missing ceph-csi params (clusterID/provisioner-secret)", scName)
	}

	name := scName + "-snap"
	vsc := &unstructured.Unstructured{}
	vsc.SetGroupVersionKind(schema.GroupVersionKind{Group: "snapshot.storage.k8s.io", Version: "v1", Kind: "VolumeSnapshotClass"})
	vsc.SetName(name)
	vsc.Object["driver"] = driver
	vsc.Object["deletionPolicy"] = "Delete"
	vsc.Object["parameters"] = map[string]interface{}{
		"clusterID": clusterID,
		"csi.storage.k8s.io/snapshotter-secret-name":      secretName,
		"csi.storage.k8s.io/snapshotter-secret-namespace": secretNS,
	}

	_, err := suiteDyn.Resource(volumeSnapshotClassGVR).Create(ctx, vsc, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create VolumeSnapshotClass %s: %w", name, err)
	}
	return name, nil
}

func createSnapshotWait(ctx context.Context, name, srcPVC, snapClass string, timeout time.Duration) error {
	snap := &unstructured.Unstructured{}
	snap.SetGroupVersionKind(schema.GroupVersionKind{Group: "snapshot.storage.k8s.io", Version: "v1", Kind: "VolumeSnapshot"})
	snap.SetNamespace(suiteCfg.namespace)
	snap.SetName(name)
	snap.Object["spec"] = map[string]interface{}{
		"volumeSnapshotClassName": snapClass,
		"source": map[string]interface{}{
			"persistentVolumeClaimName": srcPVC,
		},
	}
	if _, err := suiteDyn.Resource(volumeSnapshotGVR).Namespace(suiteCfg.namespace).Create(ctx, snap, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create VolumeSnapshot %s: %w", name, err)
	}

	deadline := time.Now().Add(timeout)
	var last string
	for {
		obj, err := suiteDyn.Resource(volumeSnapshotGVR).Namespace(suiteCfg.namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			ready, found, _ := unstructured.NestedBool(obj.Object, "status", "readyToUse")
			if found && ready {
				return nil
			}
			if msg, ok, _ := unstructured.NestedString(obj.Object, "status", "error", "message"); ok && msg != "" {
				last = msg
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for VolumeSnapshot %s readyToUse; last error: %s", name, last)
		}
		if !sleepCtx(ctx, pollInterval) {
			return ctx.Err()
		}
	}
}

// --- deletion helpers ------------------------------------------------------

func deletePodWait(ctx context.Context, podName string) error {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: suiteCfg.namespace, Name: podName}}
	if err := suiteK8s.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete pod %s: %w", podName, err)
	}
	return waitPodGone(ctx, podName, podReadyTimeout)
}

func deletePodBestEffort(ctx context.Context, podName string) error {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: suiteCfg.namespace, Name: podName}}
	if err := suiteK8s.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete pod %s: %w", podName, err)
	}
	return waitPodGone(ctx, podName, podReadyTimeout)
}

// deletePVCWaitGone deletes the PVC and waits for it to disappear. With the
// StorageClass's default (Delete) reclaim policy this proves csi-ceph called
// DeleteVolume and reclaimed the backing RBD image / CephFS subvolume.
func deletePVCWaitGone(ctx context.Context, name string, timeout time.Duration) error {
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: suiteCfg.namespace, Name: name}}
	if err := suiteK8s.Delete(ctx, pvc); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete pvc %s: %w", name, err)
	}
	deadline := time.Now().Add(timeout)
	for {
		var cur corev1.PersistentVolumeClaim
		err := suiteK8s.Get(ctx, client.ObjectKey{Namespace: suiteCfg.namespace, Name: name}, &cur)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for pvc %s to be deleted", name)
		}
		if !sleepCtx(ctx, pollInterval) {
			return ctx.Err()
		}
	}
}
