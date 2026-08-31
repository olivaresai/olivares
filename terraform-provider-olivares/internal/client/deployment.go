// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// deploymentsPath is the deploy module's definition collection (module VII routes
// mount under /v1/m/deploy/). A Terraform-managed olivares_deployment is the
// DESIRED-STATE definition; reconciling it to real infrastructure is the engine's
// separate, HITL-governed apply — declaring it here never mutates the estate.
const deploymentsPath = "/v1/m/deploy/definitions"

// Deployment is the wire representation of a deploy definition, matching the
// deploy module's definitionDTO. Spec is the desired-state document; on read it
// is the engine's canonical re-serialization.
type Deployment struct {
	ID             string          `json:"id"`
	SubjectKind    string          `json:"subject_kind"`
	SubjectRef     string          `json:"subject_ref"`
	Name           string          `json:"name"`
	Environment    string          `json:"environment"`
	Target         string          `json:"target"`
	Runtime        string          `json:"runtime"`
	DesiredStatus  string          `json:"desired_status"`
	CurrentVersion int64           `json:"current_version"`
	AppliedVersion int64           `json:"applied_version"`
	SpecHash       string          `json:"spec_hash"`
	SourceRef      string          `json:"source_ref"`
	Spec           json.RawMessage `json:"spec,omitempty"`
}

// createDeploymentRequest is the POST body declaring a new definition.
type createDeploymentRequest struct {
	SubjectKind string          `json:"subject_kind"`
	SubjectRef  string          `json:"subject_ref"`
	Name        string          `json:"name"`
	Environment string          `json:"environment"`
	Target      string          `json:"target"`
	Runtime     string          `json:"runtime"`
	SourceRef   string          `json:"source_ref,omitempty"`
	Spec        json.RawMessage `json:"spec"`
}

// updateDeploymentRequest is the PUT body. The engine's update accepts only the
// mutable fields (a new desired revision + optionally target/source_ref); the
// subject/name/environment/runtime are the definition's immutable identity, so
// the resource marks them RequiresReplace rather than sending them here.
type updateDeploymentRequest struct {
	Target    string          `json:"target"`
	SourceRef string          `json:"source_ref"`
	Spec      json.RawMessage `json:"spec"`
}

// CreateDeployment declares a new deployment definition (POST). tenantOverride,
// when non-empty, replaces the client-level tenant for this call.
func (c *Client) CreateDeployment(ctx context.Context, tenantOverride string, d Deployment) (*Deployment, error) {
	body, err := json.Marshal(createDeploymentRequest{
		SubjectKind: d.SubjectKind, SubjectRef: d.SubjectRef, Name: d.Name, Environment: d.Environment,
		Target: d.Target, Runtime: d.Runtime, SourceRef: d.SourceRef, Spec: d.Spec,
	})
	if err != nil {
		return nil, fmt.Errorf("olivares: encode deployment: %w", err)
	}
	return c.writeDeployment(ctx, http.MethodPost, c.endpoint+deploymentsPath, tenantOverride, body)
}

// UpdateDeployment declares a new desired revision (PUT). It sends only the
// mutable fields; the subject/name/environment/runtime are immutable identity.
func (c *Client) UpdateDeployment(ctx context.Context, tenantOverride, id string, d Deployment) (*Deployment, error) {
	body, err := json.Marshal(updateDeploymentRequest{Target: d.Target, SourceRef: d.SourceRef, Spec: d.Spec})
	if err != nil {
		return nil, fmt.Errorf("olivares: encode deployment: %w", err)
	}
	return c.writeDeployment(ctx, http.MethodPut, c.endpoint+deploymentsPath+"/"+id, tenantOverride, body)
}

// GetDeployment reads a definition (GET). A 404 returns ErrNotFound so callers
// can drop the resource from state.
func (c *Client) GetDeployment(ctx context.Context, tenantOverride, id string) (*Deployment, error) {
	req, err := c.newRequest(ctx, http.MethodGet, c.endpoint+deploymentsPath+"/"+id, tenantOverride, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("olivares: get deployment: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	return decodeDeployment(resp)
}

// DeleteDeployment removes a definition's desired-state record (DELETE). The
// engine refuses (409) while the deployment is still applied — it must be retired
// first; this is surfaced as an error. A 404 is treated as already-deleted.
func (c *Client) DeleteDeployment(ctx context.Context, tenantOverride, id string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, c.endpoint+deploymentsPath+"/"+id, tenantOverride, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("olivares: delete deployment: %w", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return errorFromResponse(resp)
}

// writeDeployment issues a create/update call and decodes the returned definition.
func (c *Client) writeDeployment(ctx context.Context, method, url, tenantOverride string, body []byte) (*Deployment, error) {
	req, err := c.newRequest(ctx, method, url, tenantOverride, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("olivares: write deployment: %w", err)
	}
	defer drainClose(resp.Body)
	return decodeDeployment(resp)
}

// decodeDeployment reads a 2xx Deployment body or maps a non-2xx error envelope.
func decodeDeployment(resp *http.Response) (*Deployment, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errorFromResponse(resp)
	}
	var d Deployment
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, fmt.Errorf("olivares: decode deployment (status %d): %w", resp.StatusCode, err)
	}
	return &d, nil
}
