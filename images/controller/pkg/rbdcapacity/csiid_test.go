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

package rbdcapacity

import "testing"

func TestParseCSIID(t *testing.T) {
	tests := []struct {
		in   string
		want csiID
		ok   bool
	}{
		{in: "0001-0024-6f0a1b2c-3d4e-5f60-7182-93a4b5c6d7e8-0000000000000002-a1b2c3d4-0000-1111-2222-333344445555", want: csiID{clusterID: "6f0a1b2c-3d4e-5f60-7182-93a4b5c6d7e8", poolID: 2}, ok: true},
		{in: "0001-0009-rook-ceph-000000000000000a-a1b2c3d4-0000-1111-2222-333344445555", want: csiID{clusterID: "rook-ceph", poolID: 10}, ok: true},
		{in: "0001-0009-rook-ceph-000000000000000a-short", ok: false},
		{in: "0001-0009-rook-ceph-00000000000000zz-a1b2c3d4-0000-1111-2222-333344445555", ok: false},
		{in: "0001-00zz-rook-ceph-000000000000000a-a1b2c3d4-0000-1111-2222-333344445555", ok: false},
		{in: "csi-vol-static", ok: false},
		{in: "", ok: false},
	}
	for _, tt := range tests {
		got, ok := parseCSIID(tt.in)
		if ok != tt.ok || got != tt.want {
			t.Errorf("parseCSIID(%q) = %+v, %t; want %+v, %t", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}
