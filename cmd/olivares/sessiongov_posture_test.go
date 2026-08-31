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
