# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay Erramilli and Codex

ARG GO_IMAGE=golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=0.1.0-dev
WORKDIR /src

COPY go.mod go.sum builder.yaml LICENSE ./
COPY connector/genaisketchconnector/go.mod connector/genaisketchconnector/go.sum ./connector/genaisketchconnector/
COPY connector/genaisketchconnector/*.go ./connector/genaisketchconnector/
COPY connector/genaisketchconnector/LICENSE ./connector/genaisketchconnector/LICENSE

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOWORK=off go build -o /usr/local/bin/go-licenses \
      github.com/haproxytech/go-licenses/v2 && \
    env "dist.version=$VERSION" "dist.output_path=/out" \
      GOWORK=off CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go run go.opentelemetry.io/collector/cmd/builder@v0.160.0 \
      --config=builder.yaml && \
    cp /src/LICENSE /out/LICENSE && \
    cd /out && \
    GOWORK=off CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
      /usr/local/bin/go-licenses save . \
      --save_path=/out/licenses/third-party

FROM ${RUNTIME_IMAGE}
COPY --from=build /out/otelcol-genai-sketches /otelcol-genai-sketches
COPY --from=build /out/licenses/third-party /licenses/third-party
COPY configs/production.yaml /etc/otelcol-genai-sketches/config.yaml
COPY LICENSE /licenses/otelcol-genai-sketches/LICENSE

USER 65532:65532
ENV GENAI_SKETCH_MEMORY_LIMIT_MIB=1843 \
    GENAI_SKETCH_MEMORY_SPIKE_MIB=307
EXPOSE 4317 4318 8889 13133
ENTRYPOINT ["/otelcol-genai-sketches"]
CMD ["--config", "/etc/otelcol-genai-sketches/config.yaml"]
