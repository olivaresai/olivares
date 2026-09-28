// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"bufio"
	"context"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// nilBinder is a binder implementation other than *auth.Authenticator; its
// typed nil must unbind the producer exactly as the documented contract says.
type nilBinder struct{}

func (*nilBinder) BindCredential(context.Context, auth.Principal, auth.CredentialBindingSubject) (auth.CredentialBinding, error) {
	return auth.CredentialBinding{}, nil
}

func (*nilBinder) RebindCredential(context.Context, auth.CredentialBinding, int64, auth.Principal, auth.CredentialBindingSubject, auth.Principal) (auth.CredentialBinding, error) {
	return auth.CredentialBinding{}, nil
}

func (*nilBinder) VerifyCredentialContinuation(context.Context, auth.Principal, auth.CredentialBindingSubject) error {
	return nil
}

func TestUseWorkflowCredentialBinderUnbindsEveryTypedNil(t *testing.T) {
	m := New()
	m.UseWorkflowCredentialBinder(auth.NewAuthenticator(nil, nil))
	if m.credentialBinder == nil {
		t.Fatal("a real binder was not bound")
	}
	var typedNil *nilBinder
	m.UseWorkflowCredentialBinder(typedNil)
	if m.credentialBinder != nil {
		t.Fatal("a typed-nil binder stayed bound")
	}
	m.UseWorkflowCredentialBinder(auth.NewAuthenticator(nil, nil))
	var nilAuthenticator *auth.Authenticator
	m.UseWorkflowCredentialBinder(nilAuthenticator)
	if m.credentialBinder != nil {
		t.Fatal("a typed-nil Authenticator stayed bound")
	}
}

// recordingSchemaRegistry records descriptors; the embedded interface is nil
// because registerWorkflowSchema only registers.
type recordingSchemaRegistry struct {
	store.ExtensionRegistry
	descriptors []model.EntityDescriptor
}

func (r *recordingSchemaRegistry) Register(d model.EntityDescriptor) error {
	r.descriptors = append(r.descriptors, d)
	return nil
}

// The run's handle column declares None and must cite the line that reads it,
// the parse in workStepActor, not a neighbouring blank line.
func TestCredentialBindingColumnCitesItsReader(t *testing.T) {
	reg := &recordingSchemaRegistry{}
	if err := registerWorkflowSchema(reg); err != nil {
		t.Fatal(err)
	}
	var reason string
	for _, d := range reg.descriptors {
		for _, f := range d.Fields {
			if f.Name == colWrCredentialBinding && f.Principal != nil {
				reason = f.Principal.Reason
			}
		}
	}
	m := regexp.MustCompile(`modules/orchestration/([a-z_]+\.go):(\d+)`).FindStringSubmatch(reason)
	if m == nil {
		t.Fatalf("the handle column cites no orchestration line: %q", reason)
	}
	want, _ := strconv.Atoi(m[2])
	file, err := os.Open(m[1])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	lines := bufio.NewScanner(file)
	for n := 1; lines.Scan(); n++ {
		if n == want {
			if !strings.Contains(lines.Text(), "ParseCredentialBindingStorage") {
				t.Fatalf("%s:%d is %q, not the handle's reader", m[1], want, lines.Text())
			}
			return
		}
	}
	t.Fatalf("%s has no line %d", m[1], want)
}

// unavailableBinder answers every bind with an operational failure.
type unavailableBinder struct{ nilBinder }

func (unavailableBinder) BindCredential(context.Context, auth.Principal, auth.CredentialBindingSubject) (auth.CredentialBinding, error) {
	return auth.CredentialBinding{}, context.DeadlineExceeded
}

// STD-1/F1: when the binding cannot be written for an operational reason, the
// run does not start at all; it never starts silently unbound.
func TestWorkflowRunStartRefusesWhenBindingEvidenceIsUnavailable(t *testing.T) {
	gate := newRoutedGate()
	h, mod := newHarness(t, WithApprovalGate(gate))
	mod.UseWorkflowCredentialBinder(&unavailableBinder{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "binding-outage")
	wf := h.createWorkflow(admin, tenant, "binding-outage", []map[string]any{emitStep("emit")})
	id := wf["id"].(string)
	r1 := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/run", admin, nil, tenantHdr(tenant))
	ref, _ := r1.body["approval_ref"].(string)
	gate.set(ref, StatusApproved)
	r2 := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/run", admin,
		map[string]any{"approval_ref": ref}, tenantHdr(tenant))
	if r2.code != http.StatusServiceUnavailable {
		t.Fatalf("phase 2 with binding evidence unavailable = %d %s, want 503", r2.code, r2.raw)
	}
	runs := h.do("GET", "/v1/m/orchestration/workflows/"+id+"/runs", admin, nil, tenantHdr(tenant))
	if items, _ := runs.body["items"].([]any); len(items) != 0 {
		t.Fatalf("a run started without its binding: %s", runs.raw)
	}
}
