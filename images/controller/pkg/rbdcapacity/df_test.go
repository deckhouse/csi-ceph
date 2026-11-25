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

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

const dfDetail = `{
  "stats": {"total_bytes": 322122547200, "total_avail_bytes": 300647710720, "total_used_bytes": 21474836480},
  "stats_by_class": {"hdd": {"total_bytes": 322122547200}},
  "pools": [
    {"name": ".mgr", "id": 1, "stats": {"stored": 1000, "objects": 2, "max_avail": 95000000000, "percent_used": 0, "quota_objects": 0, "quota_bytes": 0}},
    {"name": "rbd", "id": 2, "stats": {"stored": 6442450944, "stored_data": 6442450944, "objects": 1600, "kb_used": 18874368, "bytes_used": 19327352832, "percent_used": 0.06, "max_avail": 94489280512, "quota_objects": 0, "quota_bytes": 0}},
    {"name": "fast", "id": 3, "stats": {"stored": 1073741824, "objects": 256, "max_avail": 10737418240, "percent_used": 0.03, "quota_objects": 0, "quota_bytes": 53687091200}}
  ]
}`

func TestParseDFTakesTheListedPools(t *testing.T) {
	got, err := parseDF([]byte(dfDetail), []string{"fast", "rbd"})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]*PoolStats{
		"rbd":  {ID: 2, Stored: 6442450944, MaxAvail: 94489280512},
		"fast": {ID: 3, Stored: 1073741824, MaxAvail: 10737418240, Quota: 53687091200},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("parseDF() mismatch (-want +got):\n%s", diff)
	}
}

func TestParseDFMarksAMissingPool(t *testing.T) {
	got, err := parseDF([]byte(dfDetail), []string{"gone"})
	if err != nil {
		t.Fatal(err)
	}

	s, ok := got["gone"]
	if !ok || s != nil {
		t.Errorf("got[gone] = %v, %t; want nil, true", s, ok)
	}
}

func TestParseDFRejectsOutputWithoutDetail(t *testing.T) {
	_, err := parseDF([]byte(`{"pools":[{"name":"rbd","id":2,"stats":{"stored":1,"max_avail":2}}]}`), []string{"rbd"})
	if err == nil {
		t.Fatal("parseDF() error = nil; want an error for output without quota_bytes")
	}
}

func TestParseDFRejectsGarbage(t *testing.T) {
	if _, err := parseDF([]byte("not json"), []string{"rbd"}); err == nil {
		t.Fatal("parseDF() error = nil; want a parse error")
	}
}
