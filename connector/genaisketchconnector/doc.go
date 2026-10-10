// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Package genaisketchconnector tracks LLM token usage and distinct activity
// without per-user or per-session metric labels. It connects GenAI traces to
// Prometheus metrics, keyed contributor rankings and optional window summaries.
// Missing token usage is counted separately, and an existing trace backend can
// keep receiving the original spans through Collector fan-out.
package genaisketchconnector
