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
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe(controller.CephClusterAuthenticationCtrlName, func() {
	const (
		controllerNamespace          = "test-namespace"
		nameForClusterAuthentication = "cluster-authentication"
		userID                       = "admin"
		userKey                      = "key"
		newUserID                    = "admin2"
		newUserKey                   = "key2"
		secretNamePrefix             = internal.CephClusterAuthenticationSecretPrefix
	)

	var (
		ctx = context.Background()
		cl  = NewFakeClient()
		log = logger.Logger{}
	)

	It("CephClusterAuthentication positive operations", func() {
		cephClusterAuthentication := &v1alpha1.CephClusterAuthentication{
			ObjectMeta: metav1.ObjectMeta{
				Name: nameForClusterAuthentication,
			},
			Spec: v1alpha1.CephClusterAuthenticationSpec{
				UserID:  userID,
				UserKey: userKey,
			},
		}

		By("Creating CephClusterAuthentication")
		err := cl.Create(ctx, cephClusterAuthentication)
		Expect(err).NotTo(HaveOccurred())

		createdCephClusterAuthentication := &v1alpha1.CephClusterAuthentication{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForClusterAuthentication}, createdCephClusterAuthentication)
		Expect(err).NotTo(HaveOccurred())
		Expect(createdCephClusterAuthentication).NotTo(BeNil())
		Expect(createdCephClusterAuthentication.Name).To(Equal(nameForClusterAuthentication))
		Expect(createdCephClusterAuthentication.Spec.UserID).To(Equal(userID))
		Expect(createdCephClusterAuthentication.Spec.UserKey).To(Equal(userKey))
		Expect(createdCephClusterAuthentication.Finalizers).To(HaveLen(0))

		By("Running reconcile for CephClusterAuthentication creation")
		secretList := &corev1.SecretList{}
		err = cl.List(ctx, secretList)
		Expect(err).NotTo(HaveOccurred())

		shouldReconcile, _, err := controller.RunCephClusterAuthenticationEventReconcile(ctx, cl, log, secretList, createdCephClusterAuthentication, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying dependent Secret")
		verifySecret(ctx, cl, cephClusterAuthentication, controllerNamespace)

		By("Verifying CephClusterAuthentication after create reconcile")
		createdCephClusterAuthentication = &v1alpha1.CephClusterAuthentication{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForClusterAuthentication}, createdCephClusterAuthentication)
		Expect(err).NotTo(HaveOccurred())
		Expect(createdCephClusterAuthentication).NotTo(BeNil())
		Expect(createdCephClusterAuthentication.Finalizers).To(HaveLen(1))
		Expect(createdCephClusterAuthentication.Finalizers).To(ContainElement(controller.CephClusterAuthenticationControllerFinalizerName))
		// Expect(createdCephClusterAuthentication.Status).NotTo(BeNil())
		// Expect(createdCephClusterAuthentication.Status.Phase).To(Equal(v1alpha1.PhaseCreated))

		By("Updating CephClusterAuthentication")
		createdCephClusterAuthentication.Spec.UserID = newUserID
		createdCephClusterAuthentication.Spec.UserKey = newUserKey
		err = cl.Update(ctx, createdCephClusterAuthentication)
		Expect(err).NotTo(HaveOccurred())

		updatedCephClusterAuthentication := &v1alpha1.CephClusterAuthentication{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForClusterAuthentication}, updatedCephClusterAuthentication)
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedCephClusterAuthentication).NotTo(BeNil())
		Expect(updatedCephClusterAuthentication.Spec.UserID).To(Equal(newUserID))
		Expect(updatedCephClusterAuthentication.Spec.UserKey).To(Equal(newUserKey))

		By("Running reconcile for CephClusterAuthentication update")
		secretList = &corev1.SecretList{}
		err = cl.List(ctx, secretList)
		Expect(err).NotTo(HaveOccurred())

		shouldReconcile, _, err = controller.RunCephClusterAuthenticationEventReconcile(ctx, cl, log, secretList, updatedCephClusterAuthentication, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying updated Secret")
		verifySecret(ctx, cl, updatedCephClusterAuthentication, controllerNamespace)

		By("Verifying CephClusterAuthentication after update reconcile")
		updatedCephClusterAuthentication = &v1alpha1.CephClusterAuthentication{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForClusterAuthentication}, updatedCephClusterAuthentication)
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedCephClusterAuthentication).NotTo(BeNil())
		Expect(updatedCephClusterAuthentication.Finalizers).To(HaveLen(1))
		Expect(updatedCephClusterAuthentication.Finalizers).To(ContainElement(controller.CephClusterAuthenticationControllerFinalizerName))
		// Expect(updatedCephClusterAuthentication.Status).NotTo(BeNil())
		// Expect(updatedCephClusterAuthentication.Status.Phase).To(Equal(v1alpha1.PhaseCreated))

		By("Deleting CephClusterAuthentication")
		err = cl.Delete(ctx, cephClusterAuthentication)
		Expect(err).NotTo(HaveOccurred())

		By("Running reconcile for CephClusterAuthentication deletion")
		secretList = &corev1.SecretList{}
		err = cl.List(ctx, secretList)
		Expect(err).NotTo(HaveOccurred())

		deletedCephClusterAuthentication := &v1alpha1.CephClusterAuthentication{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForClusterAuthentication}, deletedCephClusterAuthentication)
		Expect(err).NotTo(HaveOccurred())
		Expect(deletedCephClusterAuthentication).NotTo(BeNil())
		Expect(deletedCephClusterAuthentication.Finalizers).To(HaveLen(1))
		Expect(deletedCephClusterAuthentication.Finalizers).To(ContainElement(controller.CephClusterAuthenticationControllerFinalizerName))

		shouldReconcile, _, err = controller.RunCephClusterAuthenticationEventReconcile(ctx, cl, log, secretList, deletedCephClusterAuthentication, controllerNamespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying Secret deletion")
		verifySecretNotExists(ctx, cl, cephClusterAuthentication, controllerNamespace)

		By("Verifying CephClusterAuthentication after delete reconcile")
		deletedCephClusterAuthentication = &v1alpha1.CephClusterAuthentication{}
		err = cl.Get(ctx, client.ObjectKey{Name: nameForClusterAuthentication}, deletedCephClusterAuthentication)
		Expect(k8serrors.IsNotFound(err)).To(BeTrue())
	})

	It("handles invalid CephClusterAuthentication spec", func() {
		By("Creating CephClusterAuthentication with empty UserID")
		cephClusterAuthentication := &v1alpha1.CephClusterAuthentication{
			ObjectMeta: metav1.ObjectMeta{
				Name: nameForClusterAuthentication,
			},
			Spec: v1alpha1.CephClusterAuthenticationSpec{
				UserID:  "",
				UserKey: userKey,
			},
		}

		err := cl.Create(ctx, cephClusterAuthentication)
		Expect(err).NotTo(HaveOccurred())

		By("Running reconcile for invalid CephClusterAuthentication")
		secretList := &corev1.SecretList{}
		err = cl.List(ctx, secretList)
		Expect(err).NotTo(HaveOccurred())

		shouldReconcile, _, err := controller.RunCephClusterAuthenticationEventReconcile(ctx, cl, log, secretList, cephClusterAuthentication, controllerNamespace)
		Expect(err).To(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying no Secret created for invalid CephClusterAuthentication")
		verifySecretNotExists(ctx, cl, cephClusterAuthentication, controllerNamespace)

		By("Creating CephClusterAuthentication with empty UserKey")
		cephClusterAuthentication.Spec.UserID = userID
		cephClusterAuthentication.Spec.UserKey = ""

		err = cl.Update(ctx, cephClusterAuthentication)
		Expect(err).NotTo(HaveOccurred())
		Expect(cephClusterAuthentication.Spec.UserKey).To(BeEmpty())

		By("Running reconcile for CephClusterAuthentication with empty UserKey")
		shouldReconcile, _, err = controller.RunCephClusterAuthenticationEventReconcile(ctx, cl, log, secretList, cephClusterAuthentication, controllerNamespace)
		Expect(err).To(HaveOccurred())
		Expect(shouldReconcile).To(BeFalse())

		By("Verifying no Secret created for CephClusterAuthentication with empty UserKey")
		verifySecretNotExists(ctx, cl, cephClusterAuthentication, controllerNamespace)
	})
})

func verifySecret(ctx context.Context, cl client.Client, cephClusterAuthentication *v1alpha1.CephClusterAuthentication, controllerNamespace string) {
	secretName := internal.CephClusterAuthenticationSecretPrefix + cephClusterAuthentication.Name
	secret := &corev1.Secret{}
	err := cl.Get(ctx, client.ObjectKey{Name: secretName, Namespace: controllerNamespace}, secret)
	Expect(err).NotTo(HaveOccurred())
	Expect(secret).NotTo(BeNil())
	Expect(secret.Finalizers).To(HaveLen(1))
	Expect(secret.Finalizers).To(ContainElement(controller.CephClusterAuthenticationControllerFinalizerName))
	Expect(secret.StringData).To(HaveKeyWithValue("userID", cephClusterAuthentication.Spec.UserID))
	Expect(secret.StringData).To(HaveKeyWithValue("userKey", cephClusterAuthentication.Spec.UserKey))
	Expect(secret.StringData).To(HaveKeyWithValue("adminID", cephClusterAuthentication.Spec.UserID))
	Expect(secret.StringData).To(HaveKeyWithValue("adminKey", cephClusterAuthentication.Spec.UserKey))
}

func verifySecretNotExists(ctx context.Context, cl client.Client, cephClusterAuthentication *v1alpha1.CephClusterAuthentication, controllerNamespace string) {
	secretName := internal.CephClusterAuthenticationSecretPrefix + cephClusterAuthentication.Name
	secret := &corev1.Secret{}
	err := cl.Get(ctx, client.ObjectKey{Name: secretName, Namespace: controllerNamespace}, secret)
	Expect(k8serrors.IsNotFound(err)).To(BeTrue())
}
