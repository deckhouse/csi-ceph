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

package templatetests

import (
	"os"
	"slices"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"helm.sh/helm/v3/pkg/releaseutil"
	"sigs.k8s.io/yaml"
)

const (
	namespace         = "d8-csi-ceph"
	sensitiveKey      = "x-kubernetes-sensitive-data"
	connectionsCRD    = "../crds/storage.deckhouse.io_cephclusterconnections.yaml"
	authenticationCRD = "../crds/storage.deckhouse.io_cephclusterauthentications.yaml"
)

var (
	readVerbs = []string{"get", "list", "watch"}

	writeVerbs = []string{"update", "patch"}

	deckhouseVersions = []string{"1.76.5", "1.78.0"}

	rbacTemplates = []string{
		"templates/controller/rbac-for-us.yaml",
		"templates/rbac-for-us.yaml",
		"templates/user-authz-cluster-roles.yaml",
		"templates/rbacv2/",
	}

	sensitiveResources = []string{"cephclusterconnections/sensitive", "cephclusterauthentications/sensitive"}
)

type object = map[string]any

func TestUserKeysAreSensitive(t *testing.T) {
	assertSensitive(t, connectionsCRD, map[string]bool{
		"userKey":   true,
		"userID":    false,
		"clusterID": false,
		"monitors":  false,
	})
	assertSensitive(t, authenticationCRD, map[string]bool{
		"userKey": true,
		"userID":  false,
	})
}

func TestControllerReadsSensitiveFields(t *testing.T) {
	for _, version := range deckhouseVersions {
		t.Run(version, func(t *testing.T) {
			role := find(t, render(t, version), "ClusterRole", "d8:csi-ceph:controller")
			for _, resource := range sensitiveResources {
				assertVerbs(t, role, resource, readVerbs)
			}
		})
	}
}

func TestControllerGetsUnmaskedWriteResponses(t *testing.T) {
	for _, version := range deckhouseVersions {
		t.Run(version, func(t *testing.T) {
			role := find(t, render(t, version), "ClusterRole", "d8:csi-ceph:controller")
			for _, resource := range []string{"cephclusterconnections/sensitive", "cephclusterauthentications/sensitive"} {
				assertVerbs(t, role, resource, writeVerbs)
			}
		})
	}
}

func TestAdminKubeconfigReadsSensitiveFields(t *testing.T) {
	for _, version := range deckhouseVersions {
		t.Run(version, func(t *testing.T) {
			role := find(t, render(t, version), "ClusterRole", "d8:csi-ceph:admin-kubeconfig")
			for _, resource := range sensitiveResources {
				assertVerbs(t, role, resource, readVerbs)
			}
		})
	}
}

func TestOnlyClusterAdminAndStorageManagerReadSensitiveFields(t *testing.T) {
	for _, version := range deckhouseVersions {
		t.Run(version, func(t *testing.T) {
			readers := map[string]bool{}
			for _, obj := range render(t, version) {
				if obj["kind"] != "ClusterRole" || !isUserRole(obj) {
					continue
				}
				for _, resource := range sensitiveResources {
					if len(granted(obj, resource)) == 0 {
						continue
					}
					role := userRole(obj)
					if role != "ClusterAdmin" && role != "manager" {
						t.Errorf("%s (%s) grants %v on %s", name(obj), role, granted(obj, resource), resource)
						continue
					}
					assertVerbs(t, obj, resource, readVerbs)
					readers[role+" "+resource] = true
				}
			}
			for _, role := range []string{"ClusterAdmin", "manager"} {
				for _, resource := range sensitiveResources {
					if !readers[role+" "+resource] {
						t.Errorf("no %s role grants %s", role, resource)
					}
				}
			}
		})
	}
}

func userRole(obj object) string {
	if level, ok := field(obj, "metadata", "annotations", "user-authz.deckhouse.io/access-level").(string); ok {
		return level
	}
	labels, _ := field(obj, "metadata", "labels").(object)
	for key, value := range labels {
		if strings.HasPrefix(key, "rbac.deckhouse.io/aggregate-to-") {
			s, _ := value.(string)
			return s
		}
	}
	return ""
}

func assertSensitive(t *testing.T, path string, fields map[string]bool) {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var crd object
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	versions, _ := field(crd, "spec", "versions").([]any)
	if len(versions) == 0 {
		t.Fatalf("%s has no versions", path)
	}
	for _, v := range versions {
		version, _ := v.(object)
		if served, ok := version["served"].(bool); ok && !served {
			continue
		}
		properties, _ := field(version, "schema", "openAPIV3Schema", "properties", "spec", "properties").(object)
		for name, sensitive := range fields {
			property, ok := properties[name].(object)
			if !ok {
				t.Errorf("%s %v: spec.%s is missing", path, version["name"], name)
				continue
			}
			if got := property[sensitiveKey] == true; got != sensitive {
				t.Errorf("%s %v: spec.%s has %s=%v, want %v", path, version["name"], name, sensitiveKey, property[sensitiveKey], sensitive)
			}
		}
	}
}

func render(t *testing.T, deckhouseVersion string) []object {
	t.Helper()

	c, err := loader.LoadDir("..")
	if err != nil {
		t.Fatalf("load the chart: %v", err)
	}
	c.Templates = slices.DeleteFunc(c.Templates, func(f *chart.File) bool {
		if strings.HasPrefix(f.Name, "templates/_") {
			return false
		}
		for _, keep := range rbacTemplates {
			if f.Name == keep || strings.HasSuffix(keep, "/") && strings.HasPrefix(f.Name, keep) {
				return false
			}
		}
		return true
	})

	values := object{
		"global": object{
			"deckhouseVersion": deckhouseVersion,
			"enabledModules":   []any{"prometheus", "storage-foundation"},
			"modulesImages": object{
				"registry": object{"base": "registry.example.com/deckhouse", "dockercfg": "e30="},
			},
		},
		"csiCeph": object{
			"registry": object{"base": "registry.example.com/modules/csi-ceph", "dockercfg": "e30="},
			"internal": object{"controllerMetricsProxyPort": 8080},
		},
	}

	renderValues, err := chartutil.ToRenderValues(c, values, chartutil.ReleaseOptions{
		Name:      c.Name(),
		Namespace: namespace,
		IsInstall: true,
	}, chartutil.DefaultCapabilities)
	if err != nil {
		t.Fatalf("build the render values: %v", err)
	}

	files, err := engine.Render(c, renderValues)
	if err != nil {
		t.Fatalf("render the chart: %v", err)
	}

	var objects []object
	for file, content := range files {
		if !strings.HasSuffix(file, ".yaml") {
			continue
		}
		for _, manifest := range releaseutil.SplitManifests(content) {
			var obj object
			if err := yaml.Unmarshal([]byte(manifest), &obj); err != nil {
				t.Fatalf("parse a document of %s: %v\n%s", file, err, manifest)
			}
			if obj != nil {
				objects = append(objects, obj)
			}
		}
	}
	return objects
}

func isUserRole(obj object) bool {
	if _, ok := field(obj, "metadata", "annotations", "user-authz.deckhouse.io/access-level").(string); ok {
		return true
	}
	labels, _ := field(obj, "metadata", "labels").(object)
	for key := range labels {
		if strings.HasPrefix(key, "rbac.deckhouse.io/aggregate-to-") {
			return true
		}
	}
	return false
}

func granted(role object, resource string) []string {
	var verbs []string
	rules, _ := role["rules"].([]any)
	for _, r := range rules {
		rule, _ := r.(object)
		groups, _ := rule["apiGroups"].([]any)
		resources, _ := rule["resources"].([]any)
		if !slices.Contains(groups, any("storage.deckhouse.io")) || !slices.Contains(resources, any(resource)) {
			continue
		}
		if names, _ := rule["resourceNames"].([]any); len(names) > 0 {
			continue
		}
		ruleVerbs, _ := rule["verbs"].([]any)
		for _, v := range ruleVerbs {
			if s, ok := v.(string); ok && !slices.Contains(verbs, s) {
				verbs = append(verbs, s)
			}
		}
	}
	slices.Sort(verbs)
	return verbs
}

func assertVerbs(t *testing.T, role object, resource string, want []string) {
	t.Helper()

	got := granted(role, resource)
	for _, verb := range want {
		if !slices.Contains(got, verb) {
			t.Errorf("%s: %s grants %v, missing %s", name(role), resource, got, verb)
		}
	}
}

func find(t *testing.T, objects []object, kind, objectName string) object {
	t.Helper()

	for _, obj := range objects {
		if obj["kind"] == kind && name(obj) == objectName {
			return obj
		}
	}
	t.Fatalf("%s %s is not rendered", kind, objectName)
	return nil
}

func name(obj object) string {
	s, _ := field(obj, "metadata", "name").(string)
	return s
}

func field(obj object, path ...string) any {
	var current any = obj
	for _, key := range path {
		m, ok := current.(object)
		if !ok {
			return nil
		}
		current = m[key]
	}
	return current
}
