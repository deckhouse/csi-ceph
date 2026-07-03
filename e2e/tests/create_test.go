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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	storagekube "github.com/deckhouse/storage-e2e/pkg/kubernetes"
	"github.com/deckhouse/storage-e2e/pkg/testkit"
)

func escRBDName() string    { return suiteCfg.ecName + "-rbd" }
func escCephFSName() string { return suiteCfg.ecName + "-fs" }

// createSpecs registers the happy-path csi-ceph coverage on a shared ElasticCluster
// (the Ceph substrate provided by sds-elastic): bring up the EC, declare RBD and
// CephFS ElasticStorageClasses (each drives csi-ceph into creating a
// CephClusterConnection + CephStorageClass + a core StorageClass), then prove the
// csi-ceph RBD and CephFS drivers by round-tripping data through PVC-backed Pods.
// Specs run in registration order inside the root Ordered container (see
// csi_ceph_suite_test.go).
func createSpecs() {
	It("brings up the shared ElasticCluster (Rook Ceph substrate) and waits for Ready", func() {
		ctx, cancel := context.WithTimeout(context.Background(), suiteCfg.ecReadyTimeout+10*time.Minute)
		defer cancel()

		By("Applying the ElasticCluster and waiting for the aggregate Ready condition")
		_, err := testkit.EnsureElasticCluster(ctx, suiteRestCfg, testkit.ElasticClusterConfig{
			Name:                           suiteCfg.ecName,
			NodeSelectorMatchLabels:        ecNodeSelector(),
			BlockDeviceSelectorMatchLabels: ecBDSelector(),
			NetworkPublic:                  suiteCfg.networkPublic,
			NetworkCluster:                 suiteCfg.networkCluster,
			ReadyTimeout:                   suiteCfg.ecReadyTimeout,
		})
		Expect(err).NotTo(HaveOccurred(), "ElasticCluster %s did not reach Ready", suiteCfg.ecName)
	})

	It("declares an RBD ElasticStorageClass and materialises the csi-ceph CephStorageClass + core StorageClass", func() {
		ctx, cancel := context.WithTimeout(context.Background(), suiteCfg.escReadyTimeout+5*time.Minute)
		defer cancel()

		_, err := testkit.EnsureElasticStorageClass(ctx, suiteRestCfg, testkit.ElasticStorageClassConfig{
			Name:         escRBDName(),
			ClusterRef:   suiteCfg.ecName,
			Type:         testkit.ElasticStorageClassTypeRBD,
			Replication:  testkit.ElasticReplicationConsistencyAndAvailability,
			ReadyTimeout: suiteCfg.escReadyTimeout,
		})
		Expect(err).NotTo(HaveOccurred(), "RBD ElasticStorageClass %s did not reach Ready", escRBDName())

		assertCsiCephWired(ctx, escRBDName())
	})

	It("declares a CephFS ElasticStorageClass and materialises the csi-ceph CephStorageClass + core StorageClass", func() {
		ctx, cancel := context.WithTimeout(context.Background(), suiteCfg.escReadyTimeout+5*time.Minute)
		defer cancel()

		_, err := testkit.EnsureElasticStorageClass(ctx, suiteRestCfg, testkit.ElasticStorageClassConfig{
			Name:         escCephFSName(),
			ClusterRef:   suiteCfg.ecName,
			Type:         testkit.ElasticStorageClassTypeCephFS,
			Replication:  testkit.ElasticReplicationConsistencyAndAvailability,
			ReadyTimeout: suiteCfg.escReadyTimeout,
		})
		Expect(err).NotTo(HaveOccurred(), "CephFS ElasticStorageClass %s did not reach Ready", escCephFSName())

		assertCsiCephWired(ctx, escCephFSName())
	})

	lifecycleSpecs(driverCase{
		name:        "rbd",
		provisioner: rbdProvisioner,
		scName:      escRBDName,
		accessMode:  corev1.ReadWriteOnce,
	})

	lifecycleSpecs(driverCase{
		name:        "cephfs",
		provisioner: cephfsProvisioner,
		scName:      escCephFSName,
		accessMode:  corev1.ReadWriteMany,
	})
}

// assertCsiCephWired asserts an ElasticStorageClass reached its backend + CSI
// stages and produced the objects csi-ceph is responsible for: the per-cluster
// CephClusterConnection (1:1 with the EC), the per-ESC CephStorageClass (1:1 with
// the ESC), and the core Kubernetes StorageClass csi-ceph materialises from it.
func assertCsiCephWired(ctx context.Context, escName string) {
	GinkgoHelper()

	Expect(waitESCCondition(ctx, escName, storagekube.ElasticStorageClassConditionPoolReady, "True", 2*time.Minute)).
		To(Succeed(), "ESC %s PoolReady should be True", escName)
	Expect(waitESCCondition(ctx, escName, storagekube.ElasticStorageClassConditionCsiStorageClassReady, "True", 2*time.Minute)).
		To(Succeed(), "ESC %s CsiStorageClassReady should be True", escName)

	_, err := suiteDyn.Resource(cephClusterConnectionGVR).Get(ctx, suiteCfg.ecName, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred(), "csi-ceph CephClusterConnection %s should exist", suiteCfg.ecName)

	_, err = suiteDyn.Resource(cephStorageClassGVR).Get(ctx, escName, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred(), "csi-ceph CephStorageClass %s should exist", escName)

	Expect(storageClassExists(ctx, escName)).
		To(Succeed(), "core StorageClass %s should be materialised by csi-ceph", escName)
}

// teardownFixtures tears the suite's ElasticStorageClasses (force, to purge their
// pools) and then the ElasticCluster down after the lifecycle specs have removed
// their PVCs/Pods. Best-effort: every step logs and continues so a partial
// failure never masks the spec results, and the nested-cluster teardown reclaims
// whatever is left.
func teardownFixtures(ctx context.Context) {
	for _, esc := range []string{escRBDName(), escCephFSName()} {
		if err := testkit.TeardownElasticStorageClass(ctx, suiteRestCfg, esc, true, resourceGoneTimeout); err != nil {
			GinkgoWriter.Printf("  warning: ElasticStorageClass %s teardown failed: %v\n", esc, err)
		}
	}

	if err := testkit.TeardownElasticCluster(ctx, suiteRestCfg, suiteCfg.ecName, resourceGoneTimeout); err != nil {
		GinkgoWriter.Printf("  warning: ElasticCluster %s teardown failed: %v\n", suiteCfg.ecName, err)
	}
}
