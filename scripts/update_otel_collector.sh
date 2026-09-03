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

current="$(awk '$1 == "OTEL_VERSION" { print $3 }' Makefile)"
if [[ ! "${current}" =~ ^v0\.[0-9]+\.[0-9]+$ ]]; then
  printf 'could not read OTEL_VERSION from Makefile\n' >&2
  exit 1
fi

if [[ "${current}" == "${target}" ]]; then
  printf 'OpenTelemetry Collector is already at %s\n' "${target}"
  exit 0
fi

current_number="${current#v}"
target_number="${target#v}"

grep -Fqx "OTEL_VERSION := ${current}" Makefile
grep -Fq "otelcol_version: ${current_number}" builder.yaml
grep -Fq "go.opentelemetry.io/collector/receiver/otlpreceiver ${current}" builder.yaml
grep -Fq "go.opentelemetry.io/collector/processor/batchprocessor ${current}" builder.yaml
grep -Fq "go.opentelemetry.io/collector/exporter/otlpexporter ${current}" builder.yaml
grep -Fq "go.opentelemetry.io/collector/exporter/otlphttpexporter ${current}" builder.yaml
grep -Fq "github.com/open-telemetry/opentelemetry-collector-contrib/exporter/prometheusexporter ${current}" builder.yaml

sed -E -i.bak "s/^OTEL_VERSION := ${current}$/OTEL_VERSION := ${target}/" Makefile
sed -E -i.bak "s/otelcol_version: ${current_number}/otelcol_version: ${target_number}/" builder.yaml
sed -E -i.bak "s#(go\.opentelemetry\.io/collector/(receiver/otlpreceiver|processor/batchprocessor|exporter/otlpexporter|exporter/otlphttpexporter)) ${current}#\\1 ${target}#g" builder.yaml
sed -E -i.bak "s#(github\.com/open-telemetry/opentelemetry-collector-contrib/exporter/prometheusexporter) ${current}#\\1 ${target}#" builder.yaml
rm -f Makefile.bak builder.yaml.bak

go -C connector/genaisketchconnector get "go.opentelemetry.io/collector/connector@${target}"
make tidy

if grep -Fq "${current}" Makefile builder.yaml connector/genaisketchconnector/go.mod; then
  printf 'old Collector version %s remains in an active dependency file\n' "${current}" >&2
  exit 1
fi

printf 'Updated OpenTelemetry Collector from %s to %s\n' "${current}" "${target}"
