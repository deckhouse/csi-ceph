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

package rbdcapacity

import (
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
	"sync"
	"time"

	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/cephconn"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/rbdwatch"
	"github.com/deckhouse/sds-common-lib/conditions"
	"github.com/deckhouse/sds-common-lib/logger"
)

const (
	PublisherName         = "rbd-capacity-publisher"
	DefaultInterval       = time.Minute
	DefaultDetailInterval = 10 * time.Minute

	refreshPeriod     = 5 * time.Minute
	changeShare       = 0.01
	connectionTimeout = 2 * time.Minute
	granularity       = int64(1) << 20
)

type PoolStats struct {
	ID            int64
	Stored        int64
	MaxAvail      int64
	Quota         int64
	ImagesInTrash int
	HeldByDeleted *int64
	MeasureErr    error
}

type PoolReader interface {
	ReadPools(ctx context.Context, creds cephconn.Credentials, pools []string, measureTrash bool) (map[string]*PoolStats, error)
}

type Publisher struct {
	Client         client.Client
	Reader         PoolReader
	Log            logger.Logger
	Interval       time.Duration
	DetailInterval time.Duration
	Now            func() time.Time

	mu         sync.Mutex
	reads      map[string]bool
	measuredAt map[string]time.Time
	details    map[poolKey]detail
}

type poolKey struct {
	connection string
	pool       string
}

type detail struct {
	allocated     int64
	heldByDeleted *int64
}

func (p *Publisher) NeedLeaderElection() bool {
	return true
}

func (p *Publisher) Start(ctx context.Context) error {
	interval := p.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if err := p.Sync(ctx); err != nil {
			p.Log.Error("unable to publish the capacity of the RBD CephStorageClasses", logger.Err(err))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (p *Publisher) Reads() map[string]bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return maps.Clone(p.reads)
}

func (p *Publisher) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Publisher) Sync(ctx context.Context) error {
	classes := &v1alpha1.CephStorageClassList{}
	if err := p.Client.List(ctx, classes); err != nil {
		return fmt.Errorf("unable to list CephStorageClasses: %w", err)
	}

	byConnection := map[string][]*v1alpha1.CephStorageClass{}
	for i := range classes.Items {
		class := &classes.Items[i]
		if class.DeletionTimestamp != nil || class.Spec.Type != v1alpha1.CephStorageClassTypeRBD || class.Spec.RBD == nil || class.Spec.RBD.Pool == "" {
			continue
		}
		byConnection[class.Spec.ClusterConnectionName] = append(byConnection[class.Spec.ClusterConnectionName], class)
	}

	p.mu.Lock()
	for name := range p.reads {
		if _, ok := byConnection[name]; !ok {
			delete(p.reads, name)
		}
	}
	for name := range p.measuredAt {
		if _, ok := byConnection[name]; !ok {
			delete(p.measuredAt, name)
		}
	}
	for key := range p.details {
		if _, ok := byConnection[key.connection]; !ok {
			delete(p.details, key)
		}
	}
	p.mu.Unlock()

	cephconn.ForEach(ctx, slices.Sorted(maps.Keys(byConnection)), cephconn.ParallelConnections, connectionTimeout, func(ctx context.Context, name string) {
		p.syncConnection(ctx, name, byConnection[name])
	})

	return nil
}

func (p *Publisher) syncConnection(ctx context.Context, name string, classes []*v1alpha1.CephStorageClass) {
	connection := &v1alpha1.CephClusterConnection{}
	if err := p.Client.Get(ctx, client.ObjectKey{Name: name}, connection); err != nil {
		p.recordRead(name, fmt.Errorf("unable to get the CephClusterConnection: %w", err), nil)
		return
	}

	var pools []string
	for _, class := range classes {
		if !slices.Contains(pools, class.Spec.RBD.Pool) {
			pools = append(pools, class.Spec.RBD.Pool)
		}
	}
	slices.Sort(pools)

	measure := p.dueForDetails(name, p.now())
	stats, err := p.Reader.ReadPools(ctx, cephconn.Credentials{
		Name:     name,
		Monitors: connection.Spec.Monitors,
		UserID:   connection.Spec.UserID,
		UserKey:  connection.Spec.UserKey,
	}, pools, measure)
	readAt := p.now()
	p.recordRead(name, err, pools)
	if measure && stats != nil {
		p.measure(ctx, name, connection.Spec.ClusterID, stats, readAt)
	}

	for _, class := range classes {
		pool := class.Spec.RBD.Pool
		s, ok := stats[pool]
		if !ok {
			continue
		}
		next := capacityFrom(pool, s, p.detail(name, pool))
		var prev *v1alpha1.CephStorageClassCapacity
		if class.Status != nil {
			prev = class.Status.Capacity
		}
		if !needsWrite(prev, next, readAt) {
			continue
		}
		if err := publish(ctx, p.Client, class, next, readAt); err != nil {
			p.Log.Error("unable to publish the capacity of the CephStorageClass", logger.Err(err), "cephStorageClass", class.Name)
		}
	}
}

func (p *Publisher) recordRead(name string, err error, pools []string) {
	p.mu.Lock()
	if p.reads == nil {
		p.reads = map[string]bool{}
	}
	prev, seen := p.reads[name]
	p.reads[name] = err == nil
	p.mu.Unlock()

	switch {
	case err != nil && (!seen || prev):
		p.Log.Error("unable to read the space of the RBD pools; the published figures stay until a read succeeds", logger.Err(err), "cephClusterConnection", name, "pools", pools)
	case err != nil:
		p.Log.Debug("the space of the RBD pools is still unreadable", "error", err.Error(), "cephClusterConnection", name, "pools", pools)
	case seen && !prev:
		p.Log.Info("the space of the RBD pools is readable again", "cephClusterConnection", name)
	}
}

func (p *Publisher) dueForDetails(name string, now time.Time) bool {
	interval := p.DetailInterval
	if interval <= 0 {
		interval = DefaultDetailInterval
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	last, ok := p.measuredAt[name]
	return !ok || now.Sub(last) >= interval
}

func (p *Publisher) detail(name, pool string) *detail {
	p.mu.Lock()
	defer p.mu.Unlock()
	d, ok := p.details[poolKey{connection: name, pool: pool}]
	if !ok {
		return nil
	}
	return &d
}

func (p *Publisher) measure(ctx context.Context, name, clusterID string, stats map[string]*PoolStats, now time.Time) {
	for pool, s := range stats {
		if s != nil && s.MeasureErr != nil {
			p.Log.Info("unable to measure the space the RBD trash holds; heldByDeleted is left out", "error", s.MeasureErr.Error(), "cephClusterConnection", name, "pool", pool)
		}
	}

	allocated, err := allocatedInPools(ctx, p.Client, clusterID, stats)
	if err != nil {
		p.Log.Error("unable to sum the provisioned size of the RBD volumes and snapshots", logger.Err(err), "cephClusterConnection", name)
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.measuredAt == nil {
		p.measuredAt = map[string]time.Time{}
	}
	if p.details == nil {
		p.details = map[poolKey]detail{}
	}
	p.measuredAt[name] = now
	for pool, s := range stats {
		key := poolKey{connection: name, pool: pool}
		if s == nil {
			delete(p.details, key)
			continue
		}
		p.details[key] = detail{allocated: allocated[pool], heldByDeleted: s.HeldByDeleted}
	}
}

func allocatedInPools(ctx context.Context, cl client.Reader, clusterID string, stats map[string]*PoolStats) (map[string]int64, error) {
	byID := map[int64]string{}
	for pool, s := range stats {
		if s != nil {
			byID[s.ID] = pool
		}
	}
	allocated := map[string]int64{}

	pvs := &corev1.PersistentVolumeList{}
	if err := cl.List(ctx, pvs); err != nil {
		return nil, fmt.Errorf("unable to list PersistentVolumes: %w", err)
	}
	for i := range pvs.Items {
		pv := &pvs.Items[i]
		csi := pv.Spec.CSI
		if csi == nil || csi.Driver != rbdwatch.Driver || csi.VolumeAttributes["clusterID"] != clusterID {
			continue
		}
		pool := csi.VolumeAttributes["pool"]
		if s, ok := stats[pool]; !ok || s == nil {
			continue
		}
		size := pv.Spec.Capacity[corev1.ResourceStorage]
		allocated[pool] += size.Value()
	}

	contents := &snapshotv1.VolumeSnapshotContentList{}
	if err := cl.List(ctx, contents); err != nil {
		return nil, fmt.Errorf("unable to list VolumeSnapshotContents: %w", err)
	}
	for i := range contents.Items {
		c := &contents.Items[i]
		if c.Spec.Driver != rbdwatch.Driver || c.Status == nil || c.Status.SnapshotHandle == nil || c.Status.RestoreSize == nil {
			continue
		}
		id, ok := parseCSIID(*c.Status.SnapshotHandle)
		if !ok || id.clusterID != clusterID {
			continue
		}
		if pool, ok := byID[id.poolID]; ok {
			allocated[pool] += *c.Status.RestoreSize
		}
	}

	return allocated, nil
}

func roundDown(v int64) int64 {
	v = max(v, 0)
	return v - v%granularity
}

func capacityFrom(pool string, s *PoolStats, d *detail) *v1alpha1.CephStorageClassCapacity {
	if s == nil {
		return nil
	}

	used := roundDown(s.Stored)
	free := roundDown(s.MaxAvail)
	capacity := &v1alpha1.CephStorageClassCapacity{
		Pool:          pool,
		Size:          *resource.NewQuantity(used+free, resource.BinarySI),
		Used:          *resource.NewQuantity(used, resource.BinarySI),
		FreeOnDisk:    *resource.NewQuantity(free, resource.BinarySI),
		ImagesInTrash: int32(min(s.ImagesInTrash, math.MaxInt32)),
	}
	if s.Quota > 0 {
		capacity.Quota = resource.NewQuantity(s.Quota, resource.BinarySI)
		capacity.FreeByQuota = resource.NewQuantity(roundDown(min(s.Quota-max(s.Stored, 0), s.MaxAvail)), resource.BinarySI)
	}
	if d != nil {
		capacity.Allocated = resource.NewQuantity(max(d.allocated, 0), resource.BinarySI)
		if d.heldByDeleted != nil {
			capacity.HeldByDeleted = resource.NewQuantity(roundDown(*d.heldByDeleted), resource.BinarySI)
		}
	}

	return capacity
}

func needsWrite(prev, next *v1alpha1.CephStorageClassCapacity, now time.Time) bool {
	if next == nil {
		return prev != nil
	}
	if prev == nil || now.Sub(prev.UpdatedAt.Time) >= refreshPeriod {
		return true
	}
	if prev.Pool != next.Pool || prev.ImagesInTrash != next.ImagesInTrash || !sameQuantity(prev.Quota, next.Quota) {
		return true
	}
	if (prev.FreeByQuota == nil) != (next.FreeByQuota == nil) {
		return true
	}
	if next.FreeByQuota != nil && moved(*prev.FreeByQuota, *next.FreeByQuota, next.Size) {
		return true
	}
	if (prev.Allocated == nil) != (next.Allocated == nil) || (prev.HeldByDeleted == nil) != (next.HeldByDeleted == nil) {
		return true
	}
	if movedOptional(prev.Allocated, next.Allocated, next.Size) || movedOptional(prev.HeldByDeleted, next.HeldByDeleted, next.Size) {
		return true
	}

	return moved(prev.Size, next.Size, next.Size) ||
		moved(prev.Used, next.Used, next.Size) ||
		moved(prev.FreeOnDisk, next.FreeOnDisk, next.Size)
}

func sameQuantity(a, b *resource.Quantity) bool {
	if a == nil || b == nil {
		return a == b
	}

	return a.Cmp(*b) == 0
}

func movedOptional(prev, next *resource.Quantity, of resource.Quantity) bool {
	if prev == nil || next == nil {
		return false
	}

	return moved(*prev, *next, of)
}

func moved(prev, next, of resource.Quantity) bool {
	delta := math.Abs(float64(next.Value() - prev.Value()))
	if delta == 0 {
		return false
	}

	return delta > changeShare*float64(of.Value())
}

func publish(ctx context.Context, cl client.Client, class *v1alpha1.CephStorageClass, next *v1alpha1.CephStorageClassCapacity, readAt time.Time) error {
	if next != nil {
		next = next.DeepCopy()
		next.UpdatedAt = metav1.NewTime(readAt)
	}

	return conditions.UpdateStatus(ctx, cl, class, func(sc *v1alpha1.CephStorageClass) {
		if next == nil && sc.Status == nil {
			return
		}
		if sc.Status == nil {
			sc.Status = &v1alpha1.CephStorageClassStatus{}
		}
		sc.Status.Capacity = next
	})
}
