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

	v1alpha1 "github.com/deckhouse/csi-ceph/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"d8-controller/pkg/controller"
	"d8-controller/pkg/internal"
	"d8-controller/pkg/logger"
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

	It("First CephClusterConnection creation", func() {
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

		By("Creating first CephClusterConnection")
		err := cl.Create(ctx, cephClusterConnection1)
		Expect(err).NotTo(HaveOccurred())

		createdCephClusterConnection1 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, createdCephClusterConnection1)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterCreate(createdCephClusterConnection1, firstClusterID, firstUserID, firstUserKey, firstMonitors)

		By("Check that no ConfigMap exists")
		configMapList := &corev1.ConfigMapList{}
		err = cl.List(ctx, configMapList)
		Expect(err).NotTo(HaveOccurred())
		Expect(configMapList.Items).To(HaveLen(0))

		By("Check that no Secret exists")
		secretList := &corev1.SecretList{}
		err = cl.List(ctx, secretList)
		Expect(err).NotTo(HaveOccurred())
		Expect(secretList.Items).To(HaveLen(0))

		By("Running reconcile for first CephClusterConnection creation")
		shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, createdCephClusterConnection1, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying dependent ConfigMap")
		verifyConfigMap(ctx, cl, firstClusterID, firstMonitors)

		By("Verifying dependent Secret")
		verifySecret(ctx, cl, cephClusterConnection1.Name, firstUserID, firstUserKey)

		By("Verifying first CephClusterConnection after create reconcile")
		createdCephClusterConnection1AfterReconcile := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, createdCephClusterConnection1AfterReconcile)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(createdCephClusterConnection1AfterReconcile, firstClusterID, firstUserID, firstUserKey, firstMonitors)
	})

	It("First CephClusterConnection updation monitors", func() {
		By("Updating first CephClusterConnection monitors")

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

		By("Running reconcile for first CephClusterConnection update")
		shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, updatedCephClusterConnection1, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying ConfigMap")
		verifyConfigMap(ctx, cl, firstClusterID, firstMonitorsNew)

		By("Verifying Secret")
		verifySecret(ctx, cl, updatedCephClusterConnection1.Name, firstUserID, firstUserKey)

		By("Verifying first CephClusterConnection after update reconcile")
		updatedCephClusterConnectionAfterReconcile1 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, updatedCephClusterConnectionAfterReconcile1)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(updatedCephClusterConnectionAfterReconcile1, firstClusterID, firstUserID, firstUserKey, firstMonitorsNew)
	})

	It("First CephClusterConnection updation user and key", func() {
		By("Updating first CephClusterConnection user and key")
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

		By("Running reconcile for first CephClusterConnection update")
		shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, updatedCephClusterConnection1, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying ConfigMap")
		verifyConfigMap(ctx, cl, firstClusterID, firstMonitorsNew)

		By("Verifying Secret")
		verifySecret(ctx, cl, updatedCephClusterConnection1.Name, firstUserIDNew, firstUserKeyNew)

		By("Verifying first CephClusterConnection after update reconcile")
		updatedCephClusterConnectionAfterReconcile1 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, updatedCephClusterConnectionAfterReconcile1)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(updatedCephClusterConnectionAfterReconcile1, firstClusterID, firstUserIDNew, firstUserKeyNew, firstMonitorsNew)
	})

	It("Second CephClusterConnection creation when first CephClusterConnection exists", func() {
		By("Creating second CephClusterConnection")
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
		//clusterID, userID, userKey , monitors
		verifyCephClusterConnectionAfterCreate(createdCephClusterConnection2, secondClusterID, secondUserID, secondUserKey, secondMonitors)

		By("Running reconcile for second CephClusterConnection creation")
		shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, createdCephClusterConnection2, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying dependent ConfigMap after second CephClusterConnection creation")
		verifyConfigMap(ctx, cl, secondClusterID, secondMonitors)

		By("Verifying dependent Secret after second CephClusterConnection creation")
		verifySecret(ctx, cl, cephClusterConnection2.Name, secondUserID, secondUserKey)

		By("Verifying second CephClusterConnection after create reconcile")
		createdCephClusterConnection2AfterReconcile := &v1alpha1.CephClusterConnection{}

		err = cl.Get(ctx, client.ObjectKey{Name: secondClusterConnectionName}, createdCephClusterConnection2AfterReconcile)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(createdCephClusterConnection2AfterReconcile, secondClusterID, secondUserID, secondUserKey, secondMonitors)

		By("Verifying first CephClusterConnection after second CephClusterConnection creation")
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

		By("Verifying first cluster connection in ConfigMap after second CephClusterConnection create reconcile")
		verifyConfigMap(ctx, cl, secondClusterID, secondMonitors)

		By("Verifying first cluster connection Secret after second CephClusterConnection create reconcile")
		verifySecret(ctx, cl, cephClusterConnectionAfterReconcile1.Name, firstUserIDNew, firstUserKeyNew)
	})

	It("Second CephClusterConnection updation monitors, user and key when first CephClusterConnection exists", func() {
		By("Updating second CephClusterConnection monitors, user and key")
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

		By("Running reconcile for second CephClusterConnection update")
		shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, updatedCephClusterConnection2, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying ConfigMap after second CephClusterConnection update")
		verifyConfigMap(ctx, cl, secondClusterID, secondMonitorsNew)

		By("Verifying Secret after second CephClusterConnection update")
		verifySecret(ctx, cl, updatedCephClusterConnection2.Name, secondUserIDNew, secondUserKeyNew)

		By("Verifying second CephClusterConnection after update reconcile")
		updatedCephClusterConnectionAfterReconcile2 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: secondClusterConnectionName}, updatedCephClusterConnectionAfterReconcile2)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(updatedCephClusterConnectionAfterReconcile2, secondClusterID, secondUserIDNew, secondUserKeyNew, secondMonitorsNew)

		By("Verifying first CephClusterConnection after second CephClusterConnection update")
		cephClusterConnectionAfterReconcile1 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, cephClusterConnectionAfterReconcile1)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(cephClusterConnectionAfterReconcile1, firstClusterID, firstUserIDNew, firstUserKeyNew, firstMonitorsNew)

		By("Verifying first cluster connection in ConfigMap after second CephClusterConnection update reconcile")
		verifyConfigMap(ctx, cl, firstClusterID, firstMonitorsNew)

		By("Verifying first cluster connection Secret after second CephClusterConnection update reconcile")
		verifySecret(ctx, cl, cephClusterConnectionAfterReconcile1.Name, firstUserIDNew, firstUserKeyNew)
	})

	It("Second CephClusterConnection invalid updation monitors when first CephClusterConnection exists", func() {
		By("Updating second CephClusterConnection with empty Monitors")
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

		By("Running reconcile for second CephClusterConnection update with empty Monitors")
		shouldReconcile, msg, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, updatedCephClusterConnection2, controllerNamespace)
		Expect(err).To(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())
		Expect(msg).To(ContainSubstring("Validation of CephClusterConnection failed: the spec.monitors field is empty;"))

		By("Verifying ConfigMap not changed after second CephClusterConnection update with empty Monitors")
		verifyConfigMap(ctx, cl, secondClusterID, secondMonitorsNew)

		By("Verifying Secret not changed after second CephClusterConnection update with empty Monitors")
		verifySecret(ctx, cl, updatedCephClusterConnection2.Name, secondUserIDNew, secondUserKeyNew)

		By("Verifying second CephClusterConnection not changed after update reconcile with empty Monitors")
		updatedCephClusterConnectionAfterReconcile2 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: secondClusterConnectionName}, updatedCephClusterConnectionAfterReconcile2)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(updatedCephClusterConnectionAfterReconcile2, secondClusterID, secondUserIDNew, secondUserKeyNew, []string{})

		By("Verifying first CephClusterConnection not changed after second CephClusterConnection update with empty Monitors")
		cephClusterConnectionAfterReconcile1 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, cephClusterConnectionAfterReconcile1)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(cephClusterConnectionAfterReconcile1, firstClusterID, firstUserIDNew, firstUserKeyNew, firstMonitorsNew)

		By("Verifying first cluster connection in ConfigMap not changed after second CephClusterConnection update with empty Monitors")
		verifyConfigMap(ctx, cl, firstClusterID, firstMonitorsNew)

		By("Verifying first cluster connection Secret not changed after second CephClusterConnection update with empty Monitors")
		verifySecret(ctx, cl, cephClusterConnectionAfterReconcile1.Name, firstUserIDNew, firstUserKeyNew)
	})

	It("Second CephClusterConnection invalid updation user and key when first CephClusterConnection exists", func() {
		By("Updating second CephClusterConnection with empty UserID and UserKey")
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

		By("Running reconcile for second CephClusterConnection update with empty UserID and UserKey")
		shouldReconcile, msg, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, updatedCephClusterConnection2, controllerNamespace)
		Expect(err).To(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())
		Expect(msg).To(ContainSubstring("Validation of CephClusterConnection failed: the spec.monitors field is empty; the spec.userID field is empty; the spec.userKey field is empty;"))

		By("Verifying ConfigMap not changed after second CephClusterConnection update with empty UserID and UserKey")
		verifyConfigMap(ctx, cl, secondClusterID, secondMonitorsNew)

		By("Verifying Secret not changed after second CephClusterConnection update with empty UserID and UserKey")
		verifySecret(ctx, cl, updatedCephClusterConnection2.Name, secondUserIDNew, secondUserKeyNew)

		By("Verifying second CephClusterConnection not changed after update reconcile with empty UserID and UserKey")
		updatedCephClusterConnectionAfterReconcile2 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: secondClusterConnectionName}, updatedCephClusterConnectionAfterReconcile2)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(updatedCephClusterConnectionAfterReconcile2, secondClusterID, "", "", []string{})

		By("Verifying first CephClusterConnection not changed after second CephClusterConnection update with empty UserID and UserKey")
		cephClusterConnectionAfterReconcile1 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, cephClusterConnectionAfterReconcile1)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(cephClusterConnectionAfterReconcile1, firstClusterID, firstUserIDNew, firstUserKeyNew, firstMonitorsNew)

		By("Verifying first cluster connection in ConfigMap not changed after second CephClusterConnection update with empty UserID and UserKey")
		verifyConfigMap(ctx, cl, firstClusterID, firstMonitorsNew)

		By("Verifying first cluster connection Secret not changed after second CephClusterConnection update with empty UserID and UserKey")
		verifySecret(ctx, cl, cephClusterConnectionAfterReconcile1.Name, firstUserIDNew, firstUserKeyNew)
	})

	It("Second CephClusterConnection with bad spec deletion when first CephClusterConnection exists", func() {
		By("Deleting second CephClusterConnection")
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

		By("Check that cluster connections exists in configmap for both CephClusterConnections before reconcile")
		verifyConfigMap(ctx, cl, firstClusterID, firstMonitorsNew)
		verifyConfigMap(ctx, cl, secondClusterID, secondMonitorsNew)

		By("Check that secrets exists for both CephClusterConnections before reconcile")
		verifySecret(ctx, cl, firstClusterConnectionName, firstUserIDNew, firstUserKeyNew)
		verifySecret(ctx, cl, secondClusterConnectionName, secondUserIDNew, secondUserKeyNew)

		By("Running reconcile for second CephClusterConnection deletion")
		shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, deletedCephClusterConnection2, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying that second cluster connection is deleted from ConfigMap after second CephClusterConnection delete reconcile")
		verifyConfigMapForInvalidClusterConnection(ctx, cl)
		configMap := &corev1.ConfigMap{}
		err = cl.Get(ctx, client.ObjectKey{Name: configMapName, Namespace: controllerNamespace}, configMap)
		Expect(err).NotTo(HaveOccurred())

		var clusterConfigs []v1alpha1.ClusterConfig
		err = json.Unmarshal([]byte(configMap.Data["config.json"]), &clusterConfigs)
		Expect(err).NotTo(HaveOccurred())
		Expect(clusterConfigs).To(HaveLen(1))
		Expect(clusterConfigs[0].ClusterID).To(Equal(firstClusterID))

		By("Verifying that first cluster connection is not deleted from ConfigMap after second CephClusterConnection delete reconcile")
		verifyConfigMap(ctx, cl, firstClusterID, firstMonitorsNew)

		By("Verifying that second secret is deleted after second CephClusterConnection delete reconcile")
		secondSecret := &corev1.Secret{}
		err = cl.Get(ctx, client.ObjectKey{Name: secondClusterConnectionName, Namespace: controllerNamespace}, secondSecret)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())

		By("Verifying that first secret is not deleted after second CephClusterConnection delete reconcile")
		verifySecret(ctx, cl, firstClusterConnectionName, firstUserIDNew, firstUserKeyNew)

		By("Verifying second CephClusterConnection after delete reconcile")
		deletedCephClusterConnection2 = &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: secondClusterConnectionName}, deletedCephClusterConnection2)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())

		By("Verifying first CephClusterConnection after second CephClusterConnection delete reconcile")
		cephClusterConnectionAfterReconcile1 := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, cephClusterConnectionAfterReconcile1)
		Expect(err).NotTo(HaveOccurred())
		verifyCephClusterConnectionAfterReconcile(cephClusterConnectionAfterReconcile1, firstClusterID, firstUserIDNew, firstUserKeyNew, firstMonitorsNew)
	})

	It("First CephClusterConnection deletion when no other CephClusterConnection exists", func() {
		By("Deleting first CephClusterConnection")
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

		By("Check that cluster connections exists in configmap for first CephClusterConnections before reconcile")
		verifyConfigMap(ctx, cl, firstClusterID, firstMonitorsNew)

		By("Check that first secret exists for first CephClusterConnections before reconcile")
		verifySecret(ctx, cl, firstClusterConnectionName, firstUserIDNew, firstUserKeyNew)

		By("Running reconcile for first CephClusterConnection deletion")
		shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, deletedCephClusterConnection1, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying that first cluster connection is deleted from ConfigMap after first CephClusterConnection delete reconcile")
		verifyConfigMapForInvalidClusterConnection(ctx, cl)
		configMap := &corev1.ConfigMap{}
		err = cl.Get(ctx, client.ObjectKey{Name: configMapName, Namespace: controllerNamespace}, configMap)
		Expect(err).NotTo(HaveOccurred())

		var clusterConfigs []v1alpha1.ClusterConfig
		err = json.Unmarshal([]byte(configMap.Data["config.json"]), &clusterConfigs)
		Expect(err).NotTo(HaveOccurred())
		Expect(clusterConfigs).To(HaveLen(0))

		By("Verifying that first secret is deleted after first CephClusterConnection delete reconcile")
		firstSecret := &corev1.Secret{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName, Namespace: controllerNamespace}, firstSecret)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())

		By("Verifying first CephClusterConnection after delete reconcile")
		deletedCephClusterConnection1 = &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: firstClusterConnectionName}, deletedCephClusterConnection1)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())
	})

	It("handles invalid CephClusterConnection spec", func() {
		By("Creating CephClusterConnection with empty ClusterID")
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

		By("Running reconcile for invalid CephClusterConnection with empty ClusterID")

		shouldReconcile, _, err := controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, cephClusterConnection, controllerNamespace)
		Expect(err).To(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying no ConfigMap entry created for invalid CephClusterConnection")
		verifyConfigMapForInvalidClusterConnection(ctx, cl)

		By("Updating CephClusterConnection with empty Monitors")
		cephClusterConnection.Spec.ClusterID = firstClusterID
		cephClusterConnection.Spec.Monitors = []string{}

		err = cl.Update(ctx, cephClusterConnection)
		Expect(err).NotTo(HaveOccurred())
		Expect(cephClusterConnection.Spec.Monitors).To(HaveLen(0))

		By("Running reconcile for invalid CephClusterConnection with empty Monitors")
		shouldReconcile, _, err = controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, cephClusterConnection, controllerNamespace)
		Expect(err).To(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying no ConfigMap entry created for CephClusterConnection with empty Monitors")
		verifyConfigMapForInvalidClusterConnection(ctx, cl)

		By("Fix CephClusterConnection")
		cephClusterConnection.Spec.Monitors = firstMonitors
		err = cl.Update(ctx, cephClusterConnection)
		Expect(err).NotTo(HaveOccurred())

		By("Running reconcile for fixed CephClusterConnection")
		shouldReconcile, _, err = controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, cephClusterConnection, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying ConfigMap entry created for fixed CephClusterConnection")
		verifyConfigMap(ctx, cl, firstClusterID, firstMonitors)

		By("Verifying CephClusterConnection after fix reconcile")
		cephClusterConnection = &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: badClusterConnectionName}, cephClusterConnection)
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
		shouldReconcile, _, err = controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, badCephClusterConnection, controllerNamespace)
		Expect(err).To(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying ConfigMap not changed for CephClusterConnection with empty Monitors after fix")
		verifyConfigMap(ctx, cl, firstClusterID, firstMonitors)

		By("Verifying CephClusterConnection not changed after fix reconcile")
		badCephClusterConnection = &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: badClusterConnectionName}, badCephClusterConnection)
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
		err = cl.Get(ctx, client.ObjectKey{Name: badClusterConnectionName}, badCephClusterConnection)
		Expect(err).NotTo(HaveOccurred())
		Expect(badCephClusterConnection).NotTo(BeNil())
		Expect(badCephClusterConnection.Finalizers).To(HaveLen(1))
		Expect(badCephClusterConnection.Finalizers).To(ContainElement(controller.CephClusterConnectionControllerFinalizerName))
		Expect(badCephClusterConnection.DeletionTimestamp).NotTo(BeNil())

		By("Running reconcile for CephClusterConnection deletion with empty Monitors")
		shouldReconcile, _, err = controller.RunCephClusterConnectionEventReconcile(ctx, cl, log, badCephClusterConnection, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying ConfigMap is empty after deletion of CephClusterConnection with empty Monitors")
		verifyConfigMapForInvalidClusterConnection(ctx, cl)

		By("Verifying CephClusterConnection is deleted after deletion of CephClusterConnection with empty Monitors")
		badCephClusterConnection = &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, client.ObjectKey{Name: badClusterConnectionName}, badCephClusterConnection)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())
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
			// Expect(cfg.CephFS).To(BeEmpty())
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
