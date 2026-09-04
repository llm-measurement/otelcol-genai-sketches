# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay Erramilli and Codex
FROM python:3.14-slim@sha256:b877e50bd90de10af8d82c57a022fc2e0dc731c5320d762a27986facfc3355c1

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1
WORKDIR /app
COPY examples/app/requirements.txt /app/requirements.txt
RUN pip install --no-cache-dir -r /app/requirements.txt
COPY examples/app/*.py /app/

USER 65532:65532
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s CMD ["python", "-c", "import importlib.util; raise SystemExit(0 if importlib.util.find_spec('opentelemetry') else 1)"]
CMD ["python", "/app/app.py"]
