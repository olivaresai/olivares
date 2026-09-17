// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// The K4 dispatch digest is the durable idempotency key of a work launch, so its
// bytes are a CONTRACT with rows that already exist: an in-flight dispatch that
// re-derives a different digest for the same request stops being a replay and
// becomes a conflict, and every historical work-bound row becomes unmatchable.
//
// This pins the two shapes that must not move — an unprofiled launch and a
// profiled launch whose profile authorizes no authentication source — as LITERAL
// bytes rather than as a property. A property test would pass against a
// consistently-wrong encoder; a literal cannot.
//
// The third case is the other half of the same contract: a launch that DOES carry
// an authorized source must digest it, so re-using a dispatch key under another
// authorization is refused instead of replayed.

func k4Params() CreateRunParams {
	return CreateRunParams{
		Transport: TransportStreamJSON, PermissionMode: "default", Isolation: IsolationNative,
		Actor: "agent:a", ActorKind: "agent", AgentRef: "agent:a",
	}
}

const k4LegacyCanonicalJSON = `{"Actor":"agent:a","ActorKind":"agent","AgentRef":"agent:a",` +
	`"AllowedTools":null,"Effort":"","EnvAllow":null,"Instructions":"","Isolation":"native",` +
	`"MaxDuration":0,"Model":"","Name":"","PermissionMode":"default","RecordRequested":false,` +
	`"ResumeOf":"","TemplateID":"","TemplateVersion":0,"Transport":"stream-json","WorkspaceRef":""}`

const k4ProfiledCanonicalJSON = `{"Actor":"agent:a","ActorKind":"agent","AgentRef":"agent:a",` +
	`"AllowedTools":null,"Effort":"","EnvAllow":null,"Instructions":"","Isolation":"native",` +
	`"MaxDuration":0,"Model":"","Name":"","PermissionMode":"default",` +
	`"ProviderHome":{"config_home":"/c","driver":"claude","environment_ref":"env",` +
	`"profile_id":"ppf_1","user_home":"/u"},"ProviderProfileRef":"ppf_1",` +
	`"RecordRequested":false,"ResumeOf":"","TemplateID":"","TemplateVersion":0,` +
	`"Transport":"stream-json","WorkspaceRef":""}`

func TestK4DigestKeepsItsLegacyAndUnsourcedBytes(t *testing.T) {
	t.Parallel()
	legacy, err := canonicalJSON(k4Params())
	if err != nil {
		t.Fatalf("canonical json: %v", err)
	}
	if string(legacy) != k4LegacyCanonicalJSON {
		t.Fatalf("an UNPROFILED dispatch changed its digest bytes.\n got: %s\nwant: %s", legacy, k4LegacyCanonicalJSON)
	}
	// A profile that authorizes no source encodes exactly as it did before the
	// field existed: `omitempty` is doing load-bearing work, not tidying.
	profiled := k4Params()
	profiled.ProviderProfileRef = "ppf_1"
	profiled.ProviderHome = &ProviderHomeSnapshot{
		ProfileID: "ppf_1", Driver: providerDriverClaude, EnvironmentRef: "env",
		ConfigHome: "/c", UserHome: "/u",
	}
	raw, err := canonicalJSON(profiled)
	if err != nil {
		t.Fatalf("canonical json: %v", err)
	}
	if string(raw) != k4ProfiledCanonicalJSON {
		t.Fatalf("an existing PROFILED dispatch changed its digest bytes.\n got: %s\nwant: %s", raw, k4ProfiledCanonicalJSON)
	}
	if strings.Contains(string(raw), "auth_source") {
		t.Fatalf("an unsourced snapshot must not emit auth_source: %s", raw)
	}
}

func TestK4DigestBindsTheAuthorizedAuthenticationSource(t *testing.T) {
	t.Parallel()
	base := k4Params()
	base.ProviderProfileRef = "ppf_1"
	unsourced := base
	unsourced.ProviderHome = &ProviderHomeSnapshot{
		ProfileID: "ppf_1", Driver: providerDriverCodex, EnvironmentRef: "env",
		ConfigHome: "/c", UserHome: "/u",
	}
	digest := func(p CreateRunParams) string {
		raw, err := canonicalJSON(p)
		if err != nil {
			t.Fatalf("canonical json: %v", err)
		}
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:])
	}
	home := unsourced
	homeSnap := *unsourced.ProviderHome
	homeSnap.AuthSource = AuthSourceAccountHome
	home.ProviderHome = &homeSnap

	managed := unsourced
	managedSnap := *unsourced.ProviderHome
	managedSnap.AuthSource = AuthSourceManagedInjection
	managed.ProviderHome = &managedSnap

	if digest(home) == digest(unsourced) {
		t.Fatal("an authorized source must change the dispatch digest")
	}
	if digest(home) == digest(managed) {
		t.Fatal("the two authorized sources must not share a dispatch digest")
	}
}
