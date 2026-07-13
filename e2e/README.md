# E2E tests for csi-ceph

End-to-end coverage for the csi-ceph RBD and CephFS data path: given a real Ceph
cluster, csi-ceph must provision a `CephClusterConnection` + `CephStorageClass`,
materialise a core Kubernetes `StorageClass`, and let workloads bind PVCs and
round-trip data through the RBD and CephFS CSI drivers.

csi-ceph has no Ceph cluster of its own (it only *connects* to one), so the suite
stands one up through an **sds-elastic `ElasticCluster`** (which vendors Rook and,
via its `ElasticStorageClass`, drives csi-ceph into creating the connection and
storage classes). This is the same substrate `sds-object`'s Heavy profile uses.

1. `storage-e2e` brings up a nested cluster from `tests/cluster_config.yml`
   (1 master + 3 storage workers) — or, in CI, a fresh cluster from the Deckhouse
   Commander template.
2. `BeforeSuite` waits for the csi-ceph module to be Ready, then (in
   `alwaysCreateNew`) attaches raw VirtualDisks to every worker and labels the
   storage nodes + OSD `BlockDevice`s via
   `storage-e2e/pkg/testkit.EnsureElasticOSDBlockDevices`. In the Commander flow
   the template already exposes the raw devices (>=4), so the attach is skipped
   and the suite adopts the pre-provisioned disks.
3. A single shared `ElasticCluster` is created (it bootstraps the vendored Rook
   `CephCluster` — ~15-25 min); an RBD and a CephFS `ElasticStorageClass` are
   declared (each drives csi-ceph into creating a `CephClusterConnection` +
   `CephStorageClass` + core `StorageClass`), then the full volume lifecycle is
   exercised **for both RBD and CephFS** (`lifecycle_test.go`, one ordered block
   per driver, sharing a base PVC):
   - **create** — PVC binds, Pod writes and reads back data;
   - **expand** — PVC resize is honoured (`allowVolumeExpansion`) and data survives;
   - **pod migration** — the Pod is rescheduled onto another node and the volume
     re-attaches with data intact (RBD RWO detach/attach; CephFS RWX);
   - **snapshot + restore** — a `VolumeSnapshot` is taken and restored into a new
     PVC that carries the original data;
   - **clone** — a new PVC is provisioned from the source PVC (`dataSource`,
     which ceph-csi implements via an internal snapshot);
   - **delete** — Pods/PVCs/snapshot are removed and the backing volumes reclaimed.

   Plus two driver-specific specs:
   - **CephFS RWX multi-node** — the same PVC is mounted RW by Pods on two
     different nodes at once and the second Pod reads the first Pod's data;
   - **RBD Block volumeMode** — a raw `volumeMode: Block` PVC is attached as a
     block device (`volumeDevices`) and round-trips a marker written with `dd`.
4. `AfterAll` tears the ElasticStorageClasses + ElasticCluster down; `AfterSuite`
   hands the cluster back to `storage-e2e`.

## msCrcData suite (`mscrcdata_test.go`, `Label("msCrcData")`, opt-in)

A separate, label-gated suite verifies the `msCrcData` openapi option end to end.
It is **excluded from the default run** (`label_filter '!msCrcData'`) and runs only
when the PR carries `e2e/label:msCrcData` (alongside `e2e/commander/run`); under
that label the default lifecycle specs are filtered out and this suite runs alone.

It brings up its **own** ElasticCluster that is *born* CRC-off, then round-trips
RBD and CephFS. In order:

- asserts the default `ms_crc_data = true` in `d8-csi-ceph/ceph-config`, then sets
  `msCrcData=false` on the csi-ceph `ModuleConfig` and verifies `ceph.conf`
  re-renders to `false` and the RBD/CephFS CSI Deployments/DaemonSets roll (their
  `checksum/ceph-config` pod annotation changes) — the client side;
- writes `ms_crc_data = false` into the sds-elastic `rook-config-override`
  **before** the ElasticCluster exists — the server side;
- creates the ElasticCluster (its Ceph daemons boot with `ms_crc_data=false`) and
  the RBD/CephFS ElasticStorageClasses, then round-trips a PVC through each.

The round-trip exercises the kernel clients (krbd map / CephFS kernel mount), which
cannot use msgr2 with CRC disabled. It passes because the csi-ceph controller, when
`msCrcData=false`, pins those StorageClasses to msgr1 via `ms_mode=legacy`
(`mapOptions` for RBD, `kernelMountOptions` for CephFS) — so this suite is also the
end-to-end check of that controller behaviour.

Override the sds-elastic namespace with `E2E_SDS_ELASTIC_NAMESPACE` (default
`d8-sds-elastic`).

> **Why born CRC-off, not a live flip.** `ms_crc_data` is not per-frame tolerated:
> a client/server mismatch is fatal — a one-sided flip makes every connection to
> the mons time out (operator `HEALTH_ERR`, provisioning hangs). And reconfiguring
> a running cluster is destructive: rollout-restarting the Rook PVC-backed OSDs
> (`storageClassDeviceSets`) races Rook's OSD lifecycle (new OSD pod points at a
> deleted data PVC `set1-data-…`, OSDs stuck `Pending`), and changing
> `rook-config-override` live deadlocks the operator. So the suite sets both sides
> to `false` *before* the cluster is created, so every daemon + operator + client
> start matched — no live reconfiguration. (Both failure modes above were observed
> on the live e2e cluster.)

> **Note on k8s external-storage CSI conformance.** The upstream
> `test/e2e/storage/testsuites` suite is *not* used here: it creates and mutates
> its own StorageClasses with the ceph provisioners, which csi-ceph's
> `d8-csi-ceph-sc-validation` webhook forbids by design (StorageClasses must be
> derived from `CephStorageClass` CRs — only the csi-ceph controller SA may
> manage them). Every selected conformance spec is therefore denied at admission.
> The RBD/CephFS PVC round-trips above validate the driver end to end within
> csi-ceph's model; socket-level CSI-spec conformance would need `csi-sanity`
> (which talks to the driver gRPC directly and is not blocked by the webhook).

All Elastic/Rook CR provisioning lives in `storage-e2e/pkg/testkit` and
`storage-e2e/pkg/kubernetes`; the suite imports them via
`require github.com/deckhouse/storage-e2e` in `e2e/go.mod`. The suite does not
import the csi-ceph API module — the csi-ceph CRs are asserted through the
dynamic client.

## Labels

The storage-node and OSD `BlockDevice` selection labels follow the same
`<module>-e2e.storage.deckhouse.io/*` pattern `sds-object` uses, module-scoped so
they never collide with sds-elastic's own e2e labels:

| Purpose            | Label                                              |
| ------------------ | -------------------------------------------------- |
| storage node       | `csi-ceph-e2e.storage.deckhouse.io/storage-node=true` |
| OSD `BlockDevice`  | `csi-ceph-e2e.storage.deckhouse.io/osd=true`          |

Override them with `E2E_STORAGE_NODE_LABEL` / `E2E_OSD_BD_LABEL` (`key` or
`key=value`).

## Why one shared ElasticCluster + Ordered specs

Creating an `ElasticCluster` runs a full Rook bootstrap and is far too slow to
repeat per spec, so the suite uses a **single shared EC** inside one
`Describe(..., Ordered)`. Spec registration goes through the `createSpecs`
builder called from the root container, so the create-before-round-trip order is
explicit. `RandomizeAllSpecs` stays **off**.

## Run modes

The suite only runs against a `storage-e2e` nested cluster; `TEST_CLUSTER_CREATE_MODE`
must be set:

- `alwaysCreateNew` — create fresh VMs (needs the SSH + base-cluster secrets);
  the suite attaches raw OSD disks itself.
- `alwaysUseExisting` — reuse a nested cluster whose disks are pre-provisioned.
- `commander` — the CI path: `.github/workflows/e2e-tests.yml` calls the reusable
  `deckhouse/storage-e2e` pipeline with `cluster_provider: commander`, gated on
  the `e2e/commander/run` PR label.

Run `make check-env` to see every knob, `make test` for the full suite, and
`make test-focus FOCUS="RBD PVC"` for a subset.

## CI

`.github/workflows/e2e-tests.yml` is a thin caller for the reusable
`deckhouse/storage-e2e/.github/workflows/e2e.yml@main` pipeline. It only fires
when the PR carries the `e2e/commander/run` label. Other labels: `e2e/keep-cluster`
(skip teardown), `e2e/label:<suite>` (Ginkgo label filter). The module image
under test is the PR's `pr<N>` tag published by `build_dev`.
