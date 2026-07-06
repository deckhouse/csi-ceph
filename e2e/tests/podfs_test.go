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
	"sigs.k8s.io/controller-runtime/pkg/client"

	storagekube "github.com/deckhouse/storage-e2e/pkg/kubernetes"
)

// verifyProbeFile cat's /data/probe.txt from inside the probe Pod and asserts
// it equals `want` (trimmed). Reads the live volume through the csi-ceph mount.
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
