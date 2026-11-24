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
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/cephconn"
	"github.com/deckhouse/sds-common-lib/logger"
)

const gi = int64(1) << 30

type fakeReader struct {
	mu    sync.Mutex
	stats map[string]map[string]*PoolStats
	err   error
	calls []cephconn.Credentials
	pools [][]string
}

func (f *fakeReader) ReadPools(_ context.Context, creds cephconn.Credentials, pools []string) (map[string]*PoolStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, creds)
	f.pools = append(f.pools, pools)
	return f.stats[creds.UserID], f.err
}

func connection(name string) *v1alpha1.CephClusterConnection {
	return &v1alpha1.CephClusterConnection{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.CephClusterConnectionSpec{
			ClusterID: "fsid-" + name,
			Monitors:  []string{"10.0.0.1:6789"},
			UserID:    "user-" + name,
			UserKey:   "key-" + name,
		},
	}
}

func rbdClass(name, conn, pool string) *v1alpha1.CephStorageClass {
	return &v1alpha1.CephStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.CephStorageClassSpec{
			ClusterConnectionName: conn,
			ReclaimPolicy:         "Delete",
			Type:                  v1alpha1.CephStorageClassTypeRBD,
			RBD:                   &v1alpha1.CephStorageClassRBD{Pool: pool},
		},
	}
}

func cephFSClass(name, conn string) *v1alpha1.CephStorageClass {
	return &v1alpha1.CephStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.CephStorageClassSpec{
			ClusterConnectionName: conn,
			ReclaimPolicy:         "Delete",
			Type:                  v1alpha1.CephStorageClassTypeCephFS,
			CephFS:                &v1alpha1.CephStorageClassCephFS{FSName: "fs"},
		},
	}
}

func withCapacity(class *v1alpha1.CephStorageClass, c *v1alpha1.CephStorageClassCapacity) *v1alpha1.CephStorageClass {
	class.Status = &v1alpha1.CephStorageClassStatus{Capacity: c}
	return class
}

func capacity(pool string, used, free int64, at time.Time) *v1alpha1.CephStorageClassCapacity {
	return &v1alpha1.CephStorageClassCapacity{
		Pool:       pool,
		Size:       *resource.NewQuantity(used+free, resource.BinarySI),
		Used:       *resource.NewQuantity(used, resource.BinarySI),
		FreeOnDisk: *resource.NewQuantity(free, resource.BinarySI),
		UpdatedAt:  metav1.NewTime(at),
	}
}

func newPublisher(t *testing.T, reader PoolReader, objs ...client.Object) *Publisher {
	t.Helper()
	scheme := apiruntime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return &Publisher{
		Client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(objs...).
			WithStatusSubresource(&v1alpha1.CephStorageClass{}).
			Build(),
		Reader: reader,
		Log:    logger.NewNop(),
	}
}

func syncAt(t *testing.T, p *Publisher, now time.Time) {
	t.Helper()
	p.Now = func() time.Time { return now }
	if err := p.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func capacityOf(t *testing.T, p *Publisher, name string) *v1alpha1.CephStorageClassCapacity {
	t.Helper()
	class := &v1alpha1.CephStorageClass{}
	if err := p.Client.Get(context.Background(), client.ObjectKey{Name: name}, class); err != nil {
		t.Fatal(err)
	}
	if class.Status == nil {
		return nil
	}
	return class.Status.Capacity
}

func sameCapacity(t *testing.T, want, got *v1alpha1.CephStorageClassCapacity) {
	t.Helper()
	if want == nil || got == nil {
		if want != got {
			t.Fatalf("capacity = %+v; want %+v", got, want)
		}
		return
	}
	type flat struct {
		Pool                          string
		Size, Used, FreeOnDisk, Quota int64
		ImagesInTrash                 int32
		UpdatedAt                     int64
	}
	f := func(c *v1alpha1.CephStorageClassCapacity) flat {
		out := flat{Pool: c.Pool, Size: c.Size.Value(), Used: c.Used.Value(), FreeOnDisk: c.FreeOnDisk.Value(), ImagesInTrash: c.ImagesInTrash, UpdatedAt: c.UpdatedAt.Unix()}
		if c.Quota != nil {
			out.Quota = c.Quota.Value()
		}
		return out
	}
	if diff := cmp.Diff(f(want), f(got)); diff != "" {
		t.Fatalf("capacity mismatch (-want +got):\n%s", diff)
	}
}

func TestRBDClassGetsTheSpaceOfItsPool(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	reader := &fakeReader{stats: map[string]map[string]*PoolStats{
		"user-ceph": {
			"rbd":  {Stored: 6 * gi, MaxAvail: 90 * gi, ImagesInTrash: 2},
			"fast": {Stored: gi, MaxAvail: 9 * gi, Quota: 50 * gi},
		},
	}}
	p := newPublisher(t, reader,
		connection("ceph"),
		rbdClass("rbd-a", "ceph", "rbd"),
		rbdClass("rbd-b", "ceph", "rbd"),
		rbdClass("fast", "ceph", "fast"),
		cephFSClass("fs", "ceph"),
	)

	syncAt(t, p, now)

	rbd := capacity("rbd", 6*gi, 90*gi, now)
	rbd.ImagesInTrash = 2
	sameCapacity(t, rbd, capacityOf(t, p, "rbd-a"))
	sameCapacity(t, rbd, capacityOf(t, p, "rbd-b"))

	fast := capacity("fast", gi, 9*gi, now)
	fast.Quota = resource.NewQuantity(50*gi, resource.BinarySI)
	sameCapacity(t, fast, capacityOf(t, p, "fast"))

	sameCapacity(t, nil, capacityOf(t, p, "fs"))

	if diff := cmp.Diff([][]string{{"fast", "rbd"}}, reader.pools); diff != "" {
		t.Errorf("pools read (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]cephconn.Credentials{{Name: "ceph", Monitors: []string{"10.0.0.1:6789"}, UserID: "user-ceph", UserKey: "key-ceph"}}, reader.calls); diff != "" {
		t.Errorf("credentials (-want +got):\n%s", diff)
	}
}

func TestEveryConnectionIsReadWithItsOwnCredentials(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	reader := &fakeReader{stats: map[string]map[string]*PoolStats{
		"user-a": {"rbd": {Stored: gi, MaxAvail: 9 * gi}},
		"user-b": {"rbd": {Stored: 2 * gi, MaxAvail: 8 * gi}},
	}}
	p := newPublisher(t, reader, connection("a"), connection("b"), rbdClass("on-a", "a", "rbd"), rbdClass("on-b", "b", "rbd"))

	syncAt(t, p, now)

	sameCapacity(t, capacity("rbd", gi, 9*gi, now), capacityOf(t, p, "on-a"))
	sameCapacity(t, capacity("rbd", 2*gi, 8*gi, now), capacityOf(t, p, "on-b"))
}

func TestSmallMoveIsNotWrittenBeforeTheRefreshPeriod(t *testing.T) {
	then := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	prev := capacity("rbd", 10*gi, 90*gi, then)
	reader := &fakeReader{stats: map[string]map[string]*PoolStats{
		"user-ceph": {"rbd": {Stored: 10*gi + gi/2, MaxAvail: 90*gi - gi/2}},
	}}
	p := newPublisher(t, reader, connection("ceph"), withCapacity(rbdClass("rbd", "ceph", "rbd"), prev))

	syncAt(t, p, then.Add(time.Minute))
	sameCapacity(t, prev, capacityOf(t, p, "rbd"))

	later := then.Add(refreshPeriod)
	syncAt(t, p, later)
	sameCapacity(t, capacity("rbd", 10*gi+gi/2, 90*gi-gi/2, later), capacityOf(t, p, "rbd"))
}

func TestMoveOfMoreThanOnePercentIsWrittenAtOnce(t *testing.T) {
	then := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	reader := &fakeReader{stats: map[string]map[string]*PoolStats{
		"user-ceph": {"rbd": {Stored: 12 * gi, MaxAvail: 88 * gi}},
	}}
	p := newPublisher(t, reader, connection("ceph"), withCapacity(rbdClass("rbd", "ceph", "rbd"), capacity("rbd", 10*gi, 90*gi, then)))

	now := then.Add(time.Minute)
	syncAt(t, p, now)
	sameCapacity(t, capacity("rbd", 12*gi, 88*gi, now), capacityOf(t, p, "rbd"))
}

func TestChangeOfTheTrashIsWrittenAtOnce(t *testing.T) {
	then := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	reader := &fakeReader{stats: map[string]map[string]*PoolStats{
		"user-ceph": {"rbd": {Stored: 10 * gi, MaxAvail: 90 * gi, ImagesInTrash: 1}},
	}}
	p := newPublisher(t, reader, connection("ceph"), withCapacity(rbdClass("rbd", "ceph", "rbd"), capacity("rbd", 10*gi, 90*gi, then)))

	now := then.Add(time.Minute)
	syncAt(t, p, now)
	want := capacity("rbd", 10*gi, 90*gi, now)
	want.ImagesInTrash = 1
	sameCapacity(t, want, capacityOf(t, p, "rbd"))
}

func TestFailedReadKeepsThePublishedCapacity(t *testing.T) {
	then := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	prev := capacity("rbd", 10*gi, 90*gi, then)
	reader := &fakeReader{err: errors.New("connection refused")}
	p := newPublisher(t, reader, connection("ceph"), withCapacity(rbdClass("rbd", "ceph", "rbd"), prev))

	syncAt(t, p, then.Add(time.Hour))
	sameCapacity(t, prev, capacityOf(t, p, "rbd"))
}

func TestPoolThatIsGoneClearsTheCapacity(t *testing.T) {
	then := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	reader := &fakeReader{stats: map[string]map[string]*PoolStats{
		"user-ceph": {"rbd": nil},
	}}
	p := newPublisher(t, reader, connection("ceph"), withCapacity(rbdClass("rbd", "ceph", "rbd"), capacity("rbd", 10*gi, 90*gi, then)))

	syncAt(t, p, then.Add(time.Minute))
	sameCapacity(t, nil, capacityOf(t, p, "rbd"))
}

func TestClassWithoutItsConnectionIsSkipped(t *testing.T) {
	reader := &fakeReader{}
	p := newPublisher(t, reader, rbdClass("rbd", "gone", "rbd"))

	syncAt(t, p, time.Now())
	if len(reader.calls) != 0 {
		t.Errorf("ReadPools called %d times; want 0", len(reader.calls))
	}
	sameCapacity(t, nil, capacityOf(t, p, "rbd"))
}

func TestClassBeingDeletedIsSkipped(t *testing.T) {
	class := rbdClass("rbd", "ceph", "rbd")
	class.Finalizers = []string{"storage.deckhouse.io/csi-ceph"}
	class.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	reader := &fakeReader{}
	p := newPublisher(t, reader, connection("ceph"), class)

	syncAt(t, p, time.Now())
	if len(reader.calls) != 0 {
		t.Errorf("ReadPools called %d times; want 0", len(reader.calls))
	}
}

func TestNegativeFiguresAreClampedToZero(t *testing.T) {
	got := capacityFrom("rbd", &PoolStats{Stored: -1, MaxAvail: -5})
	sameCapacity(t, capacity("rbd", 0, 0, time.Time{}), got)
}

func TestPublishingKeepsTheVerdictOfTheReconciler(t *testing.T) {
	then := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	class := rbdClass("rbd", "ceph", "rbd")
	class.Status = &v1alpha1.CephStorageClassStatus{
		Phase:              "Created",
		Reason:             "Successfully reconciled",
		ObservedGeneration: 3,
		Conditions: []metav1.Condition{{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "Reconciled",
			ObservedGeneration: 3,
			LastTransitionTime: metav1.NewTime(then),
		}},
	}
	want := class.Status.DeepCopy()
	reader := &fakeReader{stats: map[string]map[string]*PoolStats{
		"user-ceph": {"rbd": {Stored: gi, MaxAvail: 9 * gi}},
	}}
	p := newPublisher(t, reader, connection("ceph"), class)

	syncAt(t, p, then.Add(time.Minute))

	got := &v1alpha1.CephStorageClass{}
	if err := p.Client.Get(context.Background(), client.ObjectKey{Name: "rbd"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Capacity == nil {
		t.Fatal("capacity was not published")
	}
	got.Status.Capacity = nil
	if diff := cmp.Diff(want, got.Status); diff != "" {
		t.Fatalf("the status besides capacity changed (-want +got):\n%s", diff)
	}
}

func TestPartialReadPublishesThePoolsThatWereRead(t *testing.T) {
	then := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	prevFast := capacity("fast", gi, 9*gi, then)
	reader := &fakeReader{
		stats: map[string]map[string]*PoolStats{"user-ceph": {"rbd": {Stored: 2 * gi, MaxAvail: 8 * gi}}},
		err:   errors.New("fast: unable to list the RBD trash: permission denied"),
	}
	p := newPublisher(t, reader, connection("ceph"), rbdClass("rbd", "ceph", "rbd"), withCapacity(rbdClass("fast", "ceph", "fast"), prevFast))

	now := then.Add(time.Hour)
	syncAt(t, p, now)

	sameCapacity(t, capacity("rbd", 2*gi, 8*gi, now), capacityOf(t, p, "rbd"))
	sameCapacity(t, prevFast, capacityOf(t, p, "fast"))
	if diff := cmp.Diff(map[string]bool{"ceph": false}, p.Reads()); diff != "" {
		t.Errorf("reads (-want +got):\n%s", diff)
	}
}

func TestChangeOfTheQuotaIsWrittenAtOnce(t *testing.T) {
	then := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	withQuota := func(quota int64, at time.Time) *v1alpha1.CephStorageClassCapacity {
		c := capacity("rbd", 10*gi, 90*gi, at)
		c.Quota = resource.NewQuantity(quota, resource.BinarySI)
		c.FreeByQuota = resource.NewQuantity(min(quota-10*gi, 90*gi), resource.BinarySI)
		return c
	}

	for name, tc := range map[string]struct {
		prev  *v1alpha1.CephStorageClassCapacity
		quota int64
		want  func(time.Time) *v1alpha1.CephStorageClassCapacity
	}{
		"set": {
			prev:  capacity("rbd", 10*gi, 90*gi, then),
			quota: 50 * gi,
			want:  func(at time.Time) *v1alpha1.CephStorageClassCapacity { return withQuota(50*gi, at) },
		},
		"removed": {
			prev: withQuota(50*gi, then),
			want: func(at time.Time) *v1alpha1.CephStorageClassCapacity { return capacity("rbd", 10*gi, 90*gi, at) },
		},
		"changed": {
			prev:  withQuota(50*gi, then),
			quota: 60 * gi,
			want:  func(at time.Time) *v1alpha1.CephStorageClassCapacity { return withQuota(60*gi, at) },
		},
	} {
		t.Run(name, func(t *testing.T) {
			reader := &fakeReader{stats: map[string]map[string]*PoolStats{
				"user-ceph": {"rbd": {Stored: 10 * gi, MaxAvail: 90 * gi, Quota: tc.quota}},
			}}
			p := newPublisher(t, reader, connection("ceph"), withCapacity(rbdClass("rbd", "ceph", "rbd"), tc.prev))

			now := then.Add(time.Minute)
			syncAt(t, p, now)
			sameCapacity(t, tc.want(now), capacityOf(t, p, "rbd"))
		})
	}
}

func TestChangeOfThePoolIsWrittenAtOnce(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	prev := capacity("old", 10*gi, 90*gi, now)
	next := capacity("rbd", 10*gi, 90*gi, now)
	if !needsWrite(prev, next, now.Add(time.Second)) {
		t.Fatal("a capacity of another pool is not written")
	}
}

func TestFreeByQuotaIsTheSmallerOfTheQuotaLeftAndFreeOnDisk(t *testing.T) {
	for name, tc := range map[string]struct {
		stats PoolStats
		want  int64
	}{
		"quota is the limit":  {PoolStats{Stored: 10 * gi, MaxAvail: 90 * gi, Quota: 30 * gi}, 20 * gi},
		"disk is the limit":   {PoolStats{Stored: 10 * gi, MaxAvail: 5 * gi, Quota: 30 * gi}, 5 * gi},
		"quota is overrun":    {PoolStats{Stored: 40 * gi, MaxAvail: 50 * gi, Quota: 30 * gi}, 0},
		"rounded down to MiB": {PoolStats{Stored: gi + 1, MaxAvail: 90 * gi, Quota: 2 * gi}, gi - 1<<20},
	} {
		t.Run(name, func(t *testing.T) {
			got := capacityFrom("rbd", &tc.stats)
			if got.FreeByQuota == nil || got.FreeByQuota.Value() != tc.want {
				t.Fatalf("freeByQuota = %v; want %d", got.FreeByQuota, tc.want)
			}
		})
	}
	if got := capacityFrom("rbd", &PoolStats{Stored: gi, MaxAvail: gi}); got.FreeByQuota != nil || got.Quota != nil {
		t.Fatalf("a pool without a quota got quota %v and freeByQuota %v", got.Quota, got.FreeByQuota)
	}
}

func TestFiguresAreRoundedDownToMiB(t *testing.T) {
	got := capacityFrom("rbd", &PoolStats{Stored: 3<<20 + 5, MaxAvail: 1073741836345})
	if got.Used.Value() != 3<<20 || got.FreeOnDisk.Value() != 1073741836345-1073741836345%(1<<20) {
		t.Fatalf("used = %d, freeOnDisk = %d", got.Used.Value(), got.FreeOnDisk.Value())
	}
	if got.Size.Value() != got.Used.Value()+got.FreeOnDisk.Value() {
		t.Fatalf("size = %d; want used + freeOnDisk", got.Size.Value())
	}
	if s := got.FreeOnDisk.String(); s != "1000Gi" {
		t.Fatalf("freeOnDisk prints as %q; want 1000Gi", s)
	}
}

func TestReadsReportEveryConnectionAndForgetTheGoneOnes(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	reader := &fakeReader{stats: map[string]map[string]*PoolStats{
		"user-ok": {"rbd": {Stored: gi, MaxAvail: 9 * gi}},
	}}
	p := newPublisher(t, reader, connection("ok"), connection("broken"), rbdClass("on-ok", "ok", "rbd"), rbdClass("on-broken", "broken", "rbd"))
	p.Reader = readerFunc(func(ctx context.Context, creds cephconn.Credentials, pools []string) (map[string]*PoolStats, error) {
		if creds.Name == "broken" {
			return nil, errors.New("connection refused")
		}
		return reader.ReadPools(ctx, creds, pools)
	})

	syncAt(t, p, now)
	if diff := cmp.Diff(map[string]bool{"ok": true, "broken": false}, p.Reads()); diff != "" {
		t.Fatalf("reads (-want +got):\n%s", diff)
	}

	class := &v1alpha1.CephStorageClass{}
	if err := p.Client.Get(context.Background(), client.ObjectKey{Name: "on-broken"}, class); err != nil {
		t.Fatal(err)
	}
	if err := p.Client.Delete(context.Background(), class); err != nil {
		t.Fatal(err)
	}
	syncAt(t, p, now.Add(time.Minute))
	if diff := cmp.Diff(map[string]bool{"ok": true}, p.Reads()); diff != "" {
		t.Fatalf("reads after the class is gone (-want +got):\n%s", diff)
	}
}

func TestStartReadsAtOnceAndStopsWithTheContext(t *testing.T) {
	reader := &fakeReader{stats: map[string]map[string]*PoolStats{
		"user-ceph": {"rbd": {Stored: gi, MaxAvail: 9 * gi}},
	}}
	p := newPublisher(t, reader, connection("ceph"), rbdClass("rbd", "ceph", "rbd"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Start(ctx) }()

	deadline := time.After(10 * time.Second)
	for capacityOf(t, p, "rbd") == nil {
		select {
		case <-deadline:
			t.Fatal("Start did not publish the capacity at once")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return after the context was cancelled")
	}
	if !p.NeedLeaderElection() {
		t.Fatal("the publisher runs without the leader election")
	}
}

type readerFunc func(context.Context, cephconn.Credentials, []string) (map[string]*PoolStats, error)

func (f readerFunc) ReadPools(ctx context.Context, creds cephconn.Credentials, pools []string) (map[string]*PoolStats, error) {
	return f(ctx, creds, pools)
}
