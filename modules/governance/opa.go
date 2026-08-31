// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/auth"
)

// IDN-09 OPA PDP: the Rego adapter behind the core/auth.PolicyEvaluator seam, for
// shops standardized on Open Policy Agent. The operator runs OPA as a sidecar; this
// adapter calls its Data API (POST /v1/data/<decision_path> with {"input": …}) and
// reads the boolean decision from the response "result". To preserve restrict-only
// semantics the operator's Rego MUST use a permit-by-default decision rule
// (`default allow := true` with `allow := false { <deny condition> }`), so a true
// result means "no restriction" and a false/absent result restricts. A transport or
// non-2xx failure returns an error, so the Authorizer fails CLOSED (deny) — an
// operator who wires an external PDP accepts that it must be reachable (the embedded
// Cedar engine is the always-available alternative).

// maxOPABody caps the OPA response body.
const maxOPABody = 1 << 20

// httpDoer is the minimal HTTP interface (satisfied by *http.Client).
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// OPAEvaluator is an auth.PolicyEvaluator backed by an OPA sidecar over HTTP.
type OPAEvaluator struct {
	dataURL string // {base}/v1/data/{decision path with / separators}
	token   string // optional bearer (OPA --authentication=token)
	doer    httpDoer
	now     func() time.Time
}

var _ auth.PolicyEvaluator = (*OPAEvaluator)(nil)

// NewOPAEvaluator builds an OPA adapter. baseURL is the OPA address (e.g.
// http://127.0.0.1:8181); decisionPath is the Rego decision path in dotted or
// slashed form (e.g. "authz.allow" or "authz/allow"); token is an optional bearer.
func NewOPAEvaluator(baseURL, decisionPath, token string, doer httpDoer) (*OPAEvaluator, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("governance: opa base url is required")
	}
	path := strings.Trim(strings.ReplaceAll(strings.TrimSpace(decisionPath), ".", "/"), "/")
	if path == "" {
		return nil, fmt.Errorf("governance: opa decision path is required")
	}
	if doer == nil {
		doer = &http.Client{Timeout: 5 * time.Second}
	}
	return &OPAEvaluator{
		dataURL: baseURL + "/v1/data/" + path,
		token:   strings.TrimSpace(token),
		doer:    doer,
		now:     time.Now,
	}, nil
}

// opaInput is the input document handed to the Rego policy.
type opaInput struct {
	Principal  opaPrincipal `json:"principal"`
	Permission string       `json:"permission"`
	Tenant     string       `json:"tenant"`
	Resource   opaResource  `json:"resource"`
	Time       int64        `json:"time"`
}

type opaPrincipal struct {
	Kind       string `json:"kind"`
	UserID     string `json:"user_id"`
	CredID     string `json:"cred_id"`
	Superadmin bool   `json:"superadmin"`
}

type opaResource struct {
	Kind        string            `json:"kind"`
	ID          string            `json:"id"`
	Sensitivity string            `json:"sensitivity"`
	Extra       map[string]string `json:"extra,omitempty"`
}

// Evaluate posts the request as the Rego input and returns the boolean decision. A
// true result is "no restriction"; a false or absent result restricts. A transport
// or non-2xx failure returns an error (Authorizer fails closed). The decision_id, when
// OPA decision logging is enabled, is surfaced in the Reason for ledger correlation.
func (o *OPAEvaluator) Evaluate(ctx context.Context, req auth.Request) (auth.Decision, error) {
	input := opaInput{
		Principal: opaPrincipal{
			Kind: string(req.Principal.Kind), UserID: string(req.Principal.UserID),
			CredID: string(req.Principal.CredID), Superadmin: req.Principal.Superadmin,
		},
		Permission: string(req.Permission),
		Tenant:     string(req.Tenant),
		Resource: opaResource{
			Kind: req.Resource.Kind, ID: req.Resource.ID,
			Sensitivity: req.Resource.Sensitivity, Extra: req.Resource.Extra,
		},
		Time: o.now().Unix(),
	}
	body, err := json.Marshal(struct {
		Input opaInput `json:"input"`
	}{input})
	if err != nil {
		return auth.Decision{}, fmt.Errorf("governance: opa encode: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.dataURL, bytes.NewReader(body))
	if err != nil {
		return auth.Decision{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "application/json")
	if o.token != "" {
		httpReq.Header.Set("authorization", "Bearer "+o.token)
	}

	resp, err := o.doer.Do(httpReq)
	if err != nil {
		return auth.Decision{}, fmt.Errorf("governance: opa request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxOPABody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return auth.Decision{}, fmt.Errorf("governance: opa http %d", resp.StatusCode)
	}

	// result is a pointer so an ABSENT result (undefined decision) is distinguishable
	// from an explicit false; both restrict, but absence signals a misconfigured path.
	var out struct {
		Result     *bool  `json:"result"`
		DecisionID string `json:"decision_id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return auth.Decision{}, fmt.Errorf("governance: opa decode: %w", err)
	}
	if out.Result != nil && *out.Result {
		return auth.Decision{Allow: true, Reason: opaReason("opa: permitted", out.DecisionID)}, nil
	}
	// Split provenance: an ABSENT/undefined result is a misconfigured decision
	// path — fail-closed integrity, ClassInvariant (the zero value). An explicit
	// result:false is an authored business-policy deny — ClassPolicy (shadowable).
	if out.Result == nil {
		return auth.Decision{Allow: false, Reason: opaReason("opa: decision undefined (check decision path / default allow)", out.DecisionID)}, nil
	}
	return auth.Decision{Allow: false, Reason: opaReason("opa: denied by policy", out.DecisionID), Class: auth.ClassPolicy}, nil
}

// opaReason appends the OPA decision_id (when present) for ledger correlation.
func opaReason(reason, decisionID string) string {
	if decisionID == "" {
		return reason
	}
	return reason + " [decision_id=" + decisionID + "]"
}
