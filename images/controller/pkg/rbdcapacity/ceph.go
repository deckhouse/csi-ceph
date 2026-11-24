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
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/ceph/go-ceph/rados"
	"github.com/ceph/go-ceph/rbd"

	"github.com/deckhouse/csi-ceph/images/controller/pkg/cephconn"
)

type CephPoolReader struct {
	Conns *cephconn.Conns
}

func (r CephPoolReader) ReadPools(ctx context.Context, creds cephconn.Credentials, pools []string) (map[string]*PoolStats, error) {
	var (
		mu    sync.Mutex
		stats map[string]*PoolStats
	)
	err := r.Conns.Do(ctx, creds, func(conn *rados.Conn) error {
		out, errs := readPools(ctx, conn, pools)
		mu.Lock()
		stats = out
		mu.Unlock()
		return errs
	})

	mu.Lock()
	defer mu.Unlock()
	return stats, err
}

func readPools(ctx context.Context, conn *rados.Conn, pools []string) (map[string]*PoolStats, error) {
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
		n, err := countTrash(conn, pool)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", pool, err))
			delete(stats, pool)
			continue
		}
		stats[pool].ImagesInTrash = n
	}

	return stats, errors.Join(errs...)
}

func countTrash(conn *rados.Conn, pool string) (int, error) {
	ioctx, err := conn.OpenIOContext(pool)
	if err != nil {
		return 0, fmt.Errorf("unable to open the pool: %w", err)
	}
	defer ioctx.Destroy()

	trash, err := rbd.GetTrashList(ioctx)
	if err != nil {
		return 0, fmt.Errorf("unable to list the RBD trash: %w", err)
	}

	return len(trash), nil
}
