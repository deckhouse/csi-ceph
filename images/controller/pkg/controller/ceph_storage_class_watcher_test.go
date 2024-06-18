/*
Copyright 2024 Flant JSC

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

package controller_test

import (
	"context"
	v1alpha1 "d8-controller/api/v1alpha1"
	"d8-controller/pkg/controller"
	"d8-controller/pkg/internal"
	"d8-controller/pkg/logger"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe(controller.CephStorageClassCtrlName, func() {
	const (
		controllerNamespace = "test-namespace"
		nameForCephSC       = "example-ceph-fs"
		nameForRBDSC        = "example-rbd"
	)
	var (
		ctx = context.Background()
		cl  = NewFakeClient()
		log = logger.Logger{}

		clusterConnectionName = "ceph-connection"
		clusterID1            = "clusterID1"
		reclaimPolicyDelete   = "Delete"
		reclaimPolicyRetain   = "Retain"
		storageTypeCephFS     = "cephfs"
		storageTypeRBD        = "rbd"
		fsName                = "myfs"
		pool                  = "mypool"
		// defaultFSType         = "ext4"
	)

	It("Create_ceph_sc_with_not_existing_ceph_connection", func() {
		cephSCtemplate := generateCephStorageClass(CephStorageClassConfig{
			Name:                  nameForCephSC,
			ClusterConnectionName: "not-existing",
			ReclaimPolicy:         reclaimPolicyDelete,
			Type:                  storageTypeCephFS,
			CephFS: &CephFSConfig{
				FSName: fsName,
				Pool:   pool,
			},
		})

		err := cl.Create(ctx, cephSCtemplate)
		Expect(err).NotTo(HaveOccurred())

		csc := &v1alpha1.CephStorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForCephSC}, csc)
		Expect(err).NotTo(HaveOccurred())

		Expect(csc).NotTo(BeNil())
		Expect(csc.Name).To(Equal(nameForCephSC))
		Expect(csc.Finalizers).To(HaveLen(0))

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())

		shouldRequeue, _, err := controller.RunStorageClassEventReconcile(ctx, cl, log, scList, csc, controllerNamespace)
		Expect(err).To(HaveOccurred())
		Expect(shouldRequeue).To(BeTrue())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForCephSC}, csc)
		Expect(err).NotTo(HaveOccurred())
		Expect(csc.Finalizers).To(HaveLen(1))
		Expect(csc.Finalizers).To(ContainElement(controller.CephStorageClassControllerFinalizerName))

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForCephSC}, sc)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())

		csc.Finalizers = nil
		err = cl.Update(ctx, csc)
		Expect(err).NotTo(HaveOccurred())
		err = cl.Delete(ctx, csc)
		Expect(err).NotTo(HaveOccurred())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForCephSC}, csc)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())
	})

	It("Create_ceph_cluster_connection", func() {
		cephClusterConnection := &v1alpha1.CephClusterConnection{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterConnectionName,
			},
			Spec: v1alpha1.CephClusterConnectionSpec{
				ClusterID: clusterID1,
				Monitors:  []string{"mon1", "mon2", "mon3"},
				UserID:    "admin",
				UserKey:   "key",
			},
		}

		err := cl.Create(ctx, cephClusterConnection)
		Expect(err).NotTo(HaveOccurred())

	})

	It("Create_ceph_sc_with_cephfs", func() {
		cephSCtemplate := generateCephStorageClass(CephStorageClassConfig{
			Name:                  nameForCephSC,
			ClusterConnectionName: clusterConnectionName,
			ReclaimPolicy:         reclaimPolicyDelete,
			Type:                  storageTypeCephFS,
			CephFS: &CephFSConfig{
				FSName: fsName,
				Pool:   pool,
			},
		})

		err := cl.Create(ctx, cephSCtemplate)
		Expect(err).NotTo(HaveOccurred())

		csc := &v1alpha1.CephStorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForCephSC}, csc)
		Expect(err).NotTo(HaveOccurred())

		Expect(csc).NotTo(BeNil())
		Expect(csc.Name).To(Equal(nameForCephSC))
		Expect(csc.Finalizers).To(HaveLen(0))

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())

		shouldRequeue, _, err := controller.RunStorageClassEventReconcile(ctx, cl, log, scList, csc, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForCephSC}, csc)
		Expect(err).NotTo(HaveOccurred())
		Expect(csc.Finalizers).To(HaveLen(1))
		Expect(csc.Finalizers).To(ContainElement(controller.CephStorageClassControllerFinalizerName))

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForCephSC}, sc)
		Expect(err).NotTo(HaveOccurred())
		performStandardChecksForCephSc(sc, nameForCephSC, controllerNamespace, CephStorageClassConfig{
			ClusterConnectionName: clusterConnectionName,
			ReclaimPolicy:         reclaimPolicyDelete,
			Type:                  storageTypeCephFS,
			CephFS: &CephFSConfig{
				FSName: fsName,
				Pool:   pool,
			},
		})
	})

	It("Update_ceph_sc_with_cephfs", func() {
		csc := &v1alpha1.CephStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForCephSC}, csc)
		Expect(err).NotTo(HaveOccurred())

		csc.Spec.ReclaimPolicy = reclaimPolicyRetain

		err = cl.Update(ctx, csc)
		Expect(err).NotTo(HaveOccurred())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForCephSC}, csc)
		Expect(err).NotTo(HaveOccurred())

		Expect(csc).NotTo(BeNil())
		Expect(csc.Name).To(Equal(nameForCephSC))
		Expect(csc.Finalizers).To(HaveLen(1))
		Expect(csc.Finalizers).To(ContainElement(controller.CephStorageClassControllerFinalizerName))

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())

		shouldRequeue, _, err := controller.RunStorageClassEventReconcile(ctx, cl, log, scList, csc, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForCephSC}, csc)
		Expect(err).NotTo(HaveOccurred())
		Expect(csc.Finalizers).To(HaveLen(1))
		Expect(csc.Finalizers).To(ContainElement(controller.CephStorageClassControllerFinalizerName))

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForCephSC}, sc)
		Expect(err).NotTo(HaveOccurred())
		performStandardChecksForCephSc(sc, nameForCephSC, controllerNamespace, CephStorageClassConfig{
			ClusterConnectionName: clusterConnectionName,
			ReclaimPolicy:         reclaimPolicyRetain,
			Type:                  storageTypeCephFS,
			CephFS: &CephFSConfig{
				FSName: fsName,
				Pool:   pool,
			},
		})
	})

	It("Remove_ceph_sc_with_cephfs", func() {
		csc := &v1alpha1.CephStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForCephSC}, csc)
		Expect(err).NotTo(HaveOccurred())

		err = cl.Delete(ctx, csc)
		Expect(err).NotTo(HaveOccurred())

		csc = &v1alpha1.CephStorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForCephSC}, csc)
		Expect(err).NotTo(HaveOccurred())

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())

		shouldRequeue, _, err := controller.RunStorageClassEventReconcile(ctx, cl, log, scList, csc, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForCephSC}, csc)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForCephSC}, sc)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())
	})

	It("Create_ceph_sc_with_rbd", func() {
		cephSCtemplate := generateCephStorageClass(CephStorageClassConfig{
			Name:                  nameForRBDSC,
			ClusterConnectionName: clusterConnectionName,
			ReclaimPolicy:         reclaimPolicyDelete,
			Type:                  storageTypeRBD,
			RBD: &RBDConfig{
				DefaultFSType: "ext4",
				Pool:          pool,
			},
		})

		err := cl.Create(ctx, cephSCtemplate)
		Expect(err).NotTo(HaveOccurred())

		csc := &v1alpha1.CephStorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForRBDSC}, csc)
		Expect(err).NotTo(HaveOccurred())

		Expect(csc).NotTo(BeNil())
		Expect(csc.Name).To(Equal(nameForRBDSC))
		Expect(csc.Finalizers).To(HaveLen(0))

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())

		shouldRequeue, _, err := controller.RunStorageClassEventReconcile(ctx, cl, log, scList, csc, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForRBDSC}, csc)
		Expect(err).NotTo(HaveOccurred())
		Expect(csc.Finalizers).To(HaveLen(1))
		Expect(csc.Finalizers).To(ContainElement(controller.CephStorageClassControllerFinalizerName))

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForRBDSC}, sc)
		Expect(err).NotTo(HaveOccurred())
		performStandardChecksForCephSc(sc, nameForRBDSC, controllerNamespace, CephStorageClassConfig{
			ClusterConnectionName: clusterConnectionName,
			ReclaimPolicy:         reclaimPolicyDelete,
			Type:                  storageTypeRBD,
			RBD: &RBDConfig{
				DefaultFSType: "ext4",
				Pool:          pool,
			},
		})
	})

	It("Update_ceph_sc_with_rbd", func() {
		csc := &v1alpha1.CephStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForRBDSC}, csc)
		Expect(err).NotTo(HaveOccurred())

		csc.Spec.ReclaimPolicy = reclaimPolicyRetain

		err = cl.Update(ctx, csc)
		Expect(err).NotTo(HaveOccurred())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForRBDSC}, csc)
		Expect(err).NotTo(HaveOccurred())

		Expect(csc).NotTo(BeNil())
		Expect(csc.Name).To(Equal(nameForRBDSC))
		Expect(csc.Finalizers).To(HaveLen(1))
		Expect(csc.Finalizers).To(ContainElement(controller.CephStorageClassControllerFinalizerName))

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())

		shouldRequeue, _, err := controller.RunStorageClassEventReconcile(ctx, cl, log, scList, csc, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForRBDSC}, csc)
		Expect(err).NotTo(HaveOccurred())
		Expect(csc.Finalizers).To(HaveLen(1))
		Expect(csc.Finalizers).To(ContainElement(controller.CephStorageClassControllerFinalizerName))

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForRBDSC}, sc)
		Expect(err).NotTo(HaveOccurred())
		performStandardChecksForCephSc(sc, nameForRBDSC, controllerNamespace, CephStorageClassConfig{
			ClusterConnectionName: clusterConnectionName,
			ReclaimPolicy:         reclaimPolicyRetain,
			Type:                  storageTypeRBD,
			RBD: &RBDConfig{
				DefaultFSType: "ext4",
				Pool:          pool,
			},
		})
	})

	It("Remove_ceph_sc_with_rbd", func() {
		csc := &v1alpha1.CephStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForRBDSC}, csc)
		Expect(err).NotTo(HaveOccurred())

		err = cl.Delete(ctx, csc)
		Expect(err).NotTo(HaveOccurred())

		csc = &v1alpha1.CephStorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForRBDSC}, csc)
		Expect(err).NotTo(HaveOccurred())

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())

		shouldRequeue, _, err := controller.RunStorageClassEventReconcile(ctx, cl, log, scList, csc, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForRBDSC}, csc)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForRBDSC}, sc)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())
	})

	It("Create_ceph_sc_when_sc_with_another_provisioner_exists", func() {
		sc := &v1.StorageClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: nameForRBDSC,
			},
			Provisioner: "test-provisioner",
		}

		err := cl.Create(ctx, sc)
		Expect(err).NotTo(HaveOccurred())

		cephSCtemplate := generateCephStorageClass(CephStorageClassConfig{
			Name:                  nameForRBDSC,
			ClusterConnectionName: clusterConnectionName,
			ReclaimPolicy:         reclaimPolicyDelete,
			Type:                  storageTypeCephFS,
			CephFS: &CephFSConfig{
				FSName: fsName,
				Pool:   pool,
			},
		})

		err = cl.Create(ctx, cephSCtemplate)
		Expect(err).NotTo(HaveOccurred())

		csc := &v1alpha1.CephStorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForRBDSC}, csc)
		Expect(err).NotTo(HaveOccurred())

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())

		shouldRequeue, _, err := controller.RunStorageClassEventReconcile(ctx, cl, log, scList, csc, controllerNamespace)
		Expect(err).To(HaveOccurred())
		Expect(shouldRequeue).To(BeTrue())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForRBDSC}, sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(sc.Provisioner).To(Equal("test-provisioner"))
		Expect(sc.Finalizers).To(HaveLen(0))
		Expect(sc.Labels).To(HaveLen(0))
	})

	It("Update_ceph_sc_when_sc_with_another_provisioner_exists", func() {
		csc := &v1alpha1.CephStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForRBDSC}, csc)
		Expect(err).NotTo(HaveOccurred())

		csc.Spec.ReclaimPolicy = reclaimPolicyRetain

		err = cl.Update(ctx, csc)
		Expect(err).NotTo(HaveOccurred())

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())

		shouldRequeue, _, err := controller.RunStorageClassEventReconcile(ctx, cl, log, scList, csc, controllerNamespace)
		Expect(err).To(HaveOccurred())
		Expect(shouldRequeue).To(BeTrue())

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForRBDSC}, sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(sc.Provisioner).To(Equal("test-provisioner"))
		Expect(sc.Finalizers).To(HaveLen(0))
		Expect(sc.Labels).To(HaveLen(0))
	})

	It("Remove_ceph_sc_when_sc_with_another_provisioner_exists", func() {
		csc := &v1alpha1.CephStorageClass{}
		err := cl.Get(ctx, client.ObjectKey{Name: nameForRBDSC}, csc)
		Expect(err).NotTo(HaveOccurred())

		err = cl.Delete(ctx, csc)
		Expect(err).NotTo(HaveOccurred())

		csc = &v1alpha1.CephStorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForRBDSC}, csc)
		Expect(err).NotTo(HaveOccurred())
		Expect(csc.Finalizers).To(HaveLen(1))
		Expect(csc.Finalizers).To(ContainElement(controller.CephStorageClassControllerFinalizerName))
		Expect(csc.DeletionTimestamp).NotTo(BeNil())

		scList := &v1.StorageClassList{}
		err = cl.List(ctx, scList)
		Expect(err).NotTo(HaveOccurred())

		shouldRequeue, _, err := controller.RunStorageClassEventReconcile(ctx, cl, log, scList, csc, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		err = cl.Get(ctx, client.ObjectKey{Name: nameForRBDSC}, csc)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())

		sc := &v1.StorageClass{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForRBDSC}, sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(sc.Provisioner).To(Equal("test-provisioner"))
		Expect(sc.Finalizers).To(HaveLen(0))
		Expect(sc.Labels).To(HaveLen(0))
	})

	It("Remove_ceph_cluster_connection", func() {

		cephClusterConnection := &v1alpha1.CephClusterConnection{}
		err := cl.Get(ctx, client.ObjectKey{Name: clusterConnectionName}, cephClusterConnection)
		Expect(err).NotTo(HaveOccurred())

		err = cl.Delete(ctx, cephClusterConnection)
		Expect(err).NotTo(HaveOccurred())

		cephClusterConnection = &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: clusterConnectionName}, cephClusterConnection)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())
	})

})

type CephStorageClassConfig struct {
	Name                  string
	ClusterConnectionName string
	ReclaimPolicy         string
	Type                  string
	CephFS                *CephFSConfig
	RBD                   *RBDConfig
}

type CephFSConfig struct {
	FSName string
	Pool   string
}

type RBDConfig struct {
	DefaultFSType string
	Pool          string
}

func generateCephStorageClass(cfg CephStorageClassConfig) *v1alpha1.CephStorageClass {
	var cephFS *v1alpha1.CephStorageClassCephFS
	var rbd *v1alpha1.CephStorageClassRBD

	if cfg.CephFS != nil {
		cephFS = &v1alpha1.CephStorageClassCephFS{
			FSName: cfg.CephFS.FSName,
			Pool:   cfg.CephFS.Pool,
		}
	}

	if cfg.RBD != nil {
		rbd = &v1alpha1.CephStorageClassRBD{
			DefaultFSType: cfg.RBD.DefaultFSType,
			Pool:          cfg.RBD.Pool,
		}
	}

	return &v1alpha1.CephStorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: cfg.Name,
		},
		Spec: v1alpha1.CephStorageClassSpec{
			ClusterConnectionName: cfg.ClusterConnectionName,
			ReclaimPolicy:         cfg.ReclaimPolicy,
			Type:                  cfg.Type,
			CephFS:                cephFS,
			RBD:                   rbd,
		},
	}
}

func performStandardChecksForCephSc(sc *v1.StorageClass, nameForTestResource, controllerNamespace string, cfg CephStorageClassConfig) {
	Expect(sc).NotTo(BeNil())
	Expect(sc.Name).To(Equal(nameForTestResource))
	Expect(sc.Finalizers).To(HaveLen(1))
	Expect(sc.Finalizers).To(ContainElement(controller.CephStorageClassControllerFinalizerName))
	Expect(sc.Provisioner).To(Equal(controller.GetStorageClassProvisioner(cfg.Type)))
	Expect(*sc.ReclaimPolicy).To(Equal(corev1.PersistentVolumeReclaimPolicy(cfg.ReclaimPolicy)))
	Expect(*sc.VolumeBindingMode).To(Equal(v1.VolumeBindingImmediate))
	Expect(*sc.AllowVolumeExpansion).To(BeTrue())
	Expect(sc.Parameters).To(HaveKeyWithValue("csi.storage.k8s.io/provisioner-secret-name", internal.CephClusterConnectionSecretPrefix+cfg.ClusterConnectionName))
	Expect(sc.Parameters).To(HaveKeyWithValue("csi.storage.k8s.io/provisioner-secret-namespace", controllerNamespace))

	if cfg.Type == "cephfs" {
		Expect(sc.Parameters).To(HaveKeyWithValue("fsName", cfg.CephFS.FSName))
		Expect(sc.Parameters).To(HaveKeyWithValue("pool", cfg.CephFS.Pool))
	} else if cfg.Type == "rbd" {
		Expect(sc.Parameters).To(HaveKeyWithValue("pool", cfg.RBD.Pool))
		Expect(sc.Parameters).To(HaveKeyWithValue("csi.storage.k8s.io/fstype", cfg.RBD.DefaultFSType))
	}
}