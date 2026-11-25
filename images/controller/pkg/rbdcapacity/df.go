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
	"encoding/json"
	"fmt"
)

const dfCommand = `{"prefix":"df","detail":"detail","format":"json"}`

type dfOutput struct {
	Pools []struct {
		Name  string `json:"name"`
		ID    int64  `json:"id"`
		Stats struct {
			Stored     int64  `json:"stored"`
			MaxAvail   int64  `json:"max_avail"`
			QuotaBytes *int64 `json:"quota_bytes"`
		} `json:"stats"`
	} `json:"pools"`
}

func parseDF(data []byte, pools []string) (map[string]*PoolStats, error) {
	var out dfOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("unable to parse the output of ceph df: %w", err)
	}

	stats := make(map[string]*PoolStats, len(pools))
	for _, pool := range pools {
		stats[pool] = nil
	}
	for _, p := range out.Pools {
		if _, ok := stats[p.Name]; !ok {
			continue
		}
		if p.Stats.QuotaBytes == nil {
			return nil, fmt.Errorf("the output of ceph df has no quota_bytes for the pool %s: the command was not run with detail", p.Name)
		}
		stats[p.Name] = &PoolStats{
			ID:       p.ID,
			Stored:   p.Stats.Stored,
			MaxAvail: p.Stats.MaxAvail,
			Quota:    *p.Stats.QuotaBytes,
		}
	}

	return stats, nil
}
