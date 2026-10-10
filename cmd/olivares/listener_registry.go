// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"

	"github.com/olivaresai/olivares/cmd/olivares/internal/mcpgateway"
)

// serveListenerRegistry describes the engine's listeners in acquisition order.
// Defaults reference the same constants as the builders and CLI flags. Optional
// surfaces participate only when their builder mounted them. Source connectors
// own their transports separately and are not engine serve-family listeners.
func serveListenerRegistry(opts serveOptions, httpSrv, hitl, voice, gateway, codex, grok, claude, proxy *http.Server) []serveListenerSpec {
	registry := []serveListenerSpec{
		{name: "http", defaultAddr: defaultHTTPListen, configKey: "--listen", server: httpSrv,
			admission: "REST uses the API chain; console assets and enterprise public metadata use their own handlers"},
		{name: "grpc", defaultAddr: defaultGRPCListen, configKey: "--grpc-listen", addr: opts.grpcListen,
			admission: "gRPC protocol: TLS credentials, authentication interceptor and per-RPC authorization"},
		{name: "hitl", defaultAddr: defaultHITLListen, configKey: "OLIVARES_HITL_CONFIG.listen", server: hitl,
			admission: "webhook signatures; approval decisions call the API chain through apiDecider"},
		{name: "voice-webhook", defaultAddr: defaultVoiceCallListen, configKey: envVoiceCallConfig + ".listen", server: voice,
			admission: "OpenAI call webhook protocol: signature verification and configured tenant"},
		{name: "agent-gateway", defaultAddr: mcpgateway.DefaultListen, configKey: "OLIVARES_AGENT_GATEWAY_CONFIG.listen", server: gateway,
			admission: "MCP/A2A protocols: resource tokens, session credentials or JWT verification in each handler"},
		{name: "codex-hook-pep", defaultAddr: defaultCodexHookPEPListen, configKey: "OLIVARES_CODEX_HOOK_PEP_CONFIG.listen", server: codex,
			admission: "Codex hook response dialect: token verification, session identity and governed decisions"},
		{name: "grok-hook-pep", defaultAddr: defaultGrokHookPEPListen, configKey: "OLIVARES_GROK_HOOK_PEP_CONFIG.listen", server: grok,
			admission: "Grok hook response dialect: token verification, session identity and governed decisions"},
		{name: "claude-hook-pep", defaultAddr: defaultHookPEPListen, configKey: "OLIVARES_HOOK_PEP_CONFIG.listen", server: claude,
			admission: "Claude hook response dialect: hook proof, token verification and governed decisions; session MCP authenticates separately"},
		{name: "inference-proxy", defaultAddr: defaultInferenceProxyListen, configKey: "OLIVARES_INFERENCE_PROXY_CONFIG.listen", server: proxy,
			admission: "streaming Messages/OAuth protocols: proxy token verification and governance; OAuth handlers apply their own admission"},
	}
	active := registry[:0]
	for _, spec := range registry {
		if spec.name != "grpc" {
			if spec.server == nil {
				continue
			}
			spec.http = true
			spec.addr = httpBindAddr(spec.server.Addr, opts.insecure || (spec.server == claude && hostIsLoopback(spec.server.Addr)), opts.reusePort)
		}
		active = append(active, spec)
	}
	return active
}

// resolveServeListenerAddresses checks the complete set before any bind, even
// with SO_REUSEPORT. Acquisition uses these numeric addresses so DNS cannot
// change the checked endpoints between validation and binding.
func resolveServeListenerAddresses(ctx context.Context, specs []serveListenerSpec) ([]*net.TCPAddr, error) {
	resolved := make([]*net.TCPAddr, 0, len(specs))
	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("serve startup canceled before binding %q: %w", spec.addr, err)
		}
		addr, err := net.ResolveTCPAddr("tcp", spec.addr)
		if err != nil {
			return nil, fmt.Errorf("listener %s (%s) address %q: %w", spec.name, spec.configKey, spec.addr, err)
		}
		// The kernel ignores zones on loopback and global addresses. For
		// link-local addresses, interface names and numeric IDs are aliases.
		if !addr.IP.IsLinkLocalUnicast() && !addr.IP.IsLinkLocalMulticast() {
			addr.Zone = ""
		} else if addr.Zone != "" {
			index := 0
			if iface, err := net.InterfaceByName(addr.Zone); err == nil {
				index = iface.Index
			} else {
				index, err = strconv.Atoi(addr.Zone)
				if err != nil || index < 0 {
					return nil, fmt.Errorf("listener %s (%s) address %q: unknown IPv6 zone %q", spec.name, spec.configKey, spec.addr, addr.Zone)
				}
			}
			addr.Zone = ""
			if index != 0 {
				addr.Zone = strconv.Itoa(index)
			}
		}
		resolved = append(resolved, addr)
	}
	return resolved, checkServeListenerCollisions(specs, resolved)
}

func checkServeListenerCollisions(specs []serveListenerSpec, addresses []*net.TCPAddr) error {
	for i, addr := range addresses {
		for j, other := range addresses[:i] {
			if addr.Port == 0 || addr.Port != other.Port {
				continue // Port zero is checked again after the kernel assigns it.
			}
			// Go's "tcp" wildcard listeners can be dual-stack. Refuse an
			// overlap regardless of the host's current IPv6 socket policy.
			wildcard := len(addr.IP) == 0 || addr.IP.IsUnspecified() || len(other.IP) == 0 || other.IP.IsUnspecified()
			if wildcard || (addr.IP.Equal(other.IP) && addr.Zone == other.Zone) {
				first, second := specs[j], specs[i]
				return fmt.Errorf("listener collision: %s (%s) at %q overlaps %s (%s) at %q; configure distinct listen addresses",
					first.name, first.configKey, first.addr, second.name, second.configKey, second.addr)
			}
		}
	}
	return nil
}
