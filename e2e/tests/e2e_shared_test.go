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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	kubernetes "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/onsi/ginkgo/v2"

	cephv1alpha1 "github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/storage-e2e/pkg/cluster"
	"github.com/deckhouse/storage-e2e/pkg/testkit"
)

const (
	envTestNamespace        = "E2E_NAMESPACE"
	envCephStorageClassName = "E2E_CEPH_STORAGE_CLASS"
	envPVCSize              = "E2E_PVC_SIZE"
	envRookNamespace        = "E2E_ROOK_NAMESPACE"
	envRookOSDStorageClass  = "E2E_ROOK_OSD_STORAGE_CLASS"
	envRookOSDCount         = "E2E_ROOK_OSD_COUNT"
	envRookOSDSize          = "E2E_ROOK_OSD_SIZE"
	envRookCephImage        = "E2E_ROOK_CEPH_IMAGE"
	envRookClusterReadyTO   = "E2E_ROOK_CLUSTER_READY_TIMEOUT"
)

const (
	defaultTestNamespace       = "csi-ceph-e2e"
	defaultPVCSize             = "1Gi"
	defaultRookNamespace       = "d8-sds-elastic"
	defaultOSDBackingClassName = "sds-local-volume-thick"
	defaultOSDSize             = "10Gi"
	defaultCephImage           = "quay.io/ceph/ceph:v18.2.7"

	csiCephNamespace = "d8-csi-ceph"
	cephConfigMap    = "ceph-config"
	cephConfKey      = "ceph.conf"

	rbdControllerDeployment = "csi-controller-rbd"
	rbdNodeDaemonSet        = "csi-node-rbd"

	moduleConfigName = "csi-ceph"

	moduleReconcileTimeout        = 3 * time.Minute
	deploymentReadyTimeout        = 3 * time.Minute
	pvcBindTimeout                = 5 * time.Minute
	podReadyTimeout               = 5 * time.Minute
	pollInterval                  = 5 * time.Second
	osdBackingStorageClassTimeout = 25 * time.Minute
	nestedClusterCleanupTimeout   = 10 * time.Minute
)

var moduleConfigGVR = schema.GroupVersionResource{
	Group:    "deckhouse.io",
	Version:  "v1alpha1",
	Resource: "moduleconfigs",
}

type e2eConfig struct {
	namespace        string
	cephStorageClass string
	pvcSize          string
	rook             rookConfig
}

type rookConfig struct {
	Namespace       string
	OSDStorageClass string
	OSDCount        int
	OSDSize         string
	CephImage       string
	ClusterReadyTO  time.Duration
}

var (
	suiteCfg               e2eConfig
	suiteRestCfg           *rest.Config
	suiteK8s               client.Client
	suiteDyn               dynamic.Interface
	suiteClusterResources  *cluster.TestClusterResources
	originalMsCrcData      interface{}
	originalMsCrcDataFound bool
)

func loadConfig() e2eConfig {
	cfg := e2eConfig{
		namespace:        os.Getenv(envTestNamespace),
		cephStorageClass: os.Getenv(envCephStorageClassName),
		pvcSize:          os.Getenv(envPVCSize),
		rook: rookConfig{
			Namespace:       os.Getenv(envRookNamespace),
			OSDStorageClass: os.Getenv(envRookOSDStorageClass),
			OSDSize:         os.Getenv(envRookOSDSize),
			CephImage:       os.Getenv(envRookCephImage),
		},
	}

	if cfg.namespace == "" {
		cfg.namespace = defaultTestNamespace
	}
	if cfg.pvcSize == "" {
		cfg.pvcSize = defaultPVCSize
	}
	if cfg.rook.Namespace == "" {
		cfg.rook.Namespace = defaultRookNamespace
	}
	// cfg.rook.OSDStorageClass is intentionally not defaulted here — it is
	// populated from the name returned by EnsureDefaultStorageClass in
	// ensureOSDBackingStorageClass(), so the CephCluster always references the
	// StorageClass actually present in the nested cluster.
	if cfg.rook.OSDSize == "" {
		cfg.rook.OSDSize = defaultOSDSize
	}
	if cfg.rook.CephImage == "" {
		cfg.rook.CephImage = defaultCephImage
	}
	if raw := os.Getenv(envRookOSDCount); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			cfg.rook.OSDCount = n
		}
	}
	if cfg.rook.OSDCount == 0 {
		cfg.rook.OSDCount = 1
	}
	if raw := os.Getenv(envRookClusterReadyTO); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			cfg.rook.ClusterReadyTO = d
		}
	}

	return cfg
}

func newRuntimeClient(restCfg *rest.Config) (client.Client, error) {
	sch := scheme.Scheme
	if err := cephv1alpha1.AddToScheme(sch); err != nil {
		return nil, fmt.Errorf("add csi-ceph scheme: %w", err)
	}
	return client.New(restCfg, client.Options{Scheme: sch})
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

	ensureOSDBackingStorageClass()
}

// ensureOSDBackingStorageClass guarantees that the StorageClass referenced by
// CephCluster.spec.storage.storageClassDeviceSets exists in the nested cluster.
// It delegates to storage-e2e's idempotent EnsureDefaultStorageClass helper
// (Thick LVMVolumeGroup on top of attached VirtualDisks → LocalStorageClass)
// and treats the name it returns as the single source of truth for
// suiteCfg.rook.OSDStorageClass — so CephCluster always points at a SC that
// actually exists in the cluster.
func ensureOSDBackingStorageClass() {
	if suiteClusterResources == nil || suiteClusterResources.Kubeconfig == nil {
		return
	}

	baseSC := strings.TrimSpace(os.Getenv("TEST_CLUSTER_STORAGE_CLASS"))
	vmNamespace := strings.TrimSpace(os.Getenv("TEST_CLUSTER_NAMESPACE"))
	if vmNamespace == "" {
		vmNamespace = "e2e-csi-ceph"
	}

	requestedName := strings.TrimSpace(os.Getenv(envRookOSDStorageClass))
	if requestedName == "" {
		requestedName = defaultOSDBackingClassName
	}

	ctx, cancel := context.WithTimeout(context.Background(), osdBackingStorageClassTimeout)
	defer cancel()

	cfg := testkit.DefaultStorageClassConfig{
		StorageClassName:       requestedName,
		LVMType:                "Thick",
		VGName:                 "vg-thick",
		BaseKubeconfig:         suiteClusterResources.BaseKubeconfig,
		VMNamespace:            vmNamespace,
		BaseStorageClassName:   baseSC,
		DiskSize:               "20Gi",
		DiskAttachTimeout:      10 * time.Minute,
		BlockDeviceWaitTimeout: 8 * time.Minute,
		LVGReadyTimeout:        8 * time.Minute,
	}

	scName, err := testkit.EnsureDefaultStorageClass(ctx, suiteClusterResources.Kubeconfig, cfg)
	if err != nil {
		Fail(fmt.Sprintf("EnsureDefaultStorageClass(%s, Thick): %v", requestedName, err))
	}

	suiteCfg.rook.OSDStorageClass = scName
	GinkgoWriter.Printf("  OSD-backing storage class is ready: %s (rook CephCluster will reference it)\n", scName)
}

func cleanupNestedTestCluster() {
	if suiteClusterResources == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), nestedClusterCleanupTimeout)
	defer cancel()

	if err := cluster.CleanupTestCluster(ctx, suiteClusterResources); err != nil {
		GinkgoWriter.Printf("  warning: nested cluster cleanup failed: %v\n", err)
	} else {
		GinkgoWriter.Printf("  nested cluster cleanup finished\n")
	}
	suiteClusterResources = nil
}

func ensureNamespace(ctx context.Context, c client.Client, name string) error {
	ns := &corev1.Namespace{}
	if err := c.Get(ctx, client.ObjectKey{Name: name}, ns); err == nil {
		return nil
	}
	ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	return client.IgnoreAlreadyExists(c.Create(ctx, ns))
}

func execInPod(
	ctx context.Context,
	restCfg *rest.Config,
	namespace, pod, container string,
	cmd []string,
) (string, error) {
	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return "", fmt.Errorf("create clientset: %w", err)
	}

	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(namespace).
		SubResource("exec")

	opts := &corev1.PodExecOptions{
		Container: container,
		Command:   cmd,
		Stdout:    true,
		Stderr:    true,
	}
	req.VersionedParams(opts, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(restCfg, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("create SPDY executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	combined := stdout.String() + stderr.String()
	if err != nil {
		return combined, fmt.Errorf("exec %v in %s/%s[%s]: %w", cmd, namespace, pod, container, err)
	}
	return combined, nil
}

func getModuleConfigSetting(ctx context.Context, dyn dynamic.Interface, name, key string) (interface{}, bool, error) {
	mc, err := dyn.Resource(moduleConfigGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, false, fmt.Errorf("get ModuleConfig %q: %w", name, err)
	}
	val, found, err := unstructured.NestedFieldCopy(mc.Object, "spec", "settings", key)
	if err != nil {
		return nil, false, fmt.Errorf("read spec.settings.%s: %w", key, err)
	}
	return val, found, nil
}

func setModuleConfigSetting(
	ctx context.Context,
	dyn dynamic.Interface,
	name, key string,
	value interface{},
	deleteWhenNil bool,
) error {
	if value == nil && deleteWhenNil {
		escapedKey := strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
		patch := []byte(fmt.Sprintf(`[{"op":"remove","path":"/spec/settings/%s"}]`, escapedKey))
		_, err := dyn.Resource(moduleConfigGVR).Patch(ctx, name, types.JSONPatchType, patch, metav1.PatchOptions{})
		if err == nil {
			return nil
		}
		if apierrors.IsInvalid(err) || strings.Contains(err.Error(), "not found") {
			return nil
		}
		return fmt.Errorf("remove spec.settings.%s on ModuleConfig %q: %w", key, name, err)
	}

	payload := map[string]interface{}{
		"spec": map[string]interface{}{
			"version": 1,
			"settings": map[string]interface{}{
				key: value,
			},
		},
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal merge patch: %w", err)
	}
	_, err = dyn.Resource(moduleConfigGVR).Patch(ctx, name, types.MergePatchType, buf, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch ModuleConfig %q spec.settings.%s=%v: %w", name, key, value, err)
	}
	return nil
}

func waitCephConfigContains(ctx context.Context, c client.Client, needle string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		var cm corev1.ConfigMap
		err := c.Get(ctx, client.ObjectKey{Namespace: csiCephNamespace, Name: cephConfigMap}, &cm)
		if err == nil {
			if conf, ok := cm.Data[cephConfKey]; ok && strings.Contains(conf, needle) {
				return nil
			}
		} else if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get ConfigMap %s/%s: %w", csiCephNamespace, cephConfigMap, err)
		}
		if time.Now().After(deadline) {
			var body string
			if conf, ok := cm.Data[cephConfKey]; ok {
				body = conf
			}
			return fmt.Errorf("timeout waiting for %q in ConfigMap %s/%s; last ceph.conf=\n%s",
				needle, csiCephNamespace, cephConfigMap, body)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func waitCephConfigExcludes(ctx context.Context, c client.Client, needle string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		var cm corev1.ConfigMap
		err := c.Get(ctx, client.ObjectKey{Namespace: csiCephNamespace, Name: cephConfigMap}, &cm)
		if err == nil {
			if conf, ok := cm.Data[cephConfKey]; ok && !strings.Contains(conf, needle) {
				return nil
			}
		} else if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get ConfigMap %s/%s: %w", csiCephNamespace, cephConfigMap, err)
		}
		if time.Now().After(deadline) {
			var body string
			if conf, ok := cm.Data[cephConfKey]; ok {
				body = conf
			}
			return fmt.Errorf("timeout waiting for %q to disappear from ConfigMap %s/%s; last ceph.conf=\n%s",
				needle, csiCephNamespace, cephConfigMap, body)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func rolloutRestartDeployment(ctx context.Context, c client.Client, namespace, name string) error {
	patch := []byte(fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`,
		time.Now().UTC().Format(time.RFC3339),
	))
	return c.Patch(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
	}, client.RawPatch(types.StrategicMergePatchType, patch))
}

func rolloutRestartDaemonSet(ctx context.Context, c client.Client, namespace, name string) error {
	patch := []byte(fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`,
		time.Now().UTC().Format(time.RFC3339),
	))
	return c.Patch(ctx, &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
	}, client.RawPatch(types.StrategicMergePatchType, patch))
}

func waitDeploymentReady(ctx context.Context, c client.Client, namespace, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		var d appsv1.Deployment
		if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &d); err != nil {
			return fmt.Errorf("get Deployment %s/%s: %w", namespace, name, err)
		}
		specRepl := int32(1)
		if d.Spec.Replicas != nil {
			specRepl = *d.Spec.Replicas
		}
		if d.Status.ObservedGeneration >= d.Generation &&
			d.Status.UpdatedReplicas == specRepl &&
			d.Status.ReadyReplicas == specRepl &&
			d.Status.AvailableReplicas == specRepl {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for Deployment %s/%s: gen=%d obs=%d updated=%d ready=%d avail=%d (want %d)",
				namespace, name, d.Generation, d.Status.ObservedGeneration,
				d.Status.UpdatedReplicas, d.Status.ReadyReplicas, d.Status.AvailableReplicas, specRepl)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func waitDaemonSetReady(ctx context.Context, c client.Client, namespace, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		var ds appsv1.DaemonSet
		if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &ds); err != nil {
			return fmt.Errorf("get DaemonSet %s/%s: %w", namespace, name, err)
		}
		if ds.Status.ObservedGeneration >= ds.Generation &&
			ds.Status.UpdatedNumberScheduled == ds.Status.DesiredNumberScheduled &&
			ds.Status.NumberReady == ds.Status.DesiredNumberScheduled &&
			ds.Status.DesiredNumberScheduled > 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for DaemonSet %s/%s: gen=%d obs=%d desired=%d updated=%d ready=%d",
				namespace, name, ds.Generation, ds.Status.ObservedGeneration,
				ds.Status.DesiredNumberScheduled, ds.Status.UpdatedNumberScheduled, ds.Status.NumberReady)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// resolveCephSCConfig assembles the testkit.CephStorageClassConfig used
// for both EnsureCephStorageClass (suite bootstrap) and
// TeardownCephStorageClass (suite cleanup). We intentionally set
// SkipModuleEnablement=true: storage-e2e has already enabled
// sds-node-configurator / sds-elastic / csi-ceph while bootstrapping the
// nested cluster through cluster_config.yml, so re-enabling them would be
// redundant at best and a race at worst.
func resolveCephSCConfig(cfg e2eConfig) testkit.CephStorageClassConfig {
	scName := strings.TrimSpace(cfg.cephStorageClass)
	if scName == "" {
		scName = "ceph-rbd-r1"
	}
	scCfg := testkit.CephStorageClassConfig{
		StorageClassName:     scName,
		Namespace:            cfg.rook.Namespace,
		OSDStorageClass:      cfg.rook.OSDStorageClass,
		OSDCount:             cfg.rook.OSDCount,
		OSDSize:              cfg.rook.OSDSize,
		CephImage:            cfg.rook.CephImage,
		SkipModuleEnablement: true,
	}
	if cfg.rook.ClusterReadyTO > 0 {
		scCfg.CephClusterReadyTimeout = cfg.rook.ClusterReadyTO
	}
	return scCfg
}

func bootstrapCeph(ctx context.Context, restCfg *rest.Config, cfg e2eConfig) (string, error) {
	return testkit.EnsureCephStorageClass(ctx, restCfg, resolveCephSCConfig(cfg))
}
