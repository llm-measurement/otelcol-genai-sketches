//go:build integration

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay Erramilli and Codex

package integration

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tracecollectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	_ "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/protobuf/proto"
)

type recordingTraceSink struct {
	tracecollectorpb.UnimplementedTraceServiceServer
	received chan *tracecollectorpb.ExportTraceServiceRequest
}

func (sink *recordingTraceSink) Export(
	_ context.Context,
	request *tracecollectorpb.ExportTraceServiceRequest,
) (*tracecollectorpb.ExportTraceServiceResponse, error) {
	sink.received <- proto.Clone(request).(*tracecollectorpb.ExportTraceServiceRequest)
	return &tracecollectorpb.ExportTraceServiceResponse{}, nil
}

func TestShadowModeForwardsTracesAndDerivesMetrics(t *testing.T) {
	repo := repoRoot(t)
	binary := filepath.Join(repo, "dist", "otelcol-genai-sketches")
	requireExecutable(t, binary)

	t.Run("otlp_grpc", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen for OTLP backend: %v", err)
		}
		server := grpc.NewServer()
		sink := &recordingTraceSink{
			received: make(chan *tracecollectorpb.ExportTraceServiceRequest, 1),
		}
		tracecollectorpb.RegisterTraceServiceServer(server, sink)
		go func() {
			_ = server.Serve(listener)
		}()
		t.Cleanup(server.Stop)

		exporter := fmt.Sprintf(`  otlp_grpc/existing:
    endpoint: %s
    tls:
      insecure: true`, listener.Addr().String())
		runShadowModeForwardingTest(t, binary, exporter, "otlp_grpc/existing", sink.received)
	})

	t.Run("otlp_http", func(t *testing.T) {
		received := make(chan *tracecollectorpb.ExportTraceServiceRequest, 1)
		requestErrors := make(chan error, 1)
		backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/v1/traces" {
				requestErrors <- fmt.Errorf("OTLP HTTP path = %q, want /v1/traces", request.URL.Path)
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			if got := request.Header.Get("Authorization"); got != "Basic integration-auth" {
				requestErrors <- fmt.Errorf("authorization header = %q", got)
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			if got := request.Header.Get("X-Langfuse-Ingestion-Version"); got != "4" {
				requestErrors <- fmt.Errorf("x-langfuse-ingestion-version header = %q", got)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			var bodyReader io.Reader = request.Body
			if request.Header.Get("Content-Encoding") == "gzip" {
				compressed, err := gzip.NewReader(request.Body)
				if err != nil {
					requestErrors <- fmt.Errorf("open compressed OTLP HTTP body: %w", err)
					writer.WriteHeader(http.StatusBadRequest)
					return
				}
				defer compressed.Close()
				bodyReader = compressed
			}
			body, err := io.ReadAll(bodyReader)
			if err != nil {
				requestErrors <- fmt.Errorf("read OTLP HTTP body: %w", err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			var exported tracecollectorpb.ExportTraceServiceRequest
			if err := proto.Unmarshal(body, &exported); err != nil {
				requestErrors <- fmt.Errorf("decode OTLP HTTP body: %w", err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			received <- &exported
			response, err := proto.Marshal(&tracecollectorpb.ExportTraceServiceResponse{})
			if err != nil {
				requestErrors <- fmt.Errorf("encode OTLP HTTP response: %w", err)
				writer.WriteHeader(http.StatusInternalServerError)
				return
			}
			writer.Header().Set("Content-Type", "application/x-protobuf")
			_, _ = writer.Write(response)
		}))
		defer backend.Close()

		exporter := fmt.Sprintf(`  otlp_http/existing:
    endpoint: %s
    headers:
      authorization: "Basic integration-auth"
      x-langfuse-ingestion-version: "4"`, backend.URL)
		runShadowModeForwardingTest(t, binary, exporter, "otlp_http/existing", received)

		select {
		case err := <-requestErrors:
			t.Fatal(err)
		default:
		}
	})
}

func runShadowModeForwardingTest(
	t *testing.T,
	binary string,
	exporterConfig string,
	exporterName string,
	received <-chan *tracecollectorpb.ExportTraceServiceRequest,
) {
	t.Helper()

	otlpPort := freePort(t)
	promPort := freePort(t)
	configPath := writeShadowModeConfig(t, otlpPort, promPort, exporterConfig, exporterName)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, output := startCollector(t, ctx, binary, configPath)
	waitForProcessAlive(t, cmd, output)

	want := traceRequest(otlpSpanSpec{
		Operation:    "chat",
		Model:        "shadow-model",
		Team:         "platform",
		User:         "SHADOW_USER_SENTINEL",
		Prompt:       "SHADOW_PROMPT_SENTINEL",
		InputTokens:  intPtr(11),
		OutputTokens: intPtr(7),
	})
	sendTraceRequestEventually(t, otlpPort, want)

	var got *tracecollectorpb.ExportTraceServiceRequest
	select {
	case got = <-received:
	case <-time.After(15 * time.Second):
		t.Fatalf("forwarded trace did not reach %s; collector output:\n%s", exporterName, output.String())
	}
	normalizeEmptyTraceFields(got)
	normalizeEmptyTraceFields(want)
	if !proto.Equal(got, want) {
		t.Fatalf("forwarded OTLP trace changed:\n got: %s\nwant: %s", got, want)
	}

	metrics := scrapeEventually(t, promPort, regexp.MustCompile(
		`(?m)^gen_ai_sketch_requests_total\{[^}]*slice_value="gen_ai\.request\.model=shadow-model"[^}]*\}\s+1$`,
	))
	for _, sentinel := range []string{"SHADOW_USER_SENTINEL", "SHADOW_PROMPT_SENTINEL"} {
		if strings.Contains(metrics, sentinel) {
			t.Fatalf("sentinel %q leaked into Prometheus metrics or labels:\n%s", sentinel, metrics)
		}
	}
}

func normalizeEmptyTraceFields(request *tracecollectorpb.ExportTraceServiceRequest) {
	for _, resourceSpans := range request.ResourceSpans {
		for _, scopeSpans := range resourceSpans.ScopeSpans {
			if scopeSpans.Scope != nil && proto.Size(scopeSpans.Scope) == 0 {
				scopeSpans.Scope = nil
			}
			for _, span := range scopeSpans.Spans {
				if span.Status != nil && proto.Size(span.Status) == 0 {
					span.Status = nil
				}
			}
		}
	}
}

func writeShadowModeConfig(
	t *testing.T,
	otlpPort int,
	promPort int,
	exporterConfig string,
	exporterName string,
) string {
	t.Helper()

	config := fmt.Sprintf(`receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 127.0.0.1:%d
processors:
  batch:
    timeout: 100ms
connectors:
  genaisketch: {}
exporters:
  prometheus:
    endpoint: 127.0.0.1:%d
    translation_strategy: UnderscoreEscapingWithoutSuffixes
%s
service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [genaisketch, %s]
    metrics:
      receivers: [genaisketch]
      exporters: [prometheus]
`, otlpPort, promPort, exporterConfig, exporterName)

	path := filepath.Join(t.TempDir(), "shadow-mode.yaml")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("write shadow-mode config: %v", err)
	}
	return path
}

func sendTraceRequestEventually(
	t *testing.T,
	port int,
	request *tracecollectorpb.ExportTraceServiceRequest,
) {
	t.Helper()

	address := fmt.Sprintf("127.0.0.1:%d", port)
	eventually(t, 15*time.Second, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		connection, err := grpc.DialContext(
			ctx,
			address,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithBlock(),
		)
		if err != nil {
			return err
		}
		defer connection.Close()

		_, err = tracecollectorpb.NewTraceServiceClient(connection).Export(ctx, request)
		return err
	})
}
