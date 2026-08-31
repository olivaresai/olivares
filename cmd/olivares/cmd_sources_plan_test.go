// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

//. These tests are written against the two ways a preview verb is useless:
// it prints something that does not depend on the state it claims to describe,
// and it writes while claiming not to. A test that only looked at stdout would
// pass in both cases, so every assertion here either compares TWO runs from
// DIFFERENT starting states, or reads the store back.

const planTenantA = "00000000-0000-0000-0000-0000000000aa"
const planTenantB = "00000000-0000-0000-0000-0000000000bb"

// runCLI executes the real root command and returns stdout+stderr and the error.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	_, err := root.ExecuteC()
	return out.String(), err
}

// planJSON runs `sources plan -o json` and decodes the report.
func planJSON(t *testing.T, args ...string) (sourcePlanReport, error) {
	t.Helper()
	out, err := runCLI(t, append([]string{"sources", "plan", "-o", "json"}, args...)...)
	var rep sourcePlanReport
	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("plan emitted no JSON document at all:\n%s", out)
	}
	body := out[start:]
	if jerr := json.Unmarshal([]byte(body), &rep); jerr != nil {
		t.Fatalf("plan did not emit a JSON report (%v):\n%s", jerr, out)
	}
	return rep, err
}

// seedRoster opens an initialized data dir and writes the given rows.
func seedRoster(t *testing.T, dir string, defs ...model.SourceDef) {
	t.Helper()
	eng, err := boot(context.Background(), bootConfig{DataDir: dir, Version: "test", Logger: discardLog()})
	if err != nil {
		t.Fatalf("boot to seed %s: %v", dir, err)
	}
	defer func() {
		if cerr := eng.Close(); cerr != nil {
			t.Fatalf("close seeding engine: %v", cerr)
		}
	}()
	for _, d := range defs {
		putRow(t, eng.sourceStore, d)
	}
}

// readRoster returns the persisted roster INCLUDING BaseFields, so a caller can
// tell "unchanged" from "written with the same values" — a Put bumps Version and
// UpdatedAt even when every field it writes is identical.
func readRoster(t *testing.T, dir string) []model.SourceDef {
	t.Helper()
	eng, err := boot(context.Background(), bootConfig{DataDir: dir, Version: "test", Logger: discardLog(), ReadOnly: true})
	if err != nil {
		t.Fatalf("boot to read %s: %v", dir, err)
	}
	defer func() { _ = eng.Close() }()
	rows, lerr := eng.sourceStore.List(context.Background(), auth.GlobalSourceScope)
	if lerr != nil {
		t.Fatalf("list roster: %v", lerr)
	}
	return rows
}

// TestSourcesPlanDoesNotWireTheRoster measures what the COMMAND does, not what
// the boot flag can do.
//
// ReadOnly never covered this: it stops the boot MANUFACTURING an installation,
// and then rt.Start and the initial reconcile run as usual and PREPARE, OPEN and
// WIRE every enabled connector. So a preview verb dialed a deployment's sources
// to print a diff. The sol-max contrast caught a `sources plan` logging
// `rejected=1` from a real apply attempt.
//
// The first version of this test called boot() directly with NoIngest set, and a
// mutant that took NoIngest OUT of rosterReadBoot left it green — it was proving
// the flag worked, never that the commands used it. It now runs the real argv and
// watches the reconcile's own log line, with `sources ls` as the other direction:
// ls still takes the old boot, so it MUST wire, which is what makes the silence
// under `plan` mean something.
func TestSourcesPlanDoesNotWireTheRoster(t *testing.T) {
	dir := initialisedDataDir(t)
	seedRoster(t, dir, model.SourceDef{
		Name: "cfg-scan", Kind: "claude-config", Tenant: planTenantA, Enabled: true,
		Config: map[string]string{"root": t.TempDir()},
	})

	// The reconcile's own summary line, emitted only when the roster is wired.
	const wiredLine = "sources wired from the durable roster"

	logged := func(t *testing.T, argv ...string) string {
		t.Helper()
		var buf bytes.Buffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
		t.Cleanup(func() { slog.SetDefault(prev) })
		if _, err := runCLI(t, argv...); err != nil {
			t.Fatalf("%v: %v", argv, err)
		}
		return buf.String()
	}

	// `sources ls` still boots the old way, so it wires. Without this the assertion
	// below could pass because the log line was renamed or never emitted at all.
	if out := logged(t, "sources", "ls", "--data-dir", dir); !strings.Contains(out, wiredLine) {
		t.Fatalf("the control did not wire the roster, so the check below measures nothing:\n%s", out)
	}
	if out := logged(t, "sources", "plan", "--data-dir", dir, "--name", "cfg-scan", "--enabled=false"); strings.Contains(out, wiredLine) {
		t.Fatalf("`sources plan` wired the roster; a preview must not open a deployment's connectors:\n%s", out)
	}
}

// TestSourcesPlanChangesWithTheStartingState is the test that a plan which
// merely PRINTS cannot pass. The same argv is planned against three different
// rosters; if the output did not depend on the store, all three would agree.
func TestSourcesPlanChangesWithTheStartingState(t *testing.T) {
	argv := func(dir string) []string {
		return []string{"--data-dir", dir, "--name", "vault-prod", "--kind", "vault",
			"--tenant", planTenantA, "--config", "addr=https://vault.internal:8200"}
	}

	// 1) Absent from the roster.
	absent := initialisedDataDir(t)
	repAbsent, err := planJSON(t, argv(absent)...)
	if err != nil {
		t.Fatalf("plan against an empty roster: %v", err)
	}
	if repAbsent.Action != "create" || repAbsent.Exists {
		t.Fatalf("an absent source must plan as a create, got action=%q exists=%v", repAbsent.Action, repAbsent.Exists)
	}

	// 2) Present and already IDENTICAL: the same argv must now be a no-op.
	same := initialisedDataDir(t)
	seedRoster(t, same, model.SourceDef{
		Name: "vault-prod", Kind: "vault", Tenant: planTenantA, Enabled: true,
		Config: map[string]string{"addr": "https://vault.internal:8200"},
	})
	repSame, err := planJSON(t, argv(same)...)
	if err != nil {
		t.Fatalf("plan against an identical row: %v", err)
	}
	if repSame.Action != "no-op" || len(repSame.Changes) != 0 {
		t.Fatalf("an identical row must plan as a no-op with no changes, got action=%q changes=%v", repSame.Action, repSame.Changes)
	}

	// 3) Present and DIFFERENT in two places: exactly those two fields, and no
	// others. Both a top-level field and a config key are moved, because they
	// travel through different branches of the merge and a plan can lose one
	// while still reporting the other.
	differs := initialisedDataDir(t)
	seedRoster(t, differs, model.SourceDef{
		Name: "vault-prod", Kind: "vault", Tenant: planTenantB, Enabled: true,
		Config: map[string]string{"addr": "https://vault-old.internal:8200"},
	})
	repDiff, err := planJSON(t, argv(differs)...)
	if err != nil {
		t.Fatalf("plan against a differing row: %v", err)
	}
	if repDiff.Action != "update" {
		t.Fatalf("a differing row must plan as an update, got %q", repDiff.Action)
	}
	want := []sourceChange{
		{Field: "tenant", From: planTenantB, To: planTenantA},
		{Field: "config.addr", From: "https://vault-old.internal:8200", To: "https://vault.internal:8200"},
	}
	if !reflect.DeepEqual(repDiff.Changes, want) {
		t.Fatalf("plan must report both moved fields and nothing else\n got: %#v\nwant: %#v", repDiff.Changes, want)
	}
}

// TestSourcesPlanPersistsNothing reads the store back rather than trusting the
// words "NOTHING WAS WRITTEN" on stdout — a plan that persisted behind its own
// report would print exactly the same thing. BaseFields are compared too: a Put
// that rewrote identical values would still move Version and UpdatedAt.
func TestSourcesPlanPersistsNothing(t *testing.T) {
	dir := initialisedDataDir(t)
	seedRoster(t, dir, model.SourceDef{
		Name: "vault-prod", Kind: "vault", Tenant: planTenantA, Enabled: true,
		Config: map[string]string{"addr": "https://vault.internal:8200"},
	})
	before := readRoster(t, dir)

	// An UPDATE plan (a field really moves) and a CREATE plan (a whole new row).
	if _, err := runCLI(t, "sources", "plan", "--data-dir", dir, "--name", "vault-prod", "--tenant", planTenantB); err != nil {
		t.Fatalf("plan an update: %v", err)
	}
	if _, err := runCLI(t, "sources", "plan", "--data-dir", dir, "--name", "brand-new", "--kind", "claude-config", "--tenant", planTenantA); err != nil {
		t.Fatalf("plan a create: %v", err)
	}

	after := readRoster(t, dir)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("plan changed the roster\nbefore: %#v\nafter:  %#v", before, after)
	}
	for _, row := range after {
		if row.Name == "brand-new" {
			t.Fatal("plan created the row it was only asked to describe")
		}
	}
}

// TestSourcesPlanRefusesToCreateTheInstallationItPreviews pins the deliberate
// divergence from `set`: in a data directory holding no installation, `set`
// creates one and `plan` must not. A preview able to mint signing keys is not a
// preview, so this is a property, not an accident of the boot helper.
func TestSourcesPlanRefusesToCreateTheInstallationItPreviews(t *testing.T) {
	dir := t.TempDir()
	if _, err := runCLI(t, "sources", "plan", "--data-dir", dir, "--name", "x", "--kind", "vault", "--tenant", planTenantA); err == nil {
		t.Fatal("plan against a directory with no installation must refuse, not plan against one it would have to create")
	}
	if files := filesUnder(t, dir); len(files) != 0 {
		t.Fatalf("plan created %v in a directory that held no installation", files)
	}
}

// TestSourcesPlanAndSetAgree is the anti-divergence test, and it is the reason
// desiredSourceDef exists once instead of twice.
//
// It does not compare two implementations — it compares the plan against the
// WORLD the apply produced: every field plan said would move must hold plan's
// value afterwards, and planning the very same argv again must be a no-op. A
// plan that computed the merge differently from set fails the second assertion
// even when its own arithmetic is self-consistent.
//
// MEASURED LIMIT, so nobody over-trusts this test: it is blind to a defect the
// two sides SHARE. Disabling the --config branch of desiredSourceDef (the merge
// both verbs call) leaves this test GREEN in 1.9s — plan and set stay in perfect
// agreement about the wrong answer. What catches that is
// TestSourcesPlanChangesWithTheStartingState, which compares the plan against
// the STORE rather than against the apply. Both are needed.
func TestSourcesPlanAndSetAgree(t *testing.T) {
	dir := initialisedDataDir(t)
	seedRoster(t, dir, model.SourceDef{
		Name: "vault-prod", Kind: "vault", Tenant: planTenantA, Enabled: true, PollSeconds: 30,
		Config: map[string]string{"addr": "https://vault.internal:8200", "namespace": "team-a"},
	})
	edit := []string{"--name", "vault-prod", "--tenant", planTenantB,
		"--config", "addr=https://vault-dr.internal:8200", "--config", "namespace=", "--poll-seconds", "60"}

	planned, err := planJSON(t, append([]string{"--data-dir", dir}, edit...)...)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(planned.Changes) == 0 {
		t.Fatal("the fixture must plan a real change, otherwise this test proves nothing")
	}

	if out, serr := runCLI(t, append([]string{"sources", "set", "--data-dir", dir,
		"--actor", "ana@corp.example", "--reason", "check the preview matches the apply"}, edit...)...); serr != nil {
		t.Fatalf("set with the planned flags: %v\n%s", serr, out)
	}

	rows := readRoster(t, dir)
	if len(rows) != 1 {
		t.Fatalf("expected exactly the one seeded row, got %d", len(rows))
	}
	got := sourceFields(rows[0])
	for _, ch := range planned.Changes {
		if got[ch.Field] != ch.To {
			t.Errorf("plan said %s would become %q; the persisted row says %q", ch.Field, ch.To, got[ch.Field])
		}
	}
	// The field plan did NOT mention must be untouched.
	if got["kind"] != "vault" {
		t.Errorf("set moved a field the plan never mentioned: kind = %q", got["kind"])
	}

	replan, err := planJSON(t, append([]string{"--data-dir", dir}, edit...)...)
	if err != nil {
		t.Fatalf("re-plan: %v", err)
	}
	if replan.Action != "no-op" || len(replan.Changes) != 0 {
		t.Fatalf("planning the same flags after applying them must be a no-op; plan and set disagree.\naction=%q changes=%#v", replan.Action, replan.Changes)
	}
}

// TestSourcesPlanNamesTheReconcilersRefusalBeforeTheWrite covers the class the
// unit exists for: a definition the STORE accepts and the ENGINE then declines.
// Before this, the only way to discover it was to perform the write and read
// "persisted, but the live apply was rejected" afterwards.
func TestSourcesPlanNamesTheReconcilersRefusalBeforeTheWrite(t *testing.T) {
	dir := initialisedDataDir(t)
	seedRoster(t, dir, model.SourceDef{Name: "vault-one", Kind: "vault", Tenant: planTenantA, Enabled: true})

	rep, err := planJSON(t, "--data-dir", dir, "--name", "vault-two", "--kind", "vault", "--tenant", planTenantA)
	if exitcode.From(err) != exitcode.Conflict {
		t.Fatalf("a plan that cannot be applied must exit %d (conflict), got %v (%d)", exitcode.Conflict, err, exitcode.From(err))
	}
	if rep.Check.Valid {
		t.Fatal("a second in-process source of the same kind collides on the connector identity; the check called it valid")
	}
	if !rep.Check.refusedAt(problemAtApply) {
		t.Fatalf("the collision bites at APPLY (the store would accept the row), got %#v", rep.Check.Problems)
	}
	if rep.Check.refusedAt(problemAtWrite) {
		t.Fatalf("the store accepts this row, so nothing here may be reported as a write refusal: %#v", rep.Check.Problems)
	}

	// The direction of non-fire: the SAME roster with a kind that does not collide
	// must come back valid. Without this, a check that refused everything would
	// pass the assertions above.
	ok, oerr := planJSON(t, "--data-dir", dir, "--name", "cfg-scan", "--kind", "claude-config", "--tenant", planTenantA)
	if oerr != nil {
		t.Fatalf("a non-colliding kind must plan cleanly: %v", oerr)
	}
	if !ok.Check.Valid {
		t.Fatalf("claude-config does not collide with vault, yet the check refused it: %#v", ok.Check.Problems)
	}
}

// TestSourcesPlanRedactsAValueTheStoreWouldRefuse: a plan is pasted into
// tickets and CI logs, so a literal credential typed by mistake must not be
// echoed back. The problem still names the FIELD — the operator has to be able
// to fix it.
func TestSourcesPlanRedactsAValueTheStoreWouldRefuse(t *testing.T) {
	dir := initialisedDataDir(t)
	seedRoster(t, dir, model.SourceDef{
		Name: "vault-prod", Kind: "vault", Tenant: planTenantA, Enabled: true,
		Config: map[string]string{"token": "store:vault/token"},
	})
	const literal = "hunter2-this-must-never-be-printed"
	out, err := runCLI(t, "sources", "plan", "--data-dir", dir, "--name", "vault-prod", "--config", "token="+literal)
	if exitcode.From(err) != exitcode.Conflict {
		t.Fatalf("a literal under a credential-bearing key must be refused, got %v", err)
	}
	if strings.Contains(out, literal) {
		t.Fatalf("the plan echoed the literal credential back:\n%s", out)
	}
	if !strings.Contains(out, redactedPlanValue) {
		t.Fatalf("the plan must show that the value changed, redacted — got:\n%s", out)
	}
	if !strings.Contains(out, `"token"`) {
		t.Fatalf("the refusal must name the field so it can be fixed:\n%s", out)
	}
	// A REFERENCE is not a secret and is the whole point of reading a plan.
	refOut, rerr := runCLI(t, "sources", "plan", "--data-dir", dir, "--name", "vault-prod", "--config", "token=store:vault/other")
	if rerr != nil {
		t.Fatalf("a reference must plan cleanly: %v\n%s", rerr, refOut)
	}
	if !strings.Contains(refOut, "store:vault/other") {
		t.Fatalf("a secret REFERENCE must be shown in full, or the plan hides the change it exists to show:\n%s", refOut)
	}
}

// TestSourcesValidateWithoutNameCoversTheWholeRoster: the pre-flight before a
// reload. The bad row must be named — a verdict that says only "something is
// wrong" sends the operator back to reading rows one at a time.
func TestSourcesValidateWithoutNameCoversTheWholeRoster(t *testing.T) {
	dir := initialisedDataDir(t)
	seedRoster(t, dir,
		model.SourceDef{Name: "good-one", Kind: "claude-config", Tenant: planTenantA, Enabled: true},
		model.SourceDef{Name: "ghost-kind", Kind: "not-a-real-kind", Tenant: planTenantA, Enabled: true},
	)
	out, err := runCLI(t, "sources", "validate", "--data-dir", dir)
	if exitcode.From(err) != exitcode.Conflict {
		t.Fatalf("a roster holding an unusable row must exit %d, got %v", exitcode.Conflict, err)
	}
	if !strings.Contains(out, "ghost-kind") || !strings.Contains(out, `unknown or unsupported source kind "not-a-real-kind"`) {
		t.Fatalf("validate must name the bad row and why:\n%s", out)
	}
	if !strings.Contains(out, "good-one") {
		t.Fatalf("validate must report every row, not stop at the first bad one:\n%s", out)
	}

	// Non-fire direction: a roster with only usable rows exits 0.
	clean := initialisedDataDir(t)
	seedRoster(t, clean, model.SourceDef{Name: "good-one", Kind: "claude-config", Tenant: planTenantA, Enabled: true})
	if cout, cerr := runCLI(t, "sources", "validate", "--data-dir", clean); cerr != nil {
		t.Fatalf("a clean roster must validate: %v\n%s", cerr, cout)
	}
}

// TestSourcesValidateRefusesEditFlagsWithoutAName: `--kind` with no `--name`
// used to be ambiguous between "validate this candidate" and "validate the
// roster". It refuses instead of silently picking one.
func TestSourcesValidateRefusesEditFlagsWithoutAName(t *testing.T) {
	dir := initialisedDataDir(t)
	out, err := runCLI(t, "sources", "validate", "--data-dir", dir, "--kind", "vault")
	if err == nil {
		t.Fatalf("--kind without --name must be refused:\n%s", out)
	}
	if exitcode.From(err) != exitcode.Usage {
		t.Errorf("a malformed invocation exits %d (usage), got %d", exitcode.Usage, exitcode.From(err))
	}
	if !strings.Contains(err.Error(), "--name") {
		t.Errorf("the refusal must name the flag that is missing, got: %v", err)
	}
}

// TestSourcesValidateAndTestSayWhenTheSourceIsSimplyNotThere.
//
// A mistyped --name used to answer "a source must name the business tenant" and
// "either a kind OR a plugin": every rule failing at once, on a definition the
// operator never wrote. The row is not there, and that is the answer. `plan` is
// the deliberate exception — absence there means CREATE — and the third case
// pins that a DESCRIBED candidate is still validated, so this refusal cannot
// swallow the "check this before I create it" use.
func TestSourcesValidateAndTestSayWhenTheSourceIsSimplyNotThere(t *testing.T) {
	dir := initialisedDataDir(t)
	seedRoster(t, dir, model.SourceDef{Name: "vault-prod", Kind: "vault", Tenant: planTenantA, Enabled: true})

	for _, verb := range []string{"validate", "test"} {
		t.Run(verb, func(t *testing.T) {
			out, err := runCLI(t, "sources", verb, "--data-dir", dir, "--name", "vualt-prod")
			if exitcode.From(err) != exitcode.NotFound {
				t.Fatalf("an absent source must exit %d (not found), got %v (%d)\n%s",
					exitcode.NotFound, err, exitcode.From(err), out)
			}
			if !strings.Contains(err.Error(), "vualt-prod") {
				t.Errorf("the refusal must quote the name that was not found, got: %v", err)
			}
			if strings.Contains(err.Error(), "business tenant") {
				t.Errorf("an absent row must not be reported as a definition failing its rules: %v", err)
			}
		})
	}

	// A candidate the operator DESCRIBED is still validated, absent or not.
	if out, err := runCLI(t, "sources", "validate", "--data-dir", dir,
		"--name", "brand-new", "--kind", "claude-config", "--tenant", planTenantA); err != nil {
		t.Fatalf("validating a described candidate must work before it exists: %v\n%s", err, out)
	}
	// And plan still treats absence as a creation.
	rep, err := planJSON(t, "--data-dir", dir, "--name", "brand-new", "--kind", "claude-config", "--tenant", planTenantA)
	if err != nil {
		t.Fatalf("plan of an absent source: %v", err)
	}
	if rep.Action != "create" {
		t.Fatalf("plan must still read absence as a create, got %q", rep.Action)
	}
}

// TestSourcesSetRefusesWithoutAttributionBeforeInstallingAnything.
//
// The sol-max contrast measured what the first version of the pre-flight missed:
// a `set` that was valid except for a missing --actor exited 1 for attribution
// AFTER leaving six key files and a 6.4 MB olivares.db behind. Attribution needs
// no store, no network and no row, and `sources set` has no consent gate for it
// to compete with, so it is decided before the boot like the rest.
func TestSourcesSetRefusesWithoutAttributionBeforeInstallingAnything(t *testing.T) {
	for _, c := range []struct {
		name string
		argv []string
	}{
		{"no attribution at all", []string{"--name", "vault-prod", "--kind", "vault", "--tenant", planTenantA}},
		{"an explicitly empty tenant", []string{"--name", "vault-prod", "--kind", "vault", "--tenant", "  ",
			"--actor", "ana@corp.example", "--reason", "roster edit"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			out, err := runCLI(t, append([]string{"sources", "set", "--data-dir", dir}, c.argv...)...)
			if err == nil {
				t.Fatalf("this request was never going to be accepted:\n%s", out)
			}
			if files := filesUnder(t, dir); len(files) != 0 {
				t.Fatalf("a decided refusal installed %d file(s) on its way to saying no: %v", len(files), files)
			}
		})
	}
}

// TestSourcesSetDoesNotWriteWhenNothingMoves is the other half of plan's NO-OP.
//
// plan said "the roster already says exactly this" while set called Put
// unconditionally, and Put's Update bumps version and updated_at and appends a
// source.put audit event even when every field is identical. The preview was
// therefore describing a write as nothing — and none of these verbs could show
// it. Measured on BaseFields, which is where that write is visible.
func TestSourcesSetDoesNotWriteWhenNothingMoves(t *testing.T) {
	dir := initialisedDataDir(t)
	seedRoster(t, dir, model.SourceDef{
		Name: "vault-prod", Kind: "vault", Tenant: planTenantA, Enabled: true,
		Config: map[string]string{"addr": "https://vault.internal:8200"},
	})
	before := readRoster(t, dir)

	out, err := runCLI(t, "sources", "set", "--data-dir", dir,
		"--actor", "ana@corp.example", "--reason", "roster edit",
		"--name", "vault-prod", "--kind", "vault", "--config", "addr=https://vault.internal:8200")
	if err != nil {
		t.Fatalf("set: %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing was written") {
		t.Fatalf("a set that moves nothing must say so:\n%s", out)
	}
	after := readRoster(t, dir)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("a set that moved no field still wrote the row (version/updated_at moved)\nbefore: %#v\nafter:  %#v", before, after)
	}

	// Non-fire: a set that DOES move something still writes.
	if out, werr := runCLI(t, "sources", "set", "--data-dir", dir,
		"--actor", "ana@corp.example", "--reason", "roster edit",
		"--name", "vault-prod", "--tenant", planTenantB); werr != nil {
		t.Fatalf("a real change must still be applied: %v\n%s", werr, out)
	}
	moved := readRoster(t, dir)
	if reflect.DeepEqual(before, moved) {
		t.Fatal("a set that moved the tenant did not write")
	}
}

// TestSourcesPlanRedactsADescriptorDeclaredSecret.
//
// The heuristic key list knows token/password and their suffixes; it does not
// know `pat`, and a literal PAT carries none of the inline-credential markers.
// The github connector DECLARES pat secret, so `set` refuses it — through the
// descriptor, which the plan was not consulting. The sol-max contrast printed
// a full ghp_... to stdout and to the JSON from the very report that then
// explained set would reject it.
func TestSourcesPlanRedactsADescriptorDeclaredSecret(t *testing.T) {
	dir := initialisedDataDir(t)
	const literal = "ghp_FAKE_SENTINEL_NEVER_PRINT_7c1b"
	out, err := runCLI(t, "sources", "plan", "--data-dir", dir,
		"--name", "gh", "--kind", "github", "--tenant", planTenantA, "--config", "pat="+literal)
	if exitcode.From(err) != exitcode.Conflict {
		t.Fatalf("a literal under a descriptor-declared secret field must be refused, got %v\n%s", err, out)
	}
	if strings.Contains(out, literal) {
		t.Fatalf("the plan printed a descriptor-declared secret in full:\n%s", out)
	}
	if !strings.Contains(out, redactedPlanValue) {
		t.Fatalf("the plan must still show that the field changed, redacted:\n%s", out)
	}
	// The JSON carries the same value, so it must carry the same mask.
	jout, jerr := runCLI(t, "sources", "plan", "-o", "json", "--data-dir", dir,
		"--name", "gh", "--kind", "github", "--tenant", planTenantA, "--config", "pat="+literal)
	if exitcode.From(jerr) != exitcode.Conflict {
		t.Fatalf("same verdict expected in JSON, got %v", jerr)
	}
	if strings.Contains(jout, literal) {
		t.Fatalf("the JSON report leaked the secret the text form masked:\n%s", jout)
	}
	// Non-fire: a REFERENCE under the same field is shown, or the plan hides the
	// change it exists to show.
	refOut, rerr := runCLI(t, "sources", "plan", "--data-dir", dir,
		"--name", "gh", "--kind", "github", "--tenant", planTenantA, "--config", "pat=store:gh/pat")
	if rerr != nil {
		t.Fatalf("a reference must plan cleanly: %v\n%s", rerr, refOut)
	}
	if !strings.Contains(refOut, "store:gh/pat") {
		t.Fatalf("a secret REFERENCE must be shown in full:\n%s", refOut)
	}
}

// TestSourcesSetRefusesAMalformedNameWithoutCreatingAnInstallation.
//
// `set` boots the store BEFORE it validates, and booting is not free: with an
// explicit --data-dir it creates the directory, the database and three signing
// keys. So a name the store was always going to refuse used to mint an entire
// installation on the way to saying no. The checks that need no store now run
// first.
func TestSourcesSetRefusesAMalformedNameWithoutCreatingAnInstallation(t *testing.T) {
	for _, c := range []struct {
		name string
		argv []string
	}{
		{"a name with a space", []string{"--name", "vault prod", "--kind", "vault", "--tenant", planTenantA}},
		{"a --config without =", []string{"--name", "vault-prod", "--kind", "vault", "--tenant", planTenantA, "--config", "addr"}},
		{"a negative poll interval", []string{"--name", "vault-prod", "--kind", "vault", "--tenant", planTenantA, "--poll-seconds", "-1"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			argv := append([]string{"sources", "set", "--data-dir", dir,
				"--actor", "ana@corp.example", "--reason", "roster edit"}, c.argv...)
			out, err := runCLI(t, argv...)
			if err == nil {
				t.Fatalf("the request is malformed and must be refused:\n%s", out)
			}
			if files := filesUnder(t, dir); len(files) != 0 {
				t.Fatalf("a refused `set` left an installation behind: %v", files)
			}
		})
	}

	// Non-fire direction: a WELL-FORMED request must still get through and write,
	// or the guard above would pass by refusing everything.
	dir := t.TempDir()
	if out, err := runCLI(t, "sources", "set", "--data-dir", dir,
		"--actor", "ana@corp.example", "--reason", "roster edit",
		"--name", "vault-prod", "--kind", "vault", "--tenant", planTenantA); err != nil {
		t.Fatalf("a well-formed set must still create the installation and the row: %v\n%s", err, out)
	}
	rows := readRoster(t, dir)
	if len(rows) != 1 || rows[0].Name != "vault-prod" {
		t.Fatalf("the well-formed set did not persist its row, got %#v", rows)
	}
}

// TestSourcesSetSaysWhenNothingMoved: "updated source" over an unchanged row
// reads as a change that happened. An operator chasing a stale value would
// believe it and stop looking.
func TestSourcesSetSaysWhenNothingMoved(t *testing.T) {
	dir := initialisedDataDir(t)
	seedRoster(t, dir, model.SourceDef{Name: "vault-prod", Kind: "vault", Tenant: planTenantA, Enabled: true})
	out, err := runCLI(t, "sources", "set", "--data-dir", dir,
		"--actor", "ana@corp.example", "--reason", "roster edit", "--name", "vault-prod", "--kind", "vault")
	if err != nil {
		t.Fatalf("set: %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing was written") {
		t.Fatalf("a set that moved nothing must say so:\n%s", out)
	}
	// And the direction of non-fire: a set that DID move something must not.
	out2, err2 := runCLI(t, "sources", "set", "--data-dir", dir,
		"--actor", "ana@corp.example", "--reason", "roster edit", "--name", "vault-prod", "--tenant", planTenantB)
	if err2 != nil {
		t.Fatalf("set: %v\n%s", err2, out2)
	}
	if strings.Contains(out2, "nothing was written") {
		t.Fatalf("a set that moved the tenant claimed nothing changed:\n%s", out2)
	}
	if !strings.Contains(out2, "tenant: "+planTenantA+" → "+planTenantB) {
		t.Fatalf("set must report the field it moved:\n%s", out2)
	}
}

// TestSourcesTestNeverDialsADefinitionTheStoreWouldRefuse: the offline checks
// run first, so a probe cannot resolve secret references for a row that was
// never going to be accepted.
func TestSourcesTestNeverDialsADefinitionTheStoreWouldRefuse(t *testing.T) {
	dir := initialisedDataDir(t)
	seedRoster(t, dir, model.SourceDef{Name: "ghost", Kind: "claude-config", Tenant: planTenantA, Enabled: true})
	out, err := runCLI(t, "sources", "test", "--data-dir", dir, "--name", "ghost", "--kind", "not-a-real-kind")
	if exitcode.From(err) != exitcode.Conflict {
		t.Fatalf("an inadmissible definition must exit %d (conflict) BEFORE dialing, got %v (%d)\n%s",
			exitcode.Conflict, err, exitcode.From(err), out)
	}
	if !strings.Contains(out, "before anything is dialed") {
		t.Fatalf("the report must say the probe never ran:\n%s", out)
	}
}

// TestSourcesTestHidesTheConnectorErrorUnlessAsked: the connector's own message
// was produced against the RESOLVED configuration and can carry credential
// material into a terminal, a ticket or a CI log.
func TestSourcesTestHidesTheConnectorErrorUnlessAsked(t *testing.T) {
	dir := initialisedDataDir(t)
	// A source whose secret reference cannot resolve: the probe fails before any
	// network is touched, which is exactly the failure an operator runs `test` for.
	seedRoster(t, dir, model.SourceDef{
		Name: "vault-prod", Kind: "vault", Tenant: planTenantA, Enabled: true,
		Config: map[string]string{"addr": "https://vault.invalid:8200", "token": "store:no-such-secret"},
	})
	quiet, err := runCLI(t, "sources", "test", "--data-dir", dir, "--name", "vault-prod", "--timeout", "5s")
	if err == nil {
		t.Fatalf("an unresolvable reference cannot answer:\n%s", quiet)
	}
	if !strings.Contains(quiet, "DID NOT ANSWER") {
		t.Fatalf("the report must state the verdict:\n%s", quiet)
	}
	if strings.Contains(quiet, "connector detail") {
		t.Fatalf("the connector's own message must not be printed unless asked:\n%s", quiet)
	}
}
