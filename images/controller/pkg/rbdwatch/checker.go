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

package rbdwatch

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/cephconn"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/internal"
	"github.com/deckhouse/sds-common-lib/logger"
)

const (
	CheckerName     = "rbd-watch-checker"
	Driver          = "rbd.csi.ceph.com"
	DefaultInterval = 5 * time.Minute

	connectionTimeout = 4 * time.Minute

	EventReason = "RBDImageNotWatched"
	EventAction = "CheckWatchers"
)

type Image struct {
	Pool           string
	RadosNamespace string
	Name           string
}

func (i Image) String() string {
	if i.RadosNamespace == "" {
		return i.Pool + "/" + i.Name
	}
	return i.Pool + "/" + i.RadosNamespace + "/" + i.Name
}

type Credentials = cephconn.Credentials

type WatcherCounter interface {
	CountWatchers(ctx context.Context, creds Credentials, images []Image) (map[Image]int, error)
}

type EventRecorder interface {
	Eventf(regarding runtime.Object, related runtime.Object, eventtype, reason, action, note string, args ...any)
}

type Volume struct {
	PersistentVolume string
	Connection       string
	Image            Image
	Nodes            []string
}

type Unwatched struct {
	Volume
	Watchers int
}

type Result struct {
	Unwatched   []Unwatched
	Connections map[string]bool
}

type Checker struct {
	Client   client.Reader
	Counter  WatcherCounter
	Recorder EventRecorder
	Log      logger.Logger
	Interval time.Duration

	mu     sync.Mutex
	result Result
}

func (c *Checker) NeedLeaderElection() bool {
	return true
}

func (c *Checker) Start(ctx context.Context) error {
	interval := c.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		c.Run(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (c *Checker) Run(ctx context.Context) {
	result, err := c.Check(ctx)
	if err != nil {
		c.Log.Error("unable to check the watchers of the attached RBD volumes", logger.Err(err))
		return
	}

	c.mu.Lock()
	c.result = result
	c.mu.Unlock()

	for _, u := range result.Unwatched {
		c.Log.Warn("the RBD image of an attached volume has fewer watchers than nodes it is attached to",
			"persistentVolume", u.PersistentVolume,
			"cephClusterConnection", u.Connection,
			"image", u.Image.String(),
			"nodes", u.Nodes,
			"watchers", u.Watchers)
		c.recordEvent(ctx, u)
	}
}

func (c *Checker) Result() Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.result
}

func (c *Checker) Check(ctx context.Context) (Result, error) {
	volumes, err := c.attachedVolumes(ctx)
	if err != nil {
		return Result{}, err
	}

	byConnection := map[string][]Volume{}
	for _, v := range volumes {
		byConnection[v.Connection] = append(byConnection[v.Connection], v)
	}

	result := Result{Connections: make(map[string]bool, len(byConnection))}
	var mu sync.Mutex
	cephconn.ForEach(ctx, slices.Sorted(maps.Keys(byConnection)), cephconn.ParallelConnections, connectionTimeout, func(ctx context.Context, name string) {
		unwatched, err := c.checkConnection(ctx, name, byConnection[name])
		mu.Lock()
		result.Unwatched = append(result.Unwatched, unwatched...)
		result.Connections[name] = err == nil
		mu.Unlock()
		if err != nil {
			c.Log.Error("unable to count the watchers of the RBD images", logger.Err(err), "cephClusterConnection", name)
		}
	})
	slices.SortFunc(result.Unwatched, func(a, b Unwatched) int { return strings.Compare(a.PersistentVolume, b.PersistentVolume) })

	return result, nil
}

func (c *Checker) checkConnection(ctx context.Context, name string, volumes []Volume) ([]Unwatched, error) {
	connection := &v1alpha1.CephClusterConnection{}
	if err := c.Client.Get(ctx, client.ObjectKey{Name: name}, connection); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("the CephClusterConnection %s does not exist", name)
		}
		return nil, fmt.Errorf("unable to get the CephClusterConnection %s: %w", name, err)
	}

	images := make([]Image, 0, len(volumes))
	for _, v := range volumes {
		if !slices.Contains(images, v.Image) {
			images = append(images, v.Image)
		}
	}

	counts, err := c.Counter.CountWatchers(ctx, Credentials{
		Name:     name,
		Monitors: connection.Spec.Monitors,
		UserID:   connection.Spec.UserID,
		UserKey:  connection.Spec.UserKey,
	}, images)

	var unwatched []Unwatched
	for _, v := range volumes {
		watchers, ok := counts[v.Image]
		if ok && watchers < len(v.Nodes) {
			unwatched = append(unwatched, Unwatched{Volume: v, Watchers: watchers})
		}
	}

	return unwatched, err
}

func (c *Checker) attachedVolumes(ctx context.Context) ([]Volume, error) {
	attachments := &storagev1.VolumeAttachmentList{}
	if err := c.Client.List(ctx, attachments); err != nil {
		return nil, fmt.Errorf("unable to list VolumeAttachments: %w", err)
	}

	nodes := map[string][]string{}
	for _, a := range attachments.Items {
		if a.Spec.Attacher != Driver || !a.Status.Attached || a.Spec.Source.PersistentVolumeName == nil || a.DeletionTimestamp != nil {
			continue
		}
		pv := *a.Spec.Source.PersistentVolumeName
		if !slices.Contains(nodes[pv], a.Spec.NodeName) {
			nodes[pv] = append(nodes[pv], a.Spec.NodeName)
		}
	}
	if len(nodes) == 0 {
		return nil, nil
	}

	pvs := &corev1.PersistentVolumeList{}
	if err := c.Client.List(ctx, pvs); err != nil {
		return nil, fmt.Errorf("unable to list PersistentVolumes: %w", err)
	}

	var volumes []Volume
	for i := range pvs.Items {
		pv := &pvs.Items[i]
		attachedTo, ok := nodes[pv.Name]
		if !ok {
			continue
		}
		v, ok := c.volume(pv)
		if !ok {
			c.Log.Debug("skipping an attached volume that names no RBD image or CephClusterConnection", "persistentVolume", pv.Name)
			continue
		}
		slices.Sort(attachedTo)
		v.Nodes = attachedTo
		volumes = append(volumes, v)
	}

	slices.SortFunc(volumes, func(a, b Volume) int { return strings.Compare(a.PersistentVolume, b.PersistentVolume) })
	return volumes, nil
}

func (c *Checker) volume(pv *corev1.PersistentVolume) (Volume, bool) {
	csi := pv.Spec.CSI
	if csi == nil || csi.Driver != Driver || csi.NodeStageSecretRef == nil {
		return Volume{}, false
	}

	connection, ok := strings.CutPrefix(csi.NodeStageSecretRef.Name, internal.CephClusterConnectionSecretPrefix)
	if !ok || connection == "" {
		return Volume{}, false
	}

	name := csi.VolumeAttributes["imageName"]
	if name == "" && csi.VolumeAttributes["staticVolume"] == "true" {
		name = csi.VolumeHandle
	}
	pool := csi.VolumeAttributes["pool"]
	if name == "" || pool == "" {
		return Volume{}, false
	}

	return Volume{
		PersistentVolume: pv.Name,
		Connection:       connection,
		Image: Image{
			Pool:           pool,
			RadosNamespace: csi.VolumeAttributes["radosNamespace"],
			Name:           name,
		},
	}, true
}

func (c *Checker) recordEvent(ctx context.Context, u Unwatched) {
	if c.Recorder == nil {
		return
	}

	pv := &corev1.PersistentVolume{}
	if err := c.Client.Get(ctx, client.ObjectKey{Name: u.PersistentVolume}, pv); err != nil {
		return
	}

	c.Recorder.Eventf(pv, nil, corev1.EventTypeWarning, EventReason, EventAction,
		"The RBD image %s is attached to the nodes %s, but has %d watchers. The kernel RBD client of a node without a watch has either not mapped the image or lost its connection to it, most likely because Ceph added the client to the OSD blocklist; reads and writes of the volume on that node then fail. Find the node, drain and reboot it, as the csi-ceph FAQ describes.",
		u.Image.String(), strings.Join(u.Nodes, ", "), u.Watchers)
}
