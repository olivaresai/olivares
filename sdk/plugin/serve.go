// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package plugin

import (
	goplugin "github.com/hashicorp/go-plugin"

	"github.com/olivaresai/olivares/sdk"
)

// ServeSource runs c as a standalone plugin process over gRPC. A connector
// author's main is just:
//
//	func main() { plugin.ServeSource(myConnector{}) }
//
// It blocks, serving until the engine that launched it exits.
func ServeSource(c sdk.SourceConnector) {
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: Handshake,
		Plugins:         goplugin.PluginSet{SourcePluginName: &SourcePlugin{Impl: c}},
		GRPCServer:      goplugin.DefaultGRPCServer,
	})
}

// ServeOutput runs c as a standalone output-connector plugin process over gRPC.
func ServeOutput(c sdk.OutputConnector) {
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: Handshake,
		Plugins:         goplugin.PluginSet{OutputPluginName: &OutputPlugin{Impl: c}},
		GRPCServer:      goplugin.DefaultGRPCServer,
	})
}

// ServeContentSource runs c as a standalone content-source plugin process over
// gRPC.
func ServeContentSource(c sdk.ContentSource) {
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: Handshake,
		Plugins:         goplugin.PluginSet{ContentSourcePluginName: &ContentSourcePlugin{Impl: c}},
		GRPCServer:      goplugin.DefaultGRPCServer,
	})
}

// SourcePluginMap is the host-side plugin set used to load a source-connector
// plugin (Impl nil: the host dispenses a client, it does not serve).
func SourcePluginMap() goplugin.PluginSet {
	return goplugin.PluginSet{SourcePluginName: &SourcePlugin{}}
}

// OutputPluginMap is the host-side plugin set used to load an output-connector
// plugin.
func OutputPluginMap() goplugin.PluginSet {
	return goplugin.PluginSet{OutputPluginName: &OutputPlugin{}}
}

// ContentSourcePluginMap is the host-side plugin set used to load a
// content-source plugin.
func ContentSourcePluginMap() goplugin.PluginSet {
	return goplugin.PluginSet{ContentSourcePluginName: &ContentSourcePlugin{}}
}
