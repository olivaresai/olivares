// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferenceproxy

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
)

// ContentFirewallState is the startup attachment state of the inline Messages proxy's
// content inspector in this process. It describes composition only: not listener
// health, HA leadership, policy load, current enforcement or per-request inspection.
type ContentFirewallState string

// The attachment states. An attached inspector can be the deny-all fallback that an
// unreadable or invalid firewall configuration installs.
const (
	ContentFirewallUnobserved        ContentFirewallState = "unobserved"
	ContentFirewallPEPNotComposed    ContentFirewallState = "pep_not_composed"
	ContentFirewallInspectorAbsent   ContentFirewallState = "inspector_absent"
	ContentFirewallInspectorAttached ContentFirewallState = "inspector_attached"
)

// ContentFirewallStatusSource reads the attachment state that the composition root
// recorded when it built the Messages proxy. The module only reads it.
type ContentFirewallStatusSource interface {
	ContentFirewallState() ContentFirewallState
}

// WithContentFirewallStatus injects the composition root's attachment recorder. A
// module constructed without it reports unobserved.
func WithContentFirewallStatus(src ContentFirewallStatusSource) Option {
	return func(m *Module) { m.firewallStatus = src }
}

const (
	contentFirewallPEP  = "messages_proxy"
	contentFirewallNote = "Startup attachment for this process. This state does not report listener health or per-request inspection."
)

// contentFirewallDTO is the complete response body. It carries no tenant data.
type contentFirewallDTO struct {
	PEP   string `json:"pep"`
	State string `json:"state"`
	Note  string `json:"note"`
}

// contentFirewallState returns the recorded state, or unobserved when no recorder is
// injected or the recorder returns a value outside the published enum.
func (m *Module) contentFirewallState() ContentFirewallState {
	if m.firewallStatus == nil {
		return ContentFirewallUnobserved
	}
	switch s := m.firewallStatus.ContentFirewallState(); s {
	case ContentFirewallPEPNotComposed, ContentFirewallInspectorAbsent, ContentFirewallInspectorAttached:
		return s
	default:
		return ContentFirewallUnobserved
	}
}

// handleGetContentFirewall reports the startup attachment state of the inline Messages
// proxy content inspector in this process, where an attached inspector can be the
// deny-all fallback; it does not report listener health, policy load or per-request
// inspection.
func (m *Module) handleGetContentFirewall(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusOK, contentFirewallDTO{
		PEP:   contentFirewallPEP,
		State: string(m.contentFirewallState()),
		Note:  contentFirewallNote,
	})
}
