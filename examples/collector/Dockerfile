# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay Erramilli and Codex
FROM debian:bookworm-slim@sha256:60eac759739651111db372c07be67863818726f754804b8707c90979bda511df

COPY dist/docker/otelcol-genai-sketches /otelcol-genai-sketches
COPY examples/collector/config.yaml /etc/otelcol-genai-sketches.yaml

USER 65532:65532
EXPOSE 4317 8889
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s CMD ["/otelcol-genai-sketches", "--version"]
ENTRYPOINT ["/otelcol-genai-sketches"]
CMD ["--config", "/etc/otelcol-genai-sketches.yaml"]
