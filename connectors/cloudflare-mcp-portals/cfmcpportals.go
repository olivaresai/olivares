// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cfmcpportals

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
)

// Source is the Cloudflare One MCP Portals inventory connector. It polls the
// CF One Zero Trust API for MCP servers and portals, emits inventory edges,
// and performs shadow MCP detection.
type Source struct {
	cfg config
	cl  *client
	now func() time.Time
}

var _ sdk.SourceConnector = (*Source)(nil)

func New() *Source { return &Source{} }

func (s *Source) Descriptor() sdk.Descriptor { return descriptor() }

func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c, err := loadConfig(cfg)
	if err != nil {
		return err
	}
	s.cfg = c
	s.cl = newClient(c.apiBase, c.apiToken, &http.Client{Timeout: c.timeout})
	return nil
}

func (s *Source) Close(context.Context) error { return nil }

// Gather runs one discovery pass: list MCP servers → emit inventory edges →
// detect shadow MCPs → list portals → emit portal edges. A target that fails
// yields one health finding; the pass continues.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	at := s.clock()

	servers, err := s.listServers(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if emitErr := sink.Emit(ctx, healthFinding(originAccount, s.cfg.accountID, "Cloudflare One MCP servers list failed", err, at)); emitErr != nil {
			return emitErr
		}
	} else {
		if err := s.emitServerEdges(ctx, sink, servers, at); err != nil {
			return err
		}
		if s.cfg.shadowEnabled() {
			for _, f := range detectShadowMCPs(servers, s.cfg.approvedServers, s.cfg.accountID, at) {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := sink.Emit(ctx, f); err != nil {
					return err
				}
			}
		}
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	portals, err := s.listPortals(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return sink.Emit(ctx, healthFinding(originAccount, s.cfg.accountID, "Cloudflare One MCP portals list failed", err, at))
	}
	if err := s.emitPortalEdges(ctx, sink, portals, at); err != nil {
		return err
	}

	return ctx.Err()
}

// mcpServer is the structural subset of a CF One MCP server record.
type mcpServer struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	URL    string `json:"http_url"`
	Status string `json:"status"`
}

func (m mcpServer) nameRef() string { return strings.TrimSpace(m.Name) }

// mcpPortal is the structural subset of a CF One MCP portal record.
type mcpPortal struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
}

func (s *Source) listServers(ctx context.Context) ([]mcpServer, error) {
	path := "/accounts/" + s.cfg.accountID + "/access/ai-controls/mcp/servers"
	raw, err := s.cl.get(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	servers := make([]mcpServer, 0, len(raw))
	for _, item := range raw {
		var srv mcpServer
		if err := json.Unmarshal(item, &srv); err != nil {
			continue
		}
		if srv.nameRef() != "" {
			servers = append(servers, srv)
		}
	}
	return servers, nil
}

func (s *Source) listPortals(ctx context.Context) ([]mcpPortal, error) {
	path := "/accounts/" + s.cfg.accountID + "/access/ai-controls/mcp/portals"
	raw, err := s.cl.get(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	portals := make([]mcpPortal, 0, len(raw))
	for _, item := range raw {
		var p mcpPortal
		if err := json.Unmarshal(item, &p); err != nil {
			continue
		}
		if strings.TrimSpace(p.Name) != "" {
			portals = append(portals, p)
		}
	}
	return portals, nil
}

func (s *Source) emitServerEdges(ctx context.Context, sink sdk.Sink, servers []mcpServer, at time.Time) error {
	sort.Slice(servers, func(i, j int) bool { return servers[i].nameRef() < servers[j].nameRef() })
	for _, srv := range servers {
		if err := ctx.Err(); err != nil {
			return err
		}
		edge := inventoryEdge(
			originAccount, s.cfg.accountID,
			resMCPServer, redact.Clean(srv.nameRef()),
			redact.SanitizeURL(srv.URL),
			at,
		)
		if err := sink.Emit(ctx, edge); err != nil {
			return err
		}
	}
	return nil
}

func (s *Source) emitPortalEdges(ctx context.Context, sink sdk.Sink, portals []mcpPortal, at time.Time) error {
	sort.Slice(portals, func(i, j int) bool { return portals[i].Name < portals[j].Name })
	for _, p := range portals {
		if err := ctx.Err(); err != nil {
			return err
		}
		edge := inventoryEdge(
			originAccount, s.cfg.accountID,
			resMCPPortal, redact.Clean(strings.TrimSpace(p.Name)),
			redact.Clean(strings.TrimSpace(p.Hostname)),
			at,
		)
		if err := sink.Emit(ctx, edge); err != nil {
			return err
		}
	}
	return nil
}

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}
