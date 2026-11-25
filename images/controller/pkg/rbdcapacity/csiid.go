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
	"encoding/hex"
	"strconv"
)

const csiIDUUIDLength = 36

type csiID struct {
	clusterID string
	poolID    int64
}

func parseCSIID(s string) (csiID, bool) {
	if len(s) < 10 || s[4] != '-' || s[9] != '-' {
		return csiID{}, false
	}
	if _, err := hex.DecodeString(s[0:4]); err != nil {
		return csiID{}, false
	}
	clusterIDLength, err := strconv.ParseUint(s[5:9], 16, 16)
	if err != nil {
		return csiID{}, false
	}
	rest := s[10:]
	if uint64(len(rest)) != clusterIDLength+1+16+1+csiIDUUIDLength {
		return csiID{}, false
	}
	clusterID := rest[:clusterIDLength]
	rest = rest[clusterIDLength:]
	if rest[0] != '-' || rest[17] != '-' {
		return csiID{}, false
	}
	poolID, err := strconv.ParseUint(rest[1:17], 16, 64)
	if err != nil {
		return csiID{}, false
	}

	return csiID{clusterID: clusterID, poolID: int64(poolID)}, true
}
