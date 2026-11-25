//go:build cephclient

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
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"

	"github.com/ceph/go-ceph/rados"
	"github.com/ceph/go-ceph/rbd"

	"github.com/deckhouse/csi-ceph/images/controller/pkg/cephconn"
)

type CephPoolReader struct {
	Conns *cephconn.Conns
}

func (r CephPoolReader) ReadPools(ctx context.Context, creds cephconn.Credentials, pools []string, measureTrash bool) (map[string]*PoolStats, error) {
	var (
		mu    sync.Mutex
		stats map[string]*PoolStats
	)
	err := r.Conns.Do(ctx, creds, func(conn *rados.Conn) error {
		out, errs := readPools(ctx, conn, pools, measureTrash)
		mu.Lock()
		stats = out
		mu.Unlock()
		return errs
	})

	mu.Lock()
	defer mu.Unlock()
	return stats, err
}

func readPools(ctx context.Context, conn *rados.Conn, pools []string, measureTrash bool) (map[string]*PoolStats, error) {
	out, info, err := conn.MonCommand([]byte(dfCommand))
	if err != nil {
		return nil, fmt.Errorf("unable to run ceph df: %w: %s", err, info)
	}
	stats, err := parseDF(out, pools)
	if err != nil {
		return nil, err
	}

	var errs []error
	for _, pool := range pools {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			delete(stats, pool)
			continue
		}
		if stats[pool] == nil {
			_, err := conn.GetPoolByName(pool)
			switch {
			case errors.Is(err, rados.ErrNotFound):
			case err != nil:
				errs = append(errs, fmt.Errorf("%s: unable to look the pool up: %w", pool, err))
				delete(stats, pool)
			default:
				errs = append(errs, fmt.Errorf("%s: the pool exists, but ceph df does not report it yet", pool))
				delete(stats, pool)
			}
			continue
		}
		n, held, err := readTrash(ctx, conn, pool, measureTrash)
		if n < 0 {
			errs = append(errs, fmt.Errorf("%s: %w", pool, err))
			delete(stats, pool)
			continue
		}
		stats[pool].ImagesInTrash = n
		stats[pool].HeldByDeleted = held
		stats[pool].MeasureErr = err
	}

	return stats, errors.Join(errs...)
}

func readTrash(ctx context.Context, conn *rados.Conn, pool string, measure bool) (int, *int64, error) {
	ioctx, err := conn.OpenIOContext(pool)
	if err != nil {
		return -1, nil, fmt.Errorf("unable to open the pool: %w", err)
	}
	defer ioctx.Destroy()

	trash, err := rbd.GetTrashList(ioctx)
	if err != nil {
		return -1, nil, fmt.Errorf("unable to list the RBD trash: %w", err)
	}
	if !measure {
		return len(trash), nil, nil
	}

	var held int64
	for _, t := range trash {
		if ctx.Err() != nil {
			return len(trash), nil, ctx.Err()
		}
		n, err := trashImageUsed(ioctx, t.Id)
		if err != nil {
			return len(trash), nil, fmt.Errorf("unable to measure the image %s (%s) in the RBD trash: %w", t.Name, t.Id, err)
		}
		held += n
	}

	return len(trash), &held, nil
}

func trashImageUsed(ioctx *rados.IOContext, id string) (int64, error) {
	img, err := rbd.OpenImageByIdReadOnly(ioctx, id, rbd.NoSnapshot)
	if err != nil {
		return 0, fmt.Errorf("unable to open the image: %w", err)
	}
	defer func() { _ = img.Close() }()

	features, err := img.GetFeatures()
	if err != nil {
		return 0, fmt.Errorf("unable to get the features: %w", err)
	}
	if features&rbd.FeatureFastDiff == 0 {
		return 0, errors.New("the image has no fast-diff feature, and measuring it would read every object")
	}

	size, err := img.GetSize()
	if err != nil {
		return 0, fmt.Errorf("unable to get the size: %w", err)
	}
	snaps, err := img.GetSnapshotNames()
	if err != nil {
		return 0, fmt.Errorf("unable to list the snapshots: %w", err)
	}
	slices.SortFunc(snaps, func(a, b rbd.SnapInfo) int { return cmp.Compare(a.Id, b.Id) })

	var last uint64
	if len(snaps) > 0 {
		last = snaps[len(snaps)-1].Id
	}
	total, err := usedSince(img, last, size)
	if err != nil {
		return 0, fmt.Errorf("unable to diff the image: %w", err)
	}

	var prev uint64
	for _, s := range snaps {
		if err := img.SetSnapByID(s.Id); err != nil {
			return 0, fmt.Errorf("unable to open the snapshot %d: %w", s.Id, err)
		}
		n, err := usedSince(img, prev, s.Size)
		if err != nil {
			return 0, fmt.Errorf("unable to diff the snapshot %d: %w", s.Id, err)
		}
		total += n
		prev = s.Id
	}

	return int64(min(total, uint64(math.MaxInt64))), nil
}

func usedSince(img *rbd.Image, fromSnapID, size uint64) (uint64, error) {
	var used uint64
	err := img.DiffIterateByID(rbd.DiffIterateByIDConfig{
		FromSnapID:    fromSnapID,
		Offset:        0,
		Length:        size,
		IncludeParent: rbd.ExcludeParent,
		WholeObject:   rbd.EnableWholeObject,
		Callback: func(_, length uint64, exists int, _ interface{}) int {
			if exists != 0 {
				used += length
			}
			return 0
		},
	})

	return used, err
}
