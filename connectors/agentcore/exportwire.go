// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package agentcore

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Export write/list wire facts, VERIFIED 2026-07-04. These helpers
// deliberately share client.do so SigV4 signing and bounded response handling
// stay in one place.

const (
	pageSizeGatewayTargets          = 1000
	pageSizeEvaluators              = 100
	pageSizeOnlineEvaluationConfigs = 100
)

type writePolicyDefinition struct {
	Cedar *cedarPolicyBody `json:"cedar,omitempty"`
}

type createPolicyRequest struct {
	Name            string                `json:"name"`
	Definition      writePolicyDefinition `json:"definition"`
	Description     string                `json:"description,omitempty"`
	EnforcementMode string                `json:"enforcementMode,omitempty"`
	ValidationMode  string                `json:"validationMode,omitempty"`
	ClientToken     string                `json:"clientToken,omitempty"`
}

type updatedDescription struct {
	OptionalValue string `json:"optionalValue"`
}

type updatePolicyRequest struct {
	Definition      *writePolicyDefinition `json:"definition,omitempty"`
	Description     *updatedDescription    `json:"description,omitempty"`
	EnforcementMode string                 `json:"enforcementMode,omitempty"`
	ValidationMode  string                 `json:"validationMode,omitempty"`
	ClientToken     string                 `json:"clientToken,omitempty"`
}

type policyWriteResponse struct {
	PolicyID      string   `json:"policyId"`
	PolicyArn     string   `json:"policyArn"`
	Status        string   `json:"status"`
	StatusReasons []string `json:"statusReasons"`
}

type gatewayTargetItem struct {
	TargetID   string `json:"targetId"`
	Name       string `json:"name"`
	TargetType string `json:"targetType"`
	Status     string `json:"status"`
}

// listGatewayTargetsResponse: the array member is named "items" on the wire
// (API_ListGatewayTargets, VERIFIED 2026-07-04) — unlike every other list in
// this API, which names the member after the resource. Never "harmonize" it.
type listGatewayTargetsResponse struct {
	Targets   []gatewayTargetItem `json:"items"`
	NextToken string              `json:"nextToken"`
}

type evaluatorItem struct {
	EvaluatorID           string `json:"evaluatorId"`
	EvaluatorArn          string `json:"evaluatorArn"`
	EvaluatorName         string `json:"evaluatorName"`
	EvaluatorType         string `json:"evaluatorType"`
	Level                 string `json:"level"`
	Status                string `json:"status"`
	LockedForModification bool   `json:"lockedForModification"`
}

type listEvaluatorsResponse struct {
	Evaluators []evaluatorItem `json:"evaluators"`
	NextToken  string          `json:"nextToken"`
}

type onlineEvaluationConfigItem struct {
	OnlineEvaluationConfigID   string `json:"onlineEvaluationConfigId"`
	OnlineEvaluationConfigArn  string `json:"onlineEvaluationConfigArn"`
	OnlineEvaluationConfigName string `json:"onlineEvaluationConfigName"`
	Status                     string `json:"status"`
	ExecutionStatus            string `json:"executionStatus"`
	FailureReason              string `json:"failureReason"`
}

type listOnlineEvaluationConfigsResponse struct {
	OnlineEvaluationConfigs []onlineEvaluationConfigItem `json:"onlineEvaluationConfigs"`
	NextToken               string                       `json:"nextToken"`
}

func createPolicy(ctx context.Context, c *client, engineID string, req createPolicyRequest) (policyWriteResponse, error) {
	var resp policyWriteResponse
	err := c.sendJSON(ctx, http.MethodPost, policyEnginePoliciesPath(engineID), nil, req, &resp)
	return resp, err
}

func updatePolicy(ctx context.Context, c *client, engineID, policyID string, req updatePolicyRequest) (policyWriteResponse, error) {
	var resp policyWriteResponse
	path := policyEnginePoliciesPath(engineID) + "/" + url.PathEscape(policyID)
	err := c.sendJSON(ctx, http.MethodPatch, path, nil, req, &resp)
	return resp, err
}

func deletePolicy(ctx context.Context, c *client, engineID, policyID string) (policyWriteResponse, error) {
	var resp policyWriteResponse
	path := policyEnginePoliciesPath(engineID) + "/" + url.PathEscape(policyID)
	err := c.do(ctx, http.MethodDelete, path, nil, nil, &resp)
	return resp, err
}

// listGatewayTargets pages GET /gateways/{gatewayIdentifier}/targets/ with
// maxResults<=1000 and nextToken in the query string (verified wire).
func (s *Source) listGatewayTargets(ctx context.Context, c *client, gatewayID string) ([]gatewayTargetItem, error) {
	var out []gatewayTargetItem
	path := "/gateways/" + url.PathEscape(gatewayID) + "/targets/"
	token := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q := url.Values{"maxResults": {strconv.Itoa(pageSizeGatewayTargets)}}
		if token != "" {
			q.Set("nextToken", token)
		}
		var resp listGatewayTargetsResponse
		if err := c.getJSON(ctx, path, q, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Targets...)
		if resp.NextToken == "" {
			break
		}
		token = resp.NextToken
	}
	return out, nil
}

// listEvaluators pages POST /evaluators?maxResults=&nextToken= with NO body;
// AgentCore models this inventory read as POST-with-query, not as a JSON RPC.
func (s *Source) listEvaluators(ctx context.Context, c *client) ([]evaluatorItem, error) {
	var out []evaluatorItem
	token := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q := url.Values{"maxResults": {strconv.Itoa(pageSizeEvaluators)}}
		if token != "" {
			q.Set("nextToken", token)
		}
		var resp listEvaluatorsResponse
		if err := c.do(ctx, http.MethodPost, "/evaluators", q, nil, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Evaluators...)
		if resp.NextToken == "" {
			break
		}
		token = resp.NextToken
	}
	return out, nil
}

// listOnlineEvaluationConfigs pages POST /online-evaluation-configs with query
// parameters and no request body (verified wire).
func (s *Source) listOnlineEvaluationConfigs(ctx context.Context, c *client) ([]onlineEvaluationConfigItem, error) {
	var out []onlineEvaluationConfigItem
	token := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q := url.Values{"maxResults": {strconv.Itoa(pageSizeOnlineEvaluationConfigs)}}
		if token != "" {
			q.Set("nextToken", token)
		}
		var resp listOnlineEvaluationConfigsResponse
		if err := c.do(ctx, http.MethodPost, "/online-evaluation-configs", q, nil, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.OnlineEvaluationConfigs...)
		if resp.NextToken == "" {
			break
		}
		token = resp.NextToken
	}
	return out, nil
}

func (c *client) sendJSON(ctx context.Context, method, path string, query url.Values, in, out any) error {
	var raw []byte
	if in != nil {
		var err error
		raw, err = json.Marshal(in)
		if err != nil {
			return fmt.Errorf("agentcore: encode %s %s request: %w", method, path, err)
		}
	}
	return c.do(ctx, method, path, query, raw, out)
}

func policyEnginePoliciesPath(engineID string) string {
	return "/policy-engines/" + url.PathEscape(engineID) + "/policies"
}
