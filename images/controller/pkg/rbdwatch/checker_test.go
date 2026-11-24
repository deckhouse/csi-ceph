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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/sds-common-lib/logger"
)

type fakeCounter struct {
	mu     sync.Mutex
	counts map[Image]int
	err    error
	calls  []Credentials
	images [][]Image
}

func (f *fakeCounter) CountWatchers(_ context.Context, creds Credentials, images []Image) (map[Image]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, creds)
	f.images = append(f.images, images)
	counts := map[Image]int{}
	for _, image := range images {
		if n, ok := f.counts[image]; ok {
			counts[image] = n
		}
	}
	return counts, f.err
}

func connection(name string) *v1alpha1.CephClusterConnection {
	return &v1alpha1.CephClusterConnection{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.CephClusterConnectionSpec{
			ClusterID: "fsid-" + name,
			Monitors:  []string{"10.0.0.1:6789", "10.0.0.2:6789"},
			UserID:    "user-" + name,
			UserKey:   "key-" + name,
		},
	}
}

func rbdPV(name, conn, pool, image string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       Driver,
					VolumeHandle: "0001-0024-fsid-0000000000000001-" + name,
					VolumeAttributes: map[string]string{
						"clusterID": "fsid-" + conn,
						"pool":      pool,
						"imageName": image,
					},
					NodeStageSecretRef: &corev1.SecretReference{Name: "csi-ceph-secret-for-" + conn, Namespace: "d8-csi-ceph"},
				},
			},
		},
	}
}

func attachment(name, attacher, pv, node string, attached bool) *storagev1.VolumeAttachment {
	return &storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: storagev1.VolumeAttachmentSpec{
			Attacher: attacher,
			NodeName: node,
			Source:   storagev1.VolumeAttachmentSource{PersistentVolumeName: &pv},
		},
		Status: storagev1.VolumeAttachmentStatus{Attached: attached},
	}
}

func newChecker(t *testing.T, counter WatcherCounter, objs ...client.Object) (*Checker, *events.FakeRecorder) {
	t.Helper()
	scheme := apiruntime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	recorder := events.NewFakeRecorder(16)
	return &Checker{
		Client:   fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build(),
		Counter:  counter,
		Recorder: recorder,
		Log:      logger.NewNop(),
		Interval: time.Minute,
	}, recorder
}

func drain(r *events.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-r.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func TestVolumeWithoutWatcherIsReported(t *testing.T) {
	image := Image{Pool: "rbd", Name: "csi-vol-a"}
	counter := &fakeCounter{counts: map[Image]int{image: 0}}
	c, recorder := newChecker(t, counter,
		connection("ceph"),
		rbdPV("pv-a", "ceph", "rbd", "csi-vol-a"),
		attachment("va-a", Driver, "pv-a", "worker-1", true),
	)

	c.Run(context.Background())
	result := c.Result()

	want := Result{
		Unwatched: []Unwatched{{
			Volume:   Volume{PersistentVolume: "pv-a", Connection: "ceph", Image: image, Nodes: []string{"worker-1"}},
			Watchers: 0,
		}},
		Connections: map[string]bool{"ceph": true},
	}
	if diff := cmp.Diff(want, result); diff != "" {
		t.Fatalf("unexpected result (-want +got):\n%s", diff)
	}

	wantCreds := []Credentials{{Name: "ceph", Monitors: []string{"10.0.0.1:6789", "10.0.0.2:6789"}, UserID: "user-ceph", UserKey: "key-ceph"}}
	if diff := cmp.Diff(wantCreds, counter.calls); diff != "" {
		t.Fatalf("unexpected credentials (-want +got):\n%s", diff)
	}

	got := drain(recorder)
	if len(got) != 1 || !strings.HasPrefix(got[0], "Warning "+EventReason+" ") || !strings.Contains(got[0], "rbd/csi-vol-a") || !strings.Contains(got[0], "worker-1") {
		t.Fatalf("unexpected events: %q", got)
	}

	expected := `
# HELP csi_ceph_rbd_volume_without_watch 1 for each node an RBD volume is attached to while its image has fewer watchers than such nodes.
# TYPE csi_ceph_rbd_volume_without_watch gauge
csi_ceph_rbd_volume_without_watch{cephclusterconnection="ceph",image="csi-vol-a",node="worker-1",persistentvolume="pv-a",pool="rbd",rados_namespace=""} 1
# HELP csi_ceph_rbd_watch_check_success 1 when the last check counted the watchers of every RBD image attached through the CephClusterConnection, 0 when it failed.
# TYPE csi_ceph_rbd_watch_check_success gauge
csi_ceph_rbd_watch_check_success{cephclusterconnection="ceph"} 1
`
	if err := testutil.CollectAndCompare(Collector{Checker: c}, strings.NewReader(expected)); err != nil {
		t.Fatal(err)
	}
}

func TestWatchedVolumeIsNotReported(t *testing.T) {
	counter := &fakeCounter{counts: map[Image]int{{Pool: "rbd", Name: "csi-vol-a"}: 1}}
	c, recorder := newChecker(t, counter,
		connection("ceph"),
		rbdPV("pv-a", "ceph", "rbd", "csi-vol-a"),
		attachment("va-a", Driver, "pv-a", "worker-1", true),
	)

	c.Run(context.Background())

	if got := c.Result().Unwatched; len(got) != 0 {
		t.Fatalf("expected no unwatched volumes, got %+v", got)
	}
	if got := drain(recorder); len(got) != 0 {
		t.Fatalf("expected no events, got %q", got)
	}
}

func TestVolumeAttachedToMoreNodesThanWatchersIsReportedOnEveryNode(t *testing.T) {
	image := Image{Pool: "rbd", Name: "csi-vol-vm"}
	counter := &fakeCounter{counts: map[Image]int{image: 1}}
	c, _ := newChecker(t, counter,
		connection("ceph"),
		rbdPV("pv-vm", "ceph", "rbd", "csi-vol-vm"),
		attachment("va-2", Driver, "pv-vm", "worker-2", true),
		attachment("va-1", Driver, "pv-vm", "worker-1", true),
	)

	c.Run(context.Background())

	got := c.Result().Unwatched
	if len(got) != 1 || got[0].Watchers != 1 || !cmp.Equal(got[0].Nodes, []string{"worker-1", "worker-2"}) {
		t.Fatalf("unexpected unwatched volumes: %+v", got)
	}
	if n := testutil.CollectAndCount(Collector{Checker: c}, "csi_ceph_rbd_volume_without_watch"); n != 2 {
		t.Fatalf("expected a series per node, got %d", n)
	}
}

func TestOnlyAttachedRBDVolumesAreChecked(t *testing.T) {
	deleting := attachment("va-deleting", Driver, "pv-deleting", "worker-1", true)
	deleting.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	deleting.Finalizers = []string{"external-attacher/rbd-csi-ceph-com"}

	cephfs := rbdPV("pv-cephfs", "ceph", "cephfs", "unused")
	cephfs.Spec.CSI.Driver = "cephfs.csi.ceph.com"

	foreignSecret := rbdPV("pv-foreign", "ceph", "rbd", "csi-vol-foreign")
	foreignSecret.Spec.CSI.NodeStageSecretRef.Name = "some-other-secret"

	counter := &fakeCounter{counts: map[Image]int{}}
	c, _ := newChecker(t, counter,
		connection("ceph"),
		rbdPV("pv-detached", "ceph", "rbd", "csi-vol-detached"),
		attachment("va-detached", Driver, "pv-detached", "worker-1", false),
		rbdPV("pv-deleting", "ceph", "rbd", "csi-vol-deleting"),
		deleting,
		cephfs,
		attachment("va-cephfs", "cephfs.csi.ceph.com", "pv-cephfs", "worker-1", true),
		foreignSecret,
		attachment("va-foreign", Driver, "pv-foreign", "worker-1", true),
		rbdPV("pv-unattached", "ceph", "rbd", "csi-vol-unattached"),
	)

	c.Run(context.Background())

	if len(counter.calls) != 0 {
		t.Fatalf("expected no Ceph calls, got images %v", counter.images)
	}
	if got := c.Result(); len(got.Unwatched) != 0 || len(got.Connections) != 0 {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestStaticVolumeUsesTheVolumeHandleAsImageName(t *testing.T) {
	pv := rbdPV("pv-static", "ceph", "rbd", "")
	pv.Spec.CSI.VolumeHandle = "static-image"
	pv.Spec.CSI.VolumeAttributes["staticVolume"] = "true"
	pv.Spec.CSI.VolumeAttributes["radosNamespace"] = "ns1"

	image := Image{Pool: "rbd", RadosNamespace: "ns1", Name: "static-image"}
	counter := &fakeCounter{counts: map[Image]int{image: 0}}
	c, _ := newChecker(t, counter,
		connection("ceph"),
		pv,
		attachment("va-static", Driver, "pv-static", "worker-1", true),
	)

	c.Run(context.Background())

	if diff := cmp.Diff([][]Image{{image}}, counter.images); diff != "" {
		t.Fatalf("unexpected images (-want +got):\n%s", diff)
	}
	if got := c.Result().Unwatched; len(got) != 1 || got[0].Image != image {
		t.Fatalf("unexpected unwatched volumes: %+v", got)
	}
}

func TestVolumesAreGroupedByConnection(t *testing.T) {
	a := Image{Pool: "rbd", Name: "csi-vol-a"}
	b := Image{Pool: "fast", Name: "csi-vol-b"}
	counter := &fakeCounter{counts: map[Image]int{a: 1, b: 1}}
	c, _ := newChecker(t, counter,
		connection("first"),
		connection("second"),
		rbdPV("pv-a", "first", "rbd", "csi-vol-a"),
		rbdPV("pv-b", "second", "fast", "csi-vol-b"),
		attachment("va-a", Driver, "pv-a", "worker-1", true),
		attachment("va-b", Driver, "pv-b", "worker-1", true),
	)

	c.Run(context.Background())

	byUser := map[string][]Image{}
	for i, creds := range counter.calls {
		byUser[creds.Name] = counter.images[i]
	}
	if diff := cmp.Diff(map[string][]Image{"first": {a}, "second": {b}}, byUser); diff != "" {
		t.Fatalf("unexpected images (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string]bool{"first": true, "second": true}, c.Result().Connections); diff != "" {
		t.Fatalf("unexpected connections (-want +got):\n%s", diff)
	}
}

func TestFailedCountMarksTheConnectionAndKeepsTheCountedImages(t *testing.T) {
	counted := Image{Pool: "rbd", Name: "csi-vol-a"}
	counter := &fakeCounter{counts: map[Image]int{counted: 0}, err: errors.New("rbd/csi-vol-b: unable to open the image: permission denied")}
	c, _ := newChecker(t, counter,
		connection("ceph"),
		rbdPV("pv-a", "ceph", "rbd", "csi-vol-a"),
		rbdPV("pv-b", "ceph", "rbd", "csi-vol-b"),
		attachment("va-a", Driver, "pv-a", "worker-1", true),
		attachment("va-b", Driver, "pv-b", "worker-1", true),
	)

	c.Run(context.Background())
	result := c.Result()

	if diff := cmp.Diff(map[string]bool{"ceph": false}, result.Connections); diff != "" {
		t.Fatalf("unexpected connections (-want +got):\n%s", diff)
	}
	if len(result.Unwatched) != 1 || result.Unwatched[0].PersistentVolume != "pv-a" {
		t.Fatalf("unexpected unwatched volumes: %+v", result.Unwatched)
	}
}

func TestMissingConnectionFailsItsCheck(t *testing.T) {
	counter := &fakeCounter{}
	c, _ := newChecker(t, counter,
		rbdPV("pv-a", "gone", "rbd", "csi-vol-a"),
		attachment("va-a", Driver, "pv-a", "worker-1", true),
	)

	c.Run(context.Background())

	if len(counter.calls) != 0 {
		t.Fatalf("expected no Ceph calls, got %d", len(counter.calls))
	}
	if diff := cmp.Diff(map[string]bool{"gone": false}, c.Result().Connections); diff != "" {
		t.Fatalf("unexpected connections (-want +got):\n%s", diff)
	}
}

func TestNoAttachedVolumesClearsThePreviousResult(t *testing.T) {
	image := Image{Pool: "rbd", Name: "csi-vol-a"}
	counter := &fakeCounter{counts: map[Image]int{image: 0}}
	va := attachment("va-a", Driver, "pv-a", "worker-1", true)
	c, _ := newChecker(t, counter, connection("ceph"), rbdPV("pv-a", "ceph", "rbd", "csi-vol-a"), va)

	c.Run(context.Background())
	if len(c.Result().Unwatched) != 1 {
		t.Fatalf("expected the volume to be reported first")
	}

	if err := c.Client.(client.Client).Delete(context.Background(), va); err != nil {
		t.Fatal(err)
	}
	c.Run(context.Background())

	if got := c.Result(); len(got.Unwatched) != 0 || len(got.Connections) != 0 {
		t.Fatalf("expected an empty result, got %+v", got)
	}
	if n := testutil.CollectAndCount(Collector{Checker: c}); n != 0 {
		t.Fatalf("expected no series, got %d", n)
	}
}
