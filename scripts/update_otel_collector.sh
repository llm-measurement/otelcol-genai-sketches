#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay Erramilli and Codex

set -euo pipefail

export GOWORK=off

target="${1:-latest}"
if [[ "${target}" == "latest" ]]; then
  target="$(go list -m -f '{{.Version}}' go.opentelemetry.io/collector/cmd/builder@latest)"
fi

if [[ ! "${target}" =~ ^v0\.[0-9]+\.[0-9]+$ ]]; then
  printf 'expected a stable Collector v0 release, got %q\n' "${target}" >&2
  exit 1
fi

current="$(cat otel.version)"
if [[ ! "${current}" =~ ^v0\.[0-9]+\.[0-9]+$ ]]; then
  printf 'could not read otel.version\n' >&2
  exit 1
fi

if [[ "${current}" == "${target}" ]]; then
  printf 'OpenTelemetry Collector is already at %s\n' "${target}"
  exit 0
fi

go run ./scripts/collector-version "${target}"

go -C connector/genaisketchconnector get "go.opentelemetry.io/collector/connector@${target}"
make tidy

if grep -Fq "${current}" otel.version builder.yaml connector/genaisketchconnector/go.mod; then
  printf 'old Collector version %s remains in an active dependency file\n' "${current}" >&2
  exit 1
fi

printf 'Updated OpenTelemetry Collector from %s to %s\n' "${current}" "${target}"
