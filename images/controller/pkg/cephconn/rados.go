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

package cephconn

import (
	"errors"
	"fmt"
	"strings"
	"syscall"

	"github.com/ceph/go-ceph/rados"
)

const opTimeoutSeconds = "30"

type Conns = Pool[*rados.Conn]

func NewConns(msCrcData bool) *Conns {
	return &Conns{
		Dial:   func(creds Credentials) (*rados.Conn, error) { return dial(creds, msCrcData) },
		Close:  func(conn *rados.Conn) { conn.Shutdown() },
		Broken: broken,
	}
}

func dial(creds Credentials, msCrcData bool) (*rados.Conn, error) {
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
		{"ms_crc_data", fmt.Sprint(msCrcData)},
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

	return conn, nil
}

func broken(err error) bool {
	if errors.Is(err, rados.ErrNotConnected) {
		return true
	}
	var code interface{ ErrorCode() int }
	if !errors.As(err, &code) {
		return false
	}
	switch syscall.Errno(-code.ErrorCode()) {
	case syscall.ETIMEDOUT, syscall.ENOTCONN, syscall.ESHUTDOWN:
		return true
	}
	return false
}
