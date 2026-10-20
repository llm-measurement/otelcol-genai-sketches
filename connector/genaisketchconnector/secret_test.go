// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
)

func setConnectorTestSecret(t testing.TB) {
	t.Helper()
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GENAI_SKETCH_SECRET", hex.EncodeToString(value[:]))
}

func TestStartRejectsWeakAndPublishedSecrets(t *testing.T) {
	for _, value := range []string{
		"", strings.Repeat("x", 16), strings.Repeat("x", 31),
		"test-secret-32-bytes-for-unit-tests",
		"same-secret-32-bytes-for-unit-tests",
		"other-secret-32-bytes-for-unit-tests",
		"synthetic-test-key-0123456789-abcdef",
		"sketchkit-identity-vector-secret-v1",
		"test-key-at-least-32-bytes-123456789",
		"ci-validation-secret-32-bytes-only",
		"smoke-secret-32-bytes-for-ci-only",
		"integration-test-secret-32-bytes",
		"validation-secret-32-bytes-for-local-only",
		"unused-for-compose-inspection-only",
		"synthetic-trial-only-not-for-customer-data-20260927",
	} {
		t.Run("invalid", func(t *testing.T) {
			t.Setenv("GENAI_SKETCH_SECRET", value)
			conn := newTracesConnector(componentTelemetry(), defaultConfig(), &captureMetrics{})
			err := conn.start(false)
			if !errors.Is(err, sketchhash.ErrEmptySecret) && !errors.Is(err, sketchhash.ErrWeakSecret) {
				t.Fatalf("expected safe key refusal, got %v", err)
			}
			if value != "" && strings.Contains(err.Error(), value) {
				t.Fatal("error exposed key material")
			}
			if conn.state != nil || conn.exporter != nil || conn.debugCancel != nil {
				t.Fatal("invalid key started connector state")
			}
		})
	}
}

func TestStartAcceptsPrivateSecretWithoutChangingHash(t *testing.T) {
	setConnectorTestSecret(t)
	conn := newTracesConnector(componentTelemetry(), defaultConfig(), &captureMetrics{})
	if err := conn.start(false); err != nil {
		t.Fatal(err)
	}
	legacy, err := sketchhash.SecretFromEnv("GENAI_SKETCH_SECRET")
	if err != nil {
		t.Fatal(err)
	}
	want, err := sketchhash.Hash64(legacy, sketchhash.UserV1, []byte("private-user"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := sketchhash.Hash64(conn.state.secret, sketchhash.UserV1, []byte("private-user"))
	if err != nil || got != want {
		t.Fatal("startup validation changed keyed hashing")
	}
}
