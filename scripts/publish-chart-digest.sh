#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay and Codex
set -euo pipefail

: "${CHART_NAME:?}" "${CHART_VERSION:?}" "${GITHUB_OUTPUT:?}" "${RUNNER_TEMP:?}"
[[ "$CHART_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || exit 1
archive="release/otelcol-genai-sketches-${CHART_VERSION}.tgz"
scratch="$(mktemp -d "${RUNNER_TEMP}/chart-oci.XXXXXX")"
trap 'rm -rf "$scratch"' EXIT
helm show chart "$archive" | GOWORK=off go run ./scripts/collector-version --chart-oci-config > "$scratch/config.json"

# Upload the Helm config and unchanged package, then only the content-addressed manifest.
config_digest="sha256:$(sha256sum "$scratch/config.json" | cut -d ' ' -f 1)"
chart_digest="sha256:$(sha256sum "$archive" | cut -d ' ' -f 1)"
oras blob push "${CHART_NAME}@${config_digest}" "$scratch/config.json"
oras blob push "${CHART_NAME}@${chart_digest}" "$archive"
jq -cn --arg config "$config_digest" --arg chart "$chart_digest" \
  --slurpfile metadata "$scratch/config.json" --arg created "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --argjson config_size "$(wc -c < "$scratch/config.json")" --argjson chart_size "$(wc -c < "$archive")" \
  '($metadata[0]) as $m |
   {schemaVersion:2,mediaType:"application/vnd.oci.image.manifest.v1+json",
    config:{mediaType:"application/vnd.cncf.helm.config.v1+json",digest:$config,size:$config_size},
    layers:[{mediaType:"application/vnd.cncf.helm.chart.content.v1.tar+gzip",digest:$chart,size:$chart_size}],
    annotations: (({
      "org.opencontainers.image.description":$m.description,
      "org.opencontainers.image.url":$m.home,
      "org.opencontainers.image.created":$created,
      "org.opencontainers.image.source":$m.sources[0],
      "org.opencontainers.image.authors":([$m.maintainers[]? |
        (.name // "") + (if (.email // "") != "" then " (" + .email + ")" else "" end)] | join(", "))
    } | with_entries(select(.value != null and (.value | test("\\S"))))) + ($m.annotations // {}) + {
      "org.opencontainers.image.title":$m.name,
      "org.opencontainers.image.version":$m.version
    })}' \
  > "$scratch/manifest.json"
digest="sha256:$(sha256sum "$scratch/manifest.json" | cut -d ' ' -f 1)"
[[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || exit 1
oras manifest push "${CHART_NAME}@${digest}" "$scratch/manifest.json"
test "$(oras resolve "${CHART_NAME}@${digest}")" = "$digest"
printf '%s@%s\n' "$CHART_NAME" "$digest" > release/chart-digest.txt
printf 'digest=%s\n' "$digest" >> "$GITHUB_OUTPUT"
