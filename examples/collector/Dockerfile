# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay Erramilli and Codex
FROM debian:bookworm-slim@sha256:88200866dfff7ea7f5cbcb6ec7c8a701889efe6fe859fe64d6990e4b07ea4171

COPY dist/docker/otelcol-genai-sketches /otelcol-genai-sketches
COPY examples/collector/config.yaml /etc/otelcol-genai-sketches.yaml

USER 65532:65532
EXPOSE 4317 8889
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s CMD ["/otelcol-genai-sketches", "--version"]
ENTRYPOINT ["/otelcol-genai-sketches"]
CMD ["--config", "/etc/otelcol-genai-sketches.yaml"]
