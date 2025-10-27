/*
Copyright 2025 Flant JSC

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
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/controller"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/internal"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/logger"
)

var _ = Describe(controller.CephClusterConnectionCtrlName, func() {
	const (
		controllerNamespace        = "test-namespace"
		firstClusterConnectionName = "example-ceph-connection-1"
		firstClusterID             = "clusterID1"
		firstUserID                = "admin"
		firstUserKey               = "key"
		firstUserIDNew             = "adminnew"
		firstUserKeyNew            = "keynew"

		secondClusterConnectionName = "example-ceph-connection-2"
		secondClusterID             = "clusterID2"
		secondUserID                = "admin2"
		secondUserKey               = "key2"
		secondUserIDNew             = "adminnew2"
		secondUserKeyNew            = "keynew2"

		badClusterConnectionName = "example-ceph-connection-bad"

		configMapName = internal.CSICephConfigMapName
	)

	var (
		ctx               = context.Background()
		cl                = NewFakeClient()
		log               = logger.Logger{}
		firstMonitors     = []string{"mon1", "mon2", "mon3"}
		firstMonitorsNew  = []string{"mon4", "mon5", "mon6"}
		secondMonitors    = []string{"secondmon1", "secondmon2", "secondmon3"}
		secondMonitorsNew = []string{"secondmon4", "secondmon5", "secondmon6"}
	)

	It("Creating the first CephClusterConnection", func() {
		cephClusterConnection1 := &v1alpha1.CephClusterConnection{
			ObjectMeta: metav1.ObjectMeta{
				Name: firstClusterConnectionName,
			},
			Spec: v1alpha1.CephClusterConnectionSpec{
				ClusterID: firstClusterID,
				Monitors:  firstMonitors,
				UserID:    firstUserID,
				UserKey:   firstUserKey,
			},
		}

		By("Creating the first CephClusterConnection")
		err := cl.Create(ctx, cephClusterConnection1)
		Expect(err).NotTo(HaveOccurred())

		createdCephClusterConnection1 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, createdCephClusterConnection1)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterCreate(createdCephClusterConnection1, firstClusterID, firstUserID, firstUserKey, firstMonitors)

		By("Checking that no ConfigMap exists")
		configMapList := &corev1.ConfigMapList{}
		err = cl.List(ctx, configMapList)
		Expect(err).NotTo(HaveOccurred())
		Expect(configMapList.Items).To(HaveLen(0))

		By("Checking that no Secret exists")
		secretList := &corev1.SecretList{}
		err = cl.List(ctx, secretList)
		Expect(err).NotTo(HaveOccurred())
		Expect(secretList.Items).To(HaveLen(0))

		By("Running reconcile for the creation of the first CephClusterConnection")
		shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, createdCephClusterConnection1, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying the dependent ConfigMap")
		verifyConfigMap(ctx, cl, firstClusterID, firstMonitors)

		By("Verifying the dependent Secret")
		verifySecret(ctx, cl, cephClusterConnection1.Name, firstUserID, firstUserKey)

		By("Verifying the first CephClusterConnection after the creation reconcile")
		createdCephClusterConnection1AfterReconcile := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, createdCephClusterConnection1AfterReconcile)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(createdCephClusterConnection1AfterReconcile, firstClusterID, firstUserID, firstUserKey, firstMonitors)
	})

	It("Updating the monitors of the first CephClusterConnection", func() {
		By("Updating the first CephClusterConnection monitors")

		createdCephClusterConnection1 := &v1alpha1.CephClusterConnection{}
		err := cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, createdCephClusterConnection1)
		Expect(err).NotTo(HaveOccurred())
		Expect(createdCephClusterConnection1).NotTo(BeNil())
		Expect(createdCephClusterConnection1.Spec.Monitors).NotTo(ConsistOf(firstMonitorsNew))

		createdCephClusterConnection1.Spec.Monitors = firstMonitorsNew
		err = cl.Update(ctx, createdCephClusterConnection1)
		Expect(err).NotTo(HaveOccurred())

		updatedCephClusterConnection1 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, updatedCephClusterConnection1)
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedCephClusterConnection1).NotTo(BeNil())
		Expect(updatedCephClusterConnection1.Spec.Monitors).To(ConsistOf(firstMonitorsNew))

		By("Running reconcile for the first CephClusterConnection update")
		shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, updatedCephClusterConnection1, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying the ConfigMap")
		verifyConfigMap(ctx, cl, firstClusterID, firstMonitorsNew)

		By("Verifying the Secret")
		verifySecret(ctx, cl, updatedCephClusterConnection1.Name, firstUserID, firstUserKey)

		By("Verifying the first CephClusterConnection after the update reconcile")
		updatedCephClusterConnectionAfterReconcile1 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, updatedCephClusterConnectionAfterReconcile1)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(updatedCephClusterConnectionAfterReconcile1, firstClusterID, firstUserID, firstUserKey, firstMonitorsNew)
	})

	It("Updating the user and key of the first CephClusterConnection", func() {
		By("Updating the first CephClusterConnection user and key")
		createdCephClusterConnection1 := &v1alpha1.CephClusterConnection{}
		err := cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, createdCephClusterConnection1)
		Expect(err).NotTo(HaveOccurred())
		Expect(createdCephClusterConnection1).NotTo(BeNil())
		Expect(createdCephClusterConnection1.Spec.UserID).NotTo(Equal(firstUserIDNew))
		Expect(createdCephClusterConnection1.Spec.UserKey).NotTo(Equal(firstUserKeyNew))

		createdCephClusterConnection1.Spec.UserID = firstUserIDNew
		createdCephClusterConnection1.Spec.UserKey = firstUserKeyNew
		err = cl.Update(ctx, createdCephClusterConnection1)
		Expect(err).NotTo(HaveOccurred())

		updatedCephClusterConnection1 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, updatedCephClusterConnection1)
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedCephClusterConnection1).NotTo(BeNil())
		Expect(updatedCephClusterConnection1.Spec.UserID).To(Equal(firstUserIDNew))
		Expect(updatedCephClusterConnection1.Spec.UserKey).To(Equal(firstUserKeyNew))

		By("Running reconcile for the first CephClusterConnection update")
		shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, updatedCephClusterConnection1, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying the ConfigMap")
		verifyConfigMap(ctx, cl, firstClusterID, firstMonitorsNew)

		By("Verifying the Secret")
		verifySecret(ctx, cl, updatedCephClusterConnection1.Name, firstUserIDNew, firstUserKeyNew)

		By("Verifying the first CephClusterConnection after the update reconcile")
		updatedCephClusterConnectionAfterReconcile1 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, updatedCephClusterConnectionAfterReconcile1)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(updatedCephClusterConnectionAfterReconcile1, firstClusterID, firstUserIDNew, firstUserKeyNew, firstMonitorsNew)
	})

	It("Creating a second CephClusterConnection when the first one exists", func() {
		By("Creating the second CephClusterConnection")
		cephClusterConnection2 := &v1alpha1.CephClusterConnection{
			ObjectMeta: metav1.ObjectMeta{
				Name: secondClusterConnectionName,
			},
			Spec: v1alpha1.CephClusterConnectionSpec{
				ClusterID: secondClusterID,
				Monitors:  secondMonitors,
				UserID:    secondUserID,
				UserKey:   secondUserKey,
			},
		}

		err := cl.Create(ctx, cephClusterConnection2)
		Expect(err).NotTo(HaveOccurred())

		createdCephClusterConnection2 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: secondClusterConnectionName}, createdCephClusterConnection2)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterCreate(createdCephClusterConnection2, secondClusterID, secondUserID, secondUserKey, secondMonitors)

		By("Running reconcile for the creation of the second CephClusterConnection")
		shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, createdCephClusterConnection2, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying the dependent ConfigMap after creating the second CephClusterConnection")
		verifyConfigMap(ctx, cl, secondClusterID, secondMonitors)

		By("Verifying the dependent Secret after creating the second CephClusterConnection")
		verifySecret(ctx, cl, cephClusterConnection2.Name, secondUserID, secondUserKey)

		By("Verifying the second CephClusterConnection after the creation reconcile")
		createdCephClusterConnection2AfterReconcile := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: secondClusterConnectionName}, createdCephClusterConnection2AfterReconcile)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(createdCephClusterConnection2AfterReconcile, secondClusterID, secondUserID, secondUserKey, secondMonitors)

		By("Verifying the first CephClusterConnection after the creation of the second one")
		cephClusterConnectionAfterReconcile1 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, cephClusterConnectionAfterReconcile1)
		Expect(err).NotTo(HaveOccurred())
		Expect(cephClusterConnectionAfterReconcile1).NotTo(BeNil())
		Expect(cephClusterConnectionAfterReconcile1.Spec.ClusterID).To(Equal(firstClusterID))
		Expect(cephClusterConnectionAfterReconcile1.Spec.Monitors).To(ConsistOf(firstMonitorsNew))
		Expect(cephClusterConnectionAfterReconcile1.Spec.UserID).To(Equal(firstUserIDNew))
		Expect(cephClusterConnectionAfterReconcile1.Spec.UserKey).To(Equal(firstUserKeyNew))
		Expect(cephClusterConnectionAfterReconcile1.Finalizers).To(HaveLen(1))
		Expect(cephClusterConnectionAfterReconcile1.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))

		By("Verifying the first cluster connection in the ConfigMap after the second CephClusterConnection creation reconcile")
		verifyConfigMap(ctx, cl, secondClusterID, secondMonitors)

		By("Verifying the first cluster connection Secret after the second CephClusterConnection creation reconcile")
		verifySecret(ctx, cl, cephClusterConnectionAfterReconcile1.Name, firstUserIDNew, firstUserKeyNew)
	})

	It("Updating monitors, user, and key of the second CephClusterConnection when the first one exists", func() {
		By("Updating the second CephClusterConnection monitors, user, and key")
		createdCephClusterConnection2 := &v1alpha1.CephClusterConnection{}
		err := cl.Get(ctx, client.ObjectKey{Name: secondClusterConnectionName}, createdCephClusterConnection2)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(createdCephClusterConnection2, secondClusterID, secondUserID, secondUserKey, secondMonitors)

		createdCephClusterConnection2.Spec.Monitors = secondMonitorsNew
		createdCephClusterConnection2.Spec.UserID = secondUserIDNew
		createdCephClusterConnection2.Spec.UserKey = secondUserKeyNew
		err = cl.Update(ctx, createdCephClusterConnection2)
		Expect(err).NotTo(HaveOccurred())

		updatedCephClusterConnection2 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: secondClusterConnectionName}, updatedCephClusterConnection2)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(updatedCephClusterConnection2, secondClusterID, secondUserIDNew, secondUserKeyNew, secondMonitorsNew)

		By("Running reconcile for the second CephClusterConnection update")
		shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, updatedCephClusterConnection2, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying the ConfigMap after the second CephClusterConnection update")
		verifyConfigMap(ctx, cl, secondClusterID, secondMonitorsNew)

		By("Verifying the Secret after the second CephClusterConnection update")
		verifySecret(ctx, cl, updatedCephClusterConnection2.Name, secondUserIDNew, secondUserKeyNew)

		By("Verifying the second CephClusterConnection after the update reconcile")
		updatedCephClusterConnectionAfterReconcile2 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: secondClusterConnectionName}, updatedCephClusterConnectionAfterReconcile2)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(updatedCephClusterConnectionAfterReconcile2, secondClusterID, secondUserIDNew, secondUserKeyNew, secondMonitorsNew)

		By("Verifying the first CephClusterConnection after the second CephClusterConnection update")
		cephClusterConnectionAfterReconcile1 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, cephClusterConnectionAfterReconcile1)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(cephClusterConnectionAfterReconcile1, firstClusterID, firstUserIDNew, firstUserKeyNew, firstMonitorsNew)

		By("Verifying the first cluster connection in the ConfigMap after the second CephClusterConnection update reconcile")
		verifyConfigMap(ctx, cl, firstClusterID, firstMonitorsNew)

		By("Verifying the first cluster connection Secret after the second CephClusterConnection update reconcile")
		verifySecret(ctx, cl, cephClusterConnectionAfterReconcile1.Name, firstUserIDNew, firstUserKeyNew)
	})

	It("Attempting to update the second CephClusterConnection with invalid monitors when the first one exists", func() {
		By("Updating the second CephClusterConnection with empty monitors")
		createdCephClusterConnection2 := &v1alpha1.CephClusterConnection{}
		err := cl.Get(ctx, client.ObjectKey{Name: secondClusterConnectionName}, createdCephClusterConnection2)
		Expect(err).NotTo(HaveOccurred())
		Expect(createdCephClusterConnection2).NotTo(BeNil())
		Expect(createdCephClusterConnection2.Spec.Monitors).NotTo(ConsistOf([]string{}))

		createdCephClusterConnection2.Spec.Monitors = []string{}
		err = cl.Update(ctx, createdCephClusterConnection2)
		Expect(err).NotTo(HaveOccurred())

		updatedCephClusterConnection2 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: secondClusterConnectionName}, updatedCephClusterConnection2)
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedCephClusterConnection2).NotTo(BeNil())
		Expect(updatedCephClusterConnection2.Spec.Monitors).To(HaveLen(0))

		By("Running reconcile for the second CephClusterConnection update with empty monitors")
		shouldReconcile, msg, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, updatedCephClusterConnection2, controllerNamespace)
		Expect(err).To(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())
		Expect(msg).To(ContainSubstring("Validation of CephClusterConnection failed: the spec.monitors field is empty;"))

		By("Verifying that the ConfigMap did not change after the second CephClusterConnection update with empty monitors")
		verifyConfigMap(ctx, cl, secondClusterID, secondMonitorsNew)

		By("Verifying that the Secret did not change after the second CephClusterConnection update with empty monitors")
		verifySecret(ctx, cl, updatedCephClusterConnection2.Name, secondUserIDNew, secondUserKeyNew)

		By("Verifying that the second CephClusterConnection did not change after the update reconcile with empty monitors")
		updatedCephClusterConnectionAfterReconcile2 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: secondClusterConnectionName}, updatedCephClusterConnectionAfterReconcile2)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(updatedCephClusterConnectionAfterReconcile2, secondClusterID, secondUserIDNew, secondUserKeyNew, []string{})

		By("Verifying that the first CephClusterConnection did not change after the second CephClusterConnection update with empty monitors")
		cephClusterConnectionAfterReconcile1 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, cephClusterConnectionAfterReconcile1)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(cephClusterConnectionAfterReconcile1, firstClusterID, firstUserIDNew, firstUserKeyNew, firstMonitorsNew)

		By("Verifying that the first cluster connection in the ConfigMap did not change after the second CephClusterConnection update with empty monitors")
		verifyConfigMap(ctx, cl, firstClusterID, firstMonitorsNew)

		By("Verifying that the first cluster connection Secret did not change after the second CephClusterConnection update with empty monitors")
		verifySecret(ctx, cl, cephClusterConnectionAfterReconcile1.Name, firstUserIDNew, firstUserKeyNew)
	})

	It("Attempting to update the second CephClusterConnection with invalid user and key when the first one exists", func() {
		By("Updating the second CephClusterConnection with empty UserID and UserKey")
		createdCephClusterConnection2 := &v1alpha1.CephClusterConnection{}
		err := cl.Get(ctx, client.ObjectKey{Name: secondClusterConnectionName}, createdCephClusterConnection2)
		Expect(err).NotTo(HaveOccurred())
		Expect(createdCephClusterConnection2).NotTo(BeNil())
		Expect(createdCephClusterConnection2.Spec.UserID).NotTo(Equal(""))
		Expect(createdCephClusterConnection2.Spec.UserKey).NotTo(Equal(""))

		createdCephClusterConnection2.Spec.UserID = ""
		createdCephClusterConnection2.Spec.UserKey = ""
		err = cl.Update(ctx, createdCephClusterConnection2)
		Expect(err).NotTo(HaveOccurred())

		updatedCephClusterConnection2 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: secondClusterConnectionName}, updatedCephClusterConnection2)
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedCephClusterConnection2).NotTo(BeNil())
		Expect(updatedCephClusterConnection2.Spec.UserID).To(Equal(""))
		Expect(updatedCephClusterConnection2.Spec.UserKey).To(Equal(""))

		By("Running reconcile for the second CephClusterConnection update with empty UserID and UserKey")
		shouldReconcile, msg, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, updatedCephClusterConnection2, controllerNamespace)
		Expect(err).To(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())
		Expect(msg).To(ContainSubstring("Validation of CephClusterConnection failed: the spec.monitors field is empty; the spec.userID field is empty; the spec.userKey field is empty;"))

		By("Verifying that the ConfigMap did not change after the second CephClusterConnection update with empty UserID and UserKey")
		verifyConfigMap(ctx, cl, secondClusterID, secondMonitorsNew)

		By("Verifying that the Secret did not change after the second CephClusterConnection update with empty UserID and UserKey")
		verifySecret(ctx, cl, updatedCephClusterConnection2.Name, secondUserIDNew, secondUserKeyNew)

		By("Verifying that the second CephClusterConnection did not change after the update reconcile with empty UserID and UserKey")
		updatedCephClusterConnectionAfterReconcile2 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: secondClusterConnectionName}, updatedCephClusterConnectionAfterReconcile2)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(updatedCephClusterConnectionAfterReconcile2, secondClusterID, "", "", []string{})

		By("Verifying that the first CephClusterConnection did not change after the second CephClusterConnection update with empty UserID and UserKey")
		cephClusterConnectionAfterReconcile1 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, cephClusterConnectionAfterReconcile1)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(cephClusterConnectionAfterReconcile1, firstClusterID, firstUserIDNew, firstUserKeyNew, firstMonitorsNew)

		By("Verifying that the first cluster connection in the ConfigMap did not change after the second CephClusterConnection update with empty UserID and UserKey")
		verifyConfigMap(ctx, cl, firstClusterID, firstMonitorsNew)

		By("Verifying that the first cluster connection Secret did not change after the second CephClusterConnection update with empty UserID and UserKey")
		verifySecret(ctx, cl, cephClusterConnectionAfterReconcile1.Name, firstUserIDNew, firstUserKeyNew)
	})

	It("Deleting the second CephClusterConnection with a bad spec when the first CephClusterConnection exists", func() {
		By("Deleting the second CephClusterConnection")
		cephClusterConnection2 := &v1alpha1.CephClusterConnection{}
		err := cl.Get(ctx, client.ObjectKey{Name: secondClusterConnectionName}, cephClusterConnection2)
		Expect(err).NotTo(HaveOccurred())
		Expect(cephClusterConnection2).NotTo(BeNil())
		verifyCephClusterConnectionAfterReconcile(cephClusterConnection2, secondClusterID, "", "", []string{})

		err = cl.Delete(ctx, cephClusterConnection2)
		Expect(err).NotTo(HaveOccurred())

		deletedCephClusterConnection2 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: secondClusterConnectionName}, deletedCephClusterConnection2)
		Expect(err).NotTo(HaveOccurred())
		Expect(deletedCephClusterConnection2).NotTo(BeNil())
		Expect(deletedCephClusterConnection2.Finalizers).To(HaveLen(1))
		Expect(deletedCephClusterConnection2.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))
		Expect(deletedCephClusterConnection2.DeletionTimestamp).NotTo(BeNil())

		By("Checking that cluster connections exist in the ConfigMap for both CephClusterConnections before reconcile")
		verifyConfigMap(ctx, cl, firstClusterID, firstMonitorsNew)
		verifyConfigMap(ctx, cl, secondClusterID, secondMonitorsNew)

		By("Checking that Secrets exist for both CephClusterConnections before reconcile")
		verifySecret(ctx, cl, firstClusterConnectionName, firstUserIDNew, firstUserKeyNew)
		verifySecret(ctx, cl, secondClusterConnectionName, secondUserIDNew, secondUserKeyNew)

		By("Running reconcile for the second CephClusterConnection deletion")
		shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, deletedCephClusterConnection2, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying that the second cluster connection is removed from the ConfigMap after the second CephClusterConnection delete reconcile")
		verifyConfigMapForInvalidClusterConnection(ctx, cl)
		configMap := &corev1.ConfigMap{}
		err = cl.Get(ctx, client.ObjectKey{Name: configMapName, Namespace: controllerNamespace}, configMap)
		Expect(err).NotTo(HaveOccurred())

		var clusterConfigs []v1alpha1.ClusterConfig
		err = json.Unmarshal([]byte(configMap.Data["config.json"]), &clusterConfigs)
		Expect(err).NotTo(HaveOccurred())
		Expect(clusterConfigs).To(HaveLen(1))
		Expect(clusterConfigs[0].ClusterID).To(Equal(firstClusterID))

		By("Verifying that the first cluster connection is not removed from the ConfigMap after the second CephClusterConnection delete reconcile")
		verifyConfigMap(ctx, cl, firstClusterID, firstMonitorsNew)

		By("Verifying that the second Secret is deleted after the second CephClusterConnection delete reconcile")
		secondSecret := &corev1.Secret{}
		err = cl.Get(ctx, client.ObjectKey{Name: secondClusterConnectionName, Namespace: controllerNamespace}, secondSecret)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())

		By("Verifying that the first Secret is not deleted after the second CephClusterConnection delete reconcile")
		verifySecret(ctx, cl, firstClusterConnectionName, firstUserIDNew, firstUserKeyNew)

		By("Verifying the second CephClusterConnection after the delete reconcile")
		deletedCephClusterConnection2 = &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: secondClusterConnectionName}, deletedCephClusterConnection2)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())

		By("Verifying the first CephClusterConnection after the second CephClusterConnection delete reconcile")
		cephClusterConnectionAfterReconcile1 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, cephClusterConnectionAfterReconcile1)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(cephClusterConnectionAfterReconcile1, firstClusterID, firstUserIDNew, firstUserKeyNew, firstMonitorsNew)
	})

	It("Deleting the first CephClusterConnection when no others exist", func() {
		By("Deleting the first CephClusterConnection")
		cephClusterConnection1 := &v1alpha1.CephClusterConnection{}
		err := cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, cephClusterConnection1)
		Expect(err).NotTo(HaveOccurred())
		Expect(cephClusterConnection1).NotTo(BeNil())
		verifyCephClusterConnectionAfterReconcile(cephClusterConnection1, firstClusterID, firstUserIDNew, firstUserKeyNew, firstMonitorsNew)

		err = cl.Delete(ctx, cephClusterConnection1)
		Expect(err).NotTo(HaveOccurred())

		deletedCephClusterConnection1 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, deletedCephClusterConnection1)
		Expect(err).NotTo(HaveOccurred())
		Expect(deletedCephClusterConnection1).NotTo(BeNil())
		Expect(deletedCephClusterConnection1.Finalizers).To(HaveLen(1))
		Expect(deletedCephClusterConnection1.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))
		Expect(deletedCephClusterConnection1.DeletionTimestamp).NotTo(BeNil())

		By("Checking that the cluster connection exists in the ConfigMap for the first CephClusterConnection before reconcile")
		verifyConfigMap(ctx, cl, firstClusterID, firstMonitorsNew)

		By("Checking that the first Secret exists for the first CephClusterConnection before reconcile")
		verifySecret(ctx, cl, firstClusterConnectionName, firstUserIDNew, firstUserKeyNew)

		By("Running reconcile for the first CephClusterConnection deletion")
		shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, deletedCephClusterConnection1, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying that the first cluster connection is removed from the ConfigMap after the first CephClusterConnection delete reconcile")
		verifyConfigMapForInvalidClusterConnection(ctx, cl)
		configMap := &corev1.ConfigMap{}
		err = cl.Get(ctx, client.ObjectKey{Name: configMapName, Namespace: controllerNamespace}, configMap)
		Expect(err).NotTo(HaveOccurred())

		var clusterConfigs []v1alpha1.ClusterConfig
		err = json.Unmarshal([]byte(configMap.Data["config.json"]), &clusterConfigs)
		Expect(err).NotTo(HaveOccurred())
		Expect(clusterConfigs).To(HaveLen(0))

		By("Verifying that the first Secret is deleted after the first CephClusterConnection delete reconcile")
		firstSecret := &corev1.Secret{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName, Namespace: controllerNamespace}, firstSecret)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())

		By("Verifying the first CephClusterConnection after the delete reconcile")
		deletedCephClusterConnection1 = &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, deletedCephClusterConnection1)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())
	})

	It("Handling invalid CephClusterConnection specifications", func() {
		By("Creating a CephClusterConnection with an empty ClusterID")
		cephClusterConnection := &v1alpha1.CephClusterConnection{
			ObjectMeta: metav1.ObjectMeta{
				Name: badClusterConnectionName,
			},
			Spec: v1alpha1.CephClusterConnectionSpec{
				ClusterID: "",
				Monitors:  []string{"mon1", "mon2", "mon3"},
				UserID:    firstUserID,
				UserKey:   firstUserKey,
			},
		}

		err := cl.Create(ctx, cephClusterConnection)
		Expect(err).NotTo(HaveOccurred())

		By("Running reconcile for an invalid CephClusterConnection with an empty ClusterID")
		shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, cephClusterConnection, controllerNamespace)
		Expect(err).To(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying that no ConfigMap entry was created for the invalid CephClusterConnection")
		verifyConfigMapForInvalidClusterConnection(ctx, cl)

		By("Updating the CephClusterConnection with empty Monitors")
		cephClusterConnection.Spec.ClusterID = firstClusterID
		cephClusterConnection.Spec.Monitors = []string{}

		err = cl.Update(ctx, cephClusterConnection)
		Expect(err).NotTo(HaveOccurred())
		Expect(cephClusterConnection.Spec.Monitors).To(HaveLen(0))

		By("Running reconcile for an invalid CephClusterConnection with empty Monitors")
		shouldReconcile, _, err = controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, cephClusterConnection, controllerNamespace)
		Expect(err).To(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying that no ConfigMap entry was created for the CephClusterConnection with empty Monitors")
		verifyConfigMapForInvalidClusterConnection(ctx, cl)

		By("Fixing the CephClusterConnection")
		cephClusterConnection.Spec.Monitors = firstMonitors
		err = cl.Update(ctx, cephClusterConnection)
		Expect(err).NotTo(HaveOccurred())

		By("Running reconcile for the fixed CephClusterConnection")
		shouldReconcile, _, err = controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, cephClusterConnection, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying that a ConfigMap entry was created for the fixed CephClusterConnection")
		verifyConfigMap(ctx, cl, firstClusterID, firstMonitors)

		By("Verifying the CephClusterConnection after the fix reconcile")
		cephClusterConnection = &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: badClusterConnectionName}, cephClusterConnection)
		Expect(err).NotTo(HaveOccurred())
		Expect(cephClusterConnection).NotTo(BeNil())
		Expect(cephClusterConnection.Finalizers).To(HaveLen(1))
		Expect(cephClusterConnection.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))

		By("Updating the CephClusterConnection with empty Monitors after the fix")
		badCephClusterConnection := cephClusterConnection.DeepCopy()
		badCephClusterConnection.Spec.Monitors = []string{}
		err = cl.Update(ctx, badCephClusterConnection)
		Expect(err).NotTo(HaveOccurred())
		Expect(badCephClusterConnection.Spec.Monitors).To(HaveLen(0))

		By("Running reconcile for a CephClusterConnection with empty Monitors after the fix")
		shouldReconcile, _, err = controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, badCephClusterConnection, controllerNamespace)
		Expect(err).To(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying that the ConfigMap is not changed for the CephClusterConnection with empty Monitors after the fix")
		verifyConfigMap(ctx, cl, firstClusterID, firstMonitors)

		By("Verifying that the CephClusterConnection is not changed after the fix reconcile")
		badCephClusterConnection = &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: badClusterConnectionName}, badCephClusterConnection)
		Expect(err).NotTo(HaveOccurred())
		Expect(badCephClusterConnection).NotTo(BeNil())
		Expect(badCephClusterConnection.Finalizers).To(HaveLen(1))
		Expect(badCephClusterConnection.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))

		By("Deleting the CephClusterConnection with empty Monitors")
		err = cl.Delete(ctx, badCephClusterConnection)
		Expect(err).NotTo(HaveOccurred())

		badCephClusterConnection = &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: badClusterConnectionName}, badCephClusterConnection)
		Expect(err).NotTo(HaveOccurred())
		Expect(badCephClusterConnection).NotTo(BeNil())
		Expect(badCephClusterConnection.Finalizers).To(HaveLen(1))
		Expect(badCephClusterConnection.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))
		Expect(badCephClusterConnection.DeletionTimestamp).NotTo(BeNil())

		By("Running reconcile for the CephClusterConnection deletion with empty Monitors")
		shouldReconcile, _, err = controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, badCephClusterConnection, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying that the ConfigMap is empty after deleting the CephClusterConnection with empty Monitors")
		verifyConfigMapForInvalidClusterConnection(ctx, cl)

		By("Verifying that the CephClusterConnection is deleted after deleting the CephClusterConnection with empty Monitors")
		badCephClusterConnection = &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: badClusterConnectionName}, badCephClusterConnection)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())
	})

	Context("Testing SubvolumeGroup handling", func() {
		const (
			subvolumeGroupTestClusterConnectionName = "test-subvolumegroup-connection"
			subvolumeGroupTestClusterID             = "test-subvolumegroup-cluster"
			subvolumeGroupTestUserID                = "admin"
			subvolumeGroupTestUserKey               = "key"
			subvolumeGroupValue                     = "test-subvolume-group"
		)

		var (
			subvolumeGroupTestMonitors = []string{"mon1", "mon2", "mon3"}
		)

		It("Creating CephClusterConnection without SubvolumeGroup", func() {
			cephClusterConnection := &v1alpha1.CephClusterConnection{
				ObjectMeta: metav1.ObjectMeta{
					Name: subvolumeGroupTestClusterConnectionName,
				},
				Spec: v1alpha1.CephClusterConnectionSpec{
					ClusterID: subvolumeGroupTestClusterID,
					Monitors:  subvolumeGroupTestMonitors,
					UserID:    subvolumeGroupTestUserID,
					UserKey:   subvolumeGroupTestUserKey,
					// CephFS is omitted (empty struct)
				},
			}

			By("Creating CephClusterConnection without CephFS.SubvolumeGroup")
			err := cl.Create(ctx, cephClusterConnection)
			Expect(err).NotTo(HaveOccurred())

			createdCephClusterConnection := &v1alpha1.CephClusterConnection{}
			err = cl.Get(ctx, client.ObjectKey{Name: subvolumeGroupTestClusterConnectionName}, createdCephClusterConnection)
			Expect(err).NotTo(HaveOccurred())

			By("Running reconcile for CephClusterConnection without SubvolumeGroup")
			shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, createdCephClusterConnection, controllerNamespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(shouldReconcile).To(BeFalse())

			By("Verifying that ConfigMap is created without subvolumeGroup key")
			verifyConfigMapWithoutSubvolumeGroup(ctx, cl, subvolumeGroupTestClusterID, subvolumeGroupTestMonitors)

			By("Verifying that Secret is created correctly")
			verifySecret(ctx, cl, cephClusterConnection.Name, subvolumeGroupTestUserID, subvolumeGroupTestUserKey)
		})

		It("Updating CephClusterConnection to add SubvolumeGroup", func() {
			By("Getting the existing CephClusterConnection")
			cephClusterConnection := &v1alpha1.CephClusterConnection{}
			err := cl.Get(ctx, client.ObjectKey{Name: subvolumeGroupTestClusterConnectionName}, cephClusterConnection)
			Expect(err).NotTo(HaveOccurred())

			By("Adding SubvolumeGroup to the CephClusterConnection")
			cephClusterConnection.Spec.CephFS = &v1alpha1.CephClusterConnectionSpecCephFS{
				SubvolumeGroup: subvolumeGroupValue,
			}
			err = cl.Update(ctx, cephClusterConnection)
			Expect(err).NotTo(HaveOccurred())

			updatedCephClusterConnection := &v1alpha1.CephClusterConnection{}
			err = cl.Get(ctx, client.ObjectKey{Name: subvolumeGroupTestClusterConnectionName}, updatedCephClusterConnection)
			Expect(err).NotTo(HaveOccurred())
			Expect(updatedCephClusterConnection.Spec.CephFS.SubvolumeGroup).To(Equal(subvolumeGroupValue))

			By("Running reconcile for CephClusterConnection with SubvolumeGroup")
			shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, updatedCephClusterConnection, controllerNamespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(shouldReconcile).To(BeFalse())

			By("Verifying that ConfigMap is updated with subvolumeGroup key")
			verifyConfigMapWithSubvolumeGroup(ctx, cl, subvolumeGroupTestClusterID, subvolumeGroupTestMonitors, subvolumeGroupValue)
		})

		It("Creating CephClusterConnection with empty SubvolumeGroup", func() {
			const emptySubvolumeGroupConnectionName = "test-empty-subvolumegroup-connection"
			const emptySubvolumeGroupClusterID = "test-empty-subvolumegroup-cluster"

			cephClusterConnection := &v1alpha1.CephClusterConnection{
				ObjectMeta: metav1.ObjectMeta{
					Name: emptySubvolumeGroupConnectionName,
				},
				Spec: v1alpha1.CephClusterConnectionSpec{
					ClusterID: emptySubvolumeGroupClusterID,
					Monitors:  subvolumeGroupTestMonitors,
					UserID:    subvolumeGroupTestUserID,
					UserKey:   subvolumeGroupTestUserKey,
					CephFS: &v1alpha1.CephClusterConnectionSpecCephFS{
						SubvolumeGroup: "", // Explicitly empty
					},
				},
			}

			By("Creating CephClusterConnection with empty SubvolumeGroup")
			err := cl.Create(ctx, cephClusterConnection)
			Expect(err).NotTo(HaveOccurred())

			createdCephClusterConnection := &v1alpha1.CephClusterConnection{}
			err = cl.Get(ctx, client.ObjectKey{Name: emptySubvolumeGroupConnectionName}, createdCephClusterConnection)
			Expect(err).NotTo(HaveOccurred())

			By("Running reconcile for CephClusterConnection with empty SubvolumeGroup")
			shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, createdCephClusterConnection, controllerNamespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(shouldReconcile).To(BeFalse())

			By("Verifying that ConfigMap is created without subvolumeGroup key (empty string is ignored)")
			verifyConfigMapWithoutSubvolumeGroup(ctx, cl, emptySubvolumeGroupClusterID, subvolumeGroupTestMonitors)

			By("Cleaning up empty SubvolumeGroup test connection")
			err = cl.Delete(ctx, createdCephClusterConnection)
			Expect(err).NotTo(HaveOccurred())
			shouldReconcile, _, err = controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, createdCephClusterConnection, controllerNamespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(shouldReconcile).To(BeFalse())
		})

		It("Removing SubvolumeGroup from existing CephClusterConnection", func() {
			By("Getting the existing CephClusterConnection with SubvolumeGroup")
			cephClusterConnection := &v1alpha1.CephClusterConnection{}
			err := cl.Get(ctx, client.ObjectKey{Name: subvolumeGroupTestClusterConnectionName}, cephClusterConnection)
			Expect(err).NotTo(HaveOccurred())
			Expect(cephClusterConnection.Spec.CephFS.SubvolumeGroup).To(Equal(subvolumeGroupValue))

			By("Removing SubvolumeGroup from the CephClusterConnection")
			cephClusterConnection.Spec.CephFS = nil // Clear the field
			err = cl.Update(ctx, cephClusterConnection)
			Expect(err).NotTo(HaveOccurred())

			updatedCephClusterConnection := &v1alpha1.CephClusterConnection{}
			err = cl.Get(ctx, client.ObjectKey{Name: subvolumeGroupTestClusterConnectionName}, updatedCephClusterConnection)
			Expect(err).NotTo(HaveOccurred())
			Expect(updatedCephClusterConnection.Spec.CephFS).To(BeNil())

			By("Running reconcile for CephClusterConnection without SubvolumeGroup")
			shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, updatedCephClusterConnection, controllerNamespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(shouldReconcile).To(BeFalse())

			By("Verifying that ConfigMap is updated to remove subvolumeGroup key")
			verifyConfigMapWithoutSubvolumeGroup(ctx, cl, subvolumeGroupTestClusterID, subvolumeGroupTestMonitors)

			By("Cleaning up test connection")
			err = cl.Delete(ctx, updatedCephClusterConnection)
			Expect(err).NotTo(HaveOccurred())
			shouldReconcile, _, err = controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, updatedCephClusterConnection, controllerNamespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(shouldReconcile).To(BeFalse())
		})
	})

	Describe("generateClusterConfig function", func() {
		It("should generate config without subvolumeGroup when SubvolumeGroup is empty", func() {
			cephClusterConnection := &v1alpha1.CephClusterConnection{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: v1alpha1.CephClusterConnectionSpec{
					ClusterID: "test-cluster",
					Monitors:  []string{"mon1", "mon2"},
					UserID:    "admin",
					UserKey:   "key",
					CephFS: &v1alpha1.CephClusterConnectionSpecCephFS{
						SubvolumeGroup: "", // Empty string
					},
				},
			}

			config := controller.GenerateClusterConfigForTesting(cephClusterConnection, "test-namespace")

			Expect(config.ClusterID).To(Equal("test-cluster"))
			Expect(config.Monitors).To(ConsistOf([]string{"mon1", "mon2"}))
			Expect(config.CephFS.SubvolumeGroup).To(BeEmpty())
			Expect(config.CephFS.ControllerPublishSecretRef.Name).To(Equal("csi-ceph-secret-for-test-cluster"))
			Expect(config.CephFS.ControllerPublishSecretRef.Namespace).To(Equal("test-namespace"))
			Expect(config.RBD.ControllerPublishSecretRef.Name).To(Equal("csi-ceph-secret-for-test-cluster"))
			Expect(config.RBD.ControllerPublishSecretRef.Namespace).To(Equal("test-namespace"))
		})

		It("should generate config without subvolumeGroup when CephFS is zero-value struct", func() {
			cephClusterConnection := &v1alpha1.CephClusterConnection{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: v1alpha1.CephClusterConnectionSpec{
					ClusterID: "test-cluster",
					Monitors:  []string{"mon1", "mon2"},
					UserID:    "admin",
					UserKey:   "key",
					// CephFS is zero-value struct
				},
			}

			config := controller.GenerateClusterConfigForTesting(cephClusterConnection, "test-namespace")

			Expect(config.ClusterID).To(Equal("test-cluster"))
			Expect(config.Monitors).To(ConsistOf([]string{"mon1", "mon2"}))
			Expect(config.CephFS.SubvolumeGroup).To(BeEmpty())
			Expect(config.CephFS.ControllerPublishSecretRef.Name).To(Equal("csi-ceph-secret-for-test-cluster"))
			Expect(config.CephFS.ControllerPublishSecretRef.Namespace).To(Equal("test-namespace"))
			Expect(config.RBD.ControllerPublishSecretRef.Name).To(Equal("csi-ceph-secret-for-test-cluster"))
			Expect(config.RBD.ControllerPublishSecretRef.Namespace).To(Equal("test-namespace"))
		})

		It("should generate config with subvolumeGroup when SubvolumeGroup is not empty", func() {
			cephClusterConnection := &v1alpha1.CephClusterConnection{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: v1alpha1.CephClusterConnectionSpec{
					ClusterID: "test-cluster",
					Monitors:  []string{"mon1", "mon2"},
					UserID:    "admin",
					UserKey:   "key",
					CephFS: &v1alpha1.CephClusterConnectionSpecCephFS{
						SubvolumeGroup: "my-subvolume-group",
					},
				},
			}

			config := controller.GenerateClusterConfigForTesting(cephClusterConnection, "test-namespace")

			Expect(config.ClusterID).To(Equal("test-cluster"))
			Expect(config.Monitors).To(ConsistOf([]string{"mon1", "mon2"}))
			Expect(config.CephFS.SubvolumeGroup).To(Equal("my-subvolume-group"))
			Expect(config.CephFS.ControllerPublishSecretRef.Name).To(Equal("csi-ceph-secret-for-test-cluster"))
			Expect(config.CephFS.ControllerPublishSecretRef.Namespace).To(Equal("test-namespace"))
			Expect(config.RBD.ControllerPublishSecretRef.Name).To(Equal("csi-ceph-secret-for-test-cluster"))
			Expect(config.RBD.ControllerPublishSecretRef.Namespace).To(Equal("test-namespace"))
		})
	})
})

func verifyConfigMap(ctx context.Context, cl client.Client, clusterID string, monitors []string) {
	controllerNamespace := "test-namespace"
	configMap := &corev1.ConfigMap{}
	err := cl.Get(ctx, client.ObjectKey{Name: internal.CSICephConfigMapName, Namespace: controllerNamespace}, configMap)
	Expect(err).NotTo(HaveOccurred())
	Expect(configMap).NotTo(BeNil())
	Expect(configMap.Data).To(HaveKey("config.json"))
	Expect(configMap.Labels).To(HaveKeyWithValue(internal.StorageManagedLabelKey, controller.CephClusterConnectionCtrlName))
	Expect(configMap.Finalizers).To(HaveLen(1))
	Expect(configMap.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))

	var clusterConfigs []v1alpha1.ClusterConfig
	err = json.Unmarshal([]byte(configMap.Data["config.json"]), &clusterConfigs)
	Expect(err).NotTo(HaveOccurred())
	Expect(clusterConfigs).NotTo(BeNil())
	found := false
	for _, cfg := range clusterConfigs {
		if cfg.ClusterID == clusterID {
			Expect(cfg.Monitors).To(ConsistOf(monitors))
			found = true
			break
		}
	}
	Expect(found).To(BeTrue(), "Cluster config not found in ConfigMap")
}

func verifyConfigMapWithSubvolumeGroup(ctx context.Context, cl client.Client, clusterID string, monitors []string, expectedSubvolumeGroup string) {
	controllerNamespace := "test-namespace"
	configMap := &corev1.ConfigMap{}
	err := cl.Get(ctx, client.ObjectKey{Name: internal.CSICephConfigMapName, Namespace: controllerNamespace}, configMap)
	Expect(err).NotTo(HaveOccurred())
	Expect(configMap).NotTo(BeNil())
	Expect(configMap.Data).To(HaveKey("config.json"))
	Expect(configMap.Labels).To(HaveKeyWithValue(internal.StorageManagedLabelKey, controller.CephClusterConnectionCtrlName))
	Expect(configMap.Finalizers).To(HaveLen(1))
	Expect(configMap.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))

	var clusterConfigs []v1alpha1.ClusterConfig
	err = json.Unmarshal([]byte(configMap.Data["config.json"]), &clusterConfigs)
	Expect(err).NotTo(HaveOccurred())
	Expect(clusterConfigs).NotTo(BeNil())
	found := false
	for _, cfg := range clusterConfigs {
		if cfg.ClusterID == clusterID {
			Expect(cfg.Monitors).To(ConsistOf(monitors))
			Expect(cfg.CephFS.SubvolumeGroup).To(Equal(expectedSubvolumeGroup))
			Expect(cfg.CephFS.ControllerPublishSecretRef.Name).NotTo(BeEmpty())
			Expect(cfg.RBD.ControllerPublishSecretRef.Name).NotTo(BeEmpty())
			found = true
			break
		}
	}
	Expect(found).To(BeTrue(), "Cluster config not found in ConfigMap")
}

func verifyConfigMapWithoutSubvolumeGroup(ctx context.Context, cl client.Client, clusterID string, monitors []string) {
	controllerNamespace := "test-namespace"
	configMap := &corev1.ConfigMap{}
	err := cl.Get(ctx, client.ObjectKey{Name: internal.CSICephConfigMapName, Namespace: controllerNamespace}, configMap)
	Expect(err).NotTo(HaveOccurred())
	Expect(configMap).NotTo(BeNil())
	Expect(configMap.Data).To(HaveKey("config.json"))
	Expect(configMap.Labels).To(HaveKeyWithValue(internal.StorageManagedLabelKey, controller.CephClusterConnectionCtrlName))
	Expect(configMap.Finalizers).To(HaveLen(1))
	Expect(configMap.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))

	var clusterConfigs []v1alpha1.ClusterConfig
	err = json.Unmarshal([]byte(configMap.Data["config.json"]), &clusterConfigs)
	Expect(err).NotTo(HaveOccurred())
	Expect(clusterConfigs).NotTo(BeNil())
	found := false
	for _, cfg := range clusterConfigs {
		if cfg.ClusterID == clusterID {
			Expect(cfg.Monitors).To(ConsistOf(monitors))
			Expect(cfg.CephFS.SubvolumeGroup).To(BeEmpty())
			Expect(cfg.CephFS.ControllerPublishSecretRef.Name).NotTo(BeEmpty())
			Expect(cfg.RBD.ControllerPublishSecretRef.Name).NotTo(BeEmpty())
			found = true
			break
		}
	}
	Expect(found).To(BeTrue(), "Cluster config not found in ConfigMap")
}

func verifySecret(ctx context.Context, cl client.Client, cephClusterConnectionName, userID, userKey string) {
	controllerNamespace := "test-namespace"
	secretName := internal.CephClusterConnectionSecretPrefix + cephClusterConnectionName
	secret := &corev1.Secret{}
	err := cl.Get(ctx, client.ObjectKey{Name: secretName, Namespace: controllerNamespace}, secret)
	Expect(err).NotTo(HaveOccurred())
	Expect(secret).NotTo(BeNil())
	Expect(secret.Labels).To(HaveKeyWithValue(internal.StorageManagedLabelKey, controller.CephClusterConnectionCtrlName))
	Expect(secret.Finalizers).To(HaveLen(1))
	Expect(secret.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))
	Expect(secret.StringData).To(HaveKeyWithValue("userID", userID))
	Expect(secret.StringData).To(HaveKeyWithValue("userKey", userKey))
	Expect(secret.StringData).To(HaveKeyWithValue("adminID", userID))
	Expect(secret.StringData).To(HaveKeyWithValue("adminKey", userKey))
}

func verifyConfigMapForInvalidClusterConnection(ctx context.Context, cl client.Client) {
	controllerNamespace := "test-namespace"
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
	for _, clusterConfig := range clusterConfigs {
		Expect(clusterConfig.ClusterID).NotTo(Equal(""))
		Expect(clusterConfig.Monitors).NotTo(BeEmpty())
	}
}

func verifyCephClusterConnectionAfterCreate(cephClusterConnection *v1alpha1.CephClusterConnection, clusterID, userID, userKey string, monitors []string) {
	Expect(cephClusterConnection).NotTo(BeNil())
	Expect(cephClusterConnection.Spec.ClusterID).To(Equal(clusterID))
	Expect(cephClusterConnection.Spec.Monitors).To(ConsistOf(monitors))
	Expect(cephClusterConnection.Spec.UserID).To(Equal(userID))
	Expect(cephClusterConnection.Spec.UserKey).To(Equal(userKey))
	Expect(cephClusterConnection.Finalizers).To(HaveLen(0))
	Expect(cephClusterConnection.Status).To(BeNil())
}

func verifyCephClusterConnectionAfterReconcile(cephClusterConnection *v1alpha1.CephClusterConnection, clusterID, userID, userKey string, monitors []string) {
	Expect(cephClusterConnection).NotTo(BeNil())
	Expect(cephClusterConnection.Spec.ClusterID).To(Equal(clusterID))
	Expect(cephClusterConnection.Spec.Monitors).To(ConsistOf(monitors))
	Expect(cephClusterConnection.Spec.UserID).To(Equal(userID))
	Expect(cephClusterConnection.Spec.UserKey).To(Equal(userKey))
	Expect(cephClusterConnection.Finalizers).To(HaveLen(1))
	Expect(cephClusterConnection.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))
	Expect(cephClusterConnection.DeletionTimestamp).To(BeNil())

	//TODO: Add these checks after figuring out how to test the watcher
	// Expect(reconciledCephClusterConnection.Status).NotTo(BeNil())
	// Expect(reconciledCephClusterConnection.Status.Phase).To(Equal(internal.PhaseCreated))
}
