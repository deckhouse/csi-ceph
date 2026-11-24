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
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeConn struct {
	id     int
	closed bool
}

type dialer struct {
	mu     sync.Mutex
	dials  []Credentials
	conns  []*fakeConn
	err    error
	closed int
}

func (d *dialer) pool(now *time.Time) *Pool[*fakeConn] {
	return &Pool[*fakeConn]{
		Dial: func(creds Credentials) (*fakeConn, error) {
			d.mu.Lock()
			defer d.mu.Unlock()
			d.dials = append(d.dials, creds)
			if d.err != nil {
				return nil, d.err
			}
			c := &fakeConn{id: len(d.dials)}
			d.conns = append(d.conns, c)
			return c, nil
		},
		Close: func(c *fakeConn) {
			d.mu.Lock()
			defer d.mu.Unlock()
			c.closed = true
			d.closed++
		},
		Broken: func(err error) bool { return errors.Is(err, errBroken) },
		Now:    func() time.Time { return *now },
	}
}

func (d *dialer) dialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.dials)
}

var errBroken = errors.New("broken")

func creds(name, key string) Credentials {
	return Credentials{Name: name, Monitors: []string{"10.0.0.1:6789"}, UserID: "admin", UserKey: key}
}

func use(t *testing.T, p *Pool[*fakeConn], c Credentials) *fakeConn {
	t.Helper()
	var got *fakeConn
	if err := p.Do(context.Background(), c, func(conn *fakeConn) error { got = conn; return nil }); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestConnectionIsReusedWithoutANewLogin(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	d := &dialer{}
	p := d.pool(&now)

	first := use(t, p, creds("ceph", "k"))
	for range 10 {
		now = now.Add(time.Minute)
		if got := use(t, p, creds("ceph", "k")); got != first {
			t.Fatalf("got connection %d; want the first one", got.id)
		}
	}
	if n := d.dialCount(); n != 1 {
		t.Fatalf("%d logins; want 1", n)
	}
}

func TestConcurrentUsersShareOneLogin(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	d := &dialer{}
	p := d.pool(&now)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { use(t, p, creds("ceph", "k")) })
	}
	wg.Wait()
	if n := d.dialCount(); n != 1 {
		t.Fatalf("%d logins; want 1", n)
	}
}

func TestChangedCredentialsReconnectAndCloseTheOldConnection(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	d := &dialer{}
	p := d.pool(&now)

	old := use(t, p, creds("ceph", "old"))
	now = now.Add(time.Minute)
	next := use(t, p, creds("ceph", "new"))
	if next == old || !old.closed || next.closed {
		t.Fatalf("old %+v, next %+v; want a new connection and the old one closed", old, next)
	}
}

func TestLoginsAreLimitedToOneAMinute(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	d := &dialer{err: errors.New("connection refused")}
	p := d.pool(&now)

	if err := p.Do(context.Background(), creds("ceph", "k"), func(*fakeConn) error { return nil }); err == nil {
		t.Fatal("a failed login returned no error")
	}
	now = now.Add(30 * time.Second)
	if err := p.Do(context.Background(), creds("ceph", "k"), func(*fakeConn) error { return nil }); !errors.Is(err, ErrLoginLimited) {
		t.Fatalf("second login within a minute: %v; want ErrLoginLimited", err)
	}
	now = now.Add(30 * time.Second)
	d.mu.Lock()
	d.err = nil
	d.mu.Unlock()
	use(t, p, creds("ceph", "k"))
	if n := d.dialCount(); n != 2 {
		t.Fatalf("%d logins; want 2", n)
	}
}

func TestBrokenConnectionIsReplaced(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	d := &dialer{}
	p := d.pool(&now)

	var first *fakeConn
	err := p.Do(context.Background(), creds("ceph", "k"), func(c *fakeConn) error { first = c; return errBroken })
	if !errors.Is(err, errBroken) || !first.closed {
		t.Fatalf("err = %v, closed = %v; want errBroken and the connection closed", err, first.closed)
	}
	now = now.Add(time.Minute)
	if got := use(t, p, creds("ceph", "k")); got == first {
		t.Fatal("the broken connection was reused")
	}
}

func TestCancelledCallReturnsAtOnceAndClosesTheConnectionAfterIt(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	d := &dialer{}
	p := d.pool(&now)

	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	started := make(chan *fakeConn)
	done := make(chan error)
	go func() {
		done <- p.Do(ctx, creds("ceph", "k"), func(c *fakeConn) error {
			started <- c
			<-release
			return nil
		})
	}()
	conn := <-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Do returned %v; want context.Canceled", err)
	}
	d.mu.Lock()
	closed := conn.closed
	d.mu.Unlock()
	if closed {
		t.Fatal("the connection was closed while the call still used it")
	}
	close(release)
	deadline := time.After(5 * time.Second)
	for {
		d.mu.Lock()
		closed = conn.closed
		d.mu.Unlock()
		if closed {
			return
		}
		select {
		case <-deadline:
			t.Fatal("the connection of the cancelled call was not closed after the call returned")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestIdleConnectionIsClosed(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	d := &dialer{}
	p := d.pool(&now)

	conn := use(t, p, creds("ceph", "k"))
	now = now.Add(DefaultIdleTimeout - time.Second)
	p.Reap()
	if conn.closed {
		t.Fatal("closed before the idle timeout")
	}
	now = now.Add(time.Second)
	p.Reap()
	if !conn.closed {
		t.Fatal("not closed after the idle timeout")
	}
}

func TestCloseAllClosesEveryConnection(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	d := &dialer{}
	p := d.pool(&now)

	a := use(t, p, creds("a", "k"))
	b := use(t, p, creds("b", "k"))
	p.CloseAll()
	if !a.closed || !b.closed {
		t.Fatalf("a closed %v, b closed %v; want both", a.closed, b.closed)
	}
}

func TestForEachRunsAtMostLimitAtOnceWithADeadline(t *testing.T) {
	var (
		mu      sync.Mutex
		running int
		peak    int
		seen    []string
	)
	ForEach(context.Background(), []string{"a", "b", "c", "d", "e", "f"}, 2, time.Minute, func(ctx context.Context, name string) {
		if _, ok := ctx.Deadline(); !ok {
			t.Errorf("%s: no deadline", name)
		}
		mu.Lock()
		running++
		peak = max(peak, running)
		seen = append(seen, name)
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
	})
	if peak > 2 || len(seen) != 6 {
		t.Fatalf("peak %d, seen %v; want at most 2 at once and all 6", peak, seen)
	}
}
