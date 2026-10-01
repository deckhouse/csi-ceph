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

package controller_test

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/controller"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/internal"
	"github.com/deckhouse/sds-common-lib/logger"
)

var _ = Describe("CephClusterConnections sharing a clusterID", func() {
	const (
		controllerNamespace = "test-namespace"
		clusterID           = "shared-cluster"
		subvolumeGroup      = "csi-group"

		// Named so that picking by name alone would hand each driver the
		// secret of the other one.
		rbdConnectionName    = "b-rbd"
		cephFSConnectionName = "a-cephfs"
	)

	var (
		ctx = context.Background()
		log = logger.NewNop()
		cl  client.Client
	)

	newConnection := func(name, userID, subvolumeGroup string) *v1alpha1.CephClusterConnection {
		connection := &v1alpha1.CephClusterConnection{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: v1alpha1.CephClusterConnectionSpec{
				ClusterID: clusterID,
				Monitors:  []string{"mon1", "mon2", "mon3"},
				UserID:    userID,
				UserKey:   userID + "-key",
			},
		}
		if subvolumeGroup != "" {
			connection.Spec.CephFS = &v1alpha1.CephClusterConnectionSpecCephFS{SubvolumeGroup: subvolumeGroup}
		}
		return connection
	}

	newStorageClass := func(name, storageClassType, connectionName string) *v1alpha1.CephStorageClass {
		return &v1alpha1.CephStorageClass{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: v1alpha1.CephStorageClassSpec{
				ClusterConnectionName: connectionName,
				Type:                  storageClassType,
			},
		}
	}

	newPV := func(name, driver, pvClusterID, connectionName string, withControllerPublishSecret bool) *corev1.PersistentVolume {
		secretRef := &corev1.SecretReference{Name: internal.CephClusterConnectionSecretPrefix + connectionName, Namespace: controllerNamespace}
		pv := &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{
						Driver:             driver,
						VolumeHandle:       name,
						VolumeAttributes:   map[string]string{"clusterID": pvClusterID},
						NodeStageSecretRef: secretRef,
					},
				},
			},
		}
		if withControllerPublishSecret {
			pv.Spec.CSI.ControllerPublishSecretRef = secretRef
		}
		return pv
	}

	reconcile := func(name string) {
		connection := &v1alpha1.CephClusterConnection{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: name}, connection)).To(Succeed())
		shouldRequeue, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, connection, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())
	}

	clusterConfigs := func() []v1alpha1.ClusterConfig {
		configMap := &corev1.ConfigMap{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: internal.CSICephConfigMapName, Namespace: controllerNamespace}, configMap)).To(Succeed())
		var configs []v1alpha1.ClusterConfig
		Expect(json.Unmarshal([]byte(configMap.Data["config.json"]), &configs)).To(Succeed())
		return configs
	}

	secretOf := func(connectionName string) v1alpha1.SecretReference {
		return v1alpha1.SecretReference{Name: internal.CephClusterConnectionSecretPrefix + connectionName, Namespace: controllerNamespace}
	}

	expectEntry := func(rbdConnection, cephFSConnection, expectedSubvolumeGroup string) {
		configs := clusterConfigs()
		Expect(configs).To(HaveLen(1))
		Expect(configs[0].ClusterID).To(Equal(clusterID))
		Expect(configs[0].RBD.ControllerPublishSecretRef).To(Equal(secretOf(rbdConnection)))
		Expect(configs[0].CephFS.ControllerPublishSecretRef).To(Equal(secretOf(cephFSConnection)))
		Expect(configs[0].CephFS.SubvolumeGroup).To(Equal(expectedSubvolumeGroup))
	}

	BeforeEach(func() {
		cl = NewFakeClient()
		Expect(cl.Create(ctx, newConnection(rbdConnectionName, "rbd-user", ""))).To(Succeed())
		Expect(cl.Create(ctx, newConnection(cephFSConnectionName, "cephfs-user", subvolumeGroup))).To(Succeed())
	})

	DescribeTable("gives each driver the connection its CephStorageClasses use, whatever the reconcile order",
		func(order ...string) {
			Expect(cl.Create(ctx, newStorageClass("rbd", v1alpha1.CephStorageClassTypeRBD, rbdConnectionName))).To(Succeed())
			Expect(cl.Create(ctx, newStorageClass("cephfs", v1alpha1.CephStorageClassTypeCephFS, cephFSConnectionName))).To(Succeed())

			for _, name := range order {
				reconcile(name)
				expectEntry(rbdConnectionName, cephFSConnectionName, subvolumeGroup)
			}
		},
		Entry("RBD connection last", cephFSConnectionName, rbdConnectionName),
		Entry("CephFS connection last", rbdConnectionName, cephFSConnectionName),
		Entry("reconciled over and over", rbdConnectionName, cephFSConnectionName, rbdConnectionName, cephFSConnectionName),
	)

	It("falls back to the first connection by name when no CephStorageClass refers to any", func() {
		reconcile(rbdConnectionName)
		reconcile(cephFSConnectionName)
		reconcile(rbdConnectionName)

		expectEntry(cephFSConnectionName, cephFSConnectionName, subvolumeGroup)
	})

	It("counts a CephStorageClass that is being deleted", func() {
		storageClass := newStorageClass("rbd", v1alpha1.CephStorageClassTypeRBD, rbdConnectionName)
		storageClass.Finalizers = []string{controller.CephStorageClassControllerFinalizerName}
		Expect(cl.Create(ctx, storageClass)).To(Succeed())
		Expect(cl.Delete(ctx, storageClass)).To(Succeed())

		reconcile(cephFSConnectionName)

		expectEntry(rbdConnectionName, cephFSConnectionName, subvolumeGroup)
	})

	It("keeps the entry for the remaining connection when one of them is deleted", func() {
		Expect(cl.Create(ctx, newStorageClass("rbd", v1alpha1.CephStorageClassTypeRBD, rbdConnectionName))).To(Succeed())
		Expect(cl.Create(ctx, newStorageClass("cephfs", v1alpha1.CephStorageClassTypeCephFS, cephFSConnectionName))).To(Succeed())
		reconcile(rbdConnectionName)
		reconcile(cephFSConnectionName)

		connection := &v1alpha1.CephClusterConnection{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: rbdConnectionName}, connection)).To(Succeed())
		Expect(cl.Delete(ctx, connection)).To(Succeed())
		reconcile(rbdConnectionName)

		expectEntry(cephFSConnectionName, cephFSConnectionName, subvolumeGroup)

		Expect(cl.Get(ctx, client.ObjectKey{Name: cephFSConnectionName}, connection)).To(Succeed())
		Expect(cl.Delete(ctx, connection)).To(Succeed())
		reconcile(cephFSConnectionName)

		Expect(clusterConfigs()).To(BeEmpty())
	})

	It("leaves out a connection whose spec is invalid", func() {
		invalid := newConnection("0-invalid", "", "")
		Expect(cl.Create(ctx, invalid)).To(Succeed())

		reconcile(rbdConnectionName)

		expectEntry(cephFSConnectionName, cephFSConnectionName, subvolumeGroup)
	})

	It("falls back to the first connection by name once the last RBD CephStorageClass is gone", func() {
		storageClass := newStorageClass("rbd", v1alpha1.CephStorageClassTypeRBD, rbdConnectionName)
		Expect(cl.Create(ctx, storageClass)).To(Succeed())
		reconcile(rbdConnectionName)
		expectEntry(rbdConnectionName, cephFSConnectionName, subvolumeGroup)

		Expect(cl.Delete(ctx, storageClass)).To(Succeed())
		reconcile(cephFSConnectionName)

		expectEntry(cephFSConnectionName, cephFSConnectionName, subvolumeGroup)
	})

	It("keeps the connection of the RBD volumes provisioned without a controller-publish secret, with no CephStorageClass left", func() {
		Expect(cl.Create(ctx, newPV("legacy-rbd", controller.CephStorageClassRBDProvisioner, clusterID, rbdConnectionName, false))).To(Succeed())

		reconcile(cephFSConnectionName)

		expectEntry(rbdConnectionName, cephFSConnectionName, subvolumeGroup)
	})

	It("prefers the connection of such RBD volumes to the one a CephStorageClass refers to", func() {
		Expect(cl.Create(ctx, newStorageClass("rbd", v1alpha1.CephStorageClassTypeRBD, cephFSConnectionName))).To(Succeed())
		Expect(cl.Create(ctx, newPV("legacy-rbd", controller.CephStorageClassRBDProvisioner, clusterID, rbdConnectionName, false))).To(Succeed())

		reconcile(cephFSConnectionName)

		expectEntry(rbdConnectionName, cephFSConnectionName, subvolumeGroup)
	})

	It("does not count RBD volumes that carry their own controller-publish secret", func() {
		Expect(cl.Create(ctx, newPV("new-rbd", controller.CephStorageClassRBDProvisioner, clusterID, rbdConnectionName, true))).To(Succeed())

		reconcile(cephFSConnectionName)

		expectEntry(cephFSConnectionName, cephFSConnectionName, subvolumeGroup)
	})

	It("does not count volumes of another cluster", func() {
		Expect(cl.Create(ctx, newPV("other-rbd", controller.CephStorageClassRBDProvisioner, "other-cluster", rbdConnectionName, false))).To(Succeed())

		reconcile(cephFSConnectionName)

		expectEntry(cephFSConnectionName, cephFSConnectionName, subvolumeGroup)
	})

	It("keeps the connection of the CephFS volumes, whose subvolume group is always read from config.json", func() {
		Expect(cl.Create(ctx, newStorageClass("cephfs", v1alpha1.CephStorageClassTypeCephFS, rbdConnectionName))).To(Succeed())
		Expect(cl.Create(ctx, newPV("cephfs", controller.CephStorageClassCephFSProvisioner, clusterID, cephFSConnectionName, false))).To(Succeed())

		reconcile(rbdConnectionName)

		expectEntry(cephFSConnectionName, cephFSConnectionName, subvolumeGroup)
	})

	It("leaves the entry of another clusterID alone", func() {
		other := newConnection("0-other", "other-user", "")
		other.Spec.ClusterID = "other-cluster"
		Expect(cl.Create(ctx, other)).To(Succeed())
		Expect(cl.Create(ctx, newStorageClass("rbd", v1alpha1.CephStorageClassTypeRBD, rbdConnectionName))).To(Succeed())

		reconcile(rbdConnectionName)
		reconcile("0-other")
		reconcile(cephFSConnectionName)

		configs := clusterConfigs()
		Expect(configs).To(HaveLen(2))
		for _, config := range configs {
			switch config.ClusterID {
			case clusterID:
				Expect(config.RBD.ControllerPublishSecretRef).To(Equal(secretOf(rbdConnectionName)))
				Expect(config.CephFS.ControllerPublishSecretRef).To(Equal(secretOf(cephFSConnectionName)))
			case "other-cluster":
				Expect(config.RBD.ControllerPublishSecretRef).To(Equal(secretOf("0-other")))
				Expect(config.CephFS.ControllerPublishSecretRef).To(Equal(secretOf("0-other")))
			default:
				Fail("unexpected clusterID " + config.ClusterID)
			}
		}
	})

	It("keeps a connection being deleted, its secret and its entry while a volume uses the secret", func() {
		Expect(cl.Create(ctx, newStorageClass("rbd", v1alpha1.CephStorageClassTypeRBD, rbdConnectionName))).To(Succeed())
		reconcile(rbdConnectionName)
		reconcile(cephFSConnectionName)
		pv := newPV("new-rbd", controller.CephStorageClassRBDProvisioner, clusterID, rbdConnectionName, true)
		Expect(cl.Create(ctx, pv)).To(Succeed())

		connection := &v1alpha1.CephClusterConnection{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: rbdConnectionName}, connection)).To(Succeed())
		Expect(cl.Delete(ctx, connection)).To(Succeed())
		Expect(cl.Get(ctx, client.ObjectKey{Name: rbdConnectionName}, connection)).To(Succeed())

		shouldRequeue, msg, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, connection, controllerNamespace)
		Expect(err).To(HaveOccurred())
		Expect(shouldRequeue).To(BeTrue())
		Expect(msg).To(ContainSubstring("new-rbd"))

		expectEntry(rbdConnectionName, cephFSConnectionName, subvolumeGroup)
		secret := &corev1.Secret{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: internal.CephClusterConnectionSecretPrefix + rbdConnectionName, Namespace: controllerNamespace}, secret)).To(Succeed())
		Expect(cl.Get(ctx, client.ObjectKey{Name: rbdConnectionName}, connection)).To(Succeed())
		Expect(connection.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))

		Expect(cl.Delete(ctx, pv)).To(Succeed())
		shouldRequeue, _, err = controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, connection, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldRequeue).To(BeFalse())

		expectEntry(cephFSConnectionName, cephFSConnectionName, subvolumeGroup)
		err = cl.Get(ctx, client.ObjectKey{Name: internal.CephClusterConnectionSecretPrefix + rbdConnectionName, Namespace: controllerNamespace}, secret)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("keeps the entry of the last connection being deleted while a volume uses its secret", func() {
		reconcile(rbdConnectionName)
		reconcile(cephFSConnectionName)
		Expect(cl.Create(ctx, newPV("new-rbd", controller.CephStorageClassRBDProvisioner, clusterID, rbdConnectionName, true))).To(Succeed())

		for _, name := range []string{cephFSConnectionName, rbdConnectionName} {
			connection := &v1alpha1.CephClusterConnection{}
			Expect(cl.Get(ctx, client.ObjectKey{Name: name}, connection)).To(Succeed())
			Expect(cl.Delete(ctx, connection)).To(Succeed())
			Expect(cl.Get(ctx, client.ObjectKey{Name: name}, connection)).To(Succeed())
			_, _, _ = controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, connection, controllerNamespace)
		}

		expectEntry(rbdConnectionName, rbdConnectionName, "")
	})

	It("keeps the connection whose subvolume group the CephFS subvolumes lie in, whichever connection created them", func() {
		// The CephFS class of b-rbd created this volume while the entry held
		// the group of a-cephfs, so the subvolume lies there.
		Expect(cl.Create(ctx, newStorageClass("cephfs", v1alpha1.CephStorageClassTypeCephFS, rbdConnectionName))).To(Succeed())
		pv := newPV("cephfs", controller.CephStorageClassCephFSProvisioner, clusterID, rbdConnectionName, false)
		pv.Spec.CSI.VolumeAttributes["subvolumePath"] = "/volumes/" + subvolumeGroup + "/csi-vol-1/0b6e1c1e"
		Expect(cl.Create(ctx, pv)).To(Succeed())

		reconcile(rbdConnectionName)

		expectEntry(cephFSConnectionName, cephFSConnectionName, subvolumeGroup)
	})

	It("takes the default subvolume group of ceph-csi for a connection that names none", func() {
		Expect(cl.Create(ctx, newStorageClass("cephfs", v1alpha1.CephStorageClassTypeCephFS, cephFSConnectionName))).To(Succeed())
		pv := newPV("cephfs", controller.CephStorageClassCephFSProvisioner, clusterID, cephFSConnectionName, false)
		pv.Spec.CSI.VolumeAttributes["subvolumePath"] = "/volumes/csi/csi-vol-1/0b6e1c1e"
		Expect(cl.Create(ctx, pv)).To(Succeed())

		reconcile(cephFSConnectionName)

		expectEntry(cephFSConnectionName, rbdConnectionName, "")
	})

	It("lists the monitors of every connection of the clusterID", func() {
		connection := &v1alpha1.CephClusterConnection{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: cephFSConnectionName}, connection)).To(Succeed())
		connection.Spec.Monitors = []string{"mon3", "mon4"}
		Expect(cl.Update(ctx, connection)).To(Succeed())
		Expect(cl.Create(ctx, newStorageClass("rbd", v1alpha1.CephStorageClassTypeRBD, rbdConnectionName))).To(Succeed())

		reconcile(rbdConnectionName)

		configs := clusterConfigs()
		Expect(configs).To(HaveLen(1))
		Expect(configs[0].Monitors).To(Equal([]string{"mon1", "mon2", "mon3", "mon4"}))
	})

	DescribeTable("deletes a connection being deleted once no volume will read its secret again",
		func(phase corev1.PersistentVolumePhase, reclaimPolicy corev1.PersistentVolumeReclaimPolicy, attached, blocks bool) {
			Expect(cl.Create(ctx, newStorageClass("rbd", v1alpha1.CephStorageClassTypeRBD, rbdConnectionName))).To(Succeed())
			reconcile(rbdConnectionName)
			reconcile(cephFSConnectionName)
			pv := newPV("rbd", controller.CephStorageClassRBDProvisioner, clusterID, rbdConnectionName, true)
			pv.Spec.PersistentVolumeReclaimPolicy = reclaimPolicy
			Expect(cl.Create(ctx, pv)).To(Succeed())
			pv.Status.Phase = phase
			Expect(cl.Status().Update(ctx, pv)).To(Succeed())
			if attached {
				Expect(cl.Create(ctx, &storagev1.VolumeAttachment{
					ObjectMeta: metav1.ObjectMeta{Name: "csi-rbd"},
					Spec: storagev1.VolumeAttachmentSpec{
						Attacher: controller.CephStorageClassRBDProvisioner,
						NodeName: "node-1",
						Source:   storagev1.VolumeAttachmentSource{PersistentVolumeName: &pv.Name},
					},
				})).To(Succeed())
			}

			connection := &v1alpha1.CephClusterConnection{}
			Expect(cl.Get(ctx, client.ObjectKey{Name: rbdConnectionName}, connection)).To(Succeed())
			Expect(cl.Delete(ctx, connection)).To(Succeed())
			Expect(cl.Get(ctx, client.ObjectKey{Name: rbdConnectionName}, connection)).To(Succeed())

			shouldRequeue, msg, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, connection, controllerNamespace)
			if blocks {
				Expect(err).To(HaveOccurred())
				Expect(shouldRequeue).To(BeTrue())
				Expect(msg).To(ContainSubstring(pv.Name))
				expectEntry(rbdConnectionName, cephFSConnectionName, subvolumeGroup)
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(shouldRequeue).To(BeFalse())
			expectEntry(cephFSConnectionName, cephFSConnectionName, subvolumeGroup)
			err = cl.Get(ctx, client.ObjectKey{Name: rbdConnectionName}, connection)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		},
		Entry("Bound", corev1.VolumeBound, corev1.PersistentVolumeReclaimRetain, false, true),
		Entry("Released, deleted on release", corev1.VolumeReleased, corev1.PersistentVolumeReclaimDelete, false, true),
		Entry("Failed, deleted on release", corev1.VolumeFailed, corev1.PersistentVolumeReclaimDelete, false, true),
		Entry("Released, kept, still attached", corev1.VolumeReleased, corev1.PersistentVolumeReclaimRetain, true, true),
		Entry("Released, kept", corev1.VolumeReleased, corev1.PersistentVolumeReclaimRetain, false, false),
		Entry("Failed, kept", corev1.VolumeFailed, corev1.PersistentVolumeReclaimRetain, false, false),
	)
})
