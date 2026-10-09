// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	sketchcanon "github.com/llm-measurement/llm-sketchkit/go/sketchkit/canon"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
)

type identityDriftCase struct {
	ID        string `json:"id"`
	Input     string `json:"input_utf8_hex"`
	Canonical string `json:"canonical_hex"`
	Digest    string `json:"digest_hex"`
}

type identityDriftCorpus struct {
	Schema     int    `json:"schema_version"`
	Unicode    string `json:"unicode_version"`
	Profile    string `json:"profile"`
	Domain     string `json:"domain"`
	Algorithm  string `json:"hash_algorithm"`
	Key        string `json:"key_utf8"`
	Provenance struct {
		Count int `json:"case_count"`
	} `json:"provenance"`
	Cases []identityDriftCase `json:"cases"`
}

func loadIdentityDriftCorpus(t *testing.T, baseline string) identityDriftCorpus {
	t.Helper()
	if baseline != "connector" && baseline != "distribution" {
		t.Fatalf("unknown identity baseline %q", baseline)
	}
	data, err := os.ReadFile(filepath.Join("testdata", "identity", baseline+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus identityDriftCorpus
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Schema != 1 || corpus.Unicode != "15.0.0" || corpus.Profile != "text_v1" ||
		corpus.Domain != "prompt:v1" || corpus.Algorithm != "hmac_sha256_64" ||
		corpus.Key != "sketchkit-identity-vector-secret-v1" || len(corpus.Cases) < 8192 ||
		len(corpus.Cases) != corpus.Provenance.Count {
		t.Fatal("invalid identity corpus header or case count")
	}
	seen := make(map[string]bool, len(corpus.Cases))
	for _, c := range corpus.Cases {
		if c.ID == "" || seen[c.ID] || len(c.Digest) != 16 {
			t.Fatalf("invalid identity case: %s", c.ID)
		}
		seen[c.ID] = true
		for _, value := range []string{c.Input, c.Canonical, c.Digest} {
			if _, err := hex.DecodeString(value); err != nil || value != strings.ToLower(value) {
				t.Fatalf("invalid identity hex: %s", c.ID)
			}
		}
	}
	return corpus
}

func checkIdentityDriftCase(c identityDriftCase, state *collectorState) error {
	input, err := hex.DecodeString(c.Input)
	if err != nil || !utf8.Valid(input) {
		return fmt.Errorf("%s: invalid input_utf8_hex", c.ID)
	}
	canonical, err := sketchcanon.CanonicalizeString(sketchcanon.TextV1, string(input))
	if err != nil {
		return fmt.Errorf("%s: canonicalization: %w", c.ID, err)
	}
	if hex.EncodeToString(canonical) != c.Canonical {
		return fmt.Errorf("%s: canonical_hex got=%x want=%s", c.ID, canonical, c.Canonical)
	}
	// Exercise the production canonicalize-and-hash path, not a test substitute.
	digest, err := state.hashCanonical(sketchcanon.TextV1, sketchhash.PromptV1, string(input))
	if err != nil {
		return fmt.Errorf("%s: collector hash: %w", c.ID, err)
	}
	if got := fmt.Sprintf("%016x", digest); got != c.Digest {
		return fmt.Errorf("%s: digest_hex got=%s want=%s", c.ID, got, c.Digest)
	}
	return nil
}

func TestIdentityDrift(t *testing.T) {
	baseline := os.Getenv("GENAI_IDENTITY_BASELINE")
	if baseline == "" {
		baseline = "connector"
	}
	corpus := loadIdentityDriftCorpus(t, baseline)
	t.Setenv("GENAI_IDENTITY_TEST_KEY", corpus.Key)
	secret, err := sketchhash.SecretFromEnv("GENAI_IDENTITY_TEST_KEY")
	if err != nil {
		t.Fatal(err)
	}
	state := &collectorState{secret: secret}
	for _, c := range corpus.Cases {
		if err := checkIdentityDriftCase(c, state); err != nil {
			t.Error(err)
		}
	}
	t.Logf("checked %d frozen %s canonical/hash identities", len(corpus.Cases), baseline)
	for _, field := range []string{"canonical_hex", "digest_hex"} {
		changed := corpus.Cases[0]
		if field == "canonical_hex" {
			changed.Canonical += "00"
		} else {
			changed.Digest = strings.Repeat("0", 16)
		}
		if err := checkIdentityDriftCase(changed, state); err == nil || !strings.Contains(err.Error(), field) {
			t.Fatalf("intentional %s drift was not detected: %v", field, err)
		}
	}
}

func TestIdentityDriftInputsMatch(t *testing.T) {
	connector := loadIdentityDriftCorpus(t, "connector")
	distribution := loadIdentityDriftCorpus(t, "distribution")
	if len(connector.Cases) != len(distribution.Cases) {
		t.Fatal("identity baselines must cover the same corpus")
	}
	inputs := make(map[string]string, len(connector.Cases))
	for i, c := range connector.Cases {
		other := distribution.Cases[i]
		if c.ID != other.ID || c.Input != other.Input {
			t.Fatalf("identity baselines have different inputs at %d", i)
		}
		inputs[c.ID] = c.Input
	}
	for id, input := range map[string]string{
		"explicit_kawi_11f41_acute": "\U00011f41\u0301",
		"explicit_leading_31":       strings.Repeat("\u0300", 31),
		"explicit_after_starter_31": "a" + strings.Repeat("\u0300", 31),
	} {
		if inputs[id] != hex.EncodeToString([]byte(input)) {
			t.Fatalf("missing required identity regression: %s", id)
		}
	}
	for r := rune(0x1c); r <= 0x1f; r++ {
		id := fmt.Sprintf("explicit_control_%04x", r)
		input, err := hex.DecodeString(inputs[id])
		if err != nil || !strings.ContainsRune(string(input), r) {
			t.Fatalf("missing required identity regression: %s", id)
		}
	}
}
