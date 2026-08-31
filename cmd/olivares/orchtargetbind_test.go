// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Item 6a: the key file must resolve the SAME key across restarts (the
// earlier bug minted the raw bytes but reloaded them through a second
// derivation, forking the key and invalidating every pending binding).
func TestTargetBindingKeyStableAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tbk.key")
	getenv := func(k string) string {
		if k == envTargetBindingKeyFile {
			return path
		}
		return ""
	}
	k1, ok1 := loadTargetBindingKey(getenv, discardLog())
	k2, ok2 := loadTargetBindingKey(getenv, discardLog()) // "restart": same file
	if !ok1 || !ok2 {
		t.Fatalf("expected a key both times, got ok1=%v ok2=%v", ok1, ok2)
	}
	m1, id1, _ := k1.MAC([]byte("bind-A"))
	m2, id2, _ := k2.MAC([]byte("bind-A"))
	if id1 != id2 || string(m1) != string(m2) {
		t.Fatal("target-binding key changed across a restart — pending bindings would all be invalidated")
	}
}

// Item 6d: the generation must (a) prefer RUNTIME over A2A for a subject in
// both (the backend Fire actually picks), (b) reflect Replicas, and (c) reflect
// the CONTENT of the trust anchor file, not just its path.
func TestDispatcherGenerationEffectBearing(t *testing.T) {
	base := func() orchDispatchConfig {
		var c orchDispatchConfig
		c.Runtime.Targets = []orchRuntimeTargetJSON{{SubjectRef: "agent-1", Runtime: "k8s", Image: "img:v1", Replicas: 2}}
		return c
	}
	g := newDispatcherGeneration(base())

	// (a) runtime-vs-A2A precedence: a subject in BOTH is described by its runtime.
	both := base()
	both.A2A.Agents = []orchA2AAgentJSON{{SubjectRef: "agent-1", URL: "https://evil"}}
	gBoth := newDispatcherGeneration(both)
	if g.Generation("agent", "agent-1") != gBoth.Generation("agent", "agent-1") {
		t.Fatal("an A2A entry for a runtime subject changed the generation — it must describe the runtime backend Fire picks")
	}

	// (b) Replicas is effect-bearing.
	rep := base()
	rep.Runtime.Targets[0].Replicas = 5
	if g.Generation("agent", "agent-1") == newDispatcherGeneration(rep).Generation("agent", "agent-1") {
		t.Fatal("Replicas change did not change the generation")
	}

	// (c) trust anchor FILE CONTENT is folded in (a rotation under the same path
	// must change the generation).
	dir := t.TempDir()
	tf := filepath.Join(dir, "trust.jwks")
	_ = os.WriteFile(tf, []byte(`{"keys":["old"]}`), 0o600)
	a := orchDispatchConfig{}
	a.A2A.Agents = []orchA2AAgentJSON{{SubjectRef: "remote-1", URL: "https://r", TrustJWKSFile: tf}}
	before := newDispatcherGeneration(a).Generation("agent", "remote-1")
	_ = os.WriteFile(tf, []byte(`{"keys":["rotated"]}`), 0o600)
	after := newDispatcherGeneration(a).Generation("agent", "remote-1")
	if before == after {
		t.Fatal("rotating the trust anchor file CONTENT under the same path did not change the generation")
	}

	// (d) K5's stable peer authority and scope allowlist are effect-bearing even
	// when the transport URL and trust key stay unchanged.
	a.A2A.Agents[0].Authority = "peer.example"
	a.A2A.Agents[0].Scopes = []string{"work:execute", "artifact:write"}
	k5 := newDispatcherGeneration(a).Generation("agent", "remote-1")
	a.A2A.Agents[0].Authority = "other-peer.example"
	if k5 == newDispatcherGeneration(a).Generation("agent", "remote-1") {
		t.Fatal("changing K5 peer authority did not change the generation")
	}
	a.A2A.Agents[0].Authority = "peer.example"
	a.A2A.Agents[0].Scopes = []string{"work:execute"}
	if k5 == newDispatcherGeneration(a).Generation("agent", "remote-1") {
		t.Fatal("changing K5 scope allowlist did not change the generation")
	}
}
