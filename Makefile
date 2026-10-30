# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
OTEL_VERSION := $(shell cat otel.version)
ALLOY_IMAGE := grafana/alloy:v1.18.0@sha256:491b0578c04983fd54fe99b587b6fab4404dc46d0dc16677bd6b00cc1140b308
PROMETHEUS_IMAGE := prom/prometheus:v3.7.3@sha256:49214755b6153f90a597adcbff0252cc61069f8ab69ce8411285cd4a560e8038
DIST_BINARY := dist/otelcol-genai-sketches
VERSION ?=
DIST_MANIFEST := .cache/distribution-builder.yaml
DOCKER_GOARCH ?= $(shell go env GOARCH)
BUILDER_BIN := $(CURDIR)/.cache/bin
GOCACHE ?= $(CURDIR)/.cache/go-build
GOMODCACHE ?= $(CURDIR)/.cache/go-mod
SOAK_DURATION ?= 60m
SOAK_RATE_PER_SEC ?= 10000
SOAK_BATCH_SIZE ?= 1000
SOAK_TIMEOUT ?= 2h
SOAK_RSS_BOUND_MIB ?= 1536
SOAK_RSS_MAX_SLOPE_KIB_PER_MIN ?= 1024
PRODUCTION_IMAGE ?= otelcol-genai-sketches:local
HELM ?= helm
GO_LICENSES := $(BUILDER_BIN)/go-licenses

.PHONY: tidy
tidy:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go mod tidy
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go -C connector/genaisketchconnector mod tidy

.PHONY: check
check:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go test ./...
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go -C connector/genaisketchconnector test ./...

.PHONY: license-check
license-check: $(DIST_BINARY) $(GO_LICENSES)
	cp LICENSE dist/LICENSE
	GOWORK=off $(GO_LICENSES) check ./...
	cd connector/genaisketchconnector && GOWORK=off $(GO_LICENSES) check ./...
	cd dist && GOWORK=off $(GO_LICENSES) check ./...

CONNECTOR_SOURCES := $(wildcard connector/genaisketchconnector/*.go) connector/genaisketchconnector/go.mod connector/genaisketchconnector/go.sum

.PHONY: dist
dist: $(DIST_BINARY)

.PHONY: force-dist-manifest
force-dist-manifest:

$(DIST_MANIFEST): force-dist-manifest builder.yaml $(wildcard scripts/collector-version/*.go)
	mkdir -p .cache
	GOWORK=off GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go run ./scripts/collector-version --distribution "$(VERSION)" $(DIST_MANIFEST)

$(DIST_BINARY): Makefile otel.version $(DIST_MANIFEST) $(CONNECTOR_SOURCES)
	GOWORK=off GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go run go.opentelemetry.io/collector/cmd/builder@$(OTEL_VERSION) --config=$(DIST_MANIFEST)

$(GO_LICENSES): Makefile go.mod go.sum
	mkdir -p $(BUILDER_BIN)
	GOWORK=off GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go build -o $(GO_LICENSES) github.com/haproxytech/go-licenses/v2

.PHONY: validate-shadow-configs
validate-shadow-configs: $(DIST_BINARY)
	GENAI_SKETCH_SECRET=validation-secret-32-bytes-for-local-only EXISTING_OTLP_GRPC_ENDPOINT=127.0.0.1:5317 EXISTING_OTLP_GRPC_INSECURE=true ./$(DIST_BINARY) validate --config=examples/shadow-mode/collector-grpc.yaml
	GENAI_SKETCH_SECRET=validation-secret-32-bytes-for-local-only EXISTING_OTLP_HTTP_ENDPOINT=http://127.0.0.1:5318 EXISTING_OTLP_AUTHORIZATION="Bearer validation-only" ./$(DIST_BINARY) validate --config=examples/shadow-mode/collector-http.yaml
	GENAI_SKETCH_SECRET=validation-secret-32-bytes-for-local-only LANGFUSE_OTLP_ENDPOINT=https://cloud.langfuse.com/api/public/otel LANGFUSE_AUTH_STRING=validation-only ./$(DIST_BINARY) validate --config=examples/shadow-mode/langfuse.yaml
	GENAI_SKETCH_SECRET=validation-secret-32-bytes-for-local-only ./$(DIST_BINARY) validate --config=examples/shadow-mode/sketches-only.yaml
	EXISTING_OTLP_GRPC_ENDPOINT=127.0.0.1:5317 EXISTING_OTLP_GRPC_INSECURE=true ./$(DIST_BINARY) validate --config=examples/shadow-mode/upstream-collector.yaml

.PHONY: validate-alloy-config
validate-alloy-config:
	docker run --rm --network=none --read-only --cap-drop=ALL --security-opt=no-new-privileges -e EXISTING_OTLP_GRPC_ENDPOINT=collector.example.net:4317 -v $(CURDIR)/examples/shadow-mode/alloy-sidecar.alloy:/etc/alloy/config.alloy:ro $(ALLOY_IMAGE) validate /etc/alloy/config.alloy

.PHONY: test-integration
test-integration: validate-shadow-configs
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go test -tags integration ./integration -count=1

.PHONY: run-local
run-local: dist
	GENAI_SKETCH_SECRET=$${GENAI_SKETCH_SECRET:?set GENAI_SKETCH_SECRET to a strong local secret} ./$(DIST_BINARY) --config=configs/local.yaml

.PHONY: example-up
example-up:
	sh examples/demo.sh up

.PHONY: load
load:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go -C connector/genaisketchconnector test -tags load -run TestLoad -bench BenchmarkLoad -benchmem -count=1

.PHONY: soak
soak: $(DIST_BINARY)
	SOAK_DURATION=$(SOAK_DURATION) SOAK_RATE_PER_SEC=$(SOAK_RATE_PER_SEC) SOAK_BATCH_SIZE=$(SOAK_BATCH_SIZE) SOAK_RSS_BOUND_MIB=$(SOAK_RSS_BOUND_MIB) SOAK_RSS_MAX_SLOPE_KIB_PER_MIN=$(SOAK_RSS_MAX_SLOPE_KIB_PER_MIN) GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go test -tags "integration soak" ./integration -run TestSustainedSoak -count=1 -timeout $(SOAK_TIMEOUT) -v

.PHONY: soak-fleet
soak-fleet: $(DIST_BINARY)
	SOAK_DURATION=$(SOAK_DURATION) SOAK_RATE_PER_SEC=$(SOAK_RATE_PER_SEC) SOAK_BATCH_SIZE=$(SOAK_BATCH_SIZE) SOAK_RSS_BOUND_MIB=$(SOAK_RSS_BOUND_MIB) SOAK_RSS_MAX_SLOPE_KIB_PER_MIN=$(SOAK_RSS_MAX_SLOPE_KIB_PER_MIN) GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go test -tags "integration soak" ./integration -run TestFleetSoak -count=1 -timeout $(SOAK_TIMEOUT) -v

.PHONY: example-down
example-down:
	sh examples/demo.sh down

.PHONY: example-investigate
example-investigate:
	sh examples/demo.sh investigate

.PHONY: production-image
production-image:
	docker buildx build --load --platform=linux/$(DOCKER_GOARCH) --build-arg VERSION="$(VERSION)" -f packaging/docker/Dockerfile -t $(PRODUCTION_IMAGE) .

.PHONY: helm-check
helm-check:
	HELM=$(HELM) GOWORK=off GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go test . -run TestHelmTopKKeys -count=1
	$(HELM) lint deploy/helm/otelcol-genai-sketches
	$(HELM) template genai-sketches deploy/helm/otelcol-genai-sketches --namespace observability > /dev/null
	$(HELM) template genai-sketches deploy/helm/otelcol-genai-sketches --namespace observability --set connector.topK=0 > /dev/null
	$(HELM) template genai-sketches deploy/helm/otelcol-genai-sketches --namespace observability --set-json 'connector.topKKeys=[{"field":"prompt_key"},{"field":"user_key"},{"field":"session_key","weight":"requests"}]' > /dev/null
	# Render-only backend address example; deployments replace it with the backend's approved IP.
	$(HELM) template genai-sketches deploy/helm/otelcol-genai-sketches --namespace observability --set receiverTLS.enabled=true --set receiverTLS.existingSecret=receiver-tls --set shadow.enabled=true --set shadow.endpoint=collector.example.net:4317 --set serviceMonitor.enabled=true --set prometheusRule.enabled=true --set connector.dedup.enabled=true --set networkPolicy.enabled=true --set podDisruptionBudget.enabled=true --set-json 'networkPolicy.egress=[{"to":[{"ipBlock":{"cidr":"192.0.2.10/32"}}],"ports":[{"protocol":"TCP","port":4317}]}]' --set-json 'networkPolicy.metricsFrom=[{"podSelector":{"matchLabels":{"app":"prometheus"}}}]' > /dev/null
	@set -eu; log=$$(mktemp); trap 'rm -f "$$log"' EXIT HUP INT TERM; \
	if $(HELM) template guard-shadow deploy/helm/otelcol-genai-sketches --set shadow.enabled=true --set shadow.endpoint=collector.example.net:4317 > "$$log" 2>&1; then cat "$$log"; exit 1; else status=$$?; fi; \
	grep -F 'shadow.enabled requires networkPolicy.egress for the backend' "$$log"; \
	printf 'PASS shadow guard: helm exit %s\n' "$$status"
	@set -eu; log=$$(mktemp); trap 'rm -f "$$log"' EXIT HUP INT TERM; \
	if $(HELM) template guard-metrics deploy/helm/otelcol-genai-sketches --set serviceMonitor.enabled=true > "$$log" 2>&1; then cat "$$log"; exit 1; else status=$$?; fi; \
	grep -F 'serviceMonitor.enabled requires networkPolicy.metricsFrom or networkPolicy.ingress' "$$log"; \
	printf 'PASS ServiceMonitor guard: helm exit %s\n' "$$status"

.PHONY: prometheus-rule-check
prometheus-rule-check:
	mkdir -p .cache
	$(HELM) template genai-sketches deploy/helm/otelcol-genai-sketches --namespace observability --set prometheusRule.enabled=true --set connector.dedup.enabled=true --show-only templates/prometheusrule.yaml | awk 'BEGIN{emit=0} /^  groups:/{emit=1} emit{sub(/^  /, ""); print}' > .cache/accounting-rules.yaml
	docker run --rm --network=none --read-only --cap-drop=ALL --security-opt=no-new-privileges --user=65534:65534 --entrypoint=/bin/promtool -v $(CURDIR)/.cache/accounting-rules.yaml:/rules.yaml:ro $(PROMETHEUS_IMAGE) check rules /rules.yaml

TOKEN_BUDGET_PROMTOOL = docker run --rm --network=none --read-only --tmpfs /tmp:rw,noexec,nosuid,size=256m --cap-drop=ALL --security-opt=no-new-privileges --user=$(shell id -u):$(shell id -g) --entrypoint=/bin/promtool -v "$(CURDIR):/repo:ro" -w /repo $(PROMETHEUS_IMAGE)

.PHONY: token-budget-check
token-budget-check:
	$(TOKEN_BUDGET_PROMTOOL) check rules examples/prometheus/token-budget/budgets.example.yml examples/prometheus/token-budget/rules.yml
	$(TOKEN_BUDGET_PROMTOOL) test rules examples/prometheus/token-budget/rules.test.yml

.PHONY: token-budget-replay
token-budget-replay: token-budget-check dist
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go test -tags integration ./integration -run '^TestTokenBudgetRecipeReplay$$' -count=1 -v
	$(TOKEN_BUDGET_PROMTOOL) test rules .cache/token-budget/replay.test.yml
