// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"
)

// A launch names managed credentials only in the API field `credentials`
// (modules/sessions session_credentials.go); `--credential` is the flag a person
// uses instead of curl. The engine owns every rule about the names; the
// CLI owns the count alone, and refuses before it registers anything, like every
// other start the engine would refuse.

// TestSessionStartSendsTheNamedCredentials: repeatable --credential puts the names
// in the launch, in the order given; a start without the flag sends no
// credentials field, which is the request it always was.
func TestSessionStartSendsTheNamedCredentials(t *testing.T) {
	f := newFakeSessionEngine(t)
	_, stderr, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "--tool", "claude",
		"--credential", "prod-db", "--credential", "build-host"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("start with credentials: %v\n%s", err, stderr)
	}
	runs := f.postsTo(sessionRunsPath)
	if len(runs) != 1 {
		t.Fatalf("%d launches", len(runs))
	}
	names, ok := runs[0]["credentials"].([]any)
	if !ok || len(names) != 2 || names[0] != "prod-db" || names[1] != "build-host" {
		t.Fatalf("credentials = %v, want [prod-db build-host]", runs[0]["credentials"])
	}

	f = newFakeSessionEngine(t)
	_, stderr, err = execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "--tool", "claude"},
		sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("start without credentials: %v\n%s", err, stderr)
	}
	runs = f.postsTo(sessionRunsPath)
	if len(runs) != 1 {
		t.Fatalf("%d launches", len(runs))
	}
	if _, ok := runs[0]["credentials"]; ok {
		t.Fatalf("a start without the flag sent credentials = %v", runs[0]["credentials"])
	}
}

// TestSessionStartRefusesMoreThanEightCredentials: the count is the engine's own
// bound (sessions.CredentialMaxNames); the CLI refuses it first, so a refused start
// registers no folder and resolves no profile.
func TestSessionStartRefusesMoreThanEightCredentials(t *testing.T) {
	f := newFakeSessionEngine(t)
	args := []string{"session", "start", t.TempDir(), "--tool", "claude"}
	for _, name := range []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9"} {
		args = append(args, "--credential", name)
	}
	_, _, err := execSessionCLI(t, nil, append(args, sessionCreds(f.URL)...)...)
	if err == nil || !strings.Contains(err.Error(), "at most 8") {
		t.Fatalf("err = %v, want the at-most-8 refusal", err)
	}
	if len(f.postsTo(sessionRunsPath)) != 0 || len(f.postsTo(sessionWorkspacesPath)) != 0 {
		t.Fatal("a start the CLI refused had effects on the engine")
	}
}

// TestAgentSessionCreateRefusesMoreThanEightCredentials: the same bound on the
// scripted surface, refused before any call is made.
func TestAgentSessionCreateRefusesMoreThanEightCredentials(t *testing.T) {
	p := newCreateProbe(t, 201, createdRunJSON)
	args := []string{"agent", "session", "create", "--server", p.URL, "--token", "test-token", "--tenant", "tenant-a"}
	for _, name := range []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9"} {
		args = append(args, "--credential", name)
	}
	_, _, err := execRoot(t, args...)
	if err == nil || !strings.Contains(err.Error(), "at most 8") {
		t.Fatalf("err = %v, want the at-most-8 refusal", err)
	}
	if p.calls.Load() != 0 {
		t.Fatalf("calls = %d, want 0", p.calls.Load())
	}
}

// TestAgentSessionCreateSendsTheNamedCredentials: the same flag on the scripted
// surface, sent only when named.
func TestAgentSessionCreateSendsTheNamedCredentials(t *testing.T) {
	p := newCreateProbe(t, 201, createdRunJSON)
	out, errb, err := execRoot(t, createArgs(p.URL, "--credential", "prod-db", "--credential", "build-host")...)
	if err != nil {
		t.Fatalf("create with credentials: %v\n%s\n%s", err, errb, out)
	}
	got := decodeCreateBody(t, p.lastBody())
	names, ok := got["credentials"].([]any)
	if !ok || len(names) != 2 || names[0] != "prod-db" || names[1] != "build-host" {
		t.Fatalf("credentials = %v, want [prod-db build-host]", got["credentials"])
	}

	p = newCreateProbe(t, 201, createdRunJSON)
	_, errb, err = execRoot(t, createArgs(p.URL)...)
	if err != nil {
		t.Fatalf("create without credentials: %v\n%s", err, errb)
	}
	if got := decodeCreateBody(t, p.lastBody()); got["credentials"] != nil {
		t.Fatalf("a create without the flag sent credentials = %v", got["credentials"])
	}
}
