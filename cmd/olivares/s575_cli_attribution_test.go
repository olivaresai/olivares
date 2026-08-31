// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// B-12 — every privileged local path must name somebody in the ledger.
//
// Five of them did not. `olivares secrets rotate`, `olivares sources rm`,
// `olivares superadmin enable`, the boot seeder and a host SIGHUP each built an
// auth.Principal with a DisplayName and no user id, and Principal.Actor() — the
// value the ledger records — does not read DisplayName. All five appended events
// whose actor was the bare string "user:".
//
// The guard is deliberately NOT "no cmd_*.go contains a DisplayName literal".
// That test is narrower than the thing it guards: it passes the day the same
// anonymous principal is built from a variable, from a helper, or from another
// package, and a guard that can be walked around by moving a string is how a
// false green gets bought. What is asserted here is the BEHAVIOR — a principal
// that reaches the ledger must produce a distinguishable subject — and it is
// asserted against every declared path.

// Every declared local path must produce an attributable, distinguishable subject.
func TestEveryLocalPathIsAttributable(t *testing.T) {
	seen := map[string]string{}
	for _, via := range localPaths {
		p, err := auth.NewLocalOperator(auth.LocalOperator{
			Subject: "ana@corp.example", Via: via, Reason: "test",
		})
		if err != nil {
			t.Fatalf("%s: %v", via, err)
		}
		subject, err := p.AttributableActor()
		if err != nil {
			t.Errorf("%s: not attributable: %v", via, err)
			continue
		}
		// The failure this closes, stated exactly: a subject that is a prefix and
		// nothing else names nobody.
		if subject == "" || strings.HasSuffix(subject, ":") {
			t.Errorf("%s: subject %q names nobody", via, subject)
		}
		if prev, dup := seen[subject]; dup {
			t.Errorf("%s and %s collapse to the same subject %q", prev, via, subject)
		}
		seen[subject] = via
		// The provenance rides the principal, so a command added later inherits it.
		meta := p.AuditMeta()
		for _, k := range []string{"actor_subject", "actor_via", "actor_reason", "actor_host", "actor_uid", "actor_pid"} {
			if _, ok := meta[k]; !ok {
				t.Errorf("%s: provenance is missing %q", via, k)
			}
		}
	}
	if len(seen) != len(localPaths) {
		t.Errorf("the declared paths must produce %d distinct subjects, got %d", len(localPaths), len(seen))
	}
}

// The historical shape — a user principal with a DisplayName and no user id — is
// refused BY THE LEDGER's own accessor, wherever it was built.
func TestTheHistoricalAnonymousPrincipalIsRefused(t *testing.T) {
	anon := auth.Principal{Kind: auth.KindUser, Superadmin: true, DisplayName: "cli:secrets"}
	if got := anon.Actor(); got != "user:" {
		t.Fatalf("fixture: the historical principal must still produce %q, got %q", "user:", got)
	}
	if _, err := anon.AttributableActor(); !errors.Is(err, auth.ErrUnattributable) {
		t.Errorf("an unattributable principal must be refused, got %v", err)
	}
}

// Attribution is required, not defaulted: no environment fallback, no placeholder.
func TestAttributionIsRequiredNotDefaulted(t *testing.T) {
	cases := map[string]struct {
		op   auth.LocalOperator
		want error
	}{
		"no actor":         {auth.LocalOperator{Via: "cli:secrets", Reason: "r"}, auth.ErrActorRequired},
		"blank actor":      {auth.LocalOperator{Subject: "   ", Via: "cli:secrets", Reason: "r"}, auth.ErrActorRequired},
		"no reason":        {auth.LocalOperator{Subject: "ana", Via: "cli:secrets"}, auth.ErrReasonRequired},
		"blank reason":     {auth.LocalOperator{Subject: "ana", Via: "cli:secrets", Reason: "\t"}, auth.ErrReasonRequired},
		"no path":          {auth.LocalOperator{Subject: "ana", Reason: "r"}, auth.ErrUnattributable},
		"actor with colon": {auth.LocalOperator{Subject: "local:cli:secrets:root", Via: "cli:secrets", Reason: "r"}, auth.ErrUnattributable},
		"actor with newline": {auth.LocalOperator{
			Subject: "ana\nlocal:cli:secrets:root", Via: "cli:secrets", Reason: "r"}, auth.ErrUnattributable},
	}
	for name, c := range cases {
		if _, err := auth.NewLocalOperator(c.op); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
}

// A path no human triggered is classified as SYSTEM, not as a human operator.
// Recording a boot seed as a human would be a more precise lie than the anonymous
// subject it replaces: an auditor filtering for human privileged activity would
// find an event nobody performed.
func TestEngineTriggeredPathsAreSystemNotUser(t *testing.T) {
	sys, err := auth.NewSystemOperator(viaBootSeed, "seeding at boot")
	if err != nil {
		t.Fatal(err)
	}
	if !sys.IsSystemOperator() {
		t.Error("a boot path must report as a system operator")
	}
	if got := sys.ActorKind(); got != model.ActorSystem {
		t.Errorf("ActorKind = %q, want %q", got, model.ActorSystem)
	}
	subject, err := sys.AttributableActor()
	if err != nil {
		t.Fatalf("a system path must still be attributable: %v", err)
	}
	if !strings.HasPrefix(subject, "local:"+viaBootSeed+":") || strings.HasSuffix(subject, ":") {
		t.Errorf("subject %q must name the path and the host", subject)
	}

	human, err := auth.NewLocalOperator(auth.LocalOperator{Subject: "ana", Via: viaCLISecrets, Reason: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if human.IsSystemOperator() {
		t.Error("a CLI operator must NOT be classified as system")
	}
	if got := human.ActorKind(); got != model.ActorUser {
		t.Errorf("a human operator's ActorKind = %q, want %q", got, model.ActorUser)
	}
}

// The privileged commands REFUSE without attribution. Asserted by executing them,
// not by reading a cobra annotation: the annotation is an implementation detail
// of how the refusal is produced, and it changed once already in this unit
// (required-flag checks run before RunE and swallowed the consent message). What
// must hold is that the operation does not happen and the operator is told why.
func TestPrivilegedCommandsRefuseWithoutAttribution(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		// --yes only where the verb is destructive and therefore gated on consent;
		// the others have no such flag, and passing it would test cobra's parser
		// rather than the refusal.
		{"secrets put", []string{"secrets", "put", "--name", "x", "--value", "y"}},
		{"secrets rotate", []string{"secrets", "rotate", "--name", "x", "--value", "y"}},
		{"secrets rm", []string{"secrets", "rm", "--name", "x", "--yes"}},
		{"sources set", []string{"sources", "set", "--name", "x", "--kind", "k"}},
		{"sources rm", []string{"sources", "rm", "--name", "x", "--yes"}},
		{"superadmin enable", []string{"superadmin", "enable", "--email", "a@b.c"}},
		{"superadmin disable", []string{"superadmin", "disable", "--email", "a@b.c"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := newRootCmd()
			var out, errb bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&errb)
			root.SetArgs(append(append([]string{}, c.argv...), "--data-dir", t.TempDir()))
			_, err := root.ExecuteC()
			if err == nil {
				t.Fatalf("`olivares %s` proceeded with no attribution", strings.Join(c.argv, " "))
			}
			msg := err.Error()
			if !strings.Contains(msg, "--actor") && !strings.Contains(msg, "actor") {
				t.Errorf("the refusal must name the attribution the operator omitted, got: %v", err)
			}
		})
	}
}
