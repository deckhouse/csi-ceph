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
		userID                   = "admin"
		userKey                  = "key"
		configMapName            = internal.CSICephConfigMapName
		secretNamePrefix         = internal.CephClusterConnectionSecretPrefix
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
				UserID:    userID,
				UserKey:   userKey,
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
		Expect(createdCephClusterConnection.Spec.UserID).To(Equal(userID))
		Expect(createdCephClusterConnection.Spec.UserKey).To(Equal(userKey))
		Expect(createdCephClusterConnection.Spec.Monitors).To(ConsistOf(monitors))
		Expect(createdCephClusterConnection.Finalizers).To(HaveLen(0))

		By("Running reconcile for CephClusterConnection creation")
		secretList := &corev1.SecretList{}
		err = cl.List(ctx, secretList)
		Expect(err).NotTo(HaveOccurred())

		shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, secretList, createdCephClusterConnection, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying dependent Secret")
		verifySecret(ctx, cl, cephClusterConnection, controllerNamespace)

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
		secretList = &corev1.SecretList{}
		err = cl.List(ctx, secretList)
		Expect(err).NotTo(HaveOccurred())

		shouldReconcile, _, err = controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, secretList, updatedCephClusterConnection, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying updated Secret")
		verifySecret(ctx, cl, updatedCephClusterConnection, controllerNamespace)

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
		secretList = &corev1.SecretList{}
		err = cl.List(ctx, secretList)
		Expect(err).NotTo(HaveOccurred())

		deletedCephClusterConnection := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForClusterConnection}, deletedCephClusterConnection)
		Expect(err).NotTo(HaveOccurred())
		Expect(deletedCephClusterConnection).NotTo(BeNil())
		Expect(deletedCephClusterConnection.Finalizers).To(HaveLen(1))
		Expect(deletedCephClusterConnection.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))

		shouldReconcile, _, err = controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, secretList, deletedCephClusterConnection, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying ConfigMap update after deletion")
		verifyConfigMapWithoutClusterConnection(ctx, cl, cephClusterConnection, controllerNamespace)

		By("Verifying Secret deletion")
		verifySecretNotExists(ctx, cl, cephClusterConnection, controllerNamespace)

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
				UserID:    userID,
				UserKey:   userKey,
			},
		}

		err := cl.Create(ctx, cephClusterConnection)
		Expect(err).NotTo(HaveOccurred())

		By("Running reconcile for invalid CephClusterConnection")
		secretList := &corev1.SecretList{}
		err = cl.List(ctx, secretList)
		Expect(err).NotTo(HaveOccurred())

		shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, secretList, cephClusterConnection, controllerNamespace)
		Expect(err).To(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying no Secret created for invalid CephClusterConnection")
		verifySecretNotExists(ctx, cl, cephClusterConnection, controllerNamespace)

		By("Verifying no ConfigMap entry created for invalid CephClusterConnection")
		verifyConfigMapWithoutClusterConnection(ctx, cl, cephClusterConnection, controllerNamespace)

		By("Creating CephClusterConnection with empty Monitors")
		cephClusterConnection.Spec.ClusterID = clusterID
		cephClusterConnection.Spec.Monitors = []string{}

		err = cl.Update(ctx, cephClusterConnection)
		Expect(err).NotTo(HaveOccurred())
		Expect(cephClusterConnection.Spec.Monitors).To(HaveLen(0))

		By("Running reconcile for CephClusterConnection with empty Monitors")
		shouldReconcile, _, err = controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, secretList, cephClusterConnection, controllerNamespace)
		Expect(err).To(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying no Secret created for CephClusterConnection with empty Monitors")
		verifySecretNotExists(ctx, cl, cephClusterConnection, controllerNamespace)

		By("Verifying no ConfigMap entry created for CephClusterConnection with empty Monitors")
		verifyConfigMapWithoutClusterConnection(ctx, cl, cephClusterConnection, controllerNamespace)
	})
})

func verifySecret(ctx context.Context, cl client.Client, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace string) {
	secretName := internal.CephClusterConnectionSecretPrefix + cephClusterConnection.Name
	secret := &corev1.Secret{}
	err := cl.Get(ctx, client.ObjectKey{Name: secretName, Namespace: controllerNamespace}, secret)
	Expect(err).NotTo(HaveOccurred())
	Expect(secret).NotTo(BeNil())
	Expect(secret.Finalizers).To(HaveLen(1))
	Expect(secret.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))
	Expect(secret.StringData).To(HaveKeyWithValue("userID", cephClusterConnection.Spec.UserID))
	Expect(secret.StringData).To(HaveKeyWithValue("userKey", cephClusterConnection.Spec.UserKey))
	Expect(secret.StringData).To(HaveKeyWithValue("adminID", cephClusterConnection.Spec.UserID))
	Expect(secret.StringData).To(HaveKeyWithValue("adminKey", cephClusterConnection.Spec.UserKey))
}

func verifyConfigMap(ctx context.Context, cl client.Client, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace string) {
	configMap := &corev1.ConfigMap{}
	err := cl.Get(ctx, client.ObjectKey{Name: internal.CSICephConfigMapName, Namespace: controllerNamespace}, configMap)
	Expect(err).NotTo(HaveOccurred())
	Expect(configMap).NotTo(BeNil())
	Expect(configMap.Finalizers).To(HaveLen(1))
	Expect(configMap.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))

	clusterConfigs := v1alpha1.ClusterConfigList{}
	err = json.Unmarshal([]byte(configMap.Data["config.json"]), &clusterConfigs)
	Expect(err).NotTo(HaveOccurred())
	found := false
	for _, cfg := range clusterConfigs.Items {
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

	clusterConfigs := v1alpha1.ClusterConfigList{}
	err = json.Unmarshal([]byte(configMap.Data["config.json"]), &clusterConfigs)
	Expect(err).NotTo(HaveOccurred())
	for _, cfg := range clusterConfigs.Items {
		Expect(cfg.ClusterID).NotTo(Equal(cephClusterConnection.Spec.ClusterID))
	}
}

func verifySecretNotExists(ctx context.Context, cl client.Client, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace string) {
	secretName := internal.CephClusterConnectionSecretPrefix + cephClusterConnection.Name
	secret := &corev1.Secret{}
	err := cl.Get(ctx, client.ObjectKey{Name: secretName, Namespace: controllerNamespace}, secret)
	Expect(k8serrors.IsNotFound(err)).To(BeTrue())
}
