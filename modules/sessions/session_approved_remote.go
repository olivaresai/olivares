// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/url"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions/egress"
)

// A CONFINED SESSION'S APPROVED PUSH REMOTES.
//
// A session whose network is confined to its provider may also reach the push
// remote of each repository binding the organization approved for the run's
// workspace, at host:port level. The binding's credential never enters the
// session: the agent pushes with whatever it has, and the engine's own
// publication path stays the one that holds the organization's credential. A
// remote the agent adds itself is no destination, because the proxy admits only
// listed endpoints.
//
// ponytail: the grant is host:port, so the agent can reach another repository
// on the same host, without the organization's credential. Upgrade trigger: a
// forge that serves repositories of different trust on one host and port.

// sessionRelayEndpoints is the most hook and session relays a launch adds to its
// policy (egress.MaxEndpoints counts them).
const sessionRelayEndpoints = 3

// SessionApprovedRemotes lists the push endpoints of the repository bindings
// approved for a workspace, each as scheme://host:port/ with no path, user or
// token. The composition root binds it; unbound, no remote is granted.
type SessionApprovedRemotes interface {
	ApprovedRemotes(ctx context.Context, tenant model.TenantID, workspace model.ID) ([]string, error)
}

// configureApprovedRemotes adds the run's approved push remotes to a confined
// launch's network. https and http go with the providers, which may resolve to a
// private address (a forge is often on the organization's network). An ssh remote is
// not granted: the proxy tunnels ssh only to a public administrator-listed host, and
// one on a reserved address would refuse the whole launch; the organization's own
// push goes through the engine, with its credential, not through the session.
// A failure to list them does not refuse the launch: the session keeps its
// boundary and the remote is simply not granted.
func (m *Module) configureApprovedRemotes(ctx context.Context, tenant model.TenantID, runRef string, p *CreateRunParams, spec *LaunchSpec) {
	if m.rt.ApprovedRemotes == nil || spec.NetworkPolicy == nil {
		return
	}
	rec, err := m.loadRun(ctx, tenant, runRef)
	if err != nil {
		m.warnf("sessions: the approved push remotes were not read; none is granted", "run_ref", runRef, "err", redactErr(err))
		return
	}
	remotes, err := m.rt.ApprovedRemotes.ApprovedRemotes(ctx, tenant, model.ID(rec.String(colRunAuthzWorkspaceID)))
	if err != nil {
		m.warnf("sessions: the approved push remotes were not read; none is granted", "run_ref", runRef, "err", redactErr(err))
		return
	}
	policy := spec.NetworkPolicy
	var granted []string
	for _, raw := range remotes {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			m.warnf("sessions: an approved push remote is malformed; it is not granted", "run_ref", runRef)
			continue
		}
		// The hook and session relays are added later in the launch: their room
		// is kept.
		if len(policy.Providers)+len(policy.PublicProviders)+max(len(policy.Controls), sessionRelayEndpoints) >= egress.MaxEndpoints {
			m.warnf("sessions: the session's network has no room for more endpoints; an approved push remote is not granted", "run_ref", runRef, "remote", raw)
			continue
		}
		switch u.Scheme {
		case "https", "http":
			// The first provider is the session's bound endpoint, the one its key
			// is checked against: a remote never takes that place.
			if len(policy.Providers) == 0 {
				m.warnf("sessions: the session's network has no bound provider; an approved push remote is not granted", "run_ref", runRef, "remote", raw)
				continue
			}
			policy.Providers = append(policy.Providers, raw)
		case "ssh":
			m.warnf("sessions: an approved ssh push remote is not a session destination; the engine pushes to it", "run_ref", runRef, "remote", raw)
			continue
		default:
			m.warnf("sessions: an approved push remote has no supported scheme; it is not granted", "run_ref", runRef, "remote", raw)
			continue
		}
		granted = append(granted, raw)
	}
	if len(granted) != 0 {
		p.egressDetail = strings.TrimSpace(p.egressDetail + " approved remotes " + strings.Join(granted, ","))
	}
}
