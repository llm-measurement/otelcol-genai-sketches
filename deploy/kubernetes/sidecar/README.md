# Sidecar Example

Merge `sidecar-container.yaml` into an application Deployment, replace the image tag
with a verified digest, and point the application's OTLP exporter at
`http://127.0.0.1:4317`. The image's default production configuration listens on the
pod network and publishes Prometheus metrics on port `8889`.

Create `genai-sketch-secret` in the application's namespace. Annotate the pod for
Prometheus scraping or add a Service and ServiceMonitor that selects the workload.
Do not expose the metrics or health ports outside the cluster.

The 1 GiB sidecar limit is only a conservative starting point for a lower-volume,
pod-local stream. Measure the workload before rollout. Sidecar sketches are
independent per pod and cannot be added as HLL estimates in Prometheus.
