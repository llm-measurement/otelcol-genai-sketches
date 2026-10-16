// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex
package charttest

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func modelUsageSettings(extra ...string) []string {
	settings := append([]string{}, enabledSummary...)
	settings = append(settings, "summaryExport.modelUsage.enabled=true", "summaryExport.modelUsage.slice=by_model")
	return append(settings, extra...)
}

func TestModelUsageDisabledRenderingUnchanged(t *testing.T) {
	for _, base := range [][]string{nil, enabledSummary} {
		original, err := render(t, base...)
		if err != nil {
			t.Fatal(err)
		}
		settings := append(append([]string{}, base...),
			"summaryExport.modelUsage.enabled=false",
			"summaryExport.modelUsage.slice=not_selected",
			`summaryExport.modelUsage.models={"PRIVATE_ALIAS":"public-model"}`)
		disabled, err := render(t, settings...)
		if err != nil || !bytes.Equal(original, disabled) {
			t.Fatal("disabled settings changed rendered manifests", err)
		}
		if bytes.Contains(disabled, []byte("model_usage")) || bytes.Contains(disabled, []byte("PRIVATE_ALIAS")) {
			t.Fatal("disabled configuration escaped into manifests")
		}
	}
}

func TestModelUsageRendering(t *testing.T) {
	for _, mapping := range []map[string]string{
		{},
		{"alias-a": "claude-sonnet-5-5", "alias-b": "gpt-6.1-sol", "alias-c": "gpt-6.1-sol"},
		{"123": "123", "true": "true", "alias: with space": "vendor/model:1"},
	} {
		encoded, err := json.Marshal(mapping)
		if err != nil {
			t.Fatal(err)
		}
		cfg := summaryConfig(t, summaryDocuments(t, modelUsageSettings("summaryExport.modelUsage.models="+string(encoded))...))
		usage := nested(cfg, "connectors", "genaisketch", "summary_export", "model_usage")
		want := map[string]any{}
		for alias, public := range mapping {
			want[alias] = public
		}
		if !reflect.DeepEqual(usage, map[string]any{"slice": "by_model", "models": want}) {
			t.Fatalf("model map lost types or gained connector-only settings: %#v", usage)
		}
	}
}

func TestModelUsageRejectsInvalidValues(t *testing.T) {
	tooMany := map[string]string{}
	for i := range 5001 {
		key, _ := json.Marshal(i)
		tooMany[string(key)] = "public-model"
	}
	oversize, _ := json.Marshal(tooMany)
	for _, tc := range []struct{ name, setting string }{
		{"export-disabled", "summaryExport.enabled=false"},
		{"missing-slice", "summaryExport.modelUsage.slice="},
		{"unknown-slice", "summaryExport.modelUsage.slice=not_selected"},
		{"intersection", `connector.slices=[{"name":"by_model","keys":["gen_ai.request.model","team.id"]}]`},
		{"wrong-key", `connector.slices=[{"name":"by_model","keys":["team.id"]}]`},
		{"empty-keys", `connector.slices=[{"name":"by_model","keys":[]}]`},
		{"duplicate-slice", `connector.slices=[{"name":"by_model","keys":["gen_ai.request.model"]},{"name":"by_model","keys":["gen_ai.request.model"]}]`},
		{"null-config", "summaryExport.modelUsage=null"},
		{"unknown-field", "summaryExport.modelUsage.contract=arbitrary"},
		{"not-boolean", "summaryExport.modelUsage.enabled=yes"},
		{"null-map", "summaryExport.modelUsage.models=null"},
		{"list-map", "summaryExport.modelUsage.models=[]"},
		{"empty-alias", `summaryExport.modelUsage.models={"":"public-model"}`},
		{"long-alias", `summaryExport.modelUsage.models={"` + strings.Repeat("a", 257) + `":"public-model"}`},
		{"long-utf8-alias", `summaryExport.modelUsage.models={"` + strings.Repeat("\u00e9", 129) + `":"public-model"}`},
		{"too-many-aliases", "summaryExport.modelUsage.models=" + string(oversize)},
		{"non-string-id", `summaryExport.modelUsage.models={"alias":123}`},
		{"empty-id", `summaryExport.modelUsage.models={"alias":""}`},
		{"reserved-id", `summaryExport.modelUsage.models={"alias":"__unmapped__"}`},
		{"nonpublic-id", `summaryExport.modelUsage.models={"alias":"https://host/?secret=value"}`},
		{"long-id", `summaryExport.modelUsage.models={"alias":"` + strings.Repeat("a", 129) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := render(t, modelUsageSettings(tc.setting)...); err == nil {
				t.Fatal("invalid model usage configuration accepted")
			}
		})
	}
}
