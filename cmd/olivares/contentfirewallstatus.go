// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"slices"
	"sync/atomic"

	"github.com/olivaresai/olivares/cmd/olivares/internal/inferencepep"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/sdk"
)

// messagesInspectorBinding records the startup content-inspector attachment of the
// inline Messages proxy. buildModules creates one per module set and hands the same
// pointer to the inferenceproxy module and, through boot, to the engine.
// buildClaudeMessagesProxyServer is its only writer; the module status route reads it.
//
// The builder runs once per serve process and nothing replaces its decider, so the first
// record is final. What is recorded is the decider, not an answer: the firewall can be
// turned on and off while the engine runs, so the state is read from it each time.
type messagesInspectorBinding struct {
	recorded atomic.Bool
	state    atomic.Pointer[inferenceproxy.ContentFirewallState]
	decider  atomic.Pointer[inferencepep.Decider]
}

var _ inferenceproxy.ContentFirewallStatusSource = (*messagesInspectorBinding)(nil)

// record stores the attachment of dec, the decider the built Messages proxy serves, or
// pep_not_composed when dec is nil. It applies the proxy's own nil test to dec.Inspector,
// except that an inspector that is only the edition's decoy engine is not the content
// firewall and is reported absent. Only the first record is kept.
func (b *messagesInspectorBinding) record(dec *inferencepep.Decider) {
	if b == nil || !b.recorded.CompareAndSwap(false, true) {
		return
	}
	if dec == nil {
		state := inferenceproxy.ContentFirewallPEPNotComposed
		b.state.Store(&state)
		return
	}
	b.decider.Store(dec)
}

// firewallAttached says the content firewall itself is in use: an inspector that is on
// and is not the edition's decoy engine, looking through a chain of inspectors.
func firewallAttached(in contentInspector) bool {
	if chain, ok := in.(inspectorChain); ok {
		return slices.ContainsFunc(chain, firewallAttached)
	}
	_, decoyOnly := in.(sessions.SessionDecoySource)
	return in != nil && !decoyOnly && sdk.CapabilityEnabled(in)
}

// ContentFirewallState returns the recorded attachment, or unobserved before a record.
func (b *messagesInspectorBinding) ContentFirewallState() inferenceproxy.ContentFirewallState {
	if b == nil {
		return inferenceproxy.ContentFirewallUnobserved
	}
	if dec := b.decider.Load(); dec != nil {
		if firewallAttached(dec.Inspector) {
			return inferenceproxy.ContentFirewallInspectorAttached
		}
		return inferenceproxy.ContentFirewallInspectorAbsent
	}
	if state := b.state.Load(); state != nil {
		return *state
	}
	return inferenceproxy.ContentFirewallUnobserved
}
