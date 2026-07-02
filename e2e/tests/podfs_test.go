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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	storagekube "github.com/deckhouse/storage-e2e/pkg/kubernetes"
)

// createProbePodWithPVC provisions a PVC against scName and a Pod that mounts
// it, writes `marker` to /data/probe.txt, reads it back to its log (a genuine
// filesystem round-trip through the csi-ceph driver), then sleeps. It waits for
// the PVC to Bind and the Pod to become Ready. Idempotent against AlreadyExists.
func createProbePodWithPVC(ctx context.Context, scName, pvcName, podName, marker string) error {
	pvc := buildProbePVC(pvcName, scName)
	if err := suiteK8s.Create(ctx, pvc); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create pvc %s/%s: %w", suiteCfg.namespace, pvcName, err)
	}

	pod := buildProbePod(podName, pvcName, marker)
	if err := suiteK8s.Create(ctx, pod); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create pod %s/%s: %w", suiteCfg.namespace, podName, err)
	}

	if err := waitPVCBound(ctx, pvcName, pvcBindTimeout); err != nil {
		return err
	}
	if err := waitPodReady(ctx, podName, podReadyTimeout); err != nil {
		return err
	}
	return nil
}

// verifyProbeFile cat's /data/probe.txt from inside the probe Pod and asserts
// it equals `want` (trimmed). Reads the live volume, not the boot-time log.
func verifyProbeFile(ctx context.Context, podName, want string) error {
	got, err := storagekube.ReadFileFromPod(ctx, suiteRestCfg, suiteCfg.namespace, podName, probeContainerName, probeFilePath)
	if err != nil {
		return fmt.Errorf("read %s from %s/%s: %w", probeFilePath, suiteCfg.namespace, podName, err)
	}
	if strings.TrimSpace(got) != want {
		return fmt.Errorf("probe file mismatch in %s/%s: want %q, got %q", suiteCfg.namespace, podName, want, strings.TrimSpace(got))
	}
	return nil
}

// deleteProbe removes the probe Pod and PVC (best-effort; NotFound is ignored)
// and waits for the Pod to be gone. With the default (Delete) reclaim policy on
// the csi-ceph StorageClass, removing the PVC makes the external provisioner
// destroy the backing RBD image / CephFS subvolume, so the ElasticStorageClass
// can then be torn down without tripping its bound-volume guard.
func deleteProbe(ctx context.Context, pvcName, podName string) error {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: suiteCfg.namespace, Name: podName}}
	if err := suiteK8s.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete pod %s/%s: %w", suiteCfg.namespace, podName, err)
	}
	if err := waitPodGone(ctx, podName, podReadyTimeout); err != nil {
		return err
	}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: suiteCfg.namespace, Name: pvcName}}
	if err := suiteK8s.Delete(ctx, pvc); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete pvc %s/%s: %w", suiteCfg.namespace, pvcName, err)
	}
	return nil
}

func buildProbePVC(name, scName string) *corev1.PersistentVolumeClaim {
	sc := scName
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: suiteCfg.namespace,
			Name:      name,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: &sc,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(suiteCfg.pvcSize),
				},
			},
		},
	}
}

func buildProbePod(name, pvcName, marker string) *corev1.Pod {
	script := fmt.Sprintf(`echo -n "$MARKER" > %s && sync && cat %s && sleep 360000`, probeFilePath, probeFilePath)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: suiteCfg.namespace,
			Name:      name,
			Labels:    map[string]string{"app": "csi-ceph-e2e-probe"},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:    probeContainerName,
					Image:   suiteCfg.probeImage,
					Command: []string{"sh", "-c", script},
					Env:     []corev1.EnvVar{{Name: "MARKER", Value: marker}},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "data", MountPath: probeMountPath},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "data",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: pvcName,
						},
					},
				},
			},
		},
	}
}

func waitPVCBound(ctx context.Context, pvcName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		var pvc corev1.PersistentVolumeClaim
		err := suiteK8s.Get(ctx, client.ObjectKey{Namespace: suiteCfg.namespace, Name: pvcName}, &pvc)
		if err == nil && pvc.Status.Phase == corev1.ClaimBound {
			return nil
		}
		if time.Now().After(deadline) {
			phase := corev1.ClaimPending
			if err == nil {
				phase = pvc.Status.Phase
			}
			return fmt.Errorf("timeout waiting for pvc %s/%s to Bind (phase=%s, lastErr=%v)", suiteCfg.namespace, pvcName, phase, err)
		}
		if !sleepCtx(ctx, pollInterval) {
			return ctx.Err()
		}
	}
}

func waitPodReady(ctx context.Context, podName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		var pod corev1.Pod
		err := suiteK8s.Get(ctx, client.ObjectKey{Namespace: suiteCfg.namespace, Name: podName}, &pod)
		if err == nil && isPodReady(&pod) {
			return nil
		}
		if time.Now().After(deadline) {
			phase := corev1.PodUnknown
			if err == nil {
				phase = pod.Status.Phase
			}
			return fmt.Errorf("timeout waiting for pod %s/%s to be Ready (phase=%s, lastErr=%v)", suiteCfg.namespace, podName, phase, err)
		}
		if !sleepCtx(ctx, pollInterval) {
			return ctx.Err()
		}
	}
}

func waitPodGone(ctx context.Context, podName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		var pod corev1.Pod
		err := suiteK8s.Get(ctx, client.ObjectKey{Namespace: suiteCfg.namespace, Name: podName}, &pod)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for pod %s/%s to be deleted (lastErr=%v)", suiteCfg.namespace, podName, err)
		}
		if !sleepCtx(ctx, pollInterval) {
			return ctx.Err()
		}
	}
}

func isPodReady(p *corev1.Pod) bool {
	if p.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, cond := range p.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}
