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

package config

import (
	"reflect"
	"testing"
)

func TestParseStorageClassLabelIgnoredPrefixes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{name: "empty", in: "", want: nil},
		{name: "single", in: "argocd.argoproj.io/", want: []string{"argocd.argoproj.io/"}},
		{
			name: "multi",
			in:   "argocd.argoproj.io/,helm.toolkit.fluxcd.io/,fleet.cattle.op/",
			want: []string{"argocd.argoproj.io/", "helm.toolkit.fluxcd.io/", "fleet.cattle.op/"},
		},
		{
			name: "whitespace and stray commas",
			in:   " argocd.argoproj.io/ , , helm.toolkit.fluxcd.io/ ,,",
			want: []string{"argocd.argoproj.io/", "helm.toolkit.fluxcd.io/"},
		},
		{name: "only commas", in: ",,,", want: []string{}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseStorageClassLabelIgnoredPrefixes(c.in)
			if c.want == nil {
				if got != nil {
					t.Fatalf("expected nil, got %v", got)
				}
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("expected %v, got %v", c.want, got)
			}
		})
	}
}
