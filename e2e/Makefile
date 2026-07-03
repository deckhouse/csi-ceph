# Makefile for csi-ceph e2e tests.

.PHONY: help
help: ## Show help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

.PHONY: deps
deps: ## Download dependencies
	go mod download
	go mod tidy
	@$(MAKE) fix-mod-permissions

.PHONY: test
test: ## Run full nested-cluster E2E suite
	go test -v -count=1 -timeout 180m ./tests/ -ginkgo.timeout 180m

.PHONY: test-focus
test-focus: ## Run a focused Ginkgo spec (make test-focus FOCUS="RBD PVC")
	@if [ -z "$(FOCUS)" ]; then \
		echo "Error: specify FOCUS=<Ginkgo focus string>"; \
		exit 1; \
	fi
	go test -v -count=1 -timeout 180m ./tests/ -ginkgo.timeout 180m -ginkgo.focus "$(FOCUS)"

.PHONY: vet
vet: ## go vet
	go vet ./...

.PHONY: build
build: ## go build (compile-check only)
	go build ./...

.PHONY: lint
lint: ## golangci-lint (if installed)
	golangci-lint run ./...

.PHONY: check-env
check-env: ## Print relevant env vars for the nested flow
	@echo "=== Nested cluster (storage-e2e) ==="
	@echo "TEST_CLUSTER_CREATE_MODE:         $${TEST_CLUSTER_CREATE_MODE}  # alwaysCreateNew | alwaysUseExisting | commander"
	@echo "TEST_CLUSTER_CLEANUP:             $${TEST_CLUSTER_CLEANUP}      # set true to delete VMs after the run"
	@echo "TEST_CLUSTER_NAMESPACE:           $${TEST_CLUSTER_NAMESPACE}    # also the in-cluster namespace for PVCs/Pods"
	@echo "TEST_CLUSTER_STORAGE_CLASS:       $${TEST_CLUSTER_STORAGE_CLASS}"
	@echo "SSH_HOST:                         $${SSH_HOST}"
	@echo "SSH_USER:                         $${SSH_USER}"
	@echo "SSH_PRIVATE_KEY:                  $${SSH_PRIVATE_KEY}"
	@echo "SSH_PUBLIC_KEY:                   $${SSH_PUBLIC_KEY}  # required for alwaysCreateNew (VM authorized key)"
	@echo "SSH_VM_USER:                      $${SSH_VM_USER}  # required for alwaysCreateNew (SSH user inside the VMs)"
	@echo "DKP_LICENSE_KEY:                  $${DKP_LICENSE_KEY:+<set>}"
	@echo "REGISTRY_DOCKER_CFG:              $${REGISTRY_DOCKER_CFG:+<set>}"
	@echo "CSI_CEPH_MODULE_PULL_OVERRIDE:    $${CSI_CEPH_MODULE_PULL_OVERRIDE}  # overrides modulePullOverride (default main)"
	@echo "YAML_CONFIG_FILENAME:             $${YAML_CONFIG_FILENAME}  # default cluster_config.yml"
	@echo ""
	@echo "=== csi-ceph suite knobs ==="
	@echo "E2E_EC_NAME:                      $${E2E_EC_NAME}"
	@echo "E2E_PVC_SIZE:                     $${E2E_PVC_SIZE}"
	@echo "E2E_OSD_BD_LABEL:                 $${E2E_OSD_BD_LABEL}  # key or key=value"
	@echo "E2E_STORAGE_NODE_LABEL:           $${E2E_STORAGE_NODE_LABEL}  # key or key=value"
	@echo "E2E_NETWORK_PUBLIC:               $${E2E_NETWORK_PUBLIC}"
	@echo "E2E_NETWORK_CLUSTER:              $${E2E_NETWORK_CLUSTER}"
	@echo "E2E_EC_READY_TIMEOUT:             $${E2E_EC_READY_TIMEOUT}"
	@echo "E2E_ESC_READY_TIMEOUT:            $${E2E_ESC_READY_TIMEOUT}"
	@echo "E2E_MODULE_READY_TIMEOUT:         $${E2E_MODULE_READY_TIMEOUT}"
	@echo "E2E_OSD_DISKS_PER_WORKER:         $${E2E_OSD_DISKS_PER_WORKER}"
	@echo "E2E_OSD_DISK_SIZE:                $${E2E_OSD_DISK_SIZE}"
	@echo "E2E_PROBE_IMAGE:                  $${E2E_PROBE_IMAGE}  # image with sh+cat for PVC round-trip pods"
	@echo "E2E_KEEP_CLUSTER_ON_FAILURE:      $${E2E_KEEP_CLUSTER_ON_FAILURE}  # true|1|yes -> skip teardown if a spec failed"

.PHONY: fix-mod-permissions
fix-mod-permissions: ## Make storage-e2e module cache writable (PrepareBootstrapConfig writes temp/ under the module path)
	@GOMODCACHE=$${GOMODCACHE:-$(CURDIR)/.gomodcache}; \
	GOPATH=$${GOPATH:-$$HOME/go}; \
	for root in "$$GOMODCACHE" "$$GOPATH/pkg/mod"; do \
		[ -d "$$root" ] || continue; \
		for dir in "$$root"/github.com/deckhouse/storage-e2e@*; do \
			if [ -d "$$dir" ]; then \
				echo "Fixing permissions for $$dir"; \
				chmod -R +w "$$dir" 2>/dev/null || true; \
				mkdir -p "$$dir/temp/cluster" 2>/dev/null || true; \
			fi; \
		done; \
	done

.DEFAULT_GOAL := help
