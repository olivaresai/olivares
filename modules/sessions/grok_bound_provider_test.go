// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import "testing"

// Grok Build on a key from Providers is held to the carrier's endpoint by its documented
// variables; a launch on its own sign-in keeps its own (Root 2026-10-02 21:22Z).
func TestGrokKeyLaunchIsHeldToItsEndpoint(t *testing.T) {
	bound := envMap(grokDriver{}.LaunchEnv(DriverLaunch{BoundProvider: BoundProvider{Kind: ProviderKindXAI, Endpoint: "https://api.x.ai/v1"}}))
	for _, name := range []string{envGrokXAIAPIBaseURL, envGrokModelsBaseURL} {
		if bound[name] != "https://api.x.ai/v1" {
			t.Errorf("%s = %q, want the carrier's endpoint", name, bound[name])
		}
	}
	own := envMap(grokDriver{}.LaunchEnv(DriverLaunch{}))
	for _, name := range []string{envGrokXAIAPIBaseURL, envGrokModelsBaseURL} {
		if _, ok := own[name]; ok {
			t.Errorf("a launch on Grok's own sign-in sets %s", name)
		}
	}
}
