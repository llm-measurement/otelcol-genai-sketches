OTEL_VERSION := v0.155.0
DIST_BINARY := dist/otelcol-genai-sketches
DOCKER_DIST_DIR := dist/docker
DOCKER_GOOS ?= linux
DOCKER_GOARCH ?= $(shell go env GOARCH)
BUILDER_BIN := $(CURDIR)/.cache/bin
BUILDER := $(BUILDER_BIN)/builder
GOCACHE ?= $(CURDIR)/.cache/go-build
GOMODCACHE ?= $(CURDIR)/.cache/go-mod
SOAK_DURATION ?= 60m
SOAK_RATE_PER_SEC ?= 10000
SOAK_BATCH_SIZE ?= 1000
SOAK_TIMEOUT ?= 2h
SOAK_RSS_BOUND_MIB ?= 1536
SOAK_RSS_MAX_SLOPE_KIB_PER_MIN ?= 1024

.PHONY: tidy
tidy:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go mod tidy
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go -C connector/genaisketchconnector mod tidy

.PHONY: check
check:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go test ./...
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go -C connector/genaisketchconnector test ./...

CONNECTOR_SOURCES := $(wildcard connector/genaisketchconnector/*.go) connector/genaisketchconnector/go.mod connector/genaisketchconnector/go.sum

.PHONY: dist
dist: $(DIST_BINARY)

$(DIST_BINARY): Makefile builder.yaml $(CONNECTOR_SOURCES)
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go run go.opentelemetry.io/collector/cmd/builder@$(OTEL_VERSION) --config=builder.yaml

$(BUILDER): Makefile
	mkdir -p $(BUILDER_BIN)
	GOBIN=$(BUILDER_BIN) GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go install go.opentelemetry.io/collector/cmd/builder@$(OTEL_VERSION)

.PHONY: dist-docker
dist-docker: $(BUILDER)
	mkdir -p $(dir $(DOCKER_DIST_DIR))
	GOOS=$(DOCKER_GOOS) GOARCH=$(DOCKER_GOARCH) GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) $(BUILDER) --config=builder.yaml --output-path=$(DOCKER_DIST_DIR)

.PHONY: test-integration
test-integration: $(DIST_BINARY)
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go test -tags integration ./integration -count=1

.PHONY: run-local
run-local: dist
	GENAI_SKETCH_SECRET=$${GENAI_SKETCH_SECRET:?set GENAI_SKETCH_SECRET to a strong local secret} ./$(DIST_BINARY) --config=configs/local.yaml

.PHONY: example-up
example-up: dist-docker
	docker compose -f examples/compose.yaml up -d --build

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
	docker compose -f examples/compose.yaml down -v
