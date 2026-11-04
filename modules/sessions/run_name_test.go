// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestValidateCreateRefusesRunNameTheRenameRefuses pins that launch, handoff and
// child launch (all of which pass validateCreate) refuse a name with the same 400 and
// message as a rename, and accept what a rename accepts.
func TestValidateCreateRefusesRunNameTheRenameRefuses(t *testing.T) {
	t.Parallel()

	const msg = "name must be at most 200 characters and hold no control or invisible characters"
	for _, name := range []string{
		"zero\u200bwidth",             // ZERO WIDTH SPACE (Cf)
		"bidi\u202eoverride",          // RIGHT-TO-LEFT OVERRIDE (Cf)
		"line\u2028separator",         // LINE SEPARATOR (Zl)
		"paragraph\u2029separator",    // PARAGRAPH SEPARATOR (Zp)
		"tab\tinside",                 // control
		"hangul\u3164filler",          // HANGUL FILLER (default ignorable)
		"variation\ufe0f",             // VARIATION SELECTOR-16
		"braille\u2800blank",          // BRAILLE PATTERN BLANK, a symbol that draws nothing
		"\u2800",                      // a name of nothing but blank
		strings.Repeat("n", 201),      // over the limit
		strings.Repeat("\u00e9", 201), // 201 runes, 402 bytes
	} {
		t.Run(name, func(t *testing.T) {
			p := CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Name: name}
			err := validateCreate(&p)
			var re *runErr
			if !errors.As(err, &re) || re.status != http.StatusBadRequest || re.msg != msg {
				t.Fatalf("name %q: got %v, want a 400 with the rename's message", name, err)
			}
		})
	}

	for name, want := range map[string]string{
		"":                                     "",
		"Morning review":                       "Morning review",
		"  padded  ":                           "padded",
		strings.Repeat("n", 200):               strings.Repeat("n", 200),
		strings.Repeat("\u00e9", 200):          strings.Repeat("\u00e9", 200),
		"  " + strings.Repeat("n", 200) + "  ": strings.Repeat("n", 200), // the limit is on the trimmed name
	} {
		p := CreateRunParams{Transport: TransportStreamJSON, Isolation: IsolationNative, Name: name}
		if err := validateCreate(&p); err != nil {
			t.Fatalf("name %q refused: %v", name, err)
		}
		if p.Name != want {
			t.Fatalf("name %q normalized to %q, want %q", name, p.Name, want)
		}
	}
}

// TestCreateRunRefusesARunNameTheRenameRefuses pins the launch itself: a name the
// rename refuses is a 400 before anything is persisted or started.
func TestCreateRunRefusesARunNameTheRenameRefuses(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{initSID: "sess-name"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	_, err := createProfiledTestRun(t, m, context.Background(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, PermissionMode: "default", Effort: "high",
		Model: "claude-opus-4-8", Isolation: IsolationNative, WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()),
		Actor: "user:u1", ActorKind: "user", Name: "zero\u200bwidth",
	})
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusBadRequest || !strings.Contains(re.msg, "invisible") {
		t.Fatalf("launch with an invisible name: got %v, want a 400", err)
	}
}

// TestHandoffNameStaysWithinTheRunNameLimit pins that a handoff's derived name is
// checked like any other: a long source name is cut so "Handoff from" stays within
// the limit, and a name the caller gave is kept as trimmed.
func TestHandoffNameStaysWithinTheRunNameLimit(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("n", maxRunNameRunes)
	if got := handoffName("  Review  ", long, "run_1"); got != "Review" {
		t.Fatalf("given name = %q, want %q", got, "Review")
	}
	got := handoffName("", long, "run_1")
	if err := checkRunName(got); err != nil {
		t.Fatalf("derived name of a %d-rune source refused: %v", maxRunNameRunes, err)
	}
	if !strings.HasPrefix(got, "Handoff from nnn") || utf8.RuneCountInString(got) != maxRunNameRunes {
		t.Fatalf("derived name = %q (%d runes)", got, utf8.RuneCountInString(got))
	}
	if got := handoffName("", "", "run_1"); got != "Handoff from run_1" {
		t.Fatalf("unnamed source = %q", got)
	}
	// Multi-byte runes are cut whole: a byte cut would leave invalid UTF-8.
	multi := handoffName("", strings.Repeat("\u00e9", maxRunNameRunes), "run_1")
	if !utf8.ValidString(multi) || utf8.RuneCountInString(multi) != maxRunNameRunes {
		t.Fatalf("multi-byte derived name = %q (%d runes)", multi, utf8.RuneCountInString(multi))
	}
	// A source stored before the rule may hold a character it now refuses; the derived
	// name leaves it out instead of refusing the handoff.
	if got := handoffName("", "ab\u200bcd", "run_1"); got != "Handoff from abcd" {
		t.Fatalf("legacy invisible character = %q", got)
	}
	if err := checkRunName(handoffName("", "\u200b", "run_1")); err != nil {
		t.Fatalf("all-invisible source refused: %v", err)
	}
	// Spaces left once the invisible characters go are not a name: the ref stands in.
	if got := handoffName("", "\u200b \u200b", "run_1"); got != "Handoff from run_1" {
		t.Fatalf("source of invisible characters and a space = %q", got)
	}
	// The cut does not leave the name ending in a space.
	if got := handoffName("", strings.Repeat("n", 186)+" "+strings.Repeat("x", 20), "run_1"); strings.HasSuffix(got, " ") {
		t.Fatalf("derived name ends in a space: %q", got)
	}
}

// TestRuntimeAPILaunchRefusesARunNameTheRenameRefuses pins POST /runs end to end: the
// refusal is the 400 and the sentence a client prints, and a normal name still starts.
func TestRuntimeAPILaunchRefusesARunNameTheRenameRefuses(t *testing.T) {
	t.Parallel()

	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(&fakeRunner{initSID: "sess-api-name"}), WithCredentialSource(staticCred()))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "run-name")
	profile := ensureRuntimeTestProfileRef(t, m, tenant)
	launch := func(name string) resp {
		return h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{"name": name, "provider_profile_ref": profile}, tenantHdr(tenant))
	}

	for _, name := range []string{"zero\u200bwidth", "\u2800", strings.Repeat("n", 10000)} {
		r := launch(name)
		if r.code != http.StatusBadRequest || !strings.Contains(r.raw, "name must be at most 200 characters and hold no control or invisible characters") {
			t.Fatalf("launch with name %.20q = %d %s, want 400 with the rename's message", name, r.code, r.raw)
		}
	}
	if r := launch("Morning review"); r.code != http.StatusCreated || r.body["name"] != "Morning review" {
		t.Fatalf("a normal name = %d %s, want 201", r.code, r.raw)
	}
}

// TestRuntimeAPILaunchRefusesARunNameBeforeItCreatesAProfile pins the order in
// handleCreateRun: a launch that names no profile has the default one resolved, and
// resolving may create it, so a refused name must be answered first.
func TestRuntimeAPILaunchRefusesARunNameBeforeItCreatesAProfile(t *testing.T) {
	t.Parallel()

	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(&fakeRunner{initSID: "sess-api-name-order"}), WithCredentialSource(staticCred()))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "run-name-order")
	profiles := func() int {
		r := h.do("GET", "/v1/m/sessions/provider-profiles", admin, tenantHdr(tenant))
		items, _ := r.body["items"].([]any)
		return len(items)
	}
	if n := profiles(); n != 0 {
		t.Fatalf("a new organization has %d profiles", n)
	}
	r := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{"name": "zero\u200bwidth"}, tenantHdr(tenant))
	if r.code != http.StatusBadRequest || !strings.Contains(r.raw, "invisible") {
		t.Fatalf("launch with an invisible name and no profile = %d %s", r.code, r.raw)
	}
	if n := profiles(); n != 0 {
		t.Fatalf("the refused launch left %d profiles behind", n)
	}
}

// TestRuntimeAPILaunchRefusesANameASessionHolds pins #2978: a launch whose name a
// session that is not cleaned already carries is a 409 before anything is started,
// so two live sessions never share a name and `session stop <name>` cannot reach a
// second one. The CLI always refused this (`takenSessionNames`); the engine owns the
// rule now, so the console and a direct API call get it too.
func TestRuntimeAPILaunchRefusesANameASessionHolds(t *testing.T) {
	t.Parallel()

	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(&fakeRunner{initSID: "sess-api-held"}), WithCredentialSource(staticCred()))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "run-name-held")
	profile := ensureRuntimeTestProfileRef(t, m, tenant)
	launch := func(name string) resp {
		return h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{"name": name, "provider_profile_ref": profile}, tenantHdr(tenant))
	}

	if r := launch("api"); r.code != http.StatusCreated {
		t.Fatalf("first launch = %d %s, want 201", r.code, r.raw)
	}
	if r := launch("api"); r.code != http.StatusConflict || !strings.Contains(refusalMessage(r), `already named "api"`) {
		t.Fatalf("a launch under a held name = %d %s, want 409 naming it", r.code, r.raw)
	}
	runs := h.do("GET", "/v1/m/sessions/runs", admin, tenantHdr(tenant))
	if items, _ := runs.body["items"].([]any); len(items) != 1 {
		t.Fatalf("the refused launch left %d sessions, want 1", len(items))
	}
	if r := launch("web"); r.code != http.StatusCreated {
		t.Fatalf("a free name = %d %s, want 201", r.code, r.raw)
	}
}

// refusalMessage reads the message of a module error envelope.
func refusalMessage(r resp) string {
	env, _ := r.body["error"].(map[string]any)
	msg, _ := env["message"].(string)
	return msg
}

// A scope without the transaction-lock capability fails closed: the check must
// refuse rather than guess whether a name is held when it cannot serialize with
// the write that follows it.
func TestRefuseHeldRunNameFailsClosedWithoutTheTransactionLock(t *testing.T) {
	t.Parallel()

	var tenant model.TenantID
	err := refuseHeldRunNameIn(context.Background(), struct{ store.Scope }{}, tenant, "api", "")
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusServiceUnavailable {
		t.Fatalf("a locker-less scope = %v, want the 503 refusal", err)
	}
}

// The held-name check reads through the scope it is given, so a writer confined to
// one workspace sees only that workspace's holders: a tenant-wide probe would tell
// a confined writer names it cannot see. The same name held in the caller's own
// workspace is still refused, and a tenant-wide reader sees every holder.
func TestHeldRunNameCheckReadsTheCallersOwnScope(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "run-name-scope")
	ctx := context.Background()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(ctx, model.Workspace{Name: "api-holders", Slug: "api-holders", Status: model.StatusActive})
		if err != nil {
			return err
		}
		def, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		for _, row := range []model.Record{
			{colRunRef: "run-sibling", colRunName: "api", colState: stateRunning,
				colTransport: string(TransportStreamJSON), colPermissionMode: "default",
				colIsolation: string(IsolationNative), colLastEventSeq: int64(0), colRunAuthzWorkspaceID: ws.ID},
			{colRunRef: "run-mine", colRunName: "web", colState: stateRunning,
				colTransport: string(TransportStreamJSON), colPermissionMode: "default",
				colIsolation: string(IsolationNative), colLastEventSeq: int64(0), colRunAuthzWorkspaceID: def.ID},
		} {
			if _, err := repo.Create(ctx, row); err != nil {
				return err
			}
		}
		refusal := func(err error) bool {
			var re *runErr
			return errors.As(err, &re) && re.status == http.StatusConflict
		}
		confined, err := store.ConfineWorkspace(ctx, sc, def.ID)
		if err != nil {
			return err
		}
		if err := refuseHeldRunNameIn(ctx, confined, tenant, "api", ""); refusal(err) || err != nil {
			return fmt.Errorf("sibling workspace's holder: %v, want nil: the check must not probe the tenant", err)
		}
		if err := refuseHeldRunNameIn(ctx, confined, tenant, "web", ""); !refusal(err) {
			return fmt.Errorf("own workspace's holder: %v, want the 409 refusal", err)
		}
		if err := refuseHeldRunNameIn(ctx, sc, tenant, "api", ""); !refusal(err) {
			return fmt.Errorf("tenant-wide reader: %v, want the 409 refusal", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
