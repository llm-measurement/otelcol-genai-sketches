# Central Collector Example

Create the namespace and secret, then install the chart with `values.yaml` and an
immutable image digest. The complete commands and security choices are in
[Deployment](../../../docs/DEPLOYMENT.md).

Merge `shadow-values.yaml` only when the collector must forward the original traces
to an existing OTLP backend. Replace its endpoint and Secret name first, then
uncomment and adapt its backend egress rule for chart 0.7.0. See the
[NetworkPolicy migration](../../../docs/DEPLOYMENT.md#chart-070-network-policy-migration)
for backend and Prometheus peers.
