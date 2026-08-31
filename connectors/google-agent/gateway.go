// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package googleagent

import (
	"context"
	"net/url"
	"strconv"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
)

// Agent Gateway is a GA Network Services v1 surface — VERIFIED-RAW 2026-07-05
// against networkservices.googleapis.com v1 revision 20260626 (GA 2026-06-18).
// The v1 GA resource does not expose the Model Armor / policy binding; that is
// only extensionBindings in v1alpha1 and intentionally stays out of this
// read-only connector.
const defaultNetworkServicesEndpoint = "https://networkservices.googleapis.com"

type gatewayInventory struct {
	Gateways            []agentGateway
	UnreadableLocations []string
	Partials            []gatewayPartial
	Unreachable         []gatewayUnreachable
}

type gatewayPartial struct {
	Location string
	Reason   string
}

type gatewayUnreachable struct {
	Location    string
	Unreachable []string
}

type agentGatewaysResponse struct {
	AgentGateways []agentGateway `json:"agentGateways"`
	NextPageToken string         `json:"nextPageToken"`
	Unreachable   []string       `json:"unreachable"`
}

// agentGateway is the minimal GA shape used for posture. Other gateway fields
// and alpha-only extension bindings are intentionally not decoded.
type agentGateway struct {
	Name          string   `json:"name"`
	Registries    []string `json:"registries"`
	UpdateTime    string   `json:"updateTime"`
	GoogleManaged struct {
		GovernedAccessPath string `json:"governedAccessPath"`
	} `json:"googleManaged"`
	SelfManaged struct {
		ResourceURI string `json:"resourceUri"`
	} `json:"selfManaged"`
}

func (s *Source) readGatewayInventory(ctx context.Context, client *httpx.Client) (gatewayInventory, error) {
	var inv gatewayInventory
	for _, loc := range s.gatewayLocations {
		gateways, unreachable, boundHit, err := s.listGateways(ctx, client, loc)
		if err != nil {
			if isUnreadableStatus(err) {
				inv.UnreadableLocations = append(inv.UnreadableLocations, loc)
				continue
			}
			return gatewayInventory{}, err
		}
		inv.Gateways = append(inv.Gateways, gateways...)
		if len(unreachable) > 0 {
			inv.Unreachable = append(inv.Unreachable, gatewayUnreachable{Location: loc, Unreachable: unreachable})
		}
		if boundHit {
			inv.Partials = append(inv.Partials, gatewayPartial{Location: loc, Reason: "pagination_bound"})
		}
	}
	return inv, nil
}

func (s *Source) listGateways(ctx context.Context, client *httpx.Client, loc string) ([]agentGateway, []string, bool, error) {
	path := "/v1/projects/" + url.PathEscape(s.project) + "/locations/" + url.PathEscape(loc) + "/agentGateways"
	var out []agentGateway
	var unreachable []string
	pageToken := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, false, err
		}
		query := url.Values{"pageSize": {strconv.Itoa(s.pageSize)}}
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		var resp agentGatewaysResponse
		if err := client.GetJSON(ctx, path, query, &resp); err != nil {
			return nil, nil, false, err
		}
		out = append(out, resp.AgentGateways...)
		unreachable = append(unreachable, resp.Unreachable...)
		if resp.NextPageToken == "" {
			return out, unreachable, false, nil
		}
		pageToken = resp.NextPageToken
	}
	return out, unreachable, pageToken != "", nil
}
