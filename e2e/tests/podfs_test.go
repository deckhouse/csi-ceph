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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/onsi/ginkgo/v2"

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

// eventuallyPodFileMatches polls `path` from inside the latest Ready
// pod matching app=appLabel until predicate returns true on the file
// body, or `timeout` elapses. Returns the last successfully read body
// (useful as the "newBody" snapshot for a configChanged delta).
//
// Reader lifecycle is owned by this helper:
//
//   - If `initial` is non-nil and its PodName() still matches the
//     current latestReadyPod, it is reused — zero ephemeral-container
//     cold-starts on the no-rollout branch.
//   - If pod identity changes (Deployment rollout completed and
//     latestReadyPod returns a new pod) OR ReadFile fails because the
//     bound pod is gone (apierrors.IsNotFound), the helper drops the
//     stale reader and opens a new one against the new latest Ready
//     pod via storagekube.OpenDistrolessReader. At most one extra
//     cold-start per identity change.
//   - During the brief vacuum between an old pod's deletion and the
//     new pod becoming Ready, latestReadyPod returns an error; we
//     stash it as lastErr and retry after pollInterval.
//
// `mountDir` is required so the helper can re-resolve the target
// container after a rollout — different pod, in principle different
// spec. `initial` may target a different mountDir/container than the
// one the helper would pick — that's fine; it stays bound to its own
// pod.
//
// kubeletSyncTimeout is the right ballpark for `timeout`: projected
// ConfigMap volumes are synced into pod mounts no slower than
// kubelet's --sync-frequency (default 1m).
func eventuallyPodFileMatches(
	ctx context.Context,
	restCfg *rest.Config,
	c client.Client,
	namespace, appLabel, mountDir, path string,
	initial *storagekube.DistrolessReader,
	predicate func(string) bool,
	timeout time.Duration,
) (string, error) {
	deadlineCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	reader := initial
	var (
		lastBody string
		lastErr  error
	)
	for {
		pod, err := latestReadyPod(deadlineCtx, c, namespace, appLabel)
		if err != nil {
			lastErr = err
			if !sleepOrDeadline(deadlineCtx) {
				return lastBody, timeoutErr(timeout, namespace, appLabel, path, lastBody, lastErr)
			}
			continue
		}

		if reader == nil || reader.PodName() != pod.Name {
			container := findContainerMountingPath(pod, mountDir)
			if container == "" {
				lastErr = fmt.Errorf("no container in %s/%s mounts %s", namespace, pod.Name, mountDir)
				if !sleepOrDeadline(deadlineCtx) {
					return lastBody, timeoutErr(timeout, namespace, appLabel, path, lastBody, lastErr)
				}
				continue
			}
			r, openErr := storagekube.OpenDistrolessReader(
				deadlineCtx, restCfg, namespace, pod.Name, container,
				storagekube.ReadFileOptions{},
			)
			if openErr != nil {
				lastErr = openErr
				if !sleepOrDeadline(deadlineCtx) {
					return lastBody, timeoutErr(timeout, namespace, appLabel, path, lastBody, lastErr)
				}
				continue
			}
			if reader != nil {
				_, _ = fmt.Fprintf(GinkgoWriter,
					"  reader rebound to new Ready pod: app=%s old=%s new=%s\n",
					appLabel, reader.PodName(), pod.Name)
			}
			reader = r
		}

		body, readErr := reader.ReadFile(deadlineCtx, path)
		if readErr == nil {
			lastBody = body
			if predicate(body) {
				return body, nil
			}
			lastErr = nil
		} else {
			lastErr = readErr
			if isPodGone(readErr) {
				// The bound pod was deleted out from under us;
				// drop the reader so the next iteration rebinds
				// to whatever latestReadyPod returns.
				reader = nil
			}
		}

		if !sleepOrDeadline(deadlineCtx) {
			return lastBody, timeoutErr(timeout, namespace, appLabel, path, lastBody, lastErr)
		}
	}
}

// sleepOrDeadline returns true if it slept for pollInterval, false if
// the context deadline was reached first.
func sleepOrDeadline(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(pollInterval):
		return true
	}
}

func timeoutErr(timeout time.Duration, namespace, appLabel, path, lastBody string, lastErr error) error {
	if lastErr != nil {
		return fmt.Errorf("timeout (%s) waiting for %s in %s/app=%s to satisfy predicate; last error: %w",
			timeout, path, namespace, appLabel, lastErr)
	}
	return fmt.Errorf("timeout (%s) waiting for %s in %s/app=%s to satisfy predicate; last body=%q",
		timeout, path, namespace, appLabel, lastBody)
}

// isPodGone returns true if `err` was caused by the apiserver reporting
// the target pod as 404. Used to detect mid-poll Deployment rollouts:
// the old reader's pod just got deleted, and the helper needs to
// rebind to whatever latestReadyPod returns next. apierrors.IsNotFound
// already walks the wrap chain via errors.As, so we don't need an
// additional Unwrap loop here.
func isPodGone(err error) bool {
	return err != nil && apierrors.IsNotFound(err)
}
