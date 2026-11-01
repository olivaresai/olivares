// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

// The instructions file of Claude Code's managed tier (/etc/claude-code/CLAUDE.md) is a claude-policy
// surface like the four settings files, with one document per department: the surface is
// "managed-instructions@<department>", and its document is rendered from what the department
// carries (#530), so a session and a workstation read one source.

type fakeInstructions struct {
	byDepartment map[string]string
	err          error
}

func (f fakeInstructions) Instructions(_ context.Context, _ model.TenantID, department string) (string, error) {
	return f.byDepartment[department], f.err
}

// clientErr is the kind of error a source says the caller may be told.
type clientErr struct {
	status  int
	message string
}

func (e clientErr) Error() string         { return e.message }
func (e clientErr) ClientStatus() int     { return e.status }
func (e clientErr) ClientMessage() string { return e.message }

func instructionsHarness(t *testing.T, src governance.InstructionsSource) (*harness, model.TenantID, string) {
	t.Helper()
	h := newHarnessWith(t, harnessOpts{policyOpts: []governance.PolicyConsoleOption{governance.WithInstructionsSource(src)}})
	tenant, tok := h.tenantAdmin()
	return h, tenant, tok
}

func TestManagedInstructionsIsAnAuthoringSurface(t *testing.T) {
	h, tenant, tok := instructionsHarness(t, fakeInstructions{})
	hdr := tenantHdr(tenant)
	const base = "/v1/m/claude-policy/managed-instructions"

	good := h.do("POST", base+"/validate", tok, map[string]any{"content": "# Standards\n\nUse tabs.\n"}, hdr)
	if good.code != http.StatusOK || good.body["ok"] != true {
		t.Fatalf("validate of a prose document = %d %s; want ok", good.code, good.raw)
	}
	for name, content := range map[string]string{"empty": "  \n", "NUL byte": "a\x00b"} {
		bad := h.do("POST", base+"/validate", tok, map[string]any{"content": content}, hdr)
		if bad.code != http.StatusOK || bad.body["ok"] != false {
			t.Errorf("validate of a %s document = %d %s; want ok=false", name, bad.code, bad.raw)
		}
	}
	// An @path is something Claude Code imports from a CLAUDE.md: it is allowed, and said.
	imp := h.do("POST", base+"/validate", tok, map[string]any{"content": "Ask @alice, then read @~/notes.md\n"}, hdr)
	if imp.code != http.StatusOK || imp.body["ok"] != true || !strings.Contains(imp.raw, "import") {
		t.Errorf("validate of a document with an @path = %d %s; want ok with a note about the import", imp.code, imp.raw)
	}
	plain := h.do("POST", base+"/validate", tok, map[string]any{"content": "Mail ops@example.com about it\n"}, hdr)
	if plain.code != http.StatusOK || strings.Contains(plain.raw, "import") {
		t.Errorf("validate of an address that is not an import = %d %s; want no import note", plain.code, plain.raw)
	}
	// It is not a settings file: nothing about it is parsed as JSON.
	if r := h.do("POST", base+"/dry-run", tok, map[string]any{"content": "not { json"}, hdr); r.code != http.StatusOK {
		t.Fatalf("dry-run of prose = %d %s; want 200", r.code, r.raw)
	}
}

func TestADepartmentSurfaceNameIsCheckedBeforeAnythingIsRead(t *testing.T) {
	h, tenant, tok := instructionsHarness(t, fakeInstructions{byDepartment: map[string]string{"eng": "# Eng\n"}})
	hdr := tenantHdr(tenant)
	for surface, want := range map[string]int{
		"managed-instructions@eng":                        http.StatusOK,
		"managed-instructions@platform-2":                 http.StatusOK,
		"managed-instructions@":                           http.StatusBadRequest,
		"managed-instructions@Eng":                        http.StatusBadRequest,
		"managed-instructions@-eng":                       http.StatusBadRequest,
		"managed-instructions@a@b":                        http.StatusBadRequest,
		"managed-instructions@" + strings.Repeat("a", 63): http.StatusOK,
		"managed-instructions@" + strings.Repeat("a", 64): http.StatusBadRequest,
		"managed-settings@eng":                            http.StatusBadRequest,
		"sandbox@eng":                                     http.StatusBadRequest,
	} {
		r := h.do("POST", "/v1/m/claude-policy/"+surface+"/validate", tok, map[string]any{"content": "x"}, hdr)
		if r.code != want {
			t.Errorf("validate %q = %d %s; want %d", surface, r.code, r.raw, want)
		}
	}
}

func TestADepartmentSurfaceIsRenderedFromTheDepartment(t *testing.T) {
	const doc = "# Org standards\n\nUse tabs.\n"
	h, tenant, tok := instructionsHarness(t, fakeInstructions{byDepartment: map[string]string{"eng": doc}})
	hdr := tenantHdr(tenant)
	sum := sha256.Sum256([]byte(doc))

	// No document in the request: the department's own is validated and previewed.
	v := h.do("POST", "/v1/m/claude-policy/managed-instructions@eng/validate", tok, map[string]any{}, hdr)
	if v.code != http.StatusOK || v.body["ok"] != true {
		t.Fatalf("validate of a department's instructions = %d %s; want ok", v.code, v.raw)
	}
	d := h.do("POST", "/v1/m/claude-policy/managed-instructions@eng/dry-run", tok, map[string]any{}, hdr)
	if d.code != http.StatusOK || d.body["content"] != doc || d.body["sha256"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("dry-run of a department's instructions = %d %s; want its document %q and sha256 %x", d.code, d.raw, doc, sum)
	}
	// A dry-run says how the document stands against what the workstations were given.
	if !strings.Contains(d.raw, "nothing is published for this department yet") || !strings.Contains(d.raw, "directory group") {
		t.Errorf("dry-run notes = %s; want the published state and the group limit", d.raw)
	}
	// A department that carries none has nothing to validate.
	none := h.do("POST", "/v1/m/claude-policy/managed-instructions@sales/validate", tok, map[string]any{}, hdr)
	if none.code != http.StatusOK || none.body["ok"] != false || !strings.Contains(none.raw, "carries no instructions") {
		t.Fatalf("validate of a department with none = %d %s; want ok=false saying it carries no instructions", none.code, none.raw)
	}
	// A document typed in the request is validated as typed: this is a no-effect check.
	typed := h.do("POST", "/v1/m/claude-policy/managed-instructions@eng/dry-run", tok, map[string]any{"content": "Another."}, hdr)
	if typed.code != http.StatusOK || typed.body["content"] != nil {
		t.Fatalf("dry-run of a typed document = %d %s; want 200 and no rendered content", typed.code, typed.raw)
	}
}

func TestAnUnreadableDepartmentIsNeverEmpty(t *testing.T) {
	h, tenant, tok := instructionsHarness(t, fakeInstructions{err: errors.New("store unavailable")})
	hdr := tenantHdr(tenant)
	for _, path := range []string{"validate", "dry-run"} {
		r := h.do("POST", "/v1/m/claude-policy/managed-instructions@eng/"+path, tok, map[string]any{}, hdr)
		if r.code != http.StatusServiceUnavailable {
			t.Errorf("%s of a department whose instructions could not be read = %d %s; want 503", path, r.code, r.raw)
		}
	}
}

// What the source says the administrator can act on is told as it is; anything else is a 503.
func TestAnActionableRefusalOfTheSourceIsTold(t *testing.T) {
	for name, tc := range map[string]struct {
		err     error
		code    int
		message string
	}{
		"a department that is not one": {clientErr{http.StatusNotFound, "no department with that name"}, http.StatusNotFound, "no department with that name"},
		"over the budget":              {clientErr{http.StatusUnprocessableEntity, "the instructions of department eng are too long"}, http.StatusUnprocessableEntity, "too long"},
		"a store failure":              {clientErr{http.StatusServiceUnavailable, "database is locked: /var/lib/olivares"}, http.StatusServiceUnavailable, "could not be read"},
		"an unknown error":             {errors.New("boom: secret detail"), http.StatusServiceUnavailable, "could not be read"},
	} {
		h, tenant, tok := instructionsHarness(t, fakeInstructions{err: tc.err})
		for _, path := range []string{"validate", "dry-run"} {
			r := h.do("POST", "/v1/m/claude-policy/managed-instructions@eng/"+path, tok, map[string]any{}, tenantHdr(tenant))
			message := ""
			if e, ok := r.body["error"].(map[string]any); ok {
				message, _ = e["message"].(string)
			}
			if r.code != tc.code || !strings.Contains(message, tc.message) || strings.Contains(r.raw, "secret detail") || strings.Contains(r.raw, "/var/lib") {
				t.Errorf("%s: %s = %d %s; want %d telling %q and nothing else", name, path, r.code, r.raw, tc.code, tc.message)
			}
		}
	}
}

func TestADepartmentSurfaceIsAnAuthoringSurfaceOnlyForInstructions(t *testing.T) {
	h, tenant, tok := instructionsHarness(t, fakeInstructions{})
	r := h.do("POST", "/v1/m/claude-policy/not-a-surface/validate", tok, map[string]any{"content": "x"}, tenantHdr(tenant))
	if r.code != http.StatusBadRequest || !strings.Contains(r.raw, "managed-instructions") {
		t.Fatalf("unknown surface = %d %s; want 400 listing managed-instructions", r.code, r.raw)
	}
}
