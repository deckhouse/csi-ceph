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
	"encoding/json"

	v1alpha1 "d8-controller/api/v1alpha1"
	"d8-controller/pkg/controller"
	"d8-controller/pkg/internal"
	"d8-controller/pkg/logger"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe(controller.CephClusterConnectionCtrlName, func() {
	const (
		controllerNamespace      = "test-namespace"
		nameForClusterConnection = "example-ceph-connection"
		clusterID                = "clusterID1"
		configMapName            = internal.CSICephConfigMapName
	)

	var (
		ctx      = context.Background()
		cl       = NewFakeClient()
		log      = logger.Logger{}
		monitors = []string{"mon1", "mon2", "mon3"}
	)

	It("CephClusterConnection positive operations", func() {
		cephClusterConnection := &v1alpha1.CephClusterConnection{
			ObjectMeta: metav1.ObjectMeta{
				Name: nameForClusterConnection,
			},
			Spec: v1alpha1.CephClusterConnectionSpec{
				ClusterID: clusterID,
				Monitors:  monitors,
			},
		}

		By("Creating CephClusterConnection")
		err := cl.Create(ctx, cephClusterConnection)
		Expect(err).NotTo(HaveOccurred())

		createdCephClusterConnection := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForClusterConnection}, createdCephClusterConnection)
		Expect(err).NotTo(HaveOccurred())
		Expect(createdCephClusterConnection).NotTo(BeNil())
		Expect(createdCephClusterConnection.Name).To(Equal(nameForClusterConnection))
		Expect(createdCephClusterConnection.Spec.ClusterID).To(Equal(clusterID))
		Expect(createdCephClusterConnection.Spec.Monitors).To(ConsistOf(monitors))
		Expect(createdCephClusterConnection.Finalizers).To(HaveLen(0))

		By("Running reconcile for CephClusterConnection creation")
		configMapList := &corev1.ConfigMapList{}
		err = cl.List(ctx, configMapList)
		Expect(err).NotTo(HaveOccurred())

		shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, configMapList, createdCephClusterConnection, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying dependent ConfigMap")
		verifyConfigMap(ctx, cl, cephClusterConnection, controllerNamespace)

		By("Verifying CephClusterConnection after create reconcile")
		createdCephClusterConnection = &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForClusterConnection}, createdCephClusterConnection)
		Expect(err).NotTo(HaveOccurred())
		Expect(createdCephClusterConnection).NotTo(BeNil())
		Expect(createdCephClusterConnection.Finalizers).To(HaveLen(1))
		Expect(createdCephClusterConnection.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))
		// Expect(createdCephClusterConnection.Status).NotTo(BeNil())
		// Expect(createdCephClusterConnection.Status.Phase).To(Equal(v1alpha1.PhaseCreated))

		By("Updating CephClusterConnection")
		newMonitors := []string{"mon4", "mon5", "mon6"}
		createdCephClusterConnection.Spec.Monitors = newMonitors
		err = cl.Update(ctx, createdCephClusterConnection)
		Expect(err).NotTo(HaveOccurred())

		updatedCephClusterConnection := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForClusterConnection}, updatedCephClusterConnection)
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedCephClusterConnection).NotTo(BeNil())
		Expect(updatedCephClusterConnection.Spec.Monitors).To(ConsistOf(newMonitors))

		By("Running reconcile for CephClusterConnection update")
		configMapList = &corev1.ConfigMapList{}
		err = cl.List(ctx, configMapList)
		Expect(err).NotTo(HaveOccurred())

		shouldReconcile, _, err = controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, configMapList, updatedCephClusterConnection, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying updated ConfigMap")
		verifyConfigMap(ctx, cl, updatedCephClusterConnection, controllerNamespace)

		By("Verifying CephClusterConnection after update reconcile")
		updatedCephClusterConnection = &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForClusterConnection}, updatedCephClusterConnection)
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedCephClusterConnection).NotTo(BeNil())
		Expect(updatedCephClusterConnection.Finalizers).To(HaveLen(1))
		Expect(updatedCephClusterConnection.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))
		// Expect(updatedCephClusterConnection.Status).NotTo(BeNil())
		// Expect(updatedCephClusterConnection.Status.Phase).To(Equal(v1alpha1.PhaseCreated))

		By("Deleting CephClusterConnection")
		err = cl.Delete(ctx, cephClusterConnection)
		Expect(err).NotTo(HaveOccurred())

		By("Running reconcile for CephClusterConnection deletion")
		configMapList = &corev1.ConfigMapList{}
		err = cl.List(ctx, configMapList)
		Expect(err).NotTo(HaveOccurred())

		deletedCephClusterConnection := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForClusterConnection}, deletedCephClusterConnection)
		Expect(err).NotTo(HaveOccurred())
		Expect(deletedCephClusterConnection).NotTo(BeNil())
		Expect(deletedCephClusterConnection.Finalizers).To(HaveLen(1))
		Expect(deletedCephClusterConnection.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))

		shouldReconcile, _, err = controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, configMapList, deletedCephClusterConnection, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying ConfigMap update after deletion")
		verifyConfigMapWithoutClusterConnection(ctx, cl, cephClusterConnection, controllerNamespace)

		By("Verifying CephClusterConnection after delete reconcile")
		deletedCephClusterConnection = &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForClusterConnection}, deletedCephClusterConnection)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())
	})

	It("handles invalid CephClusterConnection spec", func() {
		By("Creating CephClusterConnection with empty ClusterID")
		cephClusterConnection := &v1alpha1.CephClusterConnection{
			ObjectMeta: metav1.ObjectMeta{
				Name: nameForClusterConnection,
			},
			Spec: v1alpha1.CephClusterConnectionSpec{
				ClusterID: "",
				Monitors:  []string{"mon1", "mon2", "mon3"},
			},
		}

		err := cl.Create(ctx, cephClusterConnection)
		Expect(err).NotTo(HaveOccurred())

		By("Running reconcile for invalid CephClusterConnection")
		configMapList := &corev1.ConfigMapList{}
		err = cl.List(ctx, configMapList)
		Expect(err).NotTo(HaveOccurred())

		shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, configMapList, cephClusterConnection, controllerNamespace)
		Expect(err).To(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying no ConfigMap entry created for invalid CephClusterConnection")
		verifyConfigMapWithoutClusterConnection(ctx, cl, cephClusterConnection, controllerNamespace)

		By("Creating CephClusterConnection with empty Monitors")
		cephClusterConnection.Spec.ClusterID = clusterID
		cephClusterConnection.Spec.Monitors = []string{}

		err = cl.Update(ctx, cephClusterConnection)
		Expect(err).NotTo(HaveOccurred())
		Expect(cephClusterConnection.Spec.Monitors).To(HaveLen(0))

		By("Running reconcile for CephClusterConnection with empty Monitors")
		shouldReconcile, _, err = controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, configMapList, cephClusterConnection, controllerNamespace)
		Expect(err).To(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying no ConfigMap entry created for CephClusterConnection with empty Monitors")
		verifyConfigMapWithoutClusterConnection(ctx, cl, cephClusterConnection, controllerNamespace)

		By("Fix CephClusterConnection")
		cephClusterConnection.Spec.Monitors = monitors
		err = cl.Update(ctx, cephClusterConnection)
		Expect(err).NotTo(HaveOccurred())

		By("Running reconcile for fixed CephClusterConnection")
		shouldReconcile, _, err = controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, configMapList, cephClusterConnection, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying ConfigMap entry created for fixed CephClusterConnection")
		verifyConfigMap(ctx, cl, cephClusterConnection, controllerNamespace)

		By("Verifying CephClusterConnection after fix reconcile")
		cephClusterConnection = &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForClusterConnection}, cephClusterConnection)
		Expect(err).NotTo(HaveOccurred())
		Expect(cephClusterConnection).NotTo(BeNil())
		Expect(cephClusterConnection.Finalizers).To(HaveLen(1))
		Expect(cephClusterConnection.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))
		// Expect(cephClusterConnection.Status).NotTo(BeNil())
		// Expect(cephClusterConnection.Status.Phase).To(Equal(v1alpha1.PhaseCreated))

		By("Updating CephClusterConnection with empty Monitors after fix")
		badCephClusterConnection := cephClusterConnection.DeepCopy()
		badCephClusterConnection.Spec.Monitors = []string{}
		err = cl.Update(ctx, badCephClusterConnection)
		Expect(err).NotTo(HaveOccurred())
		Expect(badCephClusterConnection.Spec.Monitors).To(HaveLen(0))

		By("Running reconcile for CephClusterConnection with empty Monitors after fix")
		shouldReconcile, _, err = controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, configMapList, badCephClusterConnection, controllerNamespace)
		Expect(err).To(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying ConfigMap not changed for CephClusterConnection with empty Monitors after fix")
		verifyConfigMap(ctx, cl, cephClusterConnection, controllerNamespace)

		By("Verifying CephClusterConnection not changed after fix reconcile")
		badCephClusterConnection = &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForClusterConnection}, badCephClusterConnection)
		Expect(err).NotTo(HaveOccurred())
		Expect(badCephClusterConnection).NotTo(BeNil())
		Expect(badCephClusterConnection.Finalizers).To(HaveLen(1))
		Expect(badCephClusterConnection.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))
		// Expect(badCephClusterConnection.Status).NotTo(BeNil())
		// Expect(badCephClusterConnection.Status.Phase).To(Equal(v1alpha1.PhaseFailed))

		By("Deleting CephClusterConnection with empty Monitors")
		err = cl.Delete(ctx, badCephClusterConnection)
		Expect(err).NotTo(HaveOccurred())

		badCephClusterConnection = &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForClusterConnection}, badCephClusterConnection)
		Expect(err).NotTo(HaveOccurred())
		Expect(badCephClusterConnection).NotTo(BeNil())
		Expect(badCephClusterConnection.Finalizers).To(HaveLen(1))
		Expect(badCephClusterConnection.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))
		Expect(badCephClusterConnection.DeletionTimestamp).NotTo(BeNil())

		configMapList = &corev1.ConfigMapList{}
		err = cl.List(ctx, configMapList)
		Expect(err).NotTo(HaveOccurred())

		By("Running reconcile for CephClusterConnection deletion with empty Monitors")
		shouldReconcile, _, err = controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, configMapList, badCephClusterConnection, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying ConfigMap is empty after deletion of CephClusterConnection with empty Monitors")
		verifyConfigMapWithoutClusterConnection(ctx, cl, cephClusterConnection, controllerNamespace)

		By("Verifying CephClusterConnection is deleted after deletion of CephClusterConnection with empty Monitors")
		badCephClusterConnection = &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForClusterConnection}, badCephClusterConnection)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())
	})
})

func verifyConfigMap(ctx context.Context, cl client.Client, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace string) {
	configMap := &corev1.ConfigMap{}
	err := cl.Get(ctx, client.ObjectKey{Name: internal.CSICephConfigMapName, Namespace: controllerNamespace}, configMap)
	Expect(err).NotTo(HaveOccurred())
	Expect(configMap).NotTo(BeNil())
	Expect(configMap.Finalizers).To(HaveLen(1))
	Expect(configMap.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))

	var clusterConfigs []v1alpha1.ClusterConfig
	err = json.Unmarshal([]byte(configMap.Data["config.json"]), &clusterConfigs)
	Expect(err).NotTo(HaveOccurred())
	Expect(clusterConfigs).NotTo(BeNil())
	found := false
	for _, cfg := range clusterConfigs {
		if cfg.ClusterID == cephClusterConnection.Spec.ClusterID {
			Expect(cfg.Monitors).To(ConsistOf(cephClusterConnection.Spec.Monitors))
			found = true
			break
		}
	}
	Expect(found).To(BeTrue(), "Cluster config not found in ConfigMap")
}

func verifyConfigMapWithoutClusterConnection(ctx context.Context, cl client.Client, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace string) {
	configMap := &corev1.ConfigMap{}
	err := cl.Get(ctx, client.ObjectKey{Name: internal.CSICephConfigMapName, Namespace: controllerNamespace}, configMap)
	Expect(err).NotTo(HaveOccurred())
	Expect(configMap).NotTo(BeNil())
	Expect(configMap.Finalizers).To(HaveLen(1))
	Expect(configMap.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))

	var clusterConfigs []v1alpha1.ClusterConfig
	err = json.Unmarshal([]byte(configMap.Data["config.json"]), &clusterConfigs)
	Expect(err).NotTo(HaveOccurred())
	for _, cfg := range clusterConfigs {
		Expect(cfg.ClusterID).NotTo(Equal(cephClusterConnection.Spec.ClusterID))
	}
}
