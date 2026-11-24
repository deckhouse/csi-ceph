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

package rbdwatch

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	unwatchedDesc = prometheus.NewDesc(
		"csi_ceph_rbd_volume_without_watch",
		"1 for each node an RBD volume is attached to while its image has fewer watchers than such nodes.",
		[]string{"persistentvolume", "node", "cephclusterconnection", "pool", "rados_namespace", "image"},
		nil,
	)
	checkDesc = prometheus.NewDesc(
		"csi_ceph_rbd_watch_check_success",
		"1 when the last check counted the watchers of every RBD image attached through the CephClusterConnection, 0 when it failed.",
		[]string{"cephclusterconnection"},
		nil,
	)
)

type Collector struct {
	Checker *Checker
}

func (c Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- unwatchedDesc
	ch <- checkDesc
}

func (c Collector) Collect(ch chan<- prometheus.Metric) {
	result := c.Checker.Result()

	for _, u := range result.Unwatched {
		for _, node := range u.Nodes {
			ch <- prometheus.MustNewConstMetric(unwatchedDesc, prometheus.GaugeValue, 1,
				u.PersistentVolume, node, u.Connection, u.Image.Pool, u.Image.RadosNamespace, u.Image.Name)
		}
	}

	for name, ok := range result.Connections {
		value := 0.0
		if ok {
			value = 1
		}
		ch <- prometheus.MustNewConstMetric(checkDesc, prometheus.GaugeValue, value, name)
	}
}
