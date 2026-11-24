// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package charttest

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestRootInvocationsRendering(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings []string
		want     map[string]any
	}{
		{name: "default-off"},
		{name: "explicit-off", settings: []string{"connector.rootInvocations.enabled=false"}},
		{name: "defaults", settings: []string{"connector.rootInvocations.enabled=true"}, want: map[string]any{"enabled": true, "operations": []any{"invoke_agent", "invoke_workflow"}}},
		{name: "entrypoint", settings: []string{"connector.rootInvocations.enabled=true", `connector.rootInvocations.operations=["invoke_agent","invoke_workflow","invoke_runner"]`}, want: map[string]any{"enabled": true, "operations": []any{"invoke_agent", "invoke_workflow", "invoke_runner"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := summaryConfig(t, summaryDocuments(t, tc.settings...))
			got, present := nested(config, "connectors", "genaisketch")["root_invocations"]
			if tc.want == nil {
				if present {
					t.Fatal("disabled root invocations changed the rendered collector configuration")
				}
				return
			}
			if !present || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("root configuration = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestRootInvocationsChartBounds(t *testing.T) {
	operations := make([]string, 33)
	for i := range operations {
		operations[i] = fmt.Sprintf("invoke_%02d", i)
	}
	maximum, err := json.Marshal(operations[:32])
	if err != nil {
		t.Fatal(err)
	}
	if output, err := render(t, "connector.rootInvocations.operations="+string(maximum)); err != nil {
		t.Fatalf("maximum definition rejected: %v\n%s", err, output)
	}
	tooMany, err := json.Marshal(operations)
	if err != nil {
		t.Fatal(err)
	}
	tooLong, err := json.Marshal([]string{"invoke_" + strings.Repeat("x", 58)})
	if err != nil {
		t.Fatal(err)
	}
	for _, setting := range []string{
		"connector.rootInvocations.operations=[]",
		`connector.rootInvocations.operations=["invoke_agent","invoke_agent"]`,
		`connector.rootInvocations.operations=["chat"]`,
		`connector.rootInvocations.operations=["invoke_Agent"]`,
		"connector.rootInvocations.operations=" + string(tooMany),
		"connector.rootInvocations.operations=" + string(tooLong),
		"connector.rootInvocations.enabled=not-a-boolean",
		"connector.rootInvocations.untrusted=x",
	} {
		if output, err := render(t, setting); err == nil || !strings.Contains(string(output), "rootInvocations") {
			t.Fatalf("invalid root definition was not refused by its schema: %s, %v\n%s", setting, err, output)
		}
	}
}
