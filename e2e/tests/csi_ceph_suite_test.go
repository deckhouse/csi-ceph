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
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/dynamic"

	"github.com/deckhouse/storage-e2e/pkg/testkit"
)

// anySpecFailed records whether any spec failed during the run. cleanupSuite
// consults it together with E2E_KEEP_CLUSTER_ON_FAILURE to decide whether to
// skip nested-cluster teardown.
var anySpecFailed bool

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
		// Generous: bringing up the sds-elastic ElasticCluster stands up a full
		// Rook Ceph cluster (mon/mgr/osd + csi-ceph wiring) before the csi-ceph
		// RBD/CephFS coverage even starts.
		suiteConfig.Timeout = 180 * time.Minute
	}
	// The suite shares one expensive ElasticCluster across dependency-ordered
	// specs (EC -> RBD ESC -> RBD round-trip -> CephFS ESC -> CephFS round-trip),
	// so randomization must stay OFF.
	suiteConfig.RandomizeAllSpecs = false
	reporterConfig.Verbose = true
	reporterConfig.ShowNodeEvents = false

	RunSpecs(t, "csi-ceph E2E Suite", suiteConfig, reporterConfig)
}

// The single root Ordered container. Spec registration goes through the
// createSpecs builder called from here so the create-before-round-trip order is
// explicit (per-file top-level Describes would order alphabetically).
var _ = Describe("csi-ceph e2e", Ordered, ContinueOnFailure, func() {
	BeforeAll(prepareSharedState)

	// Dump ESC conditions, csi-ceph CRs, module pods and events on any failure.
	AfterEach(func() {
		if !CurrentSpecReport().Failed() {
			return
		}
		anySpecFailed = true
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		dumpFailedSpecDiagnostics(ctx)
	})

	// Tear the ElasticStorageClasses + ElasticCluster (and their probes) down once
	// all specs have run, before the nested cluster is handed back.
	AfterAll(func() {
		ctx, cancel := context.WithTimeout(context.Background(), resourceGoneTimeout+10*time.Minute)
		defer cancel()
		teardownFixtures(ctx)
	})

	createSpecs()
})

func prepareSuite() {
	suiteCfg = loadConfig()

	GinkgoWriter.Printf("E2E config:\n")
	GinkgoWriter.Printf("  TEST_CLUSTER_CREATE_MODE:  %q\n", os.Getenv("TEST_CLUSTER_CREATE_MODE"))
	GinkgoWriter.Printf("  namespace (TEST_CLUSTER_NAMESPACE): %q\n", suiteCfg.namespace)
	GinkgoWriter.Printf("  E2E_EC_NAME:               %q\n", suiteCfg.ecName)
	GinkgoWriter.Printf("  storage node label:        %s=%s\n", suiteCfg.storageNodeLabelKey, suiteCfg.storageNodeLabelValue)
	GinkgoWriter.Printf("  OSD BlockDevice label:     %s=%s\n", suiteCfg.osdBDLabelKey, suiteCfg.osdBDLabelValue)
	GinkgoWriter.Printf("  EC ready timeout:          %s\n", suiteCfg.ecReadyTimeout)
	GinkgoWriter.Printf("  ESC ready timeout:         %s\n", suiteCfg.escReadyTimeout)
	GinkgoWriter.Printf("  module ready timeout:      %s\n", suiteCfg.moduleReadyTO)
	GinkgoWriter.Printf("  OSD disks per worker:      %d (%s each)\n", suiteCfg.osdDisksPerWorker, suiteCfg.osdDiskSize)
	GinkgoWriter.Printf("  probe image:               %q\n", suiteCfg.probeImage)

	ensureNestedTestCluster()

	var err error
	suiteRestCfg = suiteClusterResources.Kubeconfig
	suiteK8s, err = newRuntimeClient(suiteRestCfg)
	Expect(err).NotTo(HaveOccurred(), "build controller-runtime client")

	suiteDyn, err = dynamic.NewForConfig(suiteRestCfg)
	Expect(err).NotTo(HaveOccurred(), "build dynamic client")

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()

	By("Waiting for the csi-ceph module to become Ready")
	Expect(waitModuleReady(ctx)).To(Succeed(), "csi-ceph module readiness")

	By("Attaching raw OSD VirtualDisks to worker VMs (no-op on the Commander template)")
	attached, err := attachOSDDisks(ctx)
	Expect(err).NotTo(HaveOccurred(), "attach raw OSD disks")

	// Wait for exactly the disks we attached; on the pre-provisioned path
	// (attached==0, e.g. the Commander template) fall back to the >=4 floor.
	minBD := attached
	if minBD <= 0 {
		minBD = fallbackMinOSDBlockDevices
	}

	By("Labelling storage nodes and OSD BlockDevices for the ElasticCluster")
	bds, err := testkit.EnsureElasticOSDBlockDevices(ctx, suiteRestCfg, testkit.ElasticOSDBlockDevicesConfig{
		NodeLabelKey:          suiteCfg.storageNodeLabelKey,
		NodeLabelValue:        suiteCfg.storageNodeLabelValue,
		BlockDeviceLabelKey:   suiteCfg.osdBDLabelKey,
		BlockDeviceLabelValue: suiteCfg.osdBDLabelValue,
		MinBlockDevices:       minBD,
	})
	Expect(err).NotTo(HaveOccurred(), "prepare OSD BlockDevices")
	suiteOSDBlockDevices = bds
	GinkgoWriter.Printf("  prepared %d OSD BlockDevice(s)\n", len(bds))

	By("Ensuring the in-cluster test namespace exists")
	Expect(ensureNamespace(ctx, suiteCfg.namespace)).To(Succeed())
}

// prepareSharedState runs once before the Ordered specs. Clients, node/BD
// labelling and the namespace are already set up in BeforeSuite; this is the
// hook where the specs wire any additional shared fixtures.
func prepareSharedState() {
	GinkgoWriter.Printf("Shared ElasticCluster (Ceph substrate) for this run: %s (namespace %s)\n", suiteCfg.ecName, suiteCfg.namespace)
}

func cleanupSuite() {
	// Keep the nested cluster alive for manual debugging when a spec failed and
	// the operator asked for it. Otherwise tear it down (the only mandatory step;
	// resource-level cleanup is driven by teardownFixtures / the specs).
	if suiteCfg.keepClusterOnFailure && anySpecFailed {
		printKeepClusterBanner()
		return
	}
	cleanupNestedTestCluster()
}

// printKeepClusterBanner tells the operator how to reach the preserved cluster
// after a failure (E2E_KEEP_CLUSTER_ON_FAILURE).
func printKeepClusterBanner() {
	GinkgoWriter.Printf("\n========== E2E_KEEP_CLUSTER_ON_FAILURE: cluster preserved ==========\n")
	GinkgoWriter.Printf("A spec failed and nested-cluster teardown was SKIPPED for debugging.\n")
	GinkgoWriter.Printf("  namespace (PVC/Pod + base VM ns): %s\n", suiteCfg.namespace)
	GinkgoWriter.Printf("  ElasticCluster:                   %s\n", suiteCfg.ecName)
	if suiteClusterResources != nil && suiteClusterResources.KubeconfigPath != "" {
		GinkgoWriter.Printf("  kubeconfig (export KUBECONFIG):   %s\n", suiteClusterResources.KubeconfigPath)
	}
	GinkgoWriter.Printf("Remember to delete the VMs / nested cluster manually when finished.\n")
	GinkgoWriter.Printf("====================================================================\n")
}
