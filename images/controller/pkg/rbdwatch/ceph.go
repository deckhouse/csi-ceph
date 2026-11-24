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

package rbdwatch

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/ceph/go-ceph/rados"
	"github.com/ceph/go-ceph/rbd"

	"github.com/deckhouse/csi-ceph/images/controller/pkg/cephconn"
)

type CephCounter struct {
	Conns *cephconn.Conns
}

func (c CephCounter) CountWatchers(ctx context.Context, creds Credentials, images []Image) (map[Image]int, error) {
	var (
		mu     sync.Mutex
		counts map[Image]int
	)
	err := c.Conns.Do(ctx, creds, func(conn *rados.Conn) error {
		out := make(map[Image]int, len(images))
		var errs []error
		for _, image := range images {
			if ctx.Err() != nil {
				errs = append(errs, ctx.Err())
				break
			}
			n, err := countImageWatchers(conn, image)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", image, err))
				continue
			}
			out[image] = n
		}
		mu.Lock()
		counts = out
		mu.Unlock()
		return errors.Join(errs...)
	})

	mu.Lock()
	defer mu.Unlock()
	return counts, err
}

func countImageWatchers(conn *rados.Conn, image Image) (int, error) {
	ioctx, err := conn.OpenIOContext(image.Pool)
	if err != nil {
		return 0, fmt.Errorf("unable to open the pool: %w", err)
	}
	defer ioctx.Destroy()
	ioctx.SetNamespace(image.RadosNamespace)

	img, err := rbd.OpenImageReadOnly(ioctx, image.Name, rbd.NoSnapshot)
	if err != nil {
		return 0, fmt.Errorf("unable to open the image: %w", err)
	}
	defer func() { _ = img.Close() }()

	watchers, err := img.ListWatchers()
	if err != nil {
		return 0, fmt.Errorf("unable to list the watchers: %w", err)
	}

	return len(watchers), nil
}
