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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/controller"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/internal"
)

// systemIgnoredPrefixes mirrors the hardcoded list shipped in openapi/values.yaml
// under sdsCsiCeph.internal.storageClassLabelIgnoredPrefixesSystem.
var systemIgnoredPrefixes = []string{
	"app.kubernetes.io/managed-by",
	"app.kubernetes.io/instance",
	"kubernetes.io/",
	"k8s.io/",
	"storage.deckhouse.io/managed-by",
}

// userIgnoredPrefixes mirrors the default list shipped in openapi/config-values.yaml
// under csiCeph.storageClassLabelIgnoredPrefixes.
var userIgnoredPrefixes = []string{
	"argocd.argoproj.io/",
	"kustomize.toolkit.fluxcd.io/",
	"helm.toolkit.fluxcd.io/",
	"fleet.cattle.op/",
}

func ignoredPrefixesUnion() []string {
	return append(append([]string{}, systemIgnoredPrefixes...), userIgnoredPrefixes...)
}

var _ = Describe("CephStorageClass label propagation filtering", func() {
	const (
		controllerNamespace = "test-namespace"
		clusterID           = "test-cluster"
	)

	newCephSC := func(name string, labels map[string]string) *v1alpha1.CephStorageClass {
		return &v1alpha1.CephStorageClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: labels,
			},
			Spec: v1alpha1.CephStorageClassSpec{
				ClusterConnectionName: "ceph-connection",
				ReclaimPolicy:         "Delete",
				Type:                  v1alpha1.CephStorageClassTypeCephFS,
				CephFS: &v1alpha1.CephStorageClassCephFS{
					FSName: "myfs",
				},
			},
		}
	}

	It("propagates allowed labels and enforces managed-by label", func() {
		cephSC := newCephSC("ceph-sc-allowed", map[string]string{
			"team":      "storage",
			"workload":  "prod",
			"some/team": "infra",
		})

		sc := controller.ConfigureStorageClass(cephSC, controllerNamespace, clusterID, ignoredPrefixesUnion())

		Expect(sc.Labels).To(HaveKeyWithValue("team", "storage"))
		Expect(sc.Labels).To(HaveKeyWithValue("workload", "prod"))
		Expect(sc.Labels).To(HaveKeyWithValue("some/team", "infra"))
		Expect(sc.Labels).To(HaveKeyWithValue(internal.StorageManagedLabelKey, controller.CephStorageClassCtrlName))
	})

	It("filters labels matching system and user prefixes", func() {
		cephSC := newCephSC("ceph-sc-filtered", map[string]string{
			"app.kubernetes.io/managed-by":  "argo-cd",
			"app.kubernetes.io/instance":    "csi-ceph-instance",
			"kubernetes.io/region":          "eu-west-1",
			"k8s.io/cluster-name":           "prod",
			"argocd.argoproj.io/secret":     "true",
			"kustomize.toolkit.fluxcd.io/x": "1",
			"helm.toolkit.fluxcd.io/y":      "2",
			"fleet.cattle.op/bundle":        "store",
			"team":                          "storage",
		})

		sc := controller.ConfigureStorageClass(cephSC, controllerNamespace, clusterID, ignoredPrefixesUnion())

		Expect(sc.Labels).To(HaveKeyWithValue("team", "storage"))
		Expect(sc.Labels).To(HaveKeyWithValue(internal.StorageManagedLabelKey, controller.CephStorageClassCtrlName))

		for _, key := range []string{
			"app.kubernetes.io/managed-by",
			"app.kubernetes.io/instance",
			"kubernetes.io/region",
			"k8s.io/cluster-name",
			"argocd.argoproj.io/secret",
			"kustomize.toolkit.fluxcd.io/x",
			"helm.toolkit.fluxcd.io/y",
			"fleet.cattle.op/bundle",
		} {
			Expect(sc.Labels).NotTo(HaveKey(key))
		}
	})

	It("overrides storage.deckhouse.io/managed-by even when CephSC tries to set it", func() {
		cephSC := newCephSC("ceph-sc-managed-by-override", map[string]string{
			"storage.deckhouse.io/managed-by": "bogus-controller",
			"team":                            "storage",
		})

		sc := controller.ConfigureStorageClass(cephSC, controllerNamespace, clusterID, ignoredPrefixesUnion())

		Expect(sc.Labels).To(HaveKeyWithValue(internal.StorageManagedLabelKey, controller.CephStorageClassCtrlName))
		Expect(sc.Labels).To(HaveKeyWithValue("team", "storage"))
	})

	It("handles a CephSC where every label is ignored", func() {
		cephSC := newCephSC("ceph-sc-all-ignored", map[string]string{
			"argocd.argoproj.io/x":         "1",
			"app.kubernetes.io/managed-by": "argo-cd",
		})

		sc := controller.ConfigureStorageClass(cephSC, controllerNamespace, clusterID, ignoredPrefixesUnion())

		Expect(sc.Labels).To(HaveLen(1))
		Expect(sc.Labels).To(HaveKeyWithValue(internal.StorageManagedLabelKey, controller.CephStorageClassCtrlName))
	})

	It("treats an empty prefix in the ignored list as a no-op", func() {
		cephSC := newCephSC("ceph-sc-empty-prefix", map[string]string{
			"team": "storage",
		})

		sc := controller.ConfigureStorageClass(cephSC, controllerNamespace, clusterID, []string{""})

		Expect(sc.Labels).To(HaveKeyWithValue("team", "storage"))
		Expect(sc.Labels).To(HaveKeyWithValue(internal.StorageManagedLabelKey, controller.CephStorageClassCtrlName))
	})

	It("propagates all CephSC labels when no prefixes are provided", func() {
		cephSC := newCephSC("ceph-sc-no-filter", map[string]string{
			"argocd.argoproj.io/x": "1",
			"team":                 "storage",
		})

		sc := controller.ConfigureStorageClass(cephSC, controllerNamespace, clusterID, nil)

		Expect(sc.Labels).To(HaveKeyWithValue("argocd.argoproj.io/x", "1"))
		Expect(sc.Labels).To(HaveKeyWithValue("team", "storage"))
		Expect(sc.Labels).To(HaveKeyWithValue(internal.StorageManagedLabelKey, controller.CephStorageClassCtrlName))
	})
})
