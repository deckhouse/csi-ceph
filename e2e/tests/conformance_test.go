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

package tests

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The upstream ceph-csi provisioner (CSI driver) names csi-ceph registers.
const (
	rbdProvisioner    = "rbd.csi.ceph.com"
	cephfsProvisioner = "cephfs.csi.ceph.com"
)

const (
	envConformance    = "E2E_CONFORMANCE"          // "false"/"0"/"no" to skip the whole suite
	envConfFocus      = "E2E_CONFORMANCE_FOCUS"    // Ginkgo focus regex override
	envConfSkip       = "E2E_CONFORMANCE_SKIP"     // Ginkgo skip regex override
	envConfProcs      = "E2E_CONFORMANCE_PROCS"    // parallel Ginkgo processes
	envConfTimeout    = "E2E_CONFORMANCE_TIMEOUT"  // per-driver run timeout
	envK8sTestVersion = "E2E_K8S_TEST_VERSION"     // override the e2e.test version (else the server version)

	// defaultConfFocus keeps the run to the CSI capabilities this PR cares about
	// (dynamic provisioning + clone, online/offline expansion, snapshot restore,
	// and — via the block-capable driver manifest — Block volumeMode), instead of
	// the full 100+-spec External.Storage matrix. Override with E2E_CONFORMANCE_FOCUS.
	defaultConfFocus = `External.Storage.*(provisioning|volume-expand|snapshottable)`
	// defaultConfSkip drops the slow / environment-specific patterns that add
	// little signal for a build-migration PR (disruptive/serial/slow specs,
	// pre-provisioned & inline & ephemeral patterns, non-Linux fs, snapshot
	// stress). Override with E2E_CONFORMANCE_SKIP.
	defaultConfSkip = `\[Disruptive\]|\[Serial\]|\[Slow\]|\[Feature:Windows\]|Pre-provisioned|Inline-volume|Ephemeral|snapshottable-stress|\(xfs\)|\(ntfs\)`

	defaultConfProcs = "4"
	defaultConfTO    = 90 * time.Minute
)

var volumeSnapshotClassGVR = schema.GroupVersionResource{
	Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshotclasses",
}

var k8sVersionRe = regexp.MustCompile(`(\d+\.\d+\.\d+)`)

// driverManifest is the minimal input for one External.Storage testdriver run.
type driverManifest struct {
	name         string // CSI driver / provisioner name
	storageClass string // existing StorageClass the ESC created
	fsTypes      []string
	block        bool
	rwx          bool
}

// conformanceSpecs runs the upstream Kubernetes external-storage CSI test suite
// (test/e2e/storage/testsuites, the de-facto CSI conformance) against the RBD and
// CephFS StorageClasses csi-ceph materialised earlier in the run. It downloads the
// version-matched `e2e.test`, points it at the in-process cluster via a
// serialised kubeconfig, and drives one focused run per driver. Registered after
// createSpecs so the StorageClasses exist; runs before the suite's AfterAll
// teardown. Disable with E2E_CONFORMANCE=false.
func conformanceSpecs() {
	Describe("k8s external-storage CSI conformance", Ordered, func() {
		var (
			e2eBin         string
			kubeconfigPath string
		)

		BeforeAll(func() {
			if !conformanceEnabled() {
				Skip("E2E_CONFORMANCE is disabled")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			defer cancel()

			var err error
			By("Resolving and downloading the version-matched k8s e2e.test binary")
			e2eBin, err = ensureE2ETestBinary(ctx)
			Expect(err).NotTo(HaveOccurred(), "obtain k8s e2e.test")

			By("Serialising a kubeconfig for the in-process cluster connection")
			kubeconfigPath, err = writeSuiteKubeconfig()
			Expect(err).NotTo(HaveOccurred(), "write kubeconfig")
		})

		It("RBD: passes the focused External.Storage suite (Block, expansion, snapshot, clone)", func() {
			if !conformanceEnabled() {
				Skip("E2E_CONFORMANCE is disabled")
			}
			runExternalStorageForDriver(driverManifest{
				name:         rbdProvisioner,
				storageClass: escRBDName(),
				fsTypes:      []string{"", "ext4"},
				block:        true,
				rwx:          false,
			}, e2eBin, kubeconfigPath)
		})

		It("CephFS: passes the focused External.Storage suite (RWX, expansion, snapshot, clone)", func() {
			if !conformanceEnabled() {
				Skip("E2E_CONFORMANCE is disabled")
			}
			runExternalStorageForDriver(driverManifest{
				name:         cephfsProvisioner,
				storageClass: escCephFSName(),
				fsTypes:      []string{""},
				block:        false,
				rwx:          true,
			}, e2eBin, kubeconfigPath)
		})
	})
}

func conformanceEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envConformance))) {
	case "false", "0", "no", "off":
		return false
	default:
		return true
	}
}

func confTimeout() time.Duration {
	return parseDuration(os.Getenv(envConfTimeout), defaultConfTO)
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// runExternalStorageForDriver provisions the driver's VolumeSnapshotClass, writes
// its testdriver manifest and runs the focused e2e.test suite, failing the spec on
// a non-zero exit.
func runExternalStorageForDriver(dm driverManifest, e2eBin, kubeconfigPath string) {
	GinkgoHelper()
	ctx, cancel := context.WithTimeout(context.Background(), confTimeout()+10*time.Minute)
	defer cancel()

	By("Ensuring a VolumeSnapshotClass for " + dm.storageClass)
	snapClass, err := ensureVolumeSnapshotClass(ctx, dm.storageClass, dm.name)
	Expect(err).NotTo(HaveOccurred(), "VolumeSnapshotClass for %s", dm.storageClass)

	By("Writing the External.Storage testdriver manifest for " + dm.name)
	manifestPath, err := writeTestDriverManifest(dm, snapClass)
	Expect(err).NotTo(HaveOccurred(), "write testdriver manifest")

	By("Running the focused External.Storage suite for " + dm.name)
	Expect(runE2ETest(ctx, e2eBin, kubeconfigPath, manifestPath)).
		To(Succeed(), "External.Storage suite for %s", dm.name)
}

// ensureVolumeSnapshotClass derives a ceph-csi VolumeSnapshotClass from the
// parameters of the StorageClass csi-ceph created (clusterID + the provisioner
// secret, reused as the snapshotter secret). Idempotent.
func ensureVolumeSnapshotClass(ctx context.Context, scName, driver string) (string, error) {
	var sc storagev1.StorageClass
	if err := suiteK8s.Get(ctx, client.ObjectKey{Name: scName}, &sc); err != nil {
		return "", fmt.Errorf("get StorageClass %s: %w", scName, err)
	}
	clusterID := sc.Parameters["clusterID"]
	secretName := sc.Parameters["csi.storage.k8s.io/provisioner-secret-name"]
	secretNS := sc.Parameters["csi.storage.k8s.io/provisioner-secret-namespace"]
	if clusterID == "" || secretName == "" || secretNS == "" {
		return "", fmt.Errorf("StorageClass %s is missing ceph-csi params (clusterID/provisioner-secret)", scName)
	}

	name := scName + "-snap"
	vsc := &unstructured.Unstructured{}
	vsc.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "snapshot.storage.k8s.io", Version: "v1", Kind: "VolumeSnapshotClass",
	})
	vsc.SetName(name)
	vsc.Object["driver"] = driver
	vsc.Object["deletionPolicy"] = "Delete"
	vsc.Object["parameters"] = map[string]interface{}{
		"clusterID": clusterID,
		"csi.storage.k8s.io/snapshotter-secret-name":      secretName,
		"csi.storage.k8s.io/snapshotter-secret-namespace": secretNS,
	}

	_, err := suiteDyn.Resource(volumeSnapshotClassGVR).Create(ctx, vsc, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create VolumeSnapshotClass %s: %w", name, err)
	}
	return name, nil
}

// writeTestDriverManifest renders the external-storage driverDefinition
// (test/e2e/storage/external) for one csi-ceph driver against the existing
// StorageClass + VolumeSnapshotClass.
func writeTestDriverManifest(dm driverManifest, snapClass string) (string, error) {
	var fs strings.Builder
	for _, t := range dm.fsTypes {
		fmt.Fprintf(&fs, "    %q: {}\n", t)
	}
	manifest := fmt.Sprintf(`StorageClass:
  FromExistingClassName: %s
SnapshotClass:
  FromExistingClassName: %s
DriverInfo:
  Name: %s
  SupportedSizeRange:
    Min: 1Gi
  SupportedFsType:
%s  Capabilities:
    persistence: true
    exec: true
    multipods: true
    fsGroup: true
    snapshotDataSource: true
    pvcDataSource: true
    controllerExpansion: true
    nodeExpansion: true
    onlineExpansion: true
    block: %t
    RWX: %t
`, dm.storageClass, snapClass, dm.name, fs.String(), dm.block, dm.rwx)

	path := filepath.Join(os.TempDir(), "testdriver-"+sanitize(dm.name)+".yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		return "", err
	}
	GinkgoWriter.Printf("testdriver %s:\n%s\n", path, manifest)
	return path, nil
}

// ensureE2ETestBinary downloads the k8s e2e.test binary matching the cluster's
// server version (or E2E_K8S_TEST_VERSION) and caches it under TMPDIR.
func ensureE2ETestBinary(ctx context.Context) (string, error) {
	ver := strings.TrimSpace(os.Getenv(envK8sTestVersion))
	if ver == "" {
		dc, err := discovery.NewDiscoveryClientForConfig(suiteRestCfg)
		if err != nil {
			return "", fmt.Errorf("discovery client: %w", err)
		}
		info, err := dc.ServerVersion()
		if err != nil {
			return "", fmt.Errorf("server version: %w", err)
		}
		ver = info.GitVersion
	}
	m := k8sVersionRe.FindString(ver)
	if m == "" {
		return "", fmt.Errorf("cannot parse a X.Y.Z k8s version from %q", ver)
	}
	ver = "v" + m

	dir := filepath.Join(os.TempDir(), "k8s-e2e-"+ver)
	bin := filepath.Join(dir, "e2e.test")
	if fi, err := os.Stat(bin); err == nil && fi.Mode()&0o111 != 0 {
		return bin, nil
	}

	url := fmt.Sprintf("https://dl.k8s.io/%s/kubernetes-test-linux-amd64.tar.gz", ver)
	GinkgoWriter.Printf("downloading e2e.test %s from %s\n", ver, url)
	sh := fmt.Sprintf(
		`set -euo pipefail; mkdir -p %q; curl --fail -sSL %q | tar -xz -C %q --strip-components=3 kubernetes/test/bin/e2e.test`,
		dir, url, dir,
	)
	cmd := exec.CommandContext(ctx, "bash", "-c", sh)
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = GinkgoWriter
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("download/extract e2e.test %s: %w", ver, err)
	}
	return bin, nil
}

// writeSuiteKubeconfig serialises the in-process cluster connection (rest.Config)
// into a kubeconfig file so the external e2e.test subprocess can reach the same
// cluster through the live tunnel.
func writeSuiteKubeconfig() (string, error) {
	cfg := clientcmdapi.NewConfig()

	cluster := clientcmdapi.NewCluster()
	cluster.Server = suiteRestCfg.Host
	cluster.CertificateAuthorityData = suiteRestCfg.CAData
	cluster.InsecureSkipTLSVerify = suiteRestCfg.Insecure && len(suiteRestCfg.CAData) == 0

	auth := clientcmdapi.NewAuthInfo()
	auth.ClientCertificateData = suiteRestCfg.CertData
	auth.ClientKeyData = suiteRestCfg.KeyData
	auth.Token = suiteRestCfg.BearerToken
	auth.Username = suiteRestCfg.Username
	auth.Password = suiteRestCfg.Password

	kctx := clientcmdapi.NewContext()
	kctx.Cluster = "e2e"
	kctx.AuthInfo = "e2e"

	cfg.Clusters["e2e"] = cluster
	cfg.AuthInfos["e2e"] = auth
	cfg.Contexts["e2e"] = kctx
	cfg.CurrentContext = "e2e"

	path := filepath.Join(os.TempDir(), "e2e-conformance.kubeconfig")
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		return "", err
	}
	return path, nil
}

// runE2ETest executes the external-storage suite with the focused filters.
func runE2ETest(ctx context.Context, e2eBin, kubeconfigPath, manifestPath string) error {
	focus := envOr(envConfFocus, defaultConfFocus)
	skip := envOr(envConfSkip, defaultConfSkip)
	procs := envOr(envConfProcs, defaultConfProcs)

	args := []string{
		"--kubeconfig=" + kubeconfigPath,
		"--provider=skeleton",
		"--storage.testdriver=" + manifestPath,
		"--ginkgo.focus=" + focus,
		"--ginkgo.skip=" + skip,
		"--ginkgo.procs=" + procs,
		"--ginkgo.timeout=" + confTimeout().String(),
		"--ginkgo.flake-attempts=2",
		"--ginkgo.no-color",
	}
	GinkgoWriter.Printf("running: %s\n  focus=%q\n  skip=%q\n  procs=%s manifest=%s\n",
		e2eBin, focus, skip, procs, manifestPath)

	cmd := exec.CommandContext(ctx, e2eBin, args...)
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = GinkgoWriter
	return cmd.Run()
}

var nonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func sanitize(s string) string {
	return strings.Trim(nonAlnum.ReplaceAllString(s, "-"), "-")
}
