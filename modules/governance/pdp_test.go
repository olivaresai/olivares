// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestWriteStoreErrorAuditSpoolFull(t *testing.T) {
	w := httptest.NewRecorder()
	writeStoreError(w, fmt.Errorf("governance audit: %w", store.ErrAuditSpoolFull))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"message":"audit spool full"`) {
		t.Fatalf("body = %s, want audit spool full message", w.Body.String())
	}
}

// stubEval is a PolicyEvaluator returning a fixed decision/error.
type stubEval struct {
	allow bool
	err   error
}

func (s stubEval) Evaluate(context.Context, auth.Request) (auth.Decision, error) {
	if s.err != nil {
		return auth.Decision{}, s.err
	}
	return auth.Decision{Allow: s.allow, Reason: "stub"}, nil
}

func pdpReq() auth.Request {
	return auth.Request{Principal: auth.Principal{Kind: auth.KindUser}, Permission: "agent:write", Tenant: model.TenantID("t-1")}
}

func TestChainAllowsOnlyWhenAllAllow(t *testing.T) {
	ch := composeEvaluators(nil, stubEval{allow: true}, stubEval{allow: true})
	if dec, _ := ch.Evaluate(context.Background(), pdpReq()); !dec.Allow {
		t.Error("chain must allow when every member allows")
	}
}

func TestChainRestrictsAndAudits(t *testing.T) {
	var audited bool
	ch := composeEvaluators(func(auth.Request, auth.Decision) { audited = true },
		stubEval{allow: true}, stubEval{allow: false})
	dec, err := ch.Evaluate(context.Background(), pdpReq())
	if err != nil || dec.Allow {
		t.Errorf("chain must restrict when a member restricts: %+v %v", dec, err)
	}
	if !audited {
		t.Error("a restriction must invoke the audit hook")
	}
}

func TestChainPropagatesError(t *testing.T) {
	boom := errors.New("pdp down")
	ch := composeEvaluators(nil, stubEval{allow: true}, stubEval{err: boom})
	if _, err := ch.Evaluate(context.Background(), pdpReq()); !errors.Is(err, boom) {
		t.Errorf("chain must propagate a member error (fail closed), got %v", err)
	}
}

func TestComposeEvaluatorsEdgeCases(t *testing.T) {
	// All-nil composes to DenyNothing (no restriction).
	if dec, _ := composeEvaluators(nil, nil, nil).Evaluate(context.Background(), pdpReq()); !dec.Allow {
		t.Error("an all-nil compose must impose no restriction")
	}
	// A single member with no audit hook is returned directly.
	single := stubEval{allow: true}
	if _, ok := composeEvaluators(nil, single, nil).(stubEval); !ok {
		t.Error("a single member with no audit hook should be returned directly")
	}
}

func TestNewExternalPDPSelection(t *testing.T) {
	// none / empty => no PDP.
	for _, eng := range []PDPEngine{PDPNone, "none", "NONE"} {
		pdp, err := NewExternalPDP(PDPConfig{Engine: eng})
		if err != nil || pdp != nil {
			t.Errorf("engine %q => %v, %v; want nil,nil", eng, pdp, err)
		}
	}
	// cedar => a CedarEvaluator.
	pdp, err := NewExternalPDP(PDPConfig{Engine: PDPCedar, CedarPolicy: forbidSecret})
	if err != nil || pdp == nil {
		t.Fatalf("cedar engine => %v, %v", pdp, err)
	}
	if _, ok := pdp.(*CedarEvaluator); !ok {
		t.Errorf("cedar engine must build a *CedarEvaluator, got %T", pdp)
	}
	// opa => an OPAEvaluator.
	pdp, err = NewExternalPDP(PDPConfig{Engine: PDPOPA, OPABaseURL: "http://opa:8181", OPADecisionPath: "authz/allow"})
	if err != nil || pdp == nil {
		t.Fatalf("opa engine => %v, %v", pdp, err)
	}
	if _, ok := pdp.(*OPAEvaluator); !ok {
		t.Errorf("opa engine must build an *OPAEvaluator, got %T", pdp)
	}
	// unknown => error.
	if _, err := NewExternalPDP(PDPConfig{Engine: "fancy"}); err == nil {
		t.Error("an unknown engine must error")
	}
}
