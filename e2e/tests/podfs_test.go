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
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	storagekube "github.com/deckhouse/storage-e2e/pkg/kubernetes"
)

// latestReadyPod returns the most recently created Ready pod matching
// `app=<appLabel>` in `namespace`. Used by the msCrcData matrix to make
// sure we read /etc/ceph/ceph.conf from the post-rollout pod (when one
// happened) rather than from a Terminating predecessor.
//
// Returns an explicit error when no Ready pod is found instead of nil, so
// callers can wrap it in Eventually without ambiguity.
func latestReadyPod(ctx context.Context, c client.Client, namespace, appLabel string) (*corev1.Pod, error) {
	var pods corev1.PodList
	if err := c.List(ctx, &pods,
		client.InNamespace(namespace),
		client.MatchingLabels{"app": appLabel},
	); err != nil {
		return nil, fmt.Errorf("list pods app=%s in %s: %w", appLabel, namespace, err)
	}

	ready := pods.Items[:0]
	for i := range pods.Items {
		p := pods.Items[i]
		if p.DeletionTimestamp != nil {
			continue
		}
		if isPodReady(&p) {
			ready = append(ready, p)
		}
	}
	if len(ready) == 0 {
		return nil, fmt.Errorf("no Ready pods app=%s in %s (total=%d)", appLabel, namespace, len(pods.Items))
	}

	sort.SliceStable(ready, func(i, j int) bool {
		return ready[i].CreationTimestamp.After(ready[j].CreationTimestamp.Time)
	})
	pod := ready[0]
	return &pod, nil
}

// findContainerMountingPath returns the name of the first container in
// `pod` whose volumeMounts contain a mountPath equal to or under
// `mountPath`. Returns "" if no container matches.
//
// Used by the msCrcData matrix to figure out which of the (typically
// many) containers in a csi-controller-{rbd,cephfs} pod actually has
// /etc/ceph/ projected — sidecars (csi-provisioner, csi-attacher, …)
// share most of the pod but not the ceph-config mount, so reading the
// file from them would lie about the driver's actual view.
func findContainerMountingPath(pod *corev1.Pod, mountPath string) string {
	if pod == nil {
		return ""
	}
	for _, c := range pod.Spec.Containers {
		for _, m := range c.VolumeMounts {
			if m.MountPath == mountPath || (len(m.MountPath) >= len(mountPath) && m.MountPath[:len(mountPath)] == mountPath) {
				return c.Name
			}
		}
	}
	return ""
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

// eventuallyPodFileMatches polls the latest Ready pod of the given
// Deployment/DaemonSet `appLabel` and reads `path` from `targetContainer`
// (treated as distroless — the read goes through an injected ephemeral
// container, see storagekube.ReadFileFromDistrolessPod) until `predicate`
// returns true on the file body, or `timeout` elapses.
//
// Returns the last successfully read body. Useful as the post-flip
// snapshot — caller compares it to the pre-flip body to decide whether
// a rollout is expected.
//
// kubeletSyncTimeout is the right ballpark for `timeout`: projected
// ConfigMap volumes are synced into pod mounts no slower than kubelet's
// `--sync-frequency` (default 1m).
func eventuallyPodFileMatches(
	ctx context.Context,
	restCfg *rest.Config,
	c client.Client,
	namespace, appLabel, targetContainer, path string,
	predicate func(string) bool,
	timeout time.Duration,
) (string, error) {
	deadlineCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var (
		lastBody string
		lastErr  error
	)
	for {
		pod, err := latestReadyPod(deadlineCtx, c, namespace, appLabel)
		if err != nil {
			lastErr = err
		} else {
			body, err := storagekube.ReadFileFromDistrolessPod(
				deadlineCtx, restCfg, namespace, pod.Name, targetContainer, path,
				storagekube.ReadFileOptions{},
			)
			if err == nil {
				lastBody = body
				if predicate(body) {
					return body, nil
				}
			} else {
				lastErr = err
			}
		}

		select {
		case <-deadlineCtx.Done():
			if lastErr != nil {
				return lastBody, fmt.Errorf("timeout (%s) waiting for %s in %s/app=%s to satisfy predicate; last error: %w",
					timeout, path, namespace, appLabel, lastErr)
			}
			return lastBody, fmt.Errorf("timeout (%s) waiting for %s in %s/app=%s to satisfy predicate; last body=%q",
				timeout, path, namespace, appLabel, lastBody)
		case <-time.After(pollInterval):
		}
	}
}
