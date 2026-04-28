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
	go test -v -count=1 -timeout 130m ./tests/

.PHONY: test-focus
test-focus: ## Run a focused Ginkgo spec (make test-focus FOCUS="server=off")
	@if [ -z "$(FOCUS)" ]; then \
		echo "Error: specify FOCUS=<Ginkgo focus string>"; \
		exit 1; \
	fi
	go test -v -count=1 -timeout 130m ./tests/ -ginkgo.focus "$(FOCUS)"

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
	@echo "TEST_CLUSTER_STORAGE_CLASS:       $${TEST_CLUSTER_STORAGE_CLASS}"
	@echo "SSH_HOST:                         $${SSH_HOST}"
	@echo "SSH_USER:                         $${SSH_USER}"
	@echo "SSH_PRIVATE_KEY:                  $${SSH_PRIVATE_KEY}"
	@echo "DKP_LICENSE_KEY:                  $${DKP_LICENSE_KEY:+<set>}"
	@echo "REGISTRY_DOCKER_CFG:              $${REGISTRY_DOCKER_CFG:+<set>}"
	@echo "YAML_CONFIG_FILENAME:             $${YAML_CONFIG_FILENAME}  # default cluster_config.yml"
	@echo ""
	@echo "=== csi-ceph matrix knobs ==="
	@echo "E2E_NAMESPACE:                    $${E2E_NAMESPACE}"
	@echo "E2E_CEPH_STORAGE_CLASS:           $${E2E_CEPH_STORAGE_CLASS}"
	@echo "E2E_PVC_SIZE:                     $${E2E_PVC_SIZE}"
	@echo "E2E_ROOK_NAMESPACE:               $${E2E_ROOK_NAMESPACE}"
	@echo "E2E_ROOK_OSD_STORAGE_CLASS:       $${E2E_ROOK_OSD_STORAGE_CLASS}  # optional; default sds-local-volume-lvm-thick-r1"
	@echo "E2E_ROOK_OSD_COUNT:               $${E2E_ROOK_OSD_COUNT}"
	@echo "E2E_ROOK_OSD_SIZE:                $${E2E_ROOK_OSD_SIZE}"
	@echo "E2E_ROOK_CEPH_IMAGE:              $${E2E_ROOK_CEPH_IMAGE}"
	@echo "E2E_ROOK_CLUSTER_READY_TIMEOUT:   $${E2E_ROOK_CLUSTER_READY_TIMEOUT}"

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
