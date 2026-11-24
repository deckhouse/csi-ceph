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
	"strings"

	"github.com/ceph/go-ceph/rados"
	"github.com/ceph/go-ceph/rbd"
)

const opTimeoutSeconds = "30"

type CephCounter struct {
	MsCrcData bool
}

func (c CephCounter) CountWatchers(ctx context.Context, creds Credentials, images []Image) (map[Image]int, error) {
	conn, err := rados.NewConnWithUser(creds.UserID)
	if err != nil {
		return nil, fmt.Errorf("unable to create a Ceph connection: %w", err)
	}

	options := [][2]string{
		{"mon_host", strings.Join(creds.Monitors, ",")},
		{"key", creds.UserKey},
		{"client_mount_timeout", opTimeoutSeconds},
		{"rados_mon_op_timeout", opTimeoutSeconds},
		{"rados_osd_op_timeout", opTimeoutSeconds},
		{"rbd_cache", "false"},
		{"ms_crc_data", fmt.Sprint(c.MsCrcData)},
		{"admin_socket", ""},
		{"log_file", ""},
		{"log_to_stderr", "false"},
		{"err_to_stderr", "false"},
	}
	for _, o := range options {
		if err := conn.SetConfigOption(o[0], o[1]); err != nil {
			return nil, fmt.Errorf("unable to set the Ceph option %s: %w", o[0], err)
		}
	}

	if err := conn.Connect(); err != nil {
		return nil, fmt.Errorf("unable to connect to the Ceph monitors %s as client.%s: %w", strings.Join(creds.Monitors, ","), creds.UserID, err)
	}
	defer conn.Shutdown()

	counts := make(map[Image]int, len(images))
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
		counts[image] = n
	}

	return counts, errors.Join(errs...)
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
