# E2E tests for csi-ceph

End-to-end coverage for the `msCrcData` setting:

1. `storage-e2e` brings up a nested cluster from `tests/cluster_config.yml`.
2. The suite provisions Ceph through `storage-e2e/pkg/testkit` — first the
   RBD stack (`CephCluster` + `CephBlockPool` + `CephStorageClass` of type
   RBD), then a CephFS stack on top of the same `CephCluster`
   (`CephFilesystem` + `CephStorageClass` of type CephFS).
3. The `protocol x ms_crc_data x msCrcData` matrix is executed.
4. The suite tears the CephFS and RBD stacks down and hands the cluster
   back to `storage-e2e`.

All Rook/Ceph and csi-ceph CR provisioning lives in
`storage-e2e/pkg/testkit`; csi-ceph e2e imports it directly via
`require github.com/deckhouse/storage-e2e` in `e2e/go.mod`. If you need
unreleased testkit changes during local development, add a `replace`
directive to your working copy — but it must not be committed to the
repository.

## What is tested

The matrix has three mandatory cells, executed independently for RBD
and CephFS:

| protocol | server `ms_crc_data` | client `msCrcData` | Expectation |
| -------- | -------------------- | ------------------ | ----------- |
| RBD      | `false`              | `false`            | `Bound` + Pod read/write |
| RBD      | `false`              | `true`             | `NotBound` |
| RBD      | `true`               | `true`             | `Bound` + Pod read/write |
| CephFS   | `false`              | `false`            | `Bound` + Pod read/write |
| CephFS   | `false`              | `true`             | `NotBound` |
| CephFS   | `true`               | `true`             | `Bound` + Pod read/write |

The `server=true, client=false` cell is intentionally skipped: the
regression lives in the asymmetric `server=false, client=true` case,
and the two matched-state cells cover the property that explicitly
turning CRC on or off does not break provisioning.

Each cell goes through the same steps:

1. Snapshot the `checksum/ceph-config` annotation on
   `csi-controller-{rbd,cephfs}` and `csi-node-{rbd,cephfs}`.
2. Flip server-side CRC via `rook-config-override`.
3. Flip client-side CRC via `ModuleConfig csi-ceph`
   (`spec.settings.msCrcData`).
4. Wait until csi-ceph regenerates the `ConfigMap ceph-config`
   (`ms_crc_data = false` appears or disappears).
5. Wait for an **automatic** rollout of all four workloads driven by a
   change in the `checksum/ceph-config` pod-template annotation
   (computed as `sha256` of `templates/configmap.yaml`, see csi-ceph
   commit `42032d9`); the test no longer issues `kubectl rollout
   restart`.
6. Create a PVC against the appropriate `StorageClass` (RBD or CephFS).
7. For the success cells, validate the volume from a Pod (write a
   marker file, then read it back with `cat`).

## Supported run mode

Only the nested-cluster mode driven by `storage-e2e` is supported.
External mode and the in-cluster Job runner have been removed.

## Requirements

- Go **1.26+**
- A base Deckhouse cluster with the `virtualization` module enabled.
- SSH access to the master node of the base cluster.
- A Deckhouse license and a docker config for the dev registry.
- A block-mode `StorageClass` on the base cluster for VM disks.

## Environment variables

### `storage-e2e`

- `TEST_CLUSTER_CREATE_MODE`:
  one of `alwaysCreateNew`, `alwaysUseExisting`, `commander`.
- `TEST_CLUSTER_CLEANUP`:
  set to `true` to delete the VMs after the run.
- `TEST_CLUSTER_NAMESPACE`
- `TEST_CLUSTER_STORAGE_CLASS`
- `YAML_CONFIG_FILENAME`:
  defaults to `cluster_config.yml`.
- `SSH_HOST`, `SSH_USER`, `SSH_PRIVATE_KEY`
- `DKP_LICENSE_KEY`
- `REGISTRY_DOCKER_CFG`

### `csi-ceph e2e`

- `MODULE_IMAGE_TAG`:
  expanded into `modulePullOverride` in `tests/cluster_config.yml`. Set
  to `prN` on GitHub, `mrN` on GitLab, or `main` for nightly builds.
  storage-e2e fails fast at config-load time if the variable is unset.

- `E2E_NAMESPACE`:
  namespace for PVCs and Pods, defaults to `csi-ceph-e2e`.
- `E2E_CEPH_STORAGE_CLASS`:
  name of the resulting RBD `StorageClass`, defaults to `ceph-rbd-r1`.
- `E2E_CEPHFS_STORAGE_CLASS`:
  name of the resulting CephFS `StorageClass`, defaults to `ceph-fs`.
- `E2E_PVC_SIZE`:
  PVC size, defaults to `1Gi`.
- `E2E_ROOK_NAMESPACE`:
  Rook/Ceph namespace, defaults to `d8-sds-elastic`.
- `E2E_ROOK_OSD_STORAGE_CLASS`:
  base `StorageClass` for OSD PVCs, defaults to
  `sds-local-volume-lvm-thick-r1`.
- `E2E_ROOK_OSD_COUNT`:
  defaults to `1`.
- `E2E_ROOK_OSD_SIZE`:
  defaults to `10Gi`.
- `E2E_ROOK_CEPH_IMAGE`
- `E2E_ROOK_CLUSTER_READY_TIMEOUT`

If `E2E_ROOK_OSD_STORAGE_CLASS` is empty the suite assumes the typical
nested layout: a local thick configuration is set up via
`storage-e2e/testkit` and Ceph runs on top of
`sds-local-volume-lvm-thick-r1`.

## Quick start

### Run using a new cluster (VM creation)

```bash
export TEST_CLUSTER_CREATE_MODE=alwaysCreateNew
export TEST_CLUSTER_CLEANUP=true
export TEST_CLUSTER_NAMESPACE=e2e-csi-ceph
export TEST_CLUSTER_STORAGE_CLASS=linstor-r2

export SSH_HOST=<master-ip> # Hypervisor master IP
export SSH_USER=<ssh-user> # Hypervisor SSH user
export SSH_PRIVATE_KEY=~/.ssh/id_rsa # Hypervisor SSH private key
export SSH_PUBLIC_KEY=~/.ssh/id_rsa.pub # Hypervisor SSH public key
export SSH_PASSPHRASE=<passphrase> # Hypervisor SSH passphrase (optional but required for non-interactive mode with encrypted keys)

export DKP_LICENSE_KEY=<license> # Deckhouse Platform license key (used to create a new cluster)
export REGISTRY_DOCKER_CFG=<base64-docker-config> # Docker registry credentials for downloading images from Deckhouse registry

export MODULE_IMAGE_TAG=main   # or prN / mrN to test a specific PR/MR

cd e2e
make deps
make test
```

### Run using an existing cluster (no VM creation)

```bash
export TEST_CLUSTER_CREATE_MODE=alwaysUseExisting
export TEST_CLUSTER_CLEANUP=true
export TEST_CLUSTER_NAMESPACE=e2e-csi-ceph
export TEST_CLUSTER_STORAGE_CLASS=linstor-r2

export SSH_HOST=<master-ip> # Test cluster master IP (when TEST_CLUSTER_CREATE_MODE=alwaysUseExisting)
export SSH_USER=<ssh-user> # Test cluster SSH user
export SSH_PRIVATE_KEY=~/.ssh/id_rsa # Test cluster SSH private key
export SSH_PUBLIC_KEY=~/.ssh/id_rsa.pub # Test cluster SSH public key
export SSH_PASSPHRASE=<passphrase> # Test cluster SSH passphrase (optional but required for non-interactive mode with encrypted keys)

export SSH_JUMP_HOST=<jump-host-ip> # Jump host IP
export SSH_JUMP_USER=<jump-host-user> # Jump host SSH user

export MODULE_IMAGE_TAG=main   # or prN / mrN to test a specific PR/MR

cd e2e
make deps
make test
```

### ??

`tests/cluster_config.yml` pins the csi-ceph image via the
`MODULE_IMAGE_TAG` environment variable:

```yaml
- name: "csi-ceph"
  version: 1
  enabled: true
  modulePullOverride: "${MODULE_IMAGE_TAG}"
```

To target a specific PR or MR build, override the variable on the
command line, for example:

```bash
MODULE_IMAGE_TAG=pr131 make test
```

For local debugging you can run a single matrix cell:

```bash
make test-focus FOCUS="server=off"
```
