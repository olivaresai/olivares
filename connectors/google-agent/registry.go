// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package googleagent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
)

// Agent Registry v1 read surfaces — VERIFIED-RAW 2026-07-05 against
// agentregistry.googleapis.com v1 revision 20260623 (GA 2026-06-18). The API is
// read-only here: list/get/search. The connector uses list only.
const (
	defaultRegistryEndpoint = "https://agentregistry.googleapis.com"

	registryAttrFramework       = "agentregistry.googleapis.com/system/Framework"
	registryAttrRuntimeIdentity = "agentregistry.googleapis.com/system/RuntimeIdentity"
	registryAttrRuntimeRef      = "agentregistry.googleapis.com/system/RuntimeReference"
)

type registryInventory struct {
	Agents              []registryAgent
	MCPServers          []registryMCPServer
	ReadableLocations   []string
	UnreadableLocations []string
	Partials            []registryPartial
}

type registryPartial struct {
	Location string
	Resource string
	Reason   string
}

type registryAgentsResponse struct {
	Agents        []registryAgent `json:"agents"`
	NextPageToken string          `json:"nextPageToken"`
}

// registryAgent is deliberately structural and minimal. Agent resources also
// carry description, skills[] and a full A2A card, all operator/agent-authored
// text; those fields are intentionally not struct members, so they cannot be
// emitted accidentally. There is no owner or approval-state field in GA; the
// governed posture is curation-by-registration plus IAM.
type registryAgent struct {
	Name        string                     `json:"name"`
	AgentID     string                     `json:"agentId"`
	DisplayName string                     `json:"displayName"`
	Protocols   []registryAgentProtocol    `json:"protocols"`
	UpdateTime  string                     `json:"updateTime"`
	Attributes  map[string]json.RawMessage `json:"attributes"`
}

type registryAgentProtocol struct {
	Type string `json:"type"`
}

func (a registryAgent) framework() string {
	return registryAttributeString(a.Attributes, registryAttrFramework, "framework")
}

func (a registryAgent) runtimePrincipal() string {
	return registryAttributeString(a.Attributes, registryAttrRuntimeIdentity, "principal")
}

func (a registryAgent) runtimeReferenceURI() string {
	return registryAttributeString(a.Attributes, registryAttrRuntimeRef, "uri")
}

type registryMCPServersResponse struct {
	MCPServers    []registryMCPServer `json:"mcpServers"`
	NextPageToken string              `json:"nextPageToken"`
}

// registryMCPServer decodes only the stable resource identity and tool
// annotations. Tool descriptions are intentionally omitted: Google's roles docs
// warn the annotations steer downstream agents, and those booleans are the
// posture surface needed here.
type registryMCPServer struct {
	Name        string            `json:"name"`
	MCPServerID string            `json:"mcpServerId"`
	DisplayName string            `json:"displayName"`
	Tools       []registryMCPTool `json:"tools"`
}

type registryMCPTool struct {
	Name        string                     `json:"name"`
	Annotations registryMCPToolAnnotations `json:"annotations"`
}

type registryMCPToolAnnotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint"`
	DestructiveHint bool `json:"destructiveHint"`
	IdempotentHint  bool `json:"idempotentHint"`
	OpenWorldHint   bool `json:"openWorldHint"`
}

func (s *Source) readRegistryInventory(ctx context.Context, client *httpx.Client) (registryInventory, error) {
	var inv registryInventory
	for _, loc := range s.registryLocations {
		agents, boundHit, err := s.listRegistryAgents(ctx, client, loc)
		if err != nil {
			if isUnreadableStatus(err) {
				inv.UnreadableLocations = append(inv.UnreadableLocations, loc)
				continue
			}
			return registryInventory{}, err
		}
		inv.ReadableLocations = append(inv.ReadableLocations, loc)
		inv.Agents = append(inv.Agents, agents...)
		if boundHit {
			inv.Partials = append(inv.Partials, registryPartial{Location: loc, Resource: "agents", Reason: "pagination_bound"})
		}

		servers, boundHit, err := s.listRegistryMCPServers(ctx, client, loc)
		if err != nil {
			if isUnreadableStatus(err) {
				inv.Partials = append(inv.Partials, registryPartial{Location: loc, Resource: "mcpServers", Reason: "unreadable"})
				continue
			}
			return registryInventory{}, err
		}
		inv.MCPServers = append(inv.MCPServers, servers...)
		if boundHit {
			inv.Partials = append(inv.Partials, registryPartial{Location: loc, Resource: "mcpServers", Reason: "pagination_bound"})
		}
	}
	return inv, nil
}

func (s *Source) listRegistryAgents(ctx context.Context, client *httpx.Client, loc string) ([]registryAgent, bool, error) {
	path := "/v1/projects/" + url.PathEscape(s.project) + "/locations/" + url.PathEscape(loc) + "/agents"
	var out []registryAgent
	pageToken := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		query := url.Values{"pageSize": {strconv.Itoa(s.pageSize)}}
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		var resp registryAgentsResponse
		if err := client.GetJSON(ctx, path, query, &resp); err != nil {
			return nil, false, err
		}
		out = append(out, resp.Agents...)
		if resp.NextPageToken == "" {
			return out, false, nil
		}
		pageToken = resp.NextPageToken
	}
	return out, pageToken != "", nil
}

func (s *Source) listRegistryMCPServers(ctx context.Context, client *httpx.Client, loc string) ([]registryMCPServer, bool, error) {
	path := "/v1/projects/" + url.PathEscape(s.project) + "/locations/" + url.PathEscape(loc) + "/mcpServers"
	var out []registryMCPServer
	pageToken := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		query := url.Values{"pageSize": {strconv.Itoa(s.pageSize)}}
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		var resp registryMCPServersResponse
		if err := client.GetJSON(ctx, path, query, &resp); err != nil {
			return nil, false, err
		}
		out = append(out, resp.MCPServers...)
		if resp.NextPageToken == "" {
			return out, false, nil
		}
		pageToken = resp.NextPageToken
	}
	return out, pageToken != "", nil
}

func registryAttributeString(attrs map[string]json.RawMessage, key, field string) string {
	raw, ok := attrs[key]
	if !ok || len(raw) == 0 {
		return ""
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	rawField, ok := obj[field]
	if !ok {
		return ""
	}
	var out string
	if err := json.Unmarshal(rawField, &out); err != nil {
		return ""
	}
	return out
}

func isUnreadableStatus(err error) bool {
	var se *httpx.StatusError
	if !errors.As(err, &se) {
		return false
	}
	return se.Status == http.StatusForbidden || se.Status == http.StatusNotFound
}

func registryResourceLabel(name, display string) string {
	if display != "" {
		return display
	}
	if name != "" {
		return lastSegment(name)
	}
	return "unknown"
}
