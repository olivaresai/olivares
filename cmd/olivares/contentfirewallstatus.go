// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"sync/atomic"

	"github.com/olivaresai/olivares/modules/inferenceproxy"
)

// messagesInspectorBinding records the startup content-inspector attachment of the
// inline Messages proxy. buildModules creates one per module set and hands the same
// pointer to the inferenceproxy module and, through boot, to the engine.
// buildClaudeMessagesProxyServer is its only writer; the module status route reads it.
//
// The builder runs once per serve process and nothing replaces its decider, so the first
// record is final. A rebuild or hot reconfiguration must revise this contract instead of
// recording a second decider here.
type messagesInspectorBinding struct {
	state atomic.Pointer[inferenceproxy.ContentFirewallState]
}

var _ inferenceproxy.ContentFirewallStatusSource = (*messagesInspectorBinding)(nil)

// record stores the attachment of dec, the decider the built Messages proxy serves, or
// pep_not_composed when dec is nil. It applies the proxy's own nil test to dec.inspector.
// Only the first record is kept.
func (b *messagesInspectorBinding) record(dec *inferenceProxyDecider) {
	if b == nil {
		return
	}
	state := inferenceproxy.ContentFirewallPEPNotComposed
	if dec != nil {
		state = inferenceproxy.ContentFirewallInspectorAbsent
		if dec.inspector != nil {
			state = inferenceproxy.ContentFirewallInspectorAttached
		}
	}
	b.state.CompareAndSwap(nil, &state)
}

// ContentFirewallState returns the recorded attachment, or unobserved before a record.
func (b *messagesInspectorBinding) ContentFirewallState() inferenceproxy.ContentFirewallState {
	if b == nil {
		return inferenceproxy.ContentFirewallUnobserved
	}
	if state := b.state.Load(); state != nil {
		return *state
	}
	return inferenceproxy.ContentFirewallUnobserved
}
