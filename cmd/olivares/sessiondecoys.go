// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"slices"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// sessiondecoys.go is the Community seam for the Business decoy credentials (AI Runtime
// Security): credential-shaped files planted in a session's home so that anything which
// reads one, or sends its value out, is a near-certain signal. The engine is built only
// by the Business edition; here it is an inert port, so every call site below keeps its
// exact prior behavior when it is nil.
//
// It reaches the engine at the three places a decoy can be seen: the launch (planting and
// removal, sessions.SessionDecoySource), the hook PEP's tool-call inspection and the
// inference proxy's request inspection (both through the existing contentInspector port).

// sessionDecoyEngine is what the edition adds.
type sessionDecoyEngine interface {
	sessions.SessionDecoySource
	contentInspector
}

// sessionDecoyDeps are the two things the engine may do to a run it recognizes. Neither
// hands it the engine, a credential or a store.
type sessionDecoyDeps struct {
	// Finding records one finding about a governed run in that run's own findings.
	Finding func(ctx context.Context, tenant model.TenantID, runRef string, f sdkmodel.FindingReport) error
	// Stop ends a governed run for a policy reason. detail is the ledger's note.
	Stop func(ctx context.Context, tenant model.TenantID, runRef, detail string) error
}

// newSessionDecoyDeps binds the two actions to the run's current session credential, the
// same way the session cost sink does: the run must be live and its claim current.
func newSessionDecoyDeps(credentials *auth.SessionCredentials, mod *sessions.Module) sessionDecoyDeps {
	principal := func(ctx context.Context, tenant model.TenantID, runRef string) (auth.Principal, error) {
		p, scope, err := credentials.ResolveRun(ctx, tenant, runRef)
		if err != nil {
			return auth.Principal{}, err
		}
		if scope.TenantID != tenant || scope.RunRef != runRef {
			return auth.Principal{}, auth.ErrUnauthenticated
		}
		return p, nil
	}
	return sessionDecoyDeps{
		Finding: func(ctx context.Context, tenant model.TenantID, runRef string, f sdkmodel.FindingReport) error {
			p, err := principal(ctx, tenant, runRef)
			if err != nil {
				return err
			}
			f.SubjectKind, f.SubjectRef = "session", runRef
			return mod.RecordSessionObservation(ctx, p, f)
		},
		Stop: func(ctx context.Context, tenant model.TenantID, runRef, detail string) error {
			p, err := principal(ctx, tenant, runRef)
			if err != nil {
				return err
			}
			return mod.StopSessionForPolicy(p, detail)
		},
	}
}

// chainInspectors runs every non-nil inspector over the same content and returns one
// verdict that each consumer reads exactly as it reads a single inspector's: it forwards
// only if every inspector forwards, it blocks if any blocks, the first inspector that did
// not forward supplies the status and reason, findings are concatenated and the meters add
// up. With one inspector it is that inspector, and with none it is nil, so a site with no
// engine keeps the inspector it had, nil included (a nil sessionDecoyEngine converts to a
// nil contentInspector).
func chainInspectors(list ...contentInspector) contentInspector {
	var live []contentInspector
	for _, in := range list {
		if in != nil {
			live = append(live, in)
		}
	}
	switch len(live) {
	case 0:
		return nil
	case 1:
		return live[0]
	}
	return inspectorChain(live)
}

type inspectorChain []contentInspector

// CapabilityEnabled: the chain is in use while any inspector of it is, so a firewall that
// is turned off does not make the chain look absent to a consumer that asks.
func (c inspectorChain) CapabilityEnabled() bool {
	return slices.ContainsFunc(c, func(in contentInspector) bool { return sdk.CapabilityEnabled(in) })
}

func (c inspectorChain) Inspect(ctx context.Context, in claudeapi.ContentInspectionInput) claudeapi.ContentInspectionDecision {
	out := claudeapi.ContentInspectionDecision{Forward: true}
	first := true
	for _, insp := range c {
		d := insp.Inspect(ctx, in)
		out.Findings = append(out.Findings, d.Findings...)
		out.Meter.Inspections += d.Meter.Inspections
		out.Meter.Channels += d.Meter.Channels
		out.Meter.Bytes += d.Meter.Bytes
		out.Meter.Detectors += d.Meter.Detectors
		out.Block = out.Block || d.Block
		if d.Forward || !first {
			continue
		}
		first = false
		out.Forward = false
		out.Status, out.ErrorType, out.Reason, out.ApprovalIntent = d.Status, d.ErrorType, d.Reason, d.ApprovalIntent
	}
	return out
}
