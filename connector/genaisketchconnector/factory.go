// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay Erramilli and Codex
package genaisketchconnector

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
)

var connectorType = component.MustNewType("genaisketch")

func NewFactory() connector.Factory {
	return connector.NewFactory(
		connectorType,
		func() component.Config {
			return defaultConfig()
		},
		connector.WithTracesToMetrics(createTracesToMetrics, component.StabilityLevelDevelopment),
	)
}

func createTracesToMetrics(
	_ context.Context,
	set connector.Settings,
	cfg component.Config,
	next consumer.Metrics,
) (connector.Traces, error) {
	return newTracesConnector(set.TelemetrySettings, cfg.(*Config), next), nil
}
