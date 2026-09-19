// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/sessions"
)

func TestResolveAvailabilityPosture(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		name    string
		raw     string
		edition string
		want    availabilityPosture
	}{
		{name: "explicit fail-open wins in enterprise", raw: "fail-open", edition: "enterprise", want: availabilityFailOpen},
		{name: "explicit fail-closed wins in community", raw: "fail-closed", edition: "community", want: availabilityFailClosed},
		{name: "explicit value is normalized", raw: "  FAIL-OPEN ", edition: "enterprise", want: availabilityFailOpen},
		{name: "enterprise default", edition: "enterprise", want: availabilityFailClosed},
		{name: "community default", edition: "community", want: availabilityFailOpen},
		{name: "invalid fails closed", raw: "fail-clsoed", edition: "community", want: availabilityFailClosed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveAvailabilityPosture(tt.raw, tt.edition, log); got != tt.want {
				t.Fatalf("resolveAvailabilityPosture(%q, %q) = %v, want %v", tt.raw, tt.edition, got, tt.want)
			}
		})
	}
}

func TestSessionLaunchGate_BudgetFailClosedOnReadError(t *testing.T) {
	g := &sessionLaunchGate{
		fin:             fakeBudget{err: errors.New("budget ledger unavailable")},
		budgetPosture:   availabilityFailClosed,
		recordAvailable: true,
		log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	dec, err := g.Authorize(context.Background(), "t1", sessions.LaunchIntent{PermissionMode: "default"})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if dec.Allowed {
		t.Fatal("an unreadable budget control must deny under fail-closed posture")
	}
	if dec.DeniedStatus != http.StatusServiceUnavailable {
		t.Fatalf("DeniedStatus = %d, want %d", dec.DeniedStatus, http.StatusServiceUnavailable)
	}
	if dec.Reason != "session budget control unavailable (deny-closed)" {
		t.Fatalf("Reason = %q", dec.Reason)
	}
}

func TestSessionLaunchGate_BudgetFailOpenOnReadError(t *testing.T) {
	g := &sessionLaunchGate{
		fin:             fakeBudget{err: errors.New("budget ledger unavailable")},
		budgetPosture:   availabilityFailOpen,
		recordAvailable: true,
		log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	dec, err := g.Authorize(context.Background(), "t1", sessions.LaunchIntent{PermissionMode: "default"})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Allowed {
		t.Fatalf("an unreadable budget control must allow under fail-open posture: %+v", dec)
	}
}

// TestSessionLaunchGate_AdmissionNeverFollowsTheLaunchPosture pins the separation the D6
// invariant rests on. The launch gate's availability posture answers a control it could not
// READ, and its community default is fail-open. FinOps admission is a different question: a
// Reserve HOLDS money, so an unreachable ledger has no headroom to give and no handle to
// commit later. Whatever the posture, the request this gate builds carries deny.
//
// A gate that derived AdmissionRequest.Unreachable from budgetPosture would turn a WRITE
// fail-open on every community install that configured nothing: Reserve would answer
// "allowed" with no row behind it, and concurrent launches would over-admit against one cap.
// The decision the posture DOES still make — what the launch does with admission's deny — is
// asserted here too, and is the subject of the two tests above.
func TestSessionLaunchGate_AdmissionNeverFollowsTheLaunchPosture(t *testing.T) {
	cases := []struct {
		posture     availabilityPosture
		wantAllowed bool
	}{
		{posture: availabilityFailOpen, wantAllowed: true},
		{posture: availabilityFailClosed, wantAllowed: false},
	}
	for _, c := range cases {
		t.Run(c.posture.String(), func(t *testing.T) {
			spy := &spyAdmissionBudget{err: errors.New("budget ledger unavailable")}
			g := &sessionLaunchGate{
				fin:             spy,
				budgetPosture:   c.posture,
				recordAvailable: true,
				log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
			}

			dec, err := g.Authorize(context.Background(), "t1", sessions.LaunchIntent{PermissionMode: "default"})
			if err != nil {
				t.Fatalf("Authorize: %v", err)
			}
			if len(spy.seen) != 1 {
				t.Fatalf("Reserve called %d time(s), want exactly 1", len(spy.seen))
			}
			if got := spy.seen[0].Unreachable; got != finops.UnreachableDeny {
				t.Fatalf("admission Unreachable = %q under a %s launch gate, want %q: admission must "+
					"not read the launch-gate posture", got, c.posture, finops.UnreachableDeny)
			}
			if dec.Allowed != c.wantAllowed {
				t.Fatalf("launch Allowed = %v under a %s posture, want %v", dec.Allowed, c.posture, c.wantAllowed)
			}
		})
	}
}

// spyAdmissionBudget records the admission requests the gate builds. It answers exactly as
// the module does (fakeAdmissionReserve mirrors refuseUnreachable), so the recorded field and
// the resulting decision are both real.
type spyAdmissionBudget struct {
	chk  finops.BudgetCheck
	err  error
	seen []finops.AdmissionRequest
}

func (s *spyAdmissionBudget) CheckBudget(context.Context, model.TenantID, finops.SpendDims) (finops.BudgetCheck, error) {
	return s.chk, s.err
}

func (s *spyAdmissionBudget) CheckSpendLimit(context.Context, model.TenantID, string, []string) (finops.SpendLimitCheck, error) {
	return finops.SpendLimitCheck{Allowed: true}, nil
}

func (s *spyAdmissionBudget) Reserve(_ context.Context, _ model.TenantID, req finops.AdmissionRequest) (finops.Reservation, error) {
	s.seen = append(s.seen, req)
	return fakeAdmissionReserve(s.chk, s.err, req)
}

func (s *spyAdmissionBudget) Commit(context.Context, model.TenantID, string, int64) error { return nil }
func (s *spyAdmissionBudget) Release(context.Context, model.TenantID, string) error       { return nil }

func TestSessionLaunchGate_ContextFailClosedOnReadError(t *testing.T) {
	g := &sessionLaunchGate{
		contextPolicy:   &fakeSessionContextPolicy{err: errors.New("context policy unavailable")},
		contextPosture:  availabilityFailClosed,
		recordAvailable: true,
		log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	dec, err := g.Authorize(context.Background(), "t1", sessions.LaunchIntent{PermissionMode: "default"})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if dec.Allowed {
		t.Fatal("an unreadable context-policy control must deny under fail-closed posture")
	}
	if dec.DeniedStatus != http.StatusServiceUnavailable {
		t.Fatalf("DeniedStatus = %d, want %d", dec.DeniedStatus, http.StatusServiceUnavailable)
	}
	if dec.Reason != "context policy control unavailable (deny-closed)" {
		t.Fatalf("Reason = %q", dec.Reason)
	}
}

func TestSessionLaunchGate_ContextFailOpenOnReadError(t *testing.T) {
	g := &sessionLaunchGate{
		contextPolicy:   &fakeSessionContextPolicy{err: errors.New("context policy unavailable")},
		contextPosture:  availabilityFailOpen,
		recordAvailable: true,
		log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	dec, err := g.Authorize(context.Background(), "t1", sessions.LaunchIntent{PermissionMode: "default"})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Allowed {
		t.Fatalf("an unreadable context-policy control must allow under fail-open posture: %+v", dec)
	}
}

func TestSessionLaunchGate_BudgetCapDeniesRegardlessOfAvailabilityPosture(t *testing.T) {
	postures := []availabilityPosture{availabilityFailOpen, availabilityFailClosed}
	actions := []struct {
		name       string
		wantStatus int
	}{
		{name: "block", wantStatus: http.StatusPaymentRequired},
		{name: "throttle", wantStatus: http.StatusTooManyRequests},
	}

	for _, posture := range postures {
		for _, action := range actions {
			t.Run(posture.String()+"/"+action.name, func(t *testing.T) {
				g := &sessionLaunchGate{
					fin:             fakeBudget{chk: finops.BudgetCheck{Allowed: false, Action: action.name}},
					budgetPosture:   posture,
					recordAvailable: true,
				}
				dec, err := g.Authorize(context.Background(), "t1", sessions.LaunchIntent{PermissionMode: "default"})
				if err != nil {
					t.Fatalf("Authorize: %v", err)
				}
				if dec.Allowed {
					t.Fatal("a definitive budget cap must deny regardless of availability posture")
				}
				if dec.DeniedStatus != action.wantStatus {
					t.Fatalf("DeniedStatus = %d, want %d", dec.DeniedStatus, action.wantStatus)
				}
			})
		}
	}
}
