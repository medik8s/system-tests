
# CI uses a non-writable home dir, make sure .cache is writable
ifeq ("${HOME}", "/")
HOME=/tmp
endif

BASE_IMG ?= system-tests
BASE_TAG ?= latest

## Tool Versions
GINKGO_VERSION ?= $(shell grep 'github.com/onsi/ginkgo/v2 ' go.mod | awk '{print $$2}')

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
GINKGO_DIR ?= $(LOCALBIN)/ginkgo
GINKGO = $(GINKGO_DIR)/$(GINKGO_VERSION)/ginkgo

GO_PACKAGES=$(shell go list ./...)
.PHONY: lint \
        deps-update \
        vet
vet:
	go vet ${GO_PACKAGES}

lint:
	@echo "Running go lint"
	scripts/golangci-lint.sh

deps-update:
	go mod tidy

# ECO_GOINFRA_BRANCH: optional branch to sync from (e.g. release-4.20). Empty = default branch.
ECO_GOINFRA_BRANCH ?=
sync-eco-goinfra:
ifneq ($(ECO_GOINFRA_BRANCH),)
	go get github.com/rh-ecosystem-edge/eco-goinfra@$(ECO_GOINFRA_BRANCH)
else
	go get github.com/rh-ecosystem-edge/eco-goinfra
endif
	go mod tidy

.PHONY: ginkgo
ginkgo: $(LOCALBIN) ## Download ginkgo locally if necessary.
	$(call go-install-tool,$(GINKGO),$(GINKGO_DIR),github.com/onsi/ginkgo/v2/ginkgo@${GINKGO_VERSION})

# go-install-tool will delete old package $2, then 'go install' any package $3 to $1.
define go-install-tool
@[ -f $(1) ] || { \
	set -e; \
	rm -rf $(2); \
	TMP_DIR=$$(mktemp -d); \
	cd $$TMP_DIR; \
	go mod init tmp; \
	BIN_DIR=$$(dirname $(1)); \
	mkdir -p $$BIN_DIR; \
	echo "Downloading $(3)"; \
	GOBIN=$$BIN_DIR GOFLAGS='' go install $(3); \
	rm -rf $$TMP_DIR; \
}
endef

build-docker-image:
	@echo "Building docker image"
	podman build -t "${BASE_IMG}:${BASE_TAG}" -f Dockerfile

install: deps-update ginkgo
	@echo "Installing needed dependencies"

run-tests: ginkgo
	@echo "Executing test-runner script"
	GINKGO=$(GINKGO) scripts/test-runner.sh

run-internal-pkg-unit-tests:
	@echo "Executing internal package unit tests"
	UNIT_TEST=true go test -v ./tests/internal/...

# Note: To add more unit tests for more packages, add corresponding targets here
test: run-internal-pkg-unit-tests

coverage-html: test
	go tool cover -html cover.out
