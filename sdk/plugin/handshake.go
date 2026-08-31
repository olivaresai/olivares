// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package plugin is the transport layer of the Olivares AI connector/module
// SDK: the versioned gRPC/protobuf wire contract (package genpb) plus the
// hashicorp/go-plugin glue that lets a SourceConnector, OutputConnector,
// ContentSource or Module written against ./sdk run out-of-process and talk to
// the engine over gRPC. It is Apache-2.0 and, like ./sdk, never imports the
// engine (./core).
//
// A plugin author calls Serve with their component; the engine loads the plugin
// binary and dispenses a client that satisfies the same sdk interface, so the
// in-process and out-of-process paths are identical to the rest of the engine.
//
// # Compatibility
//
// The hard compatibility gate is ProtocolVersion, the integer go-plugin checks
// before any RPC: it equals the protobuf package major version (v1 ⇒ 1). A
// plugin built against an incompatible protocol is rejected at handshake with a
// clear error, never a confusing mid-RPC failure. The MagicCookie is a sanity
// check that a launched process is in fact an Olivares plugin and not some other
// executable.
package plugin

import (
	goplugin "github.com/hashicorp/go-plugin"
)

// ProtocolVersion is the go-plugin protocol version: the hard compatibility gate
// go-plugin checks before a host and a plugin will speak at all.
//
// It was 1 for the whole olivares.sdk.v1 contract. It is 2 because OutputService
// Notify changed its response from Empty to NotifyResponse, and that is a change
// proto3's unknown-field tolerance makes DANGEROUS rather than harmless: an old
// host handed the new message would ignore the fields it does not know and read a
// plugin's REFUSAL as a success, silently recording a delivery that never
// happened. Refusing the handshake is the only outcome that cannot be
// misinterpreted, so the version is what forces a rebuild instead of leaving the
// mismatch to be discovered as missing evidence.
const ProtocolVersion = 2

// Magic cookie values identify an Olivares plugin process at handshake. They are
// not a security boundary (docs/SECURITY-HARDENING.md): they only stop an unrelated binary from
// being mistaken for a plugin.
const (
	magicCookieKey   = "OLIVARES_PLUGIN"
	magicCookieValue = "olivares-ai-control-plane"
)

// Dispensed plugin names — the keys under which each component kind is served
// and dispensed. A single plugin binary serves exactly one of these.
const (
	// SourcePluginName is the dispense key for a SourceConnector plugin.
	SourcePluginName = "source"
	// OutputPluginName is the dispense key for an OutputConnector plugin.
	OutputPluginName = "output"
	// ContentSourcePluginName is the dispense key for a ContentSource plugin.
	ContentSourcePluginName = "content_source"
	// ModulePluginName is the dispense key for a Module plugin. The out-of-process
	// module transport is defined but not wired in v1 (modules run in-process).
	ModulePluginName = "module"
)

// Handshake is the shared handshake config. Host and plugin must agree on it or
// the plugin is rejected before any RPC.
var Handshake = goplugin.HandshakeConfig{
	ProtocolVersion:  ProtocolVersion,
	MagicCookieKey:   magicCookieKey,
	MagicCookieValue: magicCookieValue,
}
