# Checks and demo entry points. `make ci` runs every check CI runs
# (.github/workflows/ci.yml calls these same targets); none of them talks
# to a cluster. `make up` and `make down` drive the kind demo cluster.
#
# Works with the GNU Make 3.81 that ships with macOS.

WATCHER_DIR   := k8s-watcher
DASHBOARD_DIR := demo-cluster/dashboard
CHART         := charts/k8s-watcher
SCRIPTS_DIR   := demo-cluster/scripts
DEMO_VALUES   := demo-cluster/helm/watcher-values.yaml

# The images, release and namespace the demo uses (see demo-cluster/scripts/up.sh).
IMAGE           ?= k8s-watcher:dev
DASHBOARD_IMAGE ?= demo-dashboard:demo
HELM_RELEASE    := k8s-watcher
HELM_NS         := k8sgpt-system

GO            ?= go
GOLANGCI_LINT ?= golangci-lint
HELM          ?= helm
SHELLCHECK    ?= shellcheck
DOCKER        ?= docker
NODE          ?= node

GOVULNCHECK_VERSION ?= v1.8.0

# Value combinations `helm-lint` renders on top of the chart defaults, one
# `--set` list each.
HELM_TEMPLATE_SETS := \
	ev.enabled=true,cm.enabled=true \
	ev.enabled=true,cm.enabled=false \
	ev.enabled=false,cm.enabled=true \
	ev.probeEnrichment.enabled=false \
	serviceMonitor.enabled=true \
	dryRun=true
# Values validate.yaml must refuse.
HELM_INVALID_SETS := \
	ev.enabled=false,cm.enabled=false \
	replicaCount=2

.DEFAULT_GOAL := help

.PHONY: help ci build vet test test-js lint vuln fmt tidy helm-lint shellcheck image up down clean

help: ## Show the targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-10s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

ci: fmt tidy build vet test test-js lint vuln helm-lint shellcheck image ## Run every CI check (no cluster needed)

# The dashboard module is a single main package: without -o, `go build ./...`
# would leave its binary in the source directory.
build: ## Compile the watcher (binary in k8s-watcher/bin/watcher) and the demo dashboard
	cd $(WATCHER_DIR) && $(GO) build ./... && $(GO) build -o bin/watcher ./cmd/watcher
	cd $(DASHBOARD_DIR) && $(GO) build -o /dev/null ./...

vet: ## go vet the watcher and the demo dashboard
	cd $(WATCHER_DIR) && $(GO) vet ./...
	cd $(DASHBOARD_DIR) && $(GO) vet ./...

test: ## Run the watcher and demo dashboard Go tests with the race detector
	cd $(WATCHER_DIR) && $(GO) test -race ./...
	cd $(DASHBOARD_DIR) && $(GO) test -race ./...

test-js: ## Run the JavaScript tests of the demo dashboard (node --test)
	cd $(DASHBOARD_DIR) && $(NODE) --test jstest/*.test.mjs

lint: ## Run golangci-lint on the watcher and the demo dashboard (config: .golangci.yml)
	cd $(WATCHER_DIR) && $(GOLANGCI_LINT) run --config ../.golangci.yml ./...
	cd $(DASHBOARD_DIR) && $(GOLANGCI_LINT) run --config ../../.golangci.yml ./...

vuln: ## Scan the watcher and the demo dashboard for known vulnerabilities (govulncheck)
	cd $(WATCHER_DIR) && $(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...
	cd $(DASHBOARD_DIR) && $(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

fmt: ## Check Go formatting (fix with: gofmt -w k8s-watcher demo-cluster/dashboard)
	@files=$$(gofmt -l $(WATCHER_DIR) $(DASHBOARD_DIR)) || exit 1; \
	if [ -n "$$files" ]; then \
		echo "gofmt: these files need formatting (run: gofmt -w $(WATCHER_DIR) $(DASHBOARD_DIR)):"; \
		echo "$$files"; \
		exit 1; \
	fi; \
	echo "gofmt: ok"

tidy: ## Check that go.mod and go.sum are tidy (fix with: go mod tidy in the module)
	cd $(WATCHER_DIR) && $(GO) mod tidy -diff
	cd $(DASHBOARD_DIR) && $(GO) mod tidy -diff

helm-lint: ## Lint the watcher chart and render it per lane combination
	$(HELM) lint --strict $(CHART)
	$(HELM) lint --strict $(CHART) -f $(DEMO_VALUES)
	$(HELM) template $(HELM_RELEASE) $(CHART) -n $(HELM_NS) > /dev/null
	@for set in $(HELM_TEMPLATE_SETS); do \
		echo "$(HELM) template $(HELM_RELEASE) $(CHART) -n $(HELM_NS) --set $$set"; \
		$(HELM) template $(HELM_RELEASE) $(CHART) -n $(HELM_NS) --set "$$set" > /dev/null || exit 1; \
	done
	$(HELM) template $(HELM_RELEASE) $(CHART) -n $(HELM_NS) -f $(DEMO_VALUES) > /dev/null
	@for set in $(HELM_INVALID_SETS); do \
		if err=$$($(HELM) template $(HELM_RELEASE) $(CHART) -n $(HELM_NS) --set "$$set" 2>&1 > /dev/null); then \
			echo "helm template --set $$set rendered, but validate.yaml should refuse it"; \
			exit 1; \
		fi; \
		case "$$err" in \
			*"watcher: "*) echo "helm template --set $$set: refused, as expected";; \
			*) echo "$$err"; exit 1;; \
		esac; \
	done

# shellcheck runs from the scripts directory so `# shellcheck source=lib.sh`
# resolves.
shellcheck: ## Syntax-check (bash -n) and shellcheck the demo scripts
	@cd $(SCRIPTS_DIR) && for f in *.sh; do bash -n "$$f" || exit 1; done; echo "bash -n: ok"
	cd $(SCRIPTS_DIR) && $(SHELLCHECK) -x *.sh

image: ## Build the watcher and demo dashboard images; never pushed
	$(DOCKER) build -t $(IMAGE) $(WATCHER_DIR)
	$(DOCKER) build -t $(DASHBOARD_IMAGE) $(DASHBOARD_DIR)

up: ## Create or update the kind demo cluster (demo-cluster/scripts/up.sh)
	$(SCRIPTS_DIR)/up.sh

down: ## Delete the kind demo cluster (demo-cluster/scripts/down.sh)
	$(SCRIPTS_DIR)/down.sh

clean: ## Remove build output
	rm -rf $(WATCHER_DIR)/bin
