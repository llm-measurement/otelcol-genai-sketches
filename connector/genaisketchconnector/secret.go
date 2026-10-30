// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"fmt"
	"os"
	"strings"

	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
)

func validateConnectorSecret(env string) error {
	value := os.Getenv(env)
	if value == "" {
		return sketchhash.ErrEmptySecret
	}
	if len(value) < 32 {
		return fmt.Errorf("%w: collector requires at least 32 bytes", sketchhash.ErrWeakSecret)
	}
	// These public examples and test fixtures are not operator key material.
	switch strings.TrimSpace(value) {
	case "test-secret-32-bytes-for-unit-tests",
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
		"synthetic-trial-only-not-for-customer-data-20260927":
		return fmt.Errorf("%w: replace the published example with a private random key", sketchhash.ErrWeakSecret)
	}
	return nil
}
