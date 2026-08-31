// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/connectors/webhook"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/eventing"
)

// VER-06 lot L1 — the eventing + roster MUTATION verbs.
//
// Every test here asserts BOTH directions, because a JSON pane that quietly
// reshapes the text pane is not an added contract, it is a broken one:
//
//	(a) with -o json the leaf emits ONE parseable document whose top-level key
//	    set is pinned by name, so a renamed, dropped or retyped key fails; and
//	(b) WITHOUT -o the stdout bytes are compared to a constant, character for
//	    character, so the text contract cannot move underneath the new pane.
//
// The text constants were captured from the tree BEFORE the JSON panes existed
// (probe run 2026-08-17), which is what makes (b) a real before/after rather
// than a restatement of whatever the code happens to print now.

// runLeafCLI executes the real root command — the only place `-o/--output` is
// registered — and keeps stdout and stderr in SEPARATE buffers.
//
// Separate buffers are load-bearing, not tidiness. The `--format`/`--json`
// deprecation warnings and confirmDestructive's prompt both go to stderr, so a
// harness that merges the two (runCLI in cmd_sources_plan_test.go does, on
// purpose, for its own subject) cannot make a byte-exact claim about stdout at
// all: it would be asserting stdout PLUS whatever stderr happened to interleave.
func runLeafCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	root := newRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	root.SetContext(context.Background())
	_, err := root.ExecuteC()
	return out.String(), errOut.String(), err
}

// leafJSONKeys decodes one leaf's JSON pane and returns its sorted top-level
// key set.
//
// It decodes into map[string]any rather than into the command's own struct, and
// that is the point. Unmarshalling into the struct would IGNORE a renamed key
// and silently zero it, so the mutant that matters most — a key the operator's
// script reads by name, gone — would survive with the test still green. The key
// set is also what makes the assertion impossible to satisfy from the text
// pane: prose is not a JSON object.
func leafJSONKeys(t *testing.T, stdout string) (map[string]any, []string) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("the -o json pane did not emit ONE parseable JSON object (%v); stdout was:\n%s", err, stdout)
	}
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return doc, keys
}

// wantKeys fails naming the difference, so a mutant reports WHICH key moved.
func wantKeys(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") == strings.Join(want, ",") {
		return
	}
	t.Fatalf("%s -o json top-level keys = [%s], want [%s] — a script reads these BY NAME, so a rename is a break",
		what, strings.Join(got, ","), strings.Join(want, ","))
}

// wantString / wantBool / wantNumber pin the VALUE TYPE as well as the value.
// A key kept with its type changed (a count emitted as "0" instead of 0) breaks
// every consumer while leaving the key set intact, so the key-set check alone
// would let that mutant live.
func wantString(t *testing.T, what, key string, doc map[string]any, want string) {
	t.Helper()
	got, ok := doc[key].(string)
	if !ok {
		t.Fatalf("%s -o json %q = %#v, want the STRING %q — a retyped field breaks a consumer while the key set still matches",
			what, key, doc[key], want)
	}
	if got != want {
		t.Fatalf("%s -o json %q = %q, want %q", what, key, got, want)
	}
}

func wantBool(t *testing.T, what, key string, doc map[string]any, want bool) {
	t.Helper()
	got, ok := doc[key].(bool)
	if !ok {
		t.Fatalf("%s -o json %q = %#v, want the BOOLEAN %t", what, key, doc[key], want)
	}
	if got != want {
		t.Fatalf("%s -o json %q = %t, want %t", what, key, got, want)
	}
}

func wantNumber(t *testing.T, what, key string, doc map[string]any, want float64) {
	t.Helper()
	got, ok := doc[key].(float64)
	if !ok {
		t.Fatalf("%s -o json %q = %#v, want the NUMBER %v — encoded as a string it breaks every consumer that does arithmetic on it",
			what, key, doc[key], want)
	}
	if got != want {
		t.Fatalf("%s -o json %q = %v, want %v", what, key, got, want)
	}
}

// ---------------------------------------------------------------------------
// superadmin enable / disable — ONE constructor, so ONE witness covers both
// ---------------------------------------------------------------------------

// TestSuperadminSetActiveTextAndJSON drives BOTH routes through
// superadminSetActiveCmd (cmd_superadmin.go), because they are the same
// constructor with `active` flipped: a witness that only exercised `disable`
// would call a shared code path once and claim two leaves.
func TestSuperadminSetActiveTextAndJSON(t *testing.T) {
	dir, idA, idB := seededSuperadminDataDir(t)

	// disable B (A stays the last active one, so this is permitted).
	out, errOut, err := runLeafCLI(t, "superadmin", "disable", "--email", "b@acme.test",
		"--data-dir", dir, "--actor", "ops", "--reason", "VER-06 witness")
	if err != nil {
		t.Fatalf("superadmin disable: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	if want := "superadmin " + idB + " is now inactive\n"; out != want {
		t.Fatalf("superadmin disable TEXT stdout = %q, want %q — the text pane is a contract of facts and must not move", out, want)
	}

	out, errOut, err = runLeafCLI(t, "superadmin", "enable", "--email", "b@acme.test",
		"--data-dir", dir, "--actor", "ops", "--reason", "VER-06 witness", "-o", "json")
	if err != nil {
		t.Fatalf("superadmin enable -o json: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	doc, keys := leafJSONKeys(t, out)
	wantKeys(t, "superadmin enable", keys, []string{"id", "status"})
	wantString(t, "superadmin enable", "id", doc, idB)
	wantString(t, "superadmin enable", "status", doc, "active")
	// The account's EMAIL is deliberately absent. core/model/auth.go:28 records
	// it as PII that is "never logged or exported", and the text pane of this
	// leaf prints the id and the status and no address — so a JSON pane carrying
	// one would export, into a machine-readable document a script redirects to a
	// file, a field the human pane withholds. `superadmin status` prints emails
	// because listing the accounts IS its subject; a mutation receipt's is not.
	if _, leaked := doc["email"]; leaked {
		t.Fatalf("superadmin enable -o json carries %q: the text pane prints id+status only, and core/model/auth.go marks the address as PII that is never exported", "email")
	}

	// And the reverse route again, in JSON, so the shared constructor is proved
	// on BOTH values of `active` rather than on one.
	out, errOut, err = runLeafCLI(t, "superadmin", "disable", "--email", "b@acme.test",
		"--data-dir", dir, "--actor", "ops", "--reason", "VER-06 witness", "-o", "json")
	if err != nil {
		t.Fatalf("superadmin disable -o json: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	doc, keys = leafJSONKeys(t, out)
	wantKeys(t, "superadmin disable", keys, []string{"id", "status"})
	wantString(t, "superadmin disable", "id", doc, idB)
	wantString(t, "superadmin disable", "status", doc, "inactive")

	// A is untouched by all of the above, which is what makes the two ids
	// distinguishable rather than interchangeable in the assertions.
	if idA == idB {
		t.Fatalf("fixture is degenerate: both superadmins have id %q", idA)
	}
}

// ---------------------------------------------------------------------------
// sources set / rm
// ---------------------------------------------------------------------------

// TestSourcesSetAndRemoveTextAndJSON covers set's THREE desenlaces (create,
// update, no-op) and rm, because `action` is the only field that separates them
// and all four exit 0 — a witness that drove one would pin the shape and prove
// nothing about the discriminator.
func TestSourcesSetAndRemoveTextAndJSON(t *testing.T) {
	dir := initialisedDataDir(t)
	base := []string{"--data-dir", dir, "--actor", "ops", "--reason", "VER-06 witness"}

	// --- create, in text: byte-exact ---
	out, errOut, err := runLeafCLI(t, append([]string{"sources", "set", "--name", "vault-prod",
		"--kind", "vault", "--tenant", "t_abc123"}, base...)...)
	if err != nil {
		t.Fatalf("sources set create: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	const wantCreate = "created source \"vault-prod\" (kind \"vault\", tenant \"t_abc123\", enabled true)\n" +
		"  kind: - → vault\n" +
		"  tenant: - → t_abc123\n" +
		"  poll_seconds: - → 0\n" +
		"  enabled: - → true\n" +
		"→ reload a running engine to apply: POST /v1/console/runtime/reload, or `kill -HUP <pid>` (it also applies at next boot)\n"
	if out != wantCreate {
		t.Fatalf("sources set create TEXT stdout changed.\n got: %q\nwant: %q", out, wantCreate)
	}

	// --- no-op, both panes ---
	out, errOut, err = runLeafCLI(t, append([]string{"sources", "set", "--name", "vault-prod",
		"--kind", "vault", "--tenant", "t_abc123"}, base...)...)
	if err != nil {
		t.Fatalf("sources set no-op: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	if want := "source \"vault-prod\" is already exactly this — nothing was written\n"; out != want {
		t.Fatalf("sources set no-op TEXT stdout = %q, want %q", out, want)
	}
	out, errOut, err = runLeafCLI(t, append([]string{"sources", "set", "--name", "vault-prod",
		"--kind", "vault", "--tenant", "t_abc123", "-o", "json"}, base...)...)
	if err != nil {
		t.Fatalf("sources set no-op -o json: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	doc, keys := leafJSONKeys(t, out)
	const setKeys = "action,changes,enabled,kind,name,persisted,tenant"
	wantKeys(t, "sources set", keys, strings.Split(setKeys, ","))
	wantString(t, "sources set no-op", "action", doc, "no-op")
	wantString(t, "sources set no-op", "name", doc, "vault-prod")
	wantString(t, "sources set no-op", "kind", doc, "vault")
	// EVERY field of this desenlace's literal, by value. `tenant` and `enabled`
	// were only covered by the key set, and the key set cannot see a zero value:
	// measured mutants that reported this source as tenantless (`Tenant: ""`) and
	// as disabled (`Enabled: !existing.Enabled`) both SURVIVED the battery green.
	// The text pane prints neither on a no-op, so the JSON document is the only
	// place a wrong answer here would ever be read.
	wantString(t, "sources set no-op", "tenant", doc, "t_abc123")
	wantBool(t, "sources set no-op", "enabled", doc, true)
	// `persisted:false` on a no-op is the load-bearing half of the pair with
	// `sources plan`: the roster deliberately does not Put an unchanged row, so
	// "nothing was written" is literally true here and this is the field that
	// says it. A mutant that reports `true` claims a write that did not happen —
	// and a write that did not happen is exactly what plan promised.
	wantBool(t, "sources set no-op", "persisted", doc, false)
	changes, ok := doc["changes"].([]any)
	if !ok {
		t.Fatalf("sources set no-op -o json %q = %#v, want a JSON ARRAY — a consumer doing `.changes | length` breaks on null", "changes", doc["changes"])
	}
	if len(changes) != 0 {
		t.Fatalf("sources set no-op reported %d change(s), want 0", len(changes))
	}

	// --- update, both panes ---
	out, errOut, err = runLeafCLI(t, append([]string{"sources", "set", "--name", "vault-prod",
		"--enabled=false"}, base...)...)
	if err != nil {
		t.Fatalf("sources set update: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	const wantUpdate = "updated source \"vault-prod\" (kind \"vault\", tenant \"t_abc123\", enabled false)\n" +
		"  enabled: true → false\n" +
		"→ reload a running engine to apply: POST /v1/console/runtime/reload, or `kill -HUP <pid>` (it also applies at next boot)\n"
	if out != wantUpdate {
		t.Fatalf("sources set update TEXT stdout changed.\n got: %q\nwant: %q", out, wantUpdate)
	}
	out, errOut, err = runLeafCLI(t, append([]string{"sources", "set", "--name", "vault-prod",
		"--enabled=true", "-o", "json"}, base...)...)
	if err != nil {
		t.Fatalf("sources set update -o json: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	doc, keys = leafJSONKeys(t, out)
	wantKeys(t, "sources set", keys, strings.Split(setKeys, ","))
	wantString(t, "sources set update", "action", doc, "update")
	wantBool(t, "sources set update", "persisted", doc, true)
	wantBool(t, "sources set update", "enabled", doc, true)
	// The three identity fields the TEXT pane prints in its first line, asserted
	// by value here too — that is what "the same data in both forms" means, and
	// without them mutants that emptied `name`, `kind` and `tenant` on this
	// literal all survived while the text pane went on naming the source
	// correctly. A document that describes the row an operator just wrote as an
	// unnamed, kindless, tenantless connector is worse than no document.
	wantString(t, "sources set update", "name", doc, "vault-prod")
	wantString(t, "sources set update", "kind", doc, "vault")
	wantString(t, "sources set update", "tenant", doc, "t_abc123")
	// The diff is the field an operator compares against `sources plan`, so its
	// shape is pinned too: one row, and the row carries plan's own field/from/to.
	changes, ok = doc["changes"].([]any)
	if !ok || len(changes) != 1 {
		t.Fatalf("sources set update reported changes = %#v, want exactly one row", doc["changes"])
	}
	row, ok := changes[0].(map[string]any)
	if !ok {
		t.Fatalf("sources set update change row = %#v, want an object", changes[0])
	}
	// VALUES, not presence. sourceChange carries no `omitempty`, so every one of
	// these keys is emitted whatever it holds and a presence loop here could not
	// fail — the same tautology a mutant exposed on the egress no-op below. The
	// diff is what plan and set are compared BY, so the direction of the move is
	// the thing worth pinning: this run flipped enabled back to true.
	wantString(t, "sources set update change row", "field", row, "enabled")
	wantString(t, "sources set update change row", "from", row, "false")
	wantString(t, "sources set update change row", "to", row, "true")

	// --- create, in JSON: the third value of `action`, on its own row ---
	//
	// The text pane above proves the CREATE verb ("created source …") but the JSON
	// pane was only ever driven on update and no-op, so `action: "create"` — the
	// value a script switches on to tell a new source from an edited one — was
	// emitted by a branch no witness reached. It is a separate struct literal from
	// update's, on a separate `verb`/`action` pair, so nothing about update
	// constrains it.
	out, errOut, err = runLeafCLI(t, append([]string{"sources", "set", "--name", "vault-dr",
		"--kind", "vault", "--tenant", "t_abc123", "-o", "json"}, base...)...)
	if err != nil {
		t.Fatalf("sources set create -o json: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	doc, keys = leafJSONKeys(t, out)
	wantKeys(t, "sources set", keys, strings.Split(setKeys, ","))
	wantString(t, "sources set create", "action", doc, "create")
	wantString(t, "sources set create", "name", doc, "vault-dr")
	wantString(t, "sources set create", "kind", doc, "vault")
	wantString(t, "sources set create", "tenant", doc, "t_abc123")
	wantBool(t, "sources set create", "enabled", doc, true)
	wantBool(t, "sources set create", "persisted", doc, true)
	// A create moves every field, so the diff carries one row per field of the new
	// definition, in the plan's reading order. Pinning the ORDER matters because
	// plan and set are compared line for line, and a reordered diff cannot be.
	changes, ok = doc["changes"].([]any)
	if !ok || len(changes) != 4 {
		t.Fatalf("sources set create reported changes = %#v, want four rows (kind, tenant, poll_seconds, enabled)", doc["changes"])
	}
	// field, from AND to on every row. Pinning the field alone would leave the
	// values free, which is the same tautology this whole pass is about: `from` is
	// empty on a create because nothing was there, and `to` is what was written.
	for i, want := range []struct{ field, to string }{
		{"kind", "vault"}, {"tenant", "t_abc123"}, {"poll_seconds", "0"}, {"enabled", "true"},
	} {
		crow, rok := changes[i].(map[string]any)
		if !rok {
			t.Fatalf("sources set create change row %d = %#v, want an object", i, changes[i])
		}
		where := fmt.Sprintf("sources set create change row %d", i)
		wantString(t, where, "field", crow, want.field)
		wantString(t, where, "from", crow, "")
		wantString(t, where, "to", crow, want.to)
	}
	if _, _, rerr := runLeafCLI(t, append([]string{"sources", "rm", "--name", "vault-dr", "--yes"}, base...)...); rerr != nil {
		t.Fatalf("clean up the create witness's row: %v", rerr)
	}

	// --- rm, both panes ---
	out, errOut, err = runLeafCLI(t, append([]string{"sources", "rm", "--name", "vault-prod", "--yes"}, base...)...)
	if err != nil {
		t.Fatalf("sources rm: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	const wantRm = "deleted source \"vault-prod\"\n" +
		"→ reload a running engine to stop it live: POST /v1/console/runtime/reload, or `kill -HUP <pid>`\n"
	if out != wantRm {
		t.Fatalf("sources rm TEXT stdout changed.\n got: %q\nwant: %q", out, wantRm)
	}

	if _, _, serr := runLeafCLI(t, append([]string{"sources", "set", "--name", "vault-prod",
		"--kind", "vault", "--tenant", "t_abc123"}, base...)...); serr != nil {
		t.Fatalf("re-create for the rm json pane: %v", serr)
	}
	out, errOut, err = runLeafCLI(t, append([]string{"sources", "rm", "--name", "vault-prod", "--yes", "-o", "json"}, base...)...)
	if err != nil {
		t.Fatalf("sources rm -o json: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	doc, keys = leafJSONKeys(t, out)
	wantKeys(t, "sources rm", keys, []string{"action", "name", "persisted"})
	wantString(t, "sources rm", "action", doc, "delete")
	wantString(t, "sources rm", "name", doc, "vault-prod")
	wantBool(t, "sources rm", "persisted", doc, true)
	// rm knows the name and nothing else: it deletes by name without reading the
	// row. A kind/tenant/enabled here would be three zero values describing the
	// deleted source as an unnamed, disabled, tenantless connector.
	for _, absent := range []string{"kind", "tenant", "enabled", "changes"} {
		if _, present := doc[absent]; present {
			t.Fatalf("sources rm -o json carries %q, which it cannot know: it deletes by name and never reads the row", absent)
		}
	}
}

// ---------------------------------------------------------------------------
// eventing egress actuate
// ---------------------------------------------------------------------------

// TestEventingEgressActuateTextAndJSON covers both zero-exit desenlaces. A fresh
// SQLite install is CLASSIFIED enforced with the commitment clear, so the first
// run commits (action=actuated) and the second finds it already there
// (action=no-op) — which is the pair the `action` field exists to separate,
// since neither run can be told from the other by its exit code.
func TestEventingEgressActuateTextAndJSON(t *testing.T) {
	// ⛔ EL ACTOR NO PUEDE SALIR DEL ENTORNO DE QUIEN CORRE EL TEST. `cliEgressActor`
	// (cmd_eventing_egress.go:352-360) lee $OLIVARES_ACTOR, luego $USER, y sólo si no hay ninguno
	// devuelve `cli:unidentified-operator` — que es lo que estas aserciones esperan. Pasaba en un
	// portátil sin $USER exportado y FALLABA en CI, donde el job corre como root:
	//
	//     got:  "... decided by cli:root ..."
	//     want: "... decided by cli:unidentified-operator ..."
	//
	// (corrida 32605874242, job control-plane, 2026-08-23). No es un test flakey: es un test que
	// mide la caja. Se fija la entrada en vez de relajar la aserción, que sería apagar el control.
	t.Setenv("OLIVARES_ACTOR", "")
	t.Setenv("USER", "")
	dir := initialisedDataDir(t)
	args := []string{"eventing", "egress", "actuate", "--mode", "enforced",
		"--reason", "VER-06 witness", "--assert-writers-upgraded", "--accept-blocked", "--data-dir", dir}

	out, errOut, err := runLeafCLI(t, args...)
	if err != nil {
		t.Fatalf("egress actuate: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	const wantActuated = "egress destination control is now enforced (generation 2, decided by cli:unidentified-operator)\n" +
		"\n→ running nodes converge within seconds; no restart is needed.\n"
	if out != wantActuated {
		t.Fatalf("egress actuate TEXT stdout changed.\n got: %q\nwant: %q", out, wantActuated)
	}

	out, errOut, err = runLeafCLI(t, args...)
	if err != nil {
		t.Fatalf("egress actuate second run: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	if want := "already in mode enforced (generation 2), committed; nothing to do\n"; out != want {
		t.Fatalf("egress actuate no-op TEXT stdout = %q, want %q", out, want)
	}
	out, errOut, err = runLeafCLI(t, append(args, "-o", "json")...)
	if err != nil {
		t.Fatalf("egress actuate no-op -o json: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	doc, keys := leafJSONKeys(t, out)
	wantKeys(t, "egress actuate", keys, []string{"action", "decided_by", "enforcement_committed", "generation", "mode"})
	wantString(t, "egress actuate no-op", "action", doc, "no-op")
	wantString(t, "egress actuate no-op", "mode", doc, "enforced")
	wantNumber(t, "egress actuate no-op", "generation", doc, 2)
	wantBool(t, "egress actuate no-op", "enforcement_committed", doc, true)
	// `decided_by` is carried in the no-op too, where the text pane does not print
	// it, so the document's shape does not change with the outcome.
	//
	// Its VALUE is asserted, not its presence. A presence check here was a
	// tautology and a mutant proved it: the field has no `omitempty`, so
	// json.Marshal emits the key whatever it holds, and wantKeys above already
	// pins the key set — dropping `DecidedBy` from the no-op's struct literal left
	// the key in place with an empty string and sailed past both. What an operator
	// reads this field FOR is who decided, so who decided is what is checked.
	wantString(t, "egress actuate no-op", "decided_by", doc, "cli:unidentified-operator")

	// The ACTUATED desenlace's JSON, on its own install, so `action` is measured
	// against a run that really moved the control rather than against a no-op.
	dir2 := initialisedDataDir(t)
	out, errOut, err = runLeafCLI(t, "eventing", "egress", "actuate", "--mode", "enforced",
		"--reason", "VER-06 witness", "--assert-writers-upgraded", "--accept-blocked",
		"--data-dir", dir2, "-o", "json")
	if err != nil {
		t.Fatalf("egress actuate -o json: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	doc, keys = leafJSONKeys(t, out)
	wantKeys(t, "egress actuate", keys, []string{"action", "decided_by", "enforcement_committed", "generation", "mode"})
	wantString(t, "egress actuate", "action", doc, "actuated")
	wantString(t, "egress actuate", "mode", doc, "enforced")
	wantBool(t, "egress actuate", "enforcement_committed", doc, true)
	wantString(t, "egress actuate", "decided_by", doc, "cli:unidentified-operator")
	// The generation, on the run that MOVED the control. It was asserted on the
	// no-op above and not here, and the two are different struct literals: a
	// measured mutant that reported `generation: 0` from the actuated branch
	// survived green while the text pane printed generation 2 on the same run.
	// The generation is what a proof is bound to, so a receipt that understates it
	// is the one number in this document a caller must not have to double-check.
	wantNumber(t, "egress actuate", "generation", doc, 2)
}

// ---------------------------------------------------------------------------
// eventing fence verify / arm
// ---------------------------------------------------------------------------

// TestEventingFenceVerifyTextAndJSON covers BOTH zero-exit desenlaces, and the
// dormant one is the reason this test exists in the shape it does: ENFORCING and
// DORMANT both exit 0, so before `verified` a script could not tell "the database
// refused every governed mutation" from "nothing was demanded of anyone".
func TestEventingFenceVerifyTextAndJSON(t *testing.T) {
	// A fresh install is classified enforced, so the fence is live here.
	dir := initialisedDataDir(t)
	out, errOut, err := runLeafCLI(t, "eventing", "fence", "verify", "--data-dir", dir)
	if err != nil {
		t.Fatalf("fence verify: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	if want := "the egress writer fence is ENFORCING (generation 1, required capability 1)\n"; out != want {
		t.Fatalf("fence verify ENFORCING TEXT stdout = %q, want %q", out, want)
	}
	out, errOut, err = runLeafCLI(t, "eventing", "fence", "verify", "--data-dir", dir, "-o", "json")
	if err != nil {
		t.Fatalf("fence verify -o json: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	doc, keys := leafJSONKeys(t, out)
	const verifyKeys = "enforcement,generation,mode,required_capability,verified"
	wantKeys(t, "fence verify", keys, strings.Split(verifyKeys, ","))
	wantBool(t, "fence verify live", "verified", doc, true)
	wantString(t, "fence verify live", "enforcement", doc, fenceEnforcementLive)
	wantNumber(t, "fence verify live", "required_capability", doc, 1)
	// `mode` and `generation` are asserted on BOTH desenlaces below and here,
	// because both come out of the single projection fenceVerifyResultFrom: with
	// neither call site pinning them, measured mutants that emptied `mode` and
	// zeroed `generation` INSIDE that shared function survived the whole battery.
	// A shared projection is not covered by covering one of its callers.
	wantString(t, "fence verify live", "mode", doc, "enforced")
	wantNumber(t, "fence verify live", "generation", doc, 1)

	// --- the DORMANT desenlace, on a deployment moved off enforced ---
	dormantDir := initialisedDataDir(t)
	makeFenceDormant(t, dormantDir)
	out, errOut, err = runLeafCLI(t, "eventing", "fence", "verify", "--data-dir", dormantDir)
	if err != nil {
		t.Fatalf("fence verify on a dormant fence must exit zero: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	if !strings.HasPrefix(out, "the egress writer fence is DORMANT (mode ") || !strings.HasSuffix(out, "); nothing to verify\n") {
		t.Fatalf("fence verify DORMANT TEXT stdout = %q, want the DORMANT sentence", out)
	}
	out, errOut, err = runLeafCLI(t, "eventing", "fence", "verify", "--data-dir", dormantDir, "-o", "json")
	if err != nil {
		t.Fatalf("fence verify dormant -o json: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	doc, keys = leafJSONKeys(t, out)
	wantKeys(t, "fence verify", keys, strings.Split(verifyKeys, ","))
	// THE ASSERTION THIS TEST IS FOR. A dormant fence proves nothing in either
	// direction, so `verified` must be FALSE even though the command exits 0, and
	// `enforcement` must carry the reason. `verified: true` here would deliver
	// "I could not establish this" as "the fence holds" — the exact substitution
	// the three-state enforcement vocabulary exists to forbid.
	wantBool(t, "fence verify dormant", "verified", doc, false)
	wantString(t, "fence verify dormant", "enforcement", doc, fenceEnforcementDormant)
	wantNumber(t, "fence verify dormant", "required_capability", doc, 0)
	// The mode the deployment was moved INTO is the fact that explains `verified:
	// false`, and the text pane prints it ("DORMANT (mode policy_optional, …)").
	// Asserted by value on this side too, so the shared projection is pinned from
	// both of its call sites rather than from neither.
	wantString(t, "fence verify dormant", "mode", doc, "policy_optional")
	wantNumber(t, "fence verify dormant", "generation", doc, 2)
}

// TestEventingFenceArmTextAndJSON covers arm's two zero-exit desenlaces on a
// fresh install: the arming itself, and the already-armed shortcut.
func TestEventingFenceArmTextAndJSON(t *testing.T) {
	// ⛔ EL ACTOR NO PUEDE SALIR DEL ENTORNO DE QUIEN CORRE EL TEST. `cliEgressActor`
	// (cmd_eventing_egress.go:352-360) lee $OLIVARES_ACTOR, luego $USER, y sólo si no hay ninguno
	// devuelve `cli:unidentified-operator` — que es lo que estas aserciones esperan. Pasaba en un
	// portátil sin $USER exportado y FALLABA en CI, donde el job corre como root:
	//
	//     got:  "... decided by cli:root ..."
	//     want: "... decided by cli:unidentified-operator ..."
	//
	// (corrida 32605874242, job control-plane, 2026-08-23). No es un test flakey: es un test que
	// mide la caja. Se fija la entrada en vez de relajar la aserción, que sería apagar el control.
	t.Setenv("OLIVARES_ACTOR", "")
	t.Setenv("USER", "")
	dir := initialisedDataDir(t)
	args := []string{"eventing", "fence", "arm", "--reason", "VER-06 witness",
		"--assert-writers-upgraded", "--data-dir", dir}

	out, errOut, err := runLeafCLI(t, args...)
	if err != nil {
		t.Fatalf("fence arm: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	// The whole sequence, byte for byte, INCLUDING the "now ARMED" line that is
	// printed before the verification probe runs. That line's position is the
	// subject of a comment in cmd_eventing_fence.go: it must stay ahead of the
	// probe so an operator whose verification fails still sees that the decision
	// was recorded. Asserting the full string is what pins the ORDER, not just
	// the presence.
	const wantArmed = "egress writer fence is now ARMED (generation 2, decided by cli:unidentified-operator)\n" +
		"verified: the database refuses EVERY governed mutation that carries no capability attestation.\n" +
		"\nThis does NOT prove the fleet's composition and does NOT prove the past. It\n" +
		"makes a future violation fail visibly instead of succeeding silently.\n" +
		"\n→ running nodes converge within seconds; no restart is needed.\n"
	if out != wantArmed {
		t.Fatalf("fence arm TEXT stdout changed.\n got: %q\nwant: %q", out, wantArmed)
	}

	// --- the already-armed shortcut, both panes ---
	out, errOut, err = runLeafCLI(t, args...)
	if err != nil {
		t.Fatalf("fence arm second run: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	if want := "the egress writer fence is already armed, committed and enforcing (generation 2, decided by cli:unidentified-operator); nothing to do\n"; out != want {
		t.Fatalf("fence arm no-op TEXT stdout = %q, want %q", out, want)
	}
	out, errOut, err = runLeafCLI(t, append(args, "-o", "json")...)
	if err != nil {
		t.Fatalf("fence arm no-op -o json: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	doc, keys := leafJSONKeys(t, out)
	const armKeys = "action,armed,decided_by,generation,verified"
	wantKeys(t, "fence arm", keys, strings.Split(armKeys, ","))
	wantString(t, "fence arm no-op", "action", doc, fenceArmActionAlready)
	wantBool(t, "fence arm no-op", "armed", doc, true)
	wantBool(t, "fence arm no-op", "verified", doc, true)
	wantNumber(t, "fence arm no-op", "generation", doc, 2)
	// WHO decided is the field this whole ceremony exists to record — arming
	// refuses to run without a --reason precisely so the decision has an owner —
	// and no desenlace asserted its value. Measured: `DecidedBy: ""` on either of
	// the two literals survived the battery, so the document could report an
	// ownerless decision while the text pane named the operator.
	wantString(t, "fence arm no-op", "decided_by", doc, "cli:unidentified-operator")

	// --- the ARMING desenlace's JSON, on its own install ---
	dir2 := initialisedDataDir(t)
	out, errOut, err = runLeafCLI(t, "eventing", "fence", "arm", "--reason", "VER-06 witness",
		"--assert-writers-upgraded", "--data-dir", dir2, "-o", "json")
	if err != nil {
		t.Fatalf("fence arm -o json: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	// THE LEAK GUARD RUNS FIRST, and it was moved here by measurement rather than
	// by taste — the same correction 43a120f3a made to the four json forms of
	// sealedmaterial_json_test.go. With the decode ahead of it, a mutant that
	// dropped the `format == "text"` guard on the "now ARMED" line — prose printed
	// in front of the document a script parses — died reporting `did not emit ONE
	// parseable JSON object (invalid character 'e')`. Real kill, wrong finding: the
	// complaint was about JSON syntax, and the defect was the text pane bleeding
	// onto the machine-readable stream. Whoever later relaxed the decode would move
	// that leak from "caught" to "not looked at" with nothing recording that the
	// leak guard had never been what held.
	//
	// The TEXT half above keeps the opposite order deliberately: there the contract
	// IS byte-identity with the pre-change binary, so the byte comparison is the
	// primary assertion. This form has no byte-identity contract — it is new — so
	// its guards are peers and the leak is the finding that matters.
	for _, prose := range []string{"now ARMED", "verified:", "fleet's composition", "converge within seconds"} {
		if strings.Contains(out, prose) {
			t.Fatalf("fence arm -o json leaked the text pane's %q onto stdout:\n%s", prose, out)
		}
	}
	doc, keys = leafJSONKeys(t, out)
	wantKeys(t, "fence arm", keys, strings.Split(armKeys, ","))
	wantString(t, "fence arm", "action", doc, fenceArmActionArmed)
	wantBool(t, "fence arm", "armed", doc, true)
	wantBool(t, "fence arm", "verified", doc, true)
	// The generation and the owner, on the literal that records a NEW decision.
	// Both were unasserted on this desenlace and both survived being zeroed. The
	// generation is what every outstanding writer proof is bound to, so a receipt
	// reporting 0 for the arming that just moved it tells a script the fence is at
	// a generation that never existed.
	wantNumber(t, "fence arm", "generation", doc, 2)
	wantString(t, "fence arm", "decided_by", doc, "cli:unidentified-operator")
}

// makeFenceDormant moves the writer fence off enforced, which is what an upgraded
// estate is classified into. Modeled exactly as cmd_eventing_fence_test.go does it.
func makeFenceDormant(t *testing.T, dir string) {
	t.Helper()
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: dir, Version: "test", Logger: discardLog()})
	if err != nil {
		t.Fatalf("boot to make the fence dormant: %v", err)
	}
	defer func() { _ = eng.Close() }()
	rs, ok := eng.store.(store.RolloutStater)
	if !ok {
		t.Fatal("store exposes no durable rollout state")
	}
	cur, err := rs.RolloutState(ctx, eventing.EgressWriterFenceControlKey)
	if err != nil {
		t.Fatalf("read fence state: %v", err)
	}
	if _, err := rs.SetRolloutMode(ctx, store.RolloutTransition{
		Key: eventing.EgressWriterFenceControlKey, Mode: store.RolloutPolicyOptional,
		Actor: "test", Reason: "model a deployment that is not armed", ExpectGeneration: cur.Generation,
	}); err != nil {
		t.Fatalf("move the fence off enforced: %v", err)
	}
}

// seededSuperadminDataDir provisions two superadmins and returns their ids.
func seededSuperadminDataDir(t *testing.T) (dir, idA, idB string) {
	t.Helper()
	ctx := context.Background()
	dir = t.TempDir()
	eng, err := boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Version: "test", Logger: slog.Default()})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	a, err := eng.authr.BootstrapSuperadmin(ctx, "a@acme.test", "supersecret-pw")
	if err != nil {
		t.Fatalf("bootstrap A: %v", err)
	}
	b, err := eng.authr.CreateUser(ctx, mustTestOperator("test"), auth.NewUser{
		Email: "b@acme.test", DisplayName: "B", Password: "supersecret-pw", Superadmin: true,
	})
	if err != nil {
		t.Fatalf("create B: %v", err)
	}
	if cerr := eng.Close(); cerr != nil {
		t.Fatalf("close seeding engine: %v", cerr)
	}
	return dir, a.ID.String(), b.ID.String()
}

// ---------------------------------------------------------------------------
// eventing subscriptions create / rm / test, and dead-letters redeliver
// ---------------------------------------------------------------------------

// TestEventingSubscriptionCreateTextAndJSON covers the leaf that mints the
// signing secret.
//
// The text pane is asserted byte-exact around its TWO random values (the id and
// the secret) by extracting them and rebuilding the whole string: the template
// pins every other byte, including the blank lines and the trailing reload note.
//
// The JSON pane's `secret` is then PROVED to be the real signing key rather than
// asserted to look like one — see the sub-test below. That distinction is the
// point: a mutant that emitted the hint, or the sealed ciphertext, or an empty
// string would satisfy any "is it a non-empty string" check while handing the
// operator a credential that signs nothing.
func TestEventingSubscriptionCreateTextAndJSON(t *testing.T) {
	dir, tenant := seededTenantDataDir(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	allowLoopbackEgress(t, srv.URL)

	// --- text: byte-exact around the id and the secret ---
	out, errOut, err := runLeafCLI(t, "eventing", "subscriptions", "create",
		"--tenant", tenant, "--name", "siem-text", "--endpoint", srv.URL+"/hook",
		"--event-types", "audit.created", "--data-dir", dir)
	if err != nil {
		t.Fatalf("subscriptions create (text): %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	id, secret := parseCreatedSubscription(t, out)
	want := fmt.Sprintf("created subscription %q (id %s)\n"+
		"\nSigning secret (shown ONCE — store it now):\n  %s\n"+
		"\n→ reload a running engine to activate: POST /v1/console/runtime/reload, or `kill -HUP <pid>`\n",
		"siem-text", id, secret)
	if out != want {
		t.Fatalf("subscriptions create TEXT stdout changed.\n got: %q\nwant: %q", out, want)
	}
	if errOut != "" {
		t.Fatalf("subscriptions create (text) wrote to stderr: %q", errOut)
	}

	// --- json: the key set, and NOTHING on stderr ---
	out, errOut, err = runLeafCLI(t, "eventing", "subscriptions", "create",
		"--tenant", tenant, "--name", "siem-json", "--endpoint", srv.URL+"/hook",
		"--event-types", "audit.created", "--data-dir", dir, "-o", "json")
	if err != nil {
		t.Fatalf("subscriptions create -o json: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	if errOut != "" {
		t.Fatalf("subscriptions create -o json wrote to stderr: %q — a script parsing stdout must not need to drain stderr", errOut)
	}
	doc, keys := leafJSONKeys(t, out)
	wantKeys(t, "subscriptions create", keys, strings.Split("id,name,reload_required,secret", ","))
	wantString(t, "subscriptions create", "name", doc, "siem-json")
	wantBool(t, "subscriptions create", "reload_required", doc, true)
	jsonSecret, ok := doc["secret"].(string)
	if !ok || jsonSecret == "" {
		t.Fatalf("subscriptions create -o json %q = %#v, want the minted signing secret as a non-empty STRING", "secret", doc["secret"])
	}
	jsonID, ok := doc["id"].(string)
	if !ok || jsonID == "" {
		t.Fatalf("subscriptions create -o json %q = %#v, want the new subscription's id", "id", doc["id"])
	}

	// --- the secret in the JSON pane IS the signing key ---
	//
	// This is the assertion that cannot be faked. `subscriptions test` signs a
	// delivery with the secret it unseals FROM THE STORE; recomputing that
	// signature with the secret the JSON pane HANDED US and getting a match proves
	// the two are the same bytes. The hint, the sealed ciphertext and any
	// truncation all fail here.
	var gotSig, gotTS string
	var gotBody []byte
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Olivares-Signature")
		gotTS = r.Header.Get("X-Olivares-Timestamp")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer sink.Close()
	out, errOut, err = runLeafCLI(t, "eventing", "subscriptions", "create",
		"--tenant", tenant, "--name", "siem-signing", "--endpoint", sink.URL+"/hook",
		"--event-types", "audit.created", "--data-dir", dir, "-o", "json")
	if err != nil {
		t.Fatalf("subscriptions create for the signing proof: %v\nstderr: %s", err, errOut)
	}
	signDoc, _ := leafJSONKeys(t, out)
	mintedSecret, _ := signDoc["secret"].(string)
	mintedID, _ := signDoc["id"].(string)
	if out, errOut, err = runLeafCLI(t, "eventing", "subscriptions", "test",
		"--tenant", tenant, "--id", mintedID, "--data-dir", dir); err != nil {
		t.Fatalf("subscriptions test for the signing proof: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	if gotSig == "" {
		t.Fatalf("the endpoint received no X-Olivares-Signature; the signing proof cannot conclude")
	}
	if wantSig := "t=" + gotTS + ",v1=" + webhook.Sign(mintedSecret, gotTS, gotBody); gotSig != wantSig {
		t.Fatalf("the signature the engine sent does NOT verify under the secret `-o json` reported.\n"+
			"  sent:      %s\n  recomputed: %s\n"+
			"the JSON pane handed the operator a credential that does not sign this subscription's deliveries", gotSig, wantSig)
	}
}

// TestEventingSubscriptionRemoveTextAndJSON covers `subscriptions rm`.
func TestEventingSubscriptionRemoveTextAndJSON(t *testing.T) {
	dir, tenant := seededTenantDataDir(t)
	idText := seedSubscriptionRow(t, dir, tenant, "https://siem.example/hooks", "")
	idJSON := seedSubscriptionRow(t, dir, tenant, "https://siem.example/hooks", "")

	out, errOut, err := runLeafCLI(t, "eventing", "subscriptions", "rm",
		"--tenant", tenant, "--id", idText, "--yes", "--data-dir", dir)
	if err != nil {
		t.Fatalf("subscriptions rm (text): %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	if want := fmt.Sprintf("deleted subscription %q\n", idText); out != want {
		t.Fatalf("subscriptions rm TEXT stdout = %q, want %q", out, want)
	}

	out, errOut, err = runLeafCLI(t, "eventing", "subscriptions", "rm",
		"--tenant", tenant, "--id", idJSON, "--yes", "--data-dir", dir, "-o", "json")
	if err != nil {
		t.Fatalf("subscriptions rm -o json: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	doc, keys := leafJSONKeys(t, out)
	wantKeys(t, "subscriptions rm", keys, strings.Split("deleted,id", ","))
	// The PARSED id — the row actually deleted — not the operator's argument.
	wantString(t, "subscriptions rm", "id", doc, idJSON)
	wantBool(t, "subscriptions rm", "deleted", doc, true)
}

// TestEventingSubscriptionTestTextAndJSON covers all THREE desenlaces of
// `subscriptions test`, which all exit 0 and all report on stdout — so the exit
// code distinguishes none of them and `ok`/`http_status` is the only thing that
// does.
func TestEventingSubscriptionTestTextAndJSON(t *testing.T) {
	dir, tenant := seededTenantDataDir(t)

	ok204 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ok204.Close()
	bad500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad500.Close()
	// A listener that is CLOSED, so the dial fails and the third desenlace is a
	// real transport failure rather than a simulated one.
	deadURL := deadLoopbackURL(t)
	allowLoopbackEgress(t, ok204.URL)

	idOK := seedSubscriptionRow(t, dir, tenant, ok204.URL+"/hook", sealedTestSecret(t, dir, tenant))
	idBad := seedSubscriptionRow(t, dir, tenant, bad500.URL+"/hook", sealedTestSecret(t, dir, tenant))
	idDead := seedSubscriptionRow(t, dir, tenant, deadURL+"/hook", sealedTestSecret(t, dir, tenant))

	// --- 2xx ---
	out, errOut, err := runLeafCLI(t, "eventing", "subscriptions", "test", "--tenant", tenant, "--id", idOK, "--data-dir", dir)
	if err != nil {
		t.Fatalf("subscriptions test 204 (text): %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	if want := "test OK: HTTP 204\n"; out != want {
		t.Fatalf("subscriptions test 204 TEXT stdout = %q, want %q", out, want)
	}
	out, errOut, err = runLeafCLI(t, "eventing", "subscriptions", "test", "--tenant", tenant, "--id", idOK, "--data-dir", dir, "-o", "json")
	if err != nil {
		t.Fatalf("subscriptions test 204 -o json: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	doc, keys := leafJSONKeys(t, out)
	const testKeys = "error,http_status,ok"
	wantKeys(t, "subscriptions test 204", keys, strings.Split(testKeys, ","))
	wantBool(t, "subscriptions test 204", "ok", doc, true)
	wantNumber(t, "subscriptions test 204", "http_status", doc, 204)
	wantString(t, "subscriptions test 204", "error", doc, "")

	// --- non-2xx: still exit 0, still stdout, ok=false ---
	out, errOut, err = runLeafCLI(t, "eventing", "subscriptions", "test", "--tenant", tenant, "--id", idBad, "--data-dir", dir)
	if err != nil {
		t.Fatalf("subscriptions test 500 (text): %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	if want := "test FAILED: HTTP 500\n"; out != want {
		t.Fatalf("subscriptions test 500 TEXT stdout = %q, want %q", out, want)
	}
	out, errOut, err = runLeafCLI(t, "eventing", "subscriptions", "test", "--tenant", tenant, "--id", idBad, "--data-dir", dir, "-o", "json")
	if err != nil {
		t.Fatalf("subscriptions test 500 -o json: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	doc, keys = leafJSONKeys(t, out)
	wantKeys(t, "subscriptions test 500", keys, strings.Split(testKeys, ","))
	wantBool(t, "subscriptions test 500", "ok", doc, false)
	wantNumber(t, "subscriptions test 500", "http_status", doc, 500)
	// `error` is EMPTY on an HTTP failure: the status carries the fact, and
	// inventing a message here would make the field mean two different things.
	wantString(t, "subscriptions test 500", "error", doc, "")

	// --- transport failure: exit 0 on STDOUT, http_status 0, reason in `error` ---
	out, errOut, err = runLeafCLI(t, "eventing", "subscriptions", "test", "--tenant", tenant, "--id", idDead, "--data-dir", dir)
	if err != nil {
		t.Fatalf("subscriptions test transport failure should exit 0 (its pre-existing contract): %v\nstderr: %s", err, errOut)
	}
	if !strings.HasPrefix(out, "test FAILED: ") || strings.HasPrefix(out, "test FAILED: HTTP ") {
		t.Fatalf("subscriptions test transport failure TEXT stdout = %q, want a `test FAILED: <transport error>` line with no HTTP code", out)
	}
	// The exact reason the TEXT pane gave, kept so the json pane can be compared
	// against it instead of merely checked for emptiness. Both runs dial the same
	// closed port on the same seeded row, so the dial error is the same bytes.
	textReason := strings.TrimSuffix(strings.TrimPrefix(out, "test FAILED: "), "\n")
	out, errOut, err = runLeafCLI(t, "eventing", "subscriptions", "test", "--tenant", tenant, "--id", idDead, "--data-dir", dir, "-o", "json")
	if err != nil {
		t.Fatalf("subscriptions test transport failure -o json: %v\nstderr: %s", err, errOut)
	}
	doc, keys = leafJSONKeys(t, out)
	wantKeys(t, "subscriptions test transport failure", keys, strings.Split(testKeys, ","))
	wantBool(t, "subscriptions test transport failure", "ok", doc, false)
	// ZERO, not absent: the same three keys appear in all three desenlaces, so a
	// consumer never tests for a key's existence before reading it.
	wantNumber(t, "subscriptions test transport failure", "http_status", doc, 0)
	// THE SAME REASON, not merely a non-empty one. "is it non-empty" was the check
	// here, and a mutant measured what it costs: replacing `err.Error()` with the
	// canned sentence "the endpoint did not respond" survived green while the text
	// pane went on printing the real dial failure — so the two panes of one run
	// disagreed about WHY, which is the only thing this desenlace has to say. An
	// operator debugging a webhook needs `dial tcp …: connection refused`, and a
	// document that launders it into a slogan is worse than an absent field,
	// because a script cannot tell it is being told nothing.
	wantString(t, "subscriptions test transport failure", "error", doc, textReason)
}

// TestEventingDeadLettersRedeliverTextAndJSON covers `dead-letters redeliver`.
//
// The JSON is read back from the row the store RETURNED, so this witness pins the
// two facts a caller acts on: the delivery is queued again, and its attempt count
// was reset. A mutant that requeued to another status, or left the attempts
// standing, changes these numbers.
func TestEventingDeadLettersRedeliverTextAndJSON(t *testing.T) {
	dir, tenant := seededTenantDataDir(t)
	subID := seedSubscriptionRow(t, dir, tenant, "https://siem.example/hooks", "")
	idText := seedDeadDelivery(t, dir, tenant, subID)
	idJSON := seedDeadDelivery(t, dir, tenant, subID)

	out, errOut, err := runLeafCLI(t, "eventing", "dead-letters", "redeliver",
		"--tenant", tenant, "--id", idText, "--data-dir", dir)
	if err != nil {
		t.Fatalf("dead-letters redeliver (text): %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	want := fmt.Sprintf("requeued delivery %q (status → queued, attempts → 0)\n"+
		"→ reload a running engine to dispatch immediately, or wait for the next pump tick\n", idText)
	if out != want {
		t.Fatalf("dead-letters redeliver TEXT stdout changed.\n got: %q\nwant: %q", out, want)
	}

	out, errOut, err = runLeafCLI(t, "eventing", "dead-letters", "redeliver",
		"--tenant", tenant, "--id", idJSON, "--data-dir", dir, "-o", "json")
	if err != nil {
		t.Fatalf("dead-letters redeliver -o json: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	doc, keys := leafJSONKeys(t, out)
	wantKeys(t, "dead-letters redeliver", keys, strings.Split("attempts,id,status", ","))
	wantString(t, "dead-letters redeliver", "id", doc, idJSON)
	wantString(t, "dead-letters redeliver", "status", doc, "queued")
	wantNumber(t, "dead-letters redeliver", "attempts", doc, 0)
}

// seedSeq keeps every seeded row's unique columns distinct. Two seeded rows in
// one data dir collide on (tenant_id, name) for subscriptions and on
// (tenant_id, event_id) for events, and each witness below deliberately seeds
// TWO rows — one per pane — so that the text run and the JSON run never observe
// each other's mutation.
var seedSeq atomic.Int64

// parseCreatedSubscription pulls the id and the secret out of the text pane so the
// rest of the line can be compared byte for byte. The pattern is ANCHORED and
// pins the literal prose around both values, so it cannot match a reshaped line.
func parseCreatedSubscription(t *testing.T, stdout string) (id, secret string) {
	t.Helper()
	re := regexp.MustCompile(`^created subscription "[^"]+" \(id ([0-9a-f-]{36})\)\n` +
		`\nSigning secret \(shown ONCE — store it now\):\n  (olvw_[0-9a-f]{48})\n`)
	m := re.FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("the text pane of `subscriptions create` no longer matches its documented shape; stdout was:\n%q", stdout)
	}
	return m[1], m[2]
}

// allowLoopbackEgress authorizes loopback destinations for one test.
//
// Both switches are required and they gate DIFFERENT things: the policy file is
// the platform operator's authorization of a destination, and the loopback env is
// the separate refusal to dial private space. A test that set only one gets a
// refusal that looks like a bug in the leaf under test.
func allowLoopbackEgress(t *testing.T, _ string) {
	t.Helper()
	pol := filepath.Join(t.TempDir(), "egress.json")
	if err := os.WriteFile(pol, []byte(`{"default":{"allow":[{"cidr":"127.0.0.0/8"}]}}`), 0o600); err != nil {
		t.Fatalf("write egress policy: %v", err)
	}
	t.Setenv(envEventingEgressPolicy, pol)
	t.Setenv(eventingAllowLoopbackEnv, "1")
}

// deadLoopbackURL returns a loopback URL whose listener is already CLOSED, so a
// dial to it fails for real.
func deadLoopbackURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port to close: %v", err)
	}
	addr := ln.Addr().String()
	if cerr := ln.Close(); cerr != nil {
		t.Fatalf("close the reserved listener: %v", cerr)
	}
	return "http://" + addr
}

// sealedTestSecret seals a signing secret for this data dir, so `subscriptions
// test` can unseal it and sign.
func sealedTestSecret(t *testing.T, dir, tenant string) string {
	t.Helper()
	tid, err := model.ParseTenantID(tenant)
	if err != nil {
		t.Fatalf("parse tenant: %v", err)
	}
	sealer, err := newEventingSealer(dir, os.Getenv)
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	sealed, err := sealer.Seal(context.Background(), tid, []byte("olvw_"+strings.Repeat("a", 48)))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return sealed
}

// seedSubscriptionRow writes one subscription row directly, carrying the writer
// proof the armed fence demands of every governed writer. An empty `sealed` seeds
// a row whose secret is not usable for signing, which is all the delete and
// redeliver witnesses need.
func seedSubscriptionRow(t *testing.T, dir, tenant, endpoint, sealed string) string {
	t.Helper()
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: dir, Version: "test", Logger: discardLog()})
	if err != nil {
		t.Fatalf("boot to seed subscription: %v", err)
	}
	defer func() { _ = eng.Close() }()
	tid, err := model.ParseTenantID(tenant)
	if err != nil {
		t.Fatalf("parse tenant: %v", err)
	}
	fence, ok := newEventingWriterFence(eng.store)
	if !ok {
		t.Fatalf("store exposes no writer fence")
	}
	gen, err := eventing.FenceGeneration(ctx, fence)
	if err != nil {
		t.Fatalf("fence generation: %v", err)
	}
	var id string
	if merr := eng.store.Mutate(ctx, tid, func(sc store.Scope) error {
		repo, rerr := sc.Ext(evtSubscriptionKind)
		if rerr != nil {
			return rerr
		}
		rec := model.Record{
			evtColSubName: fmt.Sprintf("seeded-%d", seedSeq.Add(1)), evtColSubEnabled: true,
			evtColSubTypes: "audit.created", evtColSubSources: "",
			evtColSubEndpoint: endpoint, evtColSubSecret: sealed, evtColSubSecretHint: "aaaaaaaaaaaa",
			evtColSubRole: "viewer", evtColSubDescription: "",
			"owner_actor": "test", "owner_actor_kind": "user",
			evtColSubAuthType: "none", evtColSubAuthHdrName: "",
			evtColSubMaxAttempts: int64(0), evtColSubInitInterval: int64(0),
		}
		if serr := eventing.StampWriterProof(ctx, sc, rec, gen); serr != nil {
			return serr
		}
		created, cerr := repo.Create(ctx, rec)
		if cerr != nil {
			return cerr
		}
		id = created.String(model.ColID)
		return nil
	}); merr != nil {
		t.Fatalf("seed subscription: %v", merr)
	}
	return id
}

// seedDeadDelivery writes one DEAD delivery with a non-zero attempt count, so a
// requeue has something to move.
func seedDeadDelivery(t *testing.T, dir, tenant, subRef string) string {
	t.Helper()
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: dir, Version: "test", Logger: discardLog()})
	if err != nil {
		t.Fatalf("boot to seed delivery: %v", err)
	}
	defer func() { _ = eng.Close() }()
	tid, err := model.ParseTenantID(tenant)
	if err != nil {
		t.Fatalf("parse tenant: %v", err)
	}
	var id string
	if merr := eng.store.Mutate(ctx, tid, func(sc store.Scope) error {
		events, eerr := sc.Ext(evtEventKind)
		if eerr != nil {
			return eerr
		}
		seq := seedSeq.Add(1)
		eventID := fmt.Sprintf("ev-%d", seq)
		ev, cerr := events.Create(ctx, model.Record{
			evtColEvSeq: seq, evtColEvEventID: eventID, evtColEvType: "audit.created",
			evtColEvSource: "test", evtColEvOccurredAt: "2026-08-17T00:00:00Z", evtColEvPayload: "{}",
		})
		if cerr != nil {
			return cerr
		}
		repo, rerr := sc.Ext(evtDeliveryKind)
		if rerr != nil {
			return rerr
		}
		created, cerr := repo.Create(ctx, model.Record{
			evtColDelSubRef: subRef, "event_ref": ev.String(model.ColID),
			evtColDelEventID: eventID, evtColDelEventSeq: seq,
			evtColDelEventType: "audit.created", evtColDelStatus: "dead", evtColDelOrigin: "live",
			evtColDelAttempts: int64(5), evtColDelLastStatus: "http_500",
			evtColDelNextAt: "2026-08-17T00:00:00Z", evtColDelLastAt: "2026-08-17T00:00:00Z",
		})
		if cerr != nil {
			return cerr
		}
		id = created.String(model.ColID)
		return nil
	}); merr != nil {
		t.Fatalf("seed delivery: %v", merr)
	}
	return id
}
