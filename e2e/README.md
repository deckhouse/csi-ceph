# E2E tests for csi-ceph

Минимальный e2e flow для проверки параметра `msCrcData`:

1. `storage-e2e` поднимает nested-кластер по `tests/cluster_config.yml`
2. сьют поднимает Ceph через `storage-e2e/pkg/testkit`
3. гоняется короткая матрица `ms_crc_data x msCrcData`
4. сьют удаляет Ceph-стек и отдаёт кластер обратно `storage-e2e`

Весь код провиженинга Rook/Ceph и csi-ceph-CR теперь живёт в
`storage-e2e/pkg/testkit` — csi-ceph e2e импортирует его напрямую.

## Что проверяется

Матрица состоит из трёх обязательных кейсов:

| server `ms_crc_data` | client `msCrcData` | Ожидание |
| -------------------- | ------------------ | -------- |
| `false`              | `false`            | `Bound` + Pod read/write |
| `false`              | `true`             | `NotBound` |
| `true`               | `true`             | `Bound` + Pod read/write |

Каждый кейс делает одно и то же:

1. меняет server-side CRC через `rook-config-override`
2. меняет client-side `ModuleConfig csi-ceph`
3. рестартит `csi-controller-rbd` и `csi-node-rbd`
4. создаёт PVC
5. для успешных кейсов проверяет volume через Pod

## Поддерживаемый запуск

Поддерживается только nested-кластер через `storage-e2e`.
External mode и in-cluster Job больше не поддерживаются.

## Требования

- Go **1.26+**
- базовый Deckhouse-кластер с `virtualization`
- SSH-доступ к мастеру базового кластера
- лицензия Deckhouse и docker config для dev-registry
- блоковый `StorageClass` на базовом кластере для VM-дисков

## Основные переменные

### `storage-e2e`

- `TEST_CLUSTER_CREATE_MODE`:
  `alwaysCreateNew`, `alwaysUseExisting` или `commander`
- `TEST_CLUSTER_CLEANUP`:
  `true`, если после прогона VM нужно удалить
- `TEST_CLUSTER_NAMESPACE`
- `TEST_CLUSTER_STORAGE_CLASS`
- `YAML_CONFIG_FILENAME`:
  по умолчанию `cluster_config.yml`
- `SSH_HOST`, `SSH_USER`, `SSH_PRIVATE_KEY`
- `DKP_LICENSE_KEY`
- `REGISTRY_DOCKER_CFG`

### `csi-ceph e2e`

- `E2E_NAMESPACE`:
  namespace для PVC и Pod, по умолчанию `csi-ceph-e2e`
- `E2E_CEPH_STORAGE_CLASS`:
  имя итогового `StorageClass`, по умолчанию `ceph-rbd-r1`
- `E2E_PVC_SIZE`:
  размер PVC, по умолчанию `1Gi`
- `E2E_ROOK_NAMESPACE`:
  namespace Rook/Ceph, по умолчанию `d8-sds-elastic`
- `E2E_ROOK_OSD_STORAGE_CLASS`:
  core `StorageClass` для OSD PVC, по умолчанию `sds-local-volume-lvm-thick-r1`
- `E2E_ROOK_OSD_COUNT`:
  по умолчанию `1`
- `E2E_ROOK_OSD_SIZE`:
  по умолчанию `20Gi`
- `E2E_ROOK_CEPH_IMAGE`
- `E2E_ROOK_CLUSTER_READY_TIMEOUT`

Если `E2E_ROOK_OSD_STORAGE_CLASS` не задан, suite сам ожидает типичный nested
сценарий: локальная thick-конфигурация будет подготовлена через
`storage-e2e/testkit`, а Ceph будет использован с
`sds-local-volume-lvm-thick-r1`.

## Быстрый старт

```bash
export TEST_CLUSTER_CREATE_MODE=alwaysCreateNew
export TEST_CLUSTER_CLEANUP=true
export TEST_CLUSTER_NAMESPACE=e2e-csi-ceph
export TEST_CLUSTER_STORAGE_CLASS=linstor-r2

export SSH_HOST=<master-ip>
export SSH_USER=<ssh-user>
export SSH_PRIVATE_KEY=~/.ssh/id_rsa

export DKP_LICENSE_KEY=<license>
export REGISTRY_DOCKER_CFG=<base64-docker-config>

cd e2e
make deps
make test
```

Чтобы закрепить конкретный образ `csi-ceph`, отредактируйте
`tests/cluster_config.yml`:

```yaml
- name: "csi-ceph"
  version: 1
  enabled: true
  modulePullOverride: "pr131"
```

Для локальной отладки можно запускать отдельные кейсы:

```bash
make test-focus FOCUS="server=off"
```
