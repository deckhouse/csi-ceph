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
   `CephCluster` — ~15-25 min) and the ordered specs exercise:
   - RBD `ElasticStorageClass` → csi-ceph `CephClusterConnection` +
     `CephStorageClass` + core `StorageClass`, then an RBD PVC + Pod data
     round-trip;
   - CephFS `ElasticStorageClass` → same wiring, then a CephFS PVC + Pod data
     round-trip.
4. `AfterAll` tears the ElasticStorageClasses + ElasticCluster (and their probe
   PVCs/Pods) down; `AfterSuite` hands the cluster back to `storage-e2e`.

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
