// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions/egress"
)

const egressRefusalTimeout = 5 * time.Second

// watchEgressRefusals makes each key the session proxy refuses a finding on the
// session. A policy that pins no key refuses none and gets no callback.
func (m *Module) watchEgressRefusals(tenant model.TenantID, runRef string, p *egress.Policy) {
	if p == nil || p.CredentialSHA256 == ([sha256.Size]byte{}) {
		return
	}
	p.OnRefusal = func(r egress.Refusal) error { return m.recordEgressRefusal(tenant, runRef, r) }
}

// recordEgressRefusal writes one core Finding on the run's core Session naming the
// session, host and header. The proxy has already refused the request, so a write
// that fails is logged and returned (the proxy reports that refusal again next
// time) and the refusal stands. A header value cannot reach this far: Refusal has
// none. A run without a core Session still gets the finding, named by run_ref.
func (m *Module) recordEgressRefusal(tenant model.TenantID, runRef string, r egress.Refusal) error {
	ctx, cancel := context.WithTimeout(context.Background(), egressRefusalTimeout)
	defer cancel()
	err := m.runtimeData(ctx).Mutate(ctx, tenant, func(sc store.Scope) error {
		runs, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		run, err := findRunRec(ctx, runs, runRef)
		if err != nil {
			return err
		}
		subject, _ := model.ParseID(run.String(colRunCoreSessionID))
		_, err = sc.Findings().Create(ctx, model.Finding{
			Kind: "egress", Severity: model.SeverityHigh, Status: model.FindingOpen,
			Source: Name, SubjectKind: "session", SubjectID: subject,
			Title:      "Session " + runRef + " sent another key in " + r.Header + " to " + r.Host + "; refused",
			DetailHash: hashBytes([]byte("sessions.egress.credential\x00" + runRef + "\x00" + r.Host + "\x00" + r.Header)),
			OccurredAt: model.NewTimestamp(m.now()),
			Metadata:   map[string]any{"run_ref": runRef, "host": r.Host, "header": r.Header},
		})
		return err
	})
	if err != nil {
		m.warnf("sessions: a refused provider key was not recorded as a finding", "run_ref", runRef, "host", r.Host, "header", r.Header, "error", err.Error())
	}
	return err
}
