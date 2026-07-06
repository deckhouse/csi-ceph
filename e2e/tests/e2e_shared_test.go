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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"

	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/deckhouse/storage-e2e/pkg/cluster"
	storagekube "github.com/deckhouse/storage-e2e/pkg/kubernetes"
	"github.com/deckhouse/storage-e2e/pkg/testkit"
)

// --- Suite env knobs (storage-e2e cluster knobs are read by storage-e2e itself) ---
const (
	envECName            = "E2E_EC_NAME"
	envPVCSize           = "E2E_PVC_SIZE"
	envOSDBDLabel        = "E2E_OSD_BD_LABEL"
	envStorageNodeLabel  = "E2E_STORAGE_NODE_LABEL"
	envNetworkPublic     = "E2E_NETWORK_PUBLIC"
	envNetworkCluster    = "E2E_NETWORK_CLUSTER"
	envECReadyTimeout    = "E2E_EC_READY_TIMEOUT"
	envESCReadyTimeout   = "E2E_ESC_READY_TIMEOUT"
	envModuleReadyTO     = "E2E_MODULE_READY_TIMEOUT"
	envOSDDisksPerWorker = "E2E_OSD_DISKS_PER_WORKER"
	envOSDDiskSize       = "E2E_OSD_DISK_SIZE"
	envProbeImage        = "E2E_PROBE_IMAGE"

	// envKeepClusterOnFailure, when truthy, skips nested-cluster teardown if any
	// spec failed, leaving the cluster live for manual debugging.
	envKeepClusterOnFailure = "E2E_KEEP_CLUSTER_ON_FAILURE"
)

const (
	defaultECName       = "csi-ceph-e2e"
	defaultPVCSize      = "1Gi"
	defaultNamespace    = "e2e-csi-ceph"
	defaultProbeImage   = "busybox:1.36"
	defaultOSDDiskSize  = "20Gi"
	defaultDisksPerNode = 2
	defaultModuleReady  = 15 * time.Minute

	// moduleName is the Deckhouse module under test; also the suffix of its
	// namespace (d8-csi-ceph).
	moduleName = "csi-ceph"

	// csi-ceph-specific storage-node / OSD BlockDevice selection labels. They
	// follow the same "<module>-e2e.storage.deckhouse.io/*" pattern sds-object
	// uses, but are module-scoped so they never collide with sds-elastic's own
	// e2e labels when several suites share tooling.
	csiCephStorageNodeLabelKey = "csi-ceph-e2e.storage.deckhouse.io/storage-node"
	csiCephOSDLabelKey         = "csi-ceph-e2e.storage.deckhouse.io/osd"
	csiCephLabelValue          = "true"

	probeContainerName = "probe"
	probeMountPath     = "/data"
	probeFilePath      = "/data/probe.txt"
)

const (
	pollInterval        = 5 * time.Second
	pvcBindTimeout      = 5 * time.Minute
	podReadyTimeout     = 5 * time.Minute
	resourceGoneTimeout = 15 * time.Minute
)

// fallbackMinOSDBlockDevices is the floor of consumable OSD BlockDevices the
// suite waits for when it did NOT attach the disks itself (existing cluster or
// Commander template with pre-provisioned disks). The Commander template exposes
// >=4 raw devices, so 4 is the meaningful lower bound. When the suite attaches
// the disks itself, it instead waits for exactly the number it attached.
const fallbackMinOSDBlockDevices = 4

var (
	cephClusterConnectionGVR = schema.GroupVersionResource{
		Group: "storage.deckhouse.io", Version: "v1alpha1", Resource: "cephclusterconnections",
	}
	cephStorageClassGVR = schema.GroupVersionResource{
		Group: "storage.deckhouse.io", Version: "v1alpha1", Resource: "cephstorageclasses",
	}
)

type e2eConfig struct {
	// namespace is the in-cluster namespace for PVCs/Pods. Single source of
	// truth: TEST_CLUSTER_NAMESPACE (also the base-cluster VM namespace).
	namespace string

	ecName  string
	pvcSize string

	storageNodeLabelKey   string
	storageNodeLabelValue string
	osdBDLabelKey         string
	osdBDLabelValue       string

	networkPublic  string
	networkCluster string

	ecReadyTimeout  time.Duration
	escReadyTimeout time.Duration
	moduleReadyTO   time.Duration

	osdDisksPerWorker int
	osdDiskSize       string
	probeImage        string

	// vmNamespace / baseStorageClass drive the runtime VirtualDisk attach for
	// raw OSD disks (base cluster). Both come from TEST_CLUSTER_*.
	vmNamespace      string
	baseStorageClass string

	// keepClusterOnFailure, when true, makes cleanupSuite skip nested-cluster
	// teardown if any spec failed (E2E_KEEP_CLUSTER_ON_FAILURE).
	keepClusterOnFailure bool
}

var (
	suiteCfg              e2eConfig
	suiteRestCfg          *rest.Config
	suiteK8s              client.Client
	suiteDyn              dynamic.Interface
	suiteClusterResources *cluster.TestClusterResources
	suiteOSDBlockDevices  []string
)

func loadConfig() e2eConfig {
	cfg := e2eConfig{
		namespace:        strings.TrimSpace(os.Getenv("TEST_CLUSTER_NAMESPACE")),
		ecName:           os.Getenv(envECName),
		pvcSize:          os.Getenv(envPVCSize),
		networkPublic:    os.Getenv(envNetworkPublic),
		networkCluster:   os.Getenv(envNetworkCluster),
		osdDiskSize:      os.Getenv(envOSDDiskSize),
		probeImage:       os.Getenv(envProbeImage),
		vmNamespace:      strings.TrimSpace(os.Getenv("TEST_CLUSTER_NAMESPACE")),
		baseStorageClass: strings.TrimSpace(os.Getenv("TEST_CLUSTER_STORAGE_CLASS")),
	}

	if cfg.namespace == "" {
		cfg.namespace = defaultNamespace
		cfg.vmNamespace = cfg.namespace
	}
	if cfg.ecName == "" {
		cfg.ecName = defaultECName
	}
	if cfg.pvcSize == "" {
		cfg.pvcSize = defaultPVCSize
	}
	if cfg.osdDiskSize == "" {
		cfg.osdDiskSize = defaultOSDDiskSize
	}
	if cfg.probeImage == "" {
		cfg.probeImage = defaultProbeImage
	}

	cfg.storageNodeLabelKey, cfg.storageNodeLabelValue = parseLabel(
		os.Getenv(envStorageNodeLabel), csiCephStorageNodeLabelKey, csiCephLabelValue,
	)
	cfg.osdBDLabelKey, cfg.osdBDLabelValue = parseLabel(
		os.Getenv(envOSDBDLabel), csiCephOSDLabelKey, csiCephLabelValue,
	)

	cfg.ecReadyTimeout = parseDuration(os.Getenv(envECReadyTimeout), testkit.DefaultElasticClusterReadyTimeout)
	cfg.escReadyTimeout = parseDuration(os.Getenv(envESCReadyTimeout), testkit.DefaultElasticStorageClassReadyTimeout)
	cfg.moduleReadyTO = parseDuration(os.Getenv(envModuleReadyTO), defaultModuleReady)

	cfg.osdDisksPerWorker = defaultDisksPerNode
	if raw := strings.TrimSpace(os.Getenv(envOSDDisksPerWorker)); raw != "" {
		if n, err := parsePositiveInt(raw); err == nil {
			cfg.osdDisksPerWorker = n
		}
	}

	cfg.keepClusterOnFailure = envBool(os.Getenv(envKeepClusterOnFailure))

	return cfg
}

// envBool parses a permissive boolean env value ("true"/"1"/"yes", any case).
func envBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "yes", "y", "on":
		return true
	default:
		return false
	}
}

// parseLabel splits a "key=value" env value; "key" alone keeps defVal; empty
// keeps both defaults.
func parseLabel(raw, defKey, defVal string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defKey, defVal
	}
	if i := strings.Index(raw, "="); i >= 0 {
		return raw[:i], raw[i+1:]
	}
	return raw, defVal
}

func parseDuration(raw string, def time.Duration) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	return def
}

func parsePositiveInt(raw string) (int, error) {
	var n int
	if _, err := fmt.Sscanf(raw, "%d", &n); err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, fmt.Errorf("value %q must be positive", raw)
	}
	return n, nil
}

// newRuntimeClient builds a controller-runtime client on the built-in scheme
// (core/apps/storage/admissionregistration). The suite touches only core objects
// (PVC/Pod/PV/StorageClass) with the typed client; the csi-ceph CRDs are handled
// through the dynamic client.
func newRuntimeClient(restCfg *rest.Config) (client.Client, error) {
	return client.New(restCfg, client.Options{Scheme: scheme.Scheme})
}

func ensureNestedTestCluster() {
	if strings.TrimSpace(os.Getenv("TEST_CLUSTER_CREATE_MODE")) == "" {
		Fail("TEST_CLUSTER_CREATE_MODE must be set: this suite only supports storage-e2e nested clusters")
	}
	if suiteClusterResources != nil {
		return
	}
	suiteClusterResources = cluster.CreateOrConnectToTestCluster()
	if suiteClusterResources == nil || suiteClusterResources.Kubeconfig == nil {
		Fail("storage-e2e returned a nil cluster handle")
	}
}

func cleanupNestedTestCluster() {
	if suiteClusterResources == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := cluster.CleanupTestCluster(ctx, suiteClusterResources); err != nil {
		GinkgoWriter.Printf("  warning: nested cluster cleanup failed: %v\n", err)
	} else {
		GinkgoWriter.Printf("  nested cluster cleanup finished\n")
	}
	suiteClusterResources = nil
}

// attachOSDDisks attaches suiteCfg.osdDisksPerWorker raw VirtualDisks to every
// worker VM on the base cluster so sds-node-configurator surfaces them as
// consumable BlockDevices for the ElasticCluster to adopt as OSDs. Returns the
// total number of disks attached so the caller can wait for exactly that many
// consumable BlockDevices. No-op (returns 0) when BaseKubeconfig is nil — the
// Commander/existing-cluster path, where disks are pre-provisioned by the
// template.
func attachOSDDisks(ctx context.Context) (int, error) {
	if suiteClusterResources == nil || suiteClusterResources.BaseKubeconfig == nil {
		GinkgoWriter.Printf("  BaseKubeconfig is nil; skipping VirtualDisk attach (disks assumed pre-provisioned by the Commander template)\n")
		return 0, nil
	}
	workers, err := storagekube.GetWorkerNodes(ctx, suiteRestCfg)
	if err != nil {
		return 0, fmt.Errorf("list worker nodes: %w", err)
	}
	if len(workers) == 0 {
		return 0, fmt.Errorf("no worker nodes found to attach OSD disks to")
	}
	if suiteCfg.baseStorageClass == "" {
		return 0, fmt.Errorf("TEST_CLUSTER_STORAGE_CLASS must be set to attach raw OSD VirtualDisks")
	}

	attached := 0
	for _, w := range workers {
		for d := 0; d < suiteCfg.osdDisksPerWorker; d++ {
			diskName := fmt.Sprintf("%s-ceph-osd-%d", w.Name, d)
			res, err := storagekube.AttachVirtualDiskToVM(ctx, suiteClusterResources.BaseKubeconfig, storagekube.VirtualDiskAttachmentConfig{
				VMName:           w.Name,
				Namespace:        suiteCfg.vmNamespace,
				DiskName:         diskName,
				DiskSize:         suiteCfg.osdDiskSize,
				StorageClassName: suiteCfg.baseStorageClass,
			})
			if err != nil {
				return attached, fmt.Errorf("attach OSD disk %s to %s: %w", diskName, w.Name, err)
			}
			if err := storagekube.WaitForVirtualDiskAttached(ctx, suiteClusterResources.BaseKubeconfig, suiteCfg.vmNamespace, res.AttachmentName, 10*time.Second); err != nil {
				return attached, fmt.Errorf("wait OSD disk %s attach on %s: %w", diskName, w.Name, err)
			}
			attached++
		}
	}
	GinkgoWriter.Printf("  attached %d raw OSD disk(s) across %d worker(s)\n", attached, len(workers))
	return attached, nil
}

func ensureNamespace(ctx context.Context, name string) error {
	_, err := storagekube.CreateNamespaceIfNotExists(ctx, suiteRestCfg, name)
	return err
}

// waitModuleReady blocks until the csi-ceph Deckhouse module reports Ready.
func waitModuleReady(ctx context.Context) error {
	return storagekube.WaitForModuleReady(ctx, suiteRestCfg, moduleName, suiteCfg.moduleReadyTO)
}

// ecNodeSelector / ecBDSelector are the selectors the shared ElasticCluster
// uses; they MUST match the labels EnsureElasticOSDBlockDevices applied.
func ecNodeSelector() map[string]string {
	return map[string]string{suiteCfg.storageNodeLabelKey: suiteCfg.storageNodeLabelValue}
}

func ecBDSelector() map[string]string {
	return map[string]string{suiteCfg.osdBDLabelKey: suiteCfg.osdBDLabelValue}
}

// waitESCCondition polls an ElasticStorageClass status condition until it
// reaches wantStatus or the timeout elapses.
func waitESCCondition(ctx context.Context, escName, condType, wantStatus string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last string
	for {
		status, reason, _, found, err := storagekube.GetElasticStorageClassCondition(ctx, suiteRestCfg, escName, condType)
		if err == nil && found && status == wantStatus {
			return nil
		}
		last = fmt.Sprintf("found=%v status=%q reason=%q err=%v", found, status, reason, err)
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for ESC %s condition %s=%s; last: %s", escName, condType, wantStatus, last)
		}
		if !sleepCtx(ctx, pollInterval) {
			return ctx.Err()
		}
	}
}

// waitResourceGone blocks until a dynamic GET of the resource returns NotFound.
// ns="" addresses cluster-scoped resources.
func waitResourceGone(ctx context.Context, gvr schema.GroupVersionResource, ns, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		var err error
		if ns == "" {
			_, err = suiteDyn.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
		} else {
			_, err = suiteDyn.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		}
		if apierrors.IsNotFound(err) {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("timeout waiting for %s %s/%s to be gone; last get err: %w", gvr.Resource, ns, name, err)
			}
			return fmt.Errorf("timeout waiting for %s %s/%s to be gone (still present)", gvr.Resource, ns, name)
		}
		if !sleepCtx(ctx, pollInterval) {
			return ctx.Err()
		}
	}
}

// storageClassExists reports whether the core Kubernetes StorageClass `name`
// exists (the object csi-ceph materialises from a CephStorageClass).
func storageClassExists(ctx context.Context, name string) error {
	var sc storagev1.StorageClass
	return suiteK8s.Get(ctx, client.ObjectKey{Name: name}, &sc)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
