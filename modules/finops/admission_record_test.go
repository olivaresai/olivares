// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// unreadableData is a ledger handle that cannot be read OR written: the honest
// shape of "the budget store is unreachable". Both halves matter — the read is
// what makes Reserve refuse, and the write is what makes the refusal unauditable,
// because the audit row goes through the very handle that just failed.
type unreadableData struct {
	api.ModuleData
	err error
}

func (d unreadableData) View(context.Context, model.TenantID, func(store.Scope) error) error {
	return d.err
}

func (d unreadableData) Mutate(context.Context, model.TenantID, func(store.Scope) error) error {
	return d.err
}

// captureLog gives a module a logger whose output a test can read.
func captureLog(m *Module) *bytes.Buffer {
	var buf bytes.Buffer
	m.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return &buf
}

// TestReserveRecordsARefusalItCannotAudit pins the only record a deny-closed
// refusal over an unreachable ledger can leave. The audit row is written through
// the same data handle whose failure produced the refusal, so on this input it
// cannot be written at all: with the error discarded, a refused request left NO
// trace anywhere — not on the ledger, not in the log. The engine log is the
// fallback of record and it carries the fields the audit row would have carried,
// so an operator greps for the same action either way.
func TestReserveRecordsARefusalItCannotAudit(t *testing.T) {
	cases := []struct {
		name string
		arm  func(m *Module)
	}{
		{name: "no data handle", arm: func(m *Module) { m.data = nil }},
		{name: "unreadable ledger", arm: func(m *Module) {
			m.UseData(unreadableData{ModuleData: m.data, err: errors.New("dial budget store: connection refused")})
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, _, tenant, _ := newFin(t)
			c.arm(m)
			buf := captureLog(m)

			res, err := m.Reserve(context.Background(), tenant, AdmissionRequest{
				Scope: AdmissionScopeSessionLaunch, IdempotencyKey: "unreachable-1",
			})
			if err != nil {
				t.Fatalf("a deny-closed refusal must not return an error a caller fails open on: %v", err)
			}
			if res.Allowed || res.Reason != ReasonStoreUnreachable {
				t.Fatalf("an unreachable ledger must refuse: %+v", res)
			}

			out := buf.String()
			if out == "" {
				t.Fatal("a refusal that could not be audited left 0 log bytes: it is recorded nowhere at all")
			}
			for _, want := range []string{
				"level=ERROR",
				auditActionAdmissionDenied,
				"scope=" + AdmissionScopeSessionLaunch,
				tenant.String(),
			} {
				if !strings.Contains(out, want) {
					t.Fatalf("the record does not carry %q; got:\n%s", want, out)
				}
			}
		})
	}
}

// TestReserveAuditsOnAReachableLedgerWithoutLoggingAnError is the control
// positive: with a ledger that CAN take the row, the refusal is audited and the
// engine log stays quiet. Without it the test above would pass on a module that
// logs an error for every deny, which is a different (and much noisier) product.
func TestReserveAuditsOnAReachableLedgerWithoutLoggingAnError(t *testing.T) {
	forEachAdmissionEngine(t, runReserveAuditsOnAReachableLedgerWithoutLoggingAnError)
}

func runReserveAuditsOnAReachableLedgerWithoutLoggingAnError(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	buf := captureLog(m)
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: oneUSD, Action: "block",
	})
	ctx := context.Background()
	if first, err := m.Reserve(ctx, tenant, AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD, IdempotencyKey: "audited-1",
	}); err != nil || !first.Allowed {
		t.Fatalf("first reserve: %+v err=%v", first, err)
	}
	res, err := m.Reserve(ctx, tenant, AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD, IdempotencyKey: "audited-2",
	})
	if err != nil || res.Allowed {
		t.Fatalf("an exhausted budget must deny: %+v err=%v", res, err)
	}
	if n := countAuditAction(t, st, tenant, auditActionAdmissionDenied); n != 1 {
		t.Fatalf("audit rows for %s = %d, want 1", auditActionAdmissionDenied, n)
	}
	if strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("a deny the ledger DID record must not also be logged as an error:\n%s", buf.String())
	}
}

// budgetStoreDSNError is the shape a real budget store produces when it cannot be
// dialled: a wrapper naming the connection it tried, around the network error that
// actually failed. The three components are what a support bundle must not carry
// away, and the inner error is what the class is derived from.
func budgetStoreDSNError(host, user, database string) error {
	return fmt.Errorf("failed to connect to `host=%s user=%s database=%s`: %w", host, user, database,
		&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")})
}

// TestUnauditedDenyRecordsAClassAndNotTheStoresOwnError pins what the fallback
// record may say about the failure that produced it. The record is at ERROR and a
// support bundle collects it, so the store's own error text cannot travel on it:
// that text names the host, the user and the database of the budget store, which
// is topology an operator did not ask to publish and cannot unpublish.
//
// What they CAN act on is the class, so the class is what the line carries.
func TestUnauditedDenyRecordsAClassAndNotTheStoresOwnError(t *testing.T) {
	const (
		host     = "budget-ledger.internal"
		user     = "ledger-writer"
		database = "ledger-primary"
	)
	m, _, tenant, _ := newFin(t)
	m.UseData(unreadableData{ModuleData: m.data, err: budgetStoreDSNError(host, user, database)})
	buf := captureLog(m)

	res, err := m.Reserve(context.Background(), tenant, AdmissionRequest{
		Scope: AdmissionScopeSessionLaunch, IdempotencyKey: "unreachable-dsn",
	})
	if err != nil {
		t.Fatalf("a deny-closed refusal must not return an error a caller fails open on: %v", err)
	}
	if res.Allowed || res.Reason != ReasonStoreUnreachable {
		t.Fatalf("an unreachable ledger must refuse: %+v", res)
	}

	out := buf.String()
	if !strings.Contains(out, "audit_err_class="+auditFailureUnreachable) {
		t.Fatalf("the record does not name a class an operator can act on:\n%s", out)
	}
	for _, leaked := range []string{host, user, database, "connection refused"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("the record carries %q from the store's own error:\n%s", leaked, out)
		}
	}
}

// TestAuditFailureClassIsDerivedCausally pins the mapping itself, including the
// two ends that matter: a cause the classifier cannot place is "other" and not a
// guess, and the class is read from the error CHAIN — by sentinel and by type —
// so it does not move when a driver rewords its message.
func TestAuditFailureClassIsDerivedCausally(t *testing.T) {
	for _, c := range []struct {
		name  string
		cause error
		want  string
	}{
		{"a store that cannot be dialled", budgetStoreDSNError("h", "u", "d"), auditFailureUnreachable},
		{"no ledger handle at all", attemptErr(errCodeCapabilityUnavailable, nil), auditFailureUnreachable},
		{"the backend reported itself unavailable", fmt.Errorf("append: %w", store.ErrStoreUnavailable), auditFailureUnreachable},
		{"the call ran out of time", fmt.Errorf("append: %w", context.DeadlineExceeded), auditFailureTimeout},
		{"the socket ran out of time", &net.OpError{Op: "read", Err: timeoutError{}}, auditFailureTimeout},
		{"the role may not write the chain", fmt.Errorf("append: %w", store.ErrAppendOnlyGrantMissing), auditFailurePermission},
		{"the scope is read-only", fmt.Errorf("append: %w", store.ErrReadOnly), auditFailurePermission},
		{"the file cannot be opened", fmt.Errorf("open: %w", fs.ErrPermission), auditFailurePermission},
		{"anything else", errors.New("the ledger said no"), auditFailureOther},
		{"nothing at all", nil, auditFailureOther},
	} {
		if got := auditFailureClass(c.cause); got != c.want {
			t.Errorf("%s: class = %q, want %q", c.name, got, c.want)
		}
	}
}

// timeoutError is a net.Error that timed out, which is the one distinction the
// classifier draws inside "the network failed".
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
