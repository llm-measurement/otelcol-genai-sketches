// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"bytes"
	"testing"
	"time"
)

func TestDisabledCustomKeysPreserveBaseFingerprint(t *testing.T) {
	var baselineID string
	var baselineBytes []byte
	for _, tc := range []struct {
		name       string
		keys       []TopKKeyConfig
		custom     bool
		changeRule bool
	}{
		{name: "baseline"},
		{name: "unselected custom fields", custom: true},
		{name: "selected custom tokens", custom: true, keys: []TopKKeyConfig{{Field: "virtual_key"}}},
		{name: "selected custom requests", custom: true, keys: []TopKKeyConfig{{Field: "virtual_key", Weight: "requests"}}},
		{name: "different selected custom field", custom: true, keys: []TopKKeyConfig{{Field: "tenant_key"}}},
		{name: "changed optional extraction", custom: true, changeRule: true, keys: []TopKKeyConfig{{Field: "virtual_key"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, e := topKExportFixture(t, tc.keys, func(cfg *Config) {
				cfg.TopK = 0
				if tc.custom {
					cfg.Fields["virtual_key"] = FieldConfig{FromAttributes: []string{"app.virtual_key"}, Canonicalization: "text_v1", Domain: "user:v1"}
					cfg.Fields["tenant_key"] = FieldConfig{FromAttributes: []string{"app.tenant"}, Canonicalization: "text_v1", Domain: "session:v1"}
				}
				if tc.changeRule {
					cfg.Fields["virtual_key"] = FieldConfig{FromResourceAttributes: []string{"alternate.virtual_key"}, Canonicalization: "text_v1", Domain: "session:v1"}
				}
			})
			e.epoch = "fixed-fingerprint"
			mustConsume(t, s, testSpan{Model: "model", User: "user", Prompt: "prompt", InputTokens: ptr(int64(2)), OutputTokens: ptr(int64(3))})
			doc := exportDocs(t, s, e, time.Unix(180, 0))[0]
			encoded, err := doc.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			if len(e.topKContracts) != 0 {
				t.Fatal("disabled keys emitted contract markers")
			}
			if baselineID == "" {
				baselineID, baselineBytes = e.accountingID, encoded
			} else if e.accountingID != baselineID || !bytes.Equal(encoded, baselineBytes) {
				t.Fatal("disabled optional fields or key selection changed base accounting or summary bytes")
			}
		})
	}
}
