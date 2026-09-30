// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package genaisketchconnector

import "runtime/debug"

var scopeVersion = func() string {
	info, _ := debug.ReadBuildInfo()
	return connectorModuleVersion(info)
}()

// Report the component module, not the hosting distribution's unrelated version.
func connectorModuleVersion(info *debug.BuildInfo) string {
	if info != nil {
		modules := append([]*debug.Module{&info.Main}, info.Deps...)
		for _, module := range modules {
			if module.Path == scopeName && module.Version != "" && module.Version != "(devel)" && module.Version != "v0.0.0" {
				return module.Version
			}
		}
	}
	return "devel"
}
