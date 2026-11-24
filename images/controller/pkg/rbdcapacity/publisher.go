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

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/cephconn"
	"github.com/deckhouse/sds-common-lib/conditions"
	"github.com/deckhouse/sds-common-lib/logger"
)

const (
	PublisherName   = "rbd-capacity-publisher"
	DefaultInterval = time.Minute

	refreshPeriod     = 5 * time.Minute
	changeShare       = 0.01
	connectionTimeout = 2 * time.Minute
	granularity       = int64(1) << 20
)

type PoolStats struct {
	Stored        int64
	MaxAvail      int64
	Quota         int64
	ImagesInTrash int
}

type PoolReader interface {
	ReadPools(ctx context.Context, creds cephconn.Credentials, pools []string) (map[string]*PoolStats, error)
}

type Publisher struct {
	Client   client.Client
	Reader   PoolReader
	Log      logger.Logger
	Interval time.Duration
	Now      func() time.Time

	mu    sync.Mutex
	reads map[string]bool
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

	stats, err := p.Reader.ReadPools(ctx, cephconn.Credentials{
		Name:     name,
		Monitors: connection.Spec.Monitors,
		UserID:   connection.Spec.UserID,
		UserKey:  connection.Spec.UserKey,
	}, pools)
	readAt := p.now()
	p.recordRead(name, err, pools)

	for _, class := range classes {
		pool := class.Spec.RBD.Pool
		s, ok := stats[pool]
		if !ok {
			continue
		}
		next := capacityFrom(pool, s)
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

func roundDown(v int64) int64 {
	v = max(v, 0)
	return v - v%granularity
}

func capacityFrom(pool string, s *PoolStats) *v1alpha1.CephStorageClassCapacity {
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
