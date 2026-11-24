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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	DefaultMinLoginInterval = time.Minute
	DefaultIdleTimeout      = 15 * time.Minute
	DefaultReapInterval     = time.Minute
	ParallelConnections     = 4
)

var (
	ErrLoginLimited = errors.New("the last login to this Ceph cluster was less than the minimal login interval ago")
	errClosed       = errors.New("the Ceph connection is closed")
)

type Credentials struct {
	Name     string
	Monitors []string
	UserID   string
	UserKey  string
}

func (c Credentials) key() string {
	h := sha256.New()
	for _, part := range []string{strings.Join(c.Monitors, ","), c.UserID, c.UserKey} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

type Pool[C any] struct {
	Dial             func(Credentials) (C, error)
	Close            func(C)
	Broken           func(error) bool
	MinLoginInterval time.Duration
	IdleTimeout      time.Duration
	ReapInterval     time.Duration
	Now              func() time.Time

	mu      sync.Mutex
	entries map[string]*entry[C]
	logins  map[string]time.Time
}

type entry[C any] struct {
	key      string
	ready    chan struct{}
	conn     C
	err      error
	inUse    int
	lastUsed time.Time
	retired  bool
	closed   bool
}

func (p *Pool[C]) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Pool[C]) minLoginInterval() time.Duration {
	if p.MinLoginInterval > 0 {
		return p.MinLoginInterval
	}
	return DefaultMinLoginInterval
}

func (p *Pool[C]) idleTimeout() time.Duration {
	if p.IdleTimeout > 0 {
		return p.IdleTimeout
	}
	return DefaultIdleTimeout
}

func (p *Pool[C]) Do(ctx context.Context, creds Credentials, fn func(C) error) error {
	name := creds.Name
	e, err := p.acquire(name, creds)
	if err != nil {
		return err
	}

	select {
	case <-e.ready:
	case <-ctx.Done():
		p.release(name, e, nil)
		return ctx.Err()
	}
	if e.err != nil {
		p.release(name, e, nil)
		return e.err
	}

	done := make(chan error, 1)
	go func() {
		err := fn(e.conn)
		p.release(name, e, err)
		done <- err
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		p.retire(name, e)
		return ctx.Err()
	}
}

func (p *Pool[C]) acquire(name string, creds Credentials) (*entry[C], error) {
	key := creds.key()
	now := p.now()

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.entries == nil {
		p.entries = map[string]*entry[C]{}
		p.logins = map[string]time.Time{}
	}

	if e, ok := p.entries[name]; ok {
		if e.key == key && !e.retired {
			e.inUse++
			e.lastUsed = now
			return e, nil
		}
		p.retireLocked(name, e)
		p.closeIfDoneLocked(e)
	}

	if last, ok := p.logins[name]; ok && now.Sub(last) < p.minLoginInterval() {
		return nil, fmt.Errorf("%w: CephClusterConnection %s, next login after %s", ErrLoginLimited, name, last.Add(p.minLoginInterval()).Format(time.RFC3339))
	}
	p.logins[name] = now

	e := &entry[C]{key: key, ready: make(chan struct{}), inUse: 1, lastUsed: now}
	p.entries[name] = e
	go func() {
		conn, err := p.Dial(creds)
		p.mu.Lock()
		e.conn, e.err = conn, err
		if err != nil {
			p.retireLocked(name, e)
		}
		close(e.ready)
		p.closeIfDoneLocked(e)
		p.mu.Unlock()
	}()

	return e, nil
}

func (p *Pool[C]) release(name string, e *entry[C], err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	e.inUse--
	e.lastUsed = p.now()
	if err != nil && p.Broken != nil && p.Broken(err) {
		p.retireLocked(name, e)
	}
	p.closeIfDoneLocked(e)
}

func (p *Pool[C]) retire(name string, e *entry[C]) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.retireLocked(name, e)
	p.closeIfDoneLocked(e)
}

func (p *Pool[C]) retireLocked(name string, e *entry[C]) {
	e.retired = true
	if p.entries[name] == e {
		delete(p.entries, name)
	}
}

func (p *Pool[C]) closeIfDoneLocked(e *entry[C]) {
	if !e.retired || e.inUse > 0 || e.closed {
		return
	}
	select {
	case <-e.ready:
	default:
		return
	}
	e.closed = true
	if e.err != nil {
		return
	}
	e.err = errClosed
	if p.Close != nil {
		p.Close(e.conn)
	}
}

func (p *Pool[C]) Reap() {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	for name, e := range p.entries {
		if e.inUse == 0 && now.Sub(e.lastUsed) >= p.idleTimeout() {
			p.retireLocked(name, e)
			p.closeIfDoneLocked(e)
		}
	}
}

func (p *Pool[C]) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for name, e := range p.entries {
		p.retireLocked(name, e)
		p.closeIfDoneLocked(e)
	}
}

func (p *Pool[C]) Start(ctx context.Context) error {
	interval := p.ReapInterval
	if interval <= 0 {
		interval = DefaultReapInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			p.CloseAll()
			return nil
		case <-ticker.C:
			p.Reap()
		}
	}
}

func (p *Pool[C]) NeedLeaderElection() bool {
	return false
}

func ForEach(ctx context.Context, names []string, limit int, timeout time.Duration, fn func(context.Context, string)) {
	var g errgroup.Group
	g.SetLimit(max(limit, 1))
	for _, name := range names {
		if ctx.Err() != nil {
			break
		}
		g.Go(func() error {
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			fn(ctx, name)
			return nil
		})
	}
	_ = g.Wait()
}
