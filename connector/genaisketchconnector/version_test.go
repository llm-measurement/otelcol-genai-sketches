// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import (
	"runtime/debug"
	"testing"
	"time"
)

func TestComponentScopeVersion(t *testing.T) {
	for _, tc := range []struct {
		info *debug.BuildInfo
		want string
	}{
		{nil, "devel"},
		{&debug.BuildInfo{Main: debug.Module{Path: "vendor/collector", Version: "v9.0.0"}}, "devel"},
		{&debug.BuildInfo{Main: debug.Module{Path: scopeName, Version: "v0.2.0"}}, "v0.2.0"},
		{&debug.BuildInfo{Deps: []*debug.Module{{Path: scopeName, Version: "v0.2.0-dev", Replace: &debug.Module{Path: "../connector"}}}}, "v0.2.0-dev"},
		{&debug.BuildInfo{Deps: []*debug.Module{{Path: scopeName, Version: "v0.0.0"}}}, "devel"},
	} {
		if got := connectorModuleVersion(tc.info); got != tc.want {
			t.Fatalf("scope version %q, want %q", got, tc.want)
		}
	}
	s := newTestState(t, defaultConfig())
	metrics := s.buildMetrics(time.Unix(120, 0))
	if got := metrics.ResourceMetrics().At(0).ScopeMetrics().At(0).Scope().Version(); got == "" || got != scopeVersion {
		t.Fatal("metric scope version missing or inconsistent")
	}
}
