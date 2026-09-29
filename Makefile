.PHONY: run build test test-integration coverage lint fmt proto-mirror-check docker-build compose-up compose-up-kafka compose-up-signoz compose-down-signoz compose-down compose-logs tidy version hooks smoke help
GO          ?= go
APP         := go-service-template
DOCKER_IMG  ?= go-service-template
VERSION     ?= $(shell cat internal/platform/buildinfo/VERSION 2>/dev/null | tr -d '[:space:]' || echo dev)
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DIRTY       := $(shell git diff --quiet 2>/dev/null || echo "-dirty")
LDFLAGS     := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)$(DIRTY)
COVERAGE_FLOOR ?= 45

help: ; @awk 'BEGIN{FS=":.*##"} /^[a-zA-Z_-]+:.*##/ {printf "  %-18s %s\n",$$1,$$2}' $(MAKEFILE_LIST)
run:               ; @air                                                  ## Run the server locally with hot reload via air (pinned in mise.toml; config in .air.toml)
build:             ; mkdir -p bin && CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(APP) ./cmd/server  ## Compile bin/go-service-template
test:              ; @gotestsum --format=pkgname -- -race -count=1 ./...    ## Run unit tests with race detector (pretty output via gotestsum; pinned in mise.toml)
test-integration:  ; $(GO) test -tags=integration -race -count=1 -timeout=2m ./... ## Run integration tests (compose must be up)
coverage:          ## Run tests with coverage, fail if total < COVERAGE_FLOOR%
	@$(GO) test -race -coverprofile=coverage.out -covermode=atomic ./...
	@$(GO) tool cover -func=coverage.out | tail -1
	@$(GO) tool cover -func=coverage.out | awk -v t=$(COVERAGE_FLOOR) '/^total:/ { gsub("%","",$$3); if ($$3+0 < t) { printf "coverage %s%% < floor %s%%\n", $$3, t; exit 1 } else { printf "coverage %s%% ok (floor %s%%)\n", $$3, t } }'
# coverage-gate: enforce >=85% per file, with explicit bootstrap/integration
# exclusions. Files matching the patterns below are exercised by
# `make test-integration` or by running the binary — not by unit tests. See
# .claude/rules/testing.md §1.1 for the admission rule.
# GATED_INTEGRATION_FILES is the number of files the coverage-integration-gate
# pattern below must match. Without it the gate passes vacuously when the grep
# matches nothing (awk's END loop over an empty array prints "ok"), so a renamed
# file or a dropped integration test would go green.
GATED_INTEGRATION_FILES := 3

coverage-integration-gate: ## Per-file coverage gate (>=30%) on files exercised by integration tests; requires compose-up + integration env vars
	@if [ -z "$$SVC_MONGO_URI" ] || [ -z "$$SVC_VALKEY_ADDR" ]; then \
	  echo "ERROR: SVC_MONGO_URI and SVC_VALKEY_ADDR must be set (run \`make compose-up\` first; mise.local.toml should provide them)"; \
	  exit 1; \
	fi
	@$(GO) test -tags=integration -race -coverpkg=./... -coverprofile=coverage_integration.out -covermode=atomic -timeout=2m ./... >/dev/null
	@# Threshold is 30% (NOT 85%) on purpose: integration coverage is bounded
	@# by which paths the integration tests actually exercise, not by what
	@# unit tests can mock. 30% says "tests genuinely hit the real wire (Mongo
	@# / Valkey)" without forcing theater on edge-case branches.
	@#
	@# Files NOT in the positive-match list below either: (a) have no current
	@# consumer to exercise them (mongox/crud.go's generic helpers — exclude
	@# until a feature uses them), or (b) are bootstrap-only (covered by
	@# coverage-gate's exclusion list, not this one).
	@#
	@# Adding a new integration-tested file? Add it to the pattern below AND
	@# bump GATED_INTEGRATION_FILES.
	@#
	@# Overlap with coverage-gate's exclusion list is EXPECTED, not a bug, and
	@# predates this gate: coverage-gate excludes features' repository_mongo.go
	@# / repository_valkey.go by a role WILDCARD across every feature, and
	@# excludes mongox/{client,index}.go + valkey/client.go by explicit path.
	@# All three files below therefore sit in both lists. Each feature that
	@# lands an integration-tested repository adds it here and bumps
	@# GATED_INTEGRATION_FILES to match.
	@#
	@# The invariant that matters is NOT list-exclusivity (testing.md §1.2's
	@# "one list, never both" was never achievable given that wildcard) but:
	@# every production file has SOME gate as its floor. A file excluded from
	@# the 85% gate must appear here, or it has no floor at all.
	@$(GO) tool cover -func=coverage_integration.out \
	  | grep -E '(/internal/infra/mongox/(client|index)\.go:|/internal/infra/valkey/client\.go:)' \
	  | awk -v want=$(GATED_INTEGRATION_FILES) '{sub(":.*","",$$1); pct=$$3; gsub("%","",pct); sum[$$1]+=pct; n[$$1]++} END {fail=0; got=length(n); if (got!=want) {printf "FAIL: gate matched %d files, expected %d — a gated file stopped being integration-tested, or the pattern drifted\n", got, want; fail=1} for (f in n) {avg=sum[f]/n[f]; if (avg<30.0) {printf "FAIL %.1f%% %s\n", avg, f; fail=1}} if (fail) exit 1; printf "coverage-integration-gate ok: %d/%d gated files >=30%%\n", got, want}'
coverage-gate:     ## Per-file coverage gate (>=85%) skipping bootstrap/integration files
	@$(GO) test -race -coverprofile=coverage.out -covermode=atomic ./... >/dev/null
	@$(GO) tool cover -func=coverage.out \
	  | grep -v '/cmd/[^/]*/main\.go:' \
	  | grep -v '/internal/app/app\.go:' \
	  | grep -v '/internal/features/.*/indexes\.go:' \
	  | grep -v '/internal/features/.*/repository_mongo\.go:' \
	  | grep -v '/internal/features/.*/repository_valkey\.go:' \
	  | grep -v '/internal/features/.*/consumer_kafka\.go:' \
	  | grep -v '/internal/infra/mongox/client\.go:' \
	  | grep -v '/internal/infra/mongox/crud\.go:' \
	  | grep -v '/internal/infra/otelx/setup\.go:' \
	  | grep -v '/internal/infra/otelx/span\.go:' \
	  | grep -v '/internal/infra/valkey/client\.go:' \
	  | grep -v '/internal/platform/validate/validate\.go:' \
	  | grep -v '/internal/infra/kafka/producer\.go:' \
	  | grep -v '/internal/infra/kafka/consumer\.go:' \
	  | grep -v '/internal/infra/mongox/index\.go:' \
	  | grep -v '\.pb\.go:' \
	  | awk 'NR==1 || /^total:/ {next} {sub(":.*","",$$1); pct=$$3; gsub("%","",pct); sum[$$1]+=pct; n[$$1]++} END {fail=0; for (f in n) {avg=sum[f]/n[f]; if (avg<85.0) {printf "FAIL %.1f%% %s\n", avg, f; fail=1}} if (fail) exit 1; print "coverage-gate ok: every non-bootstrap file >=85%"}'
# proto-mirror-check diffs proto/challenger/internalv1 against challenger-service at the commit in
# proto/challenger/VERSION (design §6). The mirror is byte-for-byte, generated code included.
CHALLENGER_SERVICE_DIR ?= ../challenger-service
proto-mirror-check: ## Diff the challenger.internal.v1 mirror against challenger-service (CHALLENGER_SERVICE_DIR, default ../challenger-service)
	@rev=$$(cat proto/challenger/VERSION); fail=0; \
	for f in proto/challenger/internalv1/*; do \
	  git -C $(CHALLENGER_SERVICE_DIR) show "$$rev:$$f" | cmp -s - "$$f" || { echo "DRIFT $$f (vs challenger-service $$rev)"; fail=1; }; \
	done; \
	[ "$$fail" -eq 0 ] && echo "proto mirror matches challenger-service $$rev" || exit 1

smoke:             ; ./scripts/smoke.sh                                    ## Boot throwaway mongo+valkey, run integration gate, start the binary, probe HTTP + graceful shutdown
lint:              ; golangci-lint run                                     ## Run golangci-lint
fmt:               ; gofmt -s -w . && goimports -w -local github.com/Everfit-io/go-service-template .  ## Run gofmt + goimports
docker-build:      ; docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT)$(DIRTY) -t $(DOCKER_IMG):$(VERSION) -t $(DOCKER_IMG):latest .  ## Build Docker image
compose-up:        ; VERSION=$(VERSION) COMMIT=$(COMMIT)$(DIRTY) docker compose -f docker-compose.local.yml up -d --wait --build  ## Start mongo + valkey (fast)
compose-up-kafka:  ; VERSION=$(VERSION) COMMIT=$(COMMIT)$(DIRTY) docker compose -f docker-compose.local.yml --profile kafka up -d --wait --build  ## Start mongo + valkey + Kafka stack (zk + 3 brokers + UI on :8081)
compose-up-signoz: ## Start mongo + valkey + app + full SigNoz stack (UI :8080 — Datadog-like UX, self-hosted, no API key)
	@VERSION=$(VERSION) COMMIT=$(COMMIT)$(DIRTY) docker compose \
	  -f docker-compose.local.yml \
	  -f signoz/docker-compose.yml \
	  -f docker-compose.signoz.yml \
	  up -d --wait --build
compose-down-signoz: ## Stop the SigNoz-flavored stack + remove volumes
	@docker compose -f docker-compose.local.yml -f signoz/docker-compose.yml -f docker-compose.signoz.yml down -v
compose-down:      ; docker compose -f docker-compose.local.yml --profile kafka down -v  ## Stop the base stack (mongo + valkey + app + optional kafka) + remove volumes
compose-logs:      ; docker compose -f docker-compose.local.yml logs -f --tail=200  ## Tail compose logs
tidy:              ; $(GO) mod tidy                                        ## Tidy go.mod / go.sum
version:           ; @echo $(VERSION)                                      ## Print version
hooks:             ; lefthook install                                      ## Install git hooks via lefthook
