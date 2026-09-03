# Central Collector Example

Create the namespace and secret, then install the chart with `values.yaml` and an
immutable image digest. The complete commands and security choices are in
[Deployment](../../../docs/DEPLOYMENT.md).

Merge `shadow-values.yaml` only when the collector must forward the original traces
to an existing OTLP backend. Replace its endpoint and Secret name first.
