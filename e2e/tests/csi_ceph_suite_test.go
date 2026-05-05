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
	"os"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/dynamic"

	"github.com/deckhouse/storage-e2e/pkg/testkit"
)

var _ = BeforeSuite(func() {
	prepareSuite()
})

var _ = AfterSuite(func() {
	cleanupSuite()
})

func TestCsiCeph(t *testing.T) {
	RegisterFailHandler(Fail)

	suiteConfig, reporterConfig := GinkgoConfiguration()
	if os.Getenv("CI") != "" {
		suiteConfig.FailFast = true
		suiteConfig.Timeout = 130 * time.Minute
	}
	// Randomize across the whole spec tree, not just top-level Describes.
	// The msCrcData matrix is intentionally state-leak-resilient (each cell
	// asserts auto-rollout vs. steady based on a real ceph.conf FS delta,
	// not on residual state from a predecessor), so randomization is the
	// strongest available regression guard against hidden ordering
	// assumptions creeping back in. Ginkgo prints the seed at the start of
	// every run; override with `-ginkgo.seed=<N>` to reproduce a failure.
	suiteConfig.RandomizeAllSpecs = true
	reporterConfig.Verbose = true
	reporterConfig.ShowNodeEvents = false

	RunSpecs(t, "csi-ceph E2E Suite", suiteConfig, reporterConfig)
}

func prepareSuite() {
	suiteCfg = loadConfig()

	GinkgoWriter.Printf("E2E config:\n")
	GinkgoWriter.Printf("  TEST_CLUSTER_CREATE_MODE:         %q\n", os.Getenv("TEST_CLUSTER_CREATE_MODE"))
	GinkgoWriter.Printf("  E2E_NAMESPACE:                    %q\n", suiteCfg.namespace)
	GinkgoWriter.Printf("  E2E_CEPH_STORAGE_CLASS:           %q\n", suiteCfg.cephStorageClass)
	GinkgoWriter.Printf("  E2E_PVC_SIZE:                     %q\n", suiteCfg.pvcSize)
	GinkgoWriter.Printf("  E2E_ROOK_NAMESPACE:               %q\n", suiteCfg.rook.Namespace)
	GinkgoWriter.Printf("  E2E_ROOK_OSD_STORAGE_CLASS:       %q\n", suiteCfg.rook.OSDStorageClass)
	GinkgoWriter.Printf("  E2E_ROOK_OSD_COUNT:               %d\n", suiteCfg.rook.OSDCount)
	GinkgoWriter.Printf("  E2E_ROOK_OSD_SIZE:                %q\n", suiteCfg.rook.OSDSize)
	GinkgoWriter.Printf("  E2E_ROOK_CEPH_IMAGE:              %q\n", suiteCfg.rook.CephImage)
	GinkgoWriter.Printf("  E2E_ROOK_CLUSTER_READY_TIMEOUT:   %s\n", suiteCfg.rook.ClusterReadyTO)

	ensureNestedTestCluster()

	var err error
	suiteRestCfg = suiteClusterResources.Kubeconfig
	suiteK8s, err = newRuntimeClient(suiteRestCfg)
	Expect(err).NotTo(HaveOccurred(), "build controller-runtime client")

	suiteDyn, err = dynamic.NewForConfig(suiteRestCfg)
	Expect(err).NotTo(HaveOccurred(), "build dynamic client")

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()

	By("Bootstrapping Rook/Ceph + csi-ceph CephStorageClass")
	scName, err := bootstrapCeph(ctx, suiteRestCfg, suiteCfg)
	Expect(err).NotTo(HaveOccurred(), "ceph.EnsureCephStorageClass failed")
	if suiteCfg.cephStorageClass == "" {
		suiteCfg.cephStorageClass = scName
	}

	By("Ensuring the test namespace exists")
	Expect(ensureNamespace(ctx, suiteK8s, suiteCfg.namespace)).To(Succeed())

	By("Snapshotting ModuleConfig csi-ceph.spec.settings.msCrcData")
	originalMsCrcData, originalMsCrcDataFound, err = getModuleConfigSetting(ctx, suiteDyn, moduleConfigName, "msCrcData")
	Expect(err).NotTo(HaveOccurred(), "ModuleConfig %q must exist", moduleConfigName)
}

func cleanupSuite() {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()

	var cleanupErrors []string
	defer func() {
		cleanupNestedTestCluster()
		if len(cleanupErrors) > 0 {
			Fail("suite cleanup failed:\n- " + strings.Join(cleanupErrors, "\n- "))
		}
	}()

	if suiteDyn != nil {
		By("Restoring ModuleConfig msCrcData")
		var err error
		if originalMsCrcDataFound {
			err = setModuleConfigSetting(ctx, suiteDyn, moduleConfigName, "msCrcData", originalMsCrcData, false)
		} else {
			err = setModuleConfigSetting(ctx, suiteDyn, moduleConfigName, "msCrcData", nil, true)
		}
		if err != nil {
			GinkgoWriter.Printf("    warning: restore ModuleConfig msCrcData failed: %v\n", err)
			cleanupErrors = append(cleanupErrors, "restore ModuleConfig msCrcData: "+err.Error())
		}
	}

	if suiteRestCfg != nil {
		By("Resetting server-side CRC override to the Ceph default")
		if err := testkit.ResetServerCRCToDefault(ctx, suiteRestCfg, suiteCfg.rook.Namespace); err != nil {
			GinkgoWriter.Printf("    warning: ResetServerCRCToDefault failed: %v\n", err)
			cleanupErrors = append(cleanupErrors, "reset server-side CRC: "+err.Error())
		}

		By("Deleting the Ceph stack created for the suite")
		if err := testkit.TeardownCephStorageClass(ctx, suiteRestCfg, resolveCephSCConfig(suiteCfg)); err != nil {
			GinkgoWriter.Printf("    warning: TeardownCephStorageClass failed: %v\n", err)
			cleanupErrors = append(cleanupErrors, "delete Ceph stack: "+err.Error())
		}
	}
}
