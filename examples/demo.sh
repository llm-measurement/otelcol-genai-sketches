#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay Erramilli and Codex
set -eu

cd "$(dirname "$0")/.."
action=${1:-up}
case "$action" in
  up)
    if [ -z "${GENAI_SKETCH_SECRET:-}" ]; then
      GENAI_SKETCH_SECRET=$(docker run --rm --network=none --read-only \
        --cap-drop=ALL --security-opt=no-new-privileges --user=65532:65532 \
        python:3.14-slim@sha256:b877e50bd90de10af8d82c57a022fc2e0dc731c5320d762a27986facfc3355c1 \
        python -c 'import secrets; print(secrets.token_hex(32))')
    fi
    ;;
  down|logs|ps|investigate)
    GENAI_SKETCH_SECRET=${GENAI_SKETCH_SECRET:-unused-for-compose-inspection-only}
    ;;
  *)
    printf 'Usage: sh examples/demo.sh [up|investigate|logs|ps|down]\n' >&2
    exit 2
    ;;
esac
export GENAI_SKETCH_SECRET

case "$action" in
  up)
    docker compose -f examples/compose.yaml up -d --build
    printf '\nGrafana: http://localhost:%s/d/genai-sketches\n' "${GENAI_DEMO_GRAFANA_PORT:-3000}"
    printf 'Allow one minute for rates and the first top-k snapshot.\n'
    ;;
  investigate)
    for service in collector prometheus; do
      if [ -z "$(docker compose -f examples/compose.yaml ps --status running -q "$service")" ]; then
        printf 'Start the demo first: sh examples/demo.sh up\n' >&2
        exit 1
      fi
    done
    docker compose -f examples/compose.yaml run --rm -T --build --no-deps app python /app/investigate.py
    ;;
  down) docker compose -f examples/compose.yaml down -v ;;
  logs) docker compose -f examples/compose.yaml logs collector ;;
  ps) docker compose -f examples/compose.yaml ps ;;
esac
