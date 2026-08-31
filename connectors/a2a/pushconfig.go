// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// pushconfig.go is the client side of the A2A v1.0 push-notification configuration
// surface (spec §3.1.7–§3.1.10): Create/Get/List/Delete of TaskPushNotificationConfig
// on a remote agent. v1.0 RENAMED the v0.3 tasks/pushNotificationConfig/* methods to
// the PascalCase forms (List is plural "Configs") and FLATTENED the model — the v0.3
// nested PushNotificationConfig object no longer exists; TaskPushNotificationConfig
// carries url/token/authentication directly, and authentication is the singular
// {scheme, credentials} AuthenticationInfo (v0.3's schemes[] array is gone).
//
// This is what closes the loop with the PushReceiver (pushrecv.go): the same plane
// that mounts the webhook can now REGISTER it with a governed remote agent, so Task
// lifecycle updates flow back without polling.
//
// Governance posture (deny-closed, mirrors the rest of the Delegator surface):
// every operation verifies the signed AgentCard first, then requires the SIGNED
// capabilities to advertise pushNotifications (§3.3.4 — the client refuses before
// probing a surface the agent does not claim), then requires the agent on the
// operator allowlist (AllowedAgent — config lifecycle on an existing governed
// relationship, like CancelTask: no fresh ApprovalGate, nothing emits work). The
// webhook URL must be HTTPS (receiver-side SHOULD in §13.2; this plane's floor).
//
// MINIMAL DATA NOTE: TaskPushNotificationConfig.authentication.credentials is, BY
// SPEC DESIGN, a credential carried in the payload — it is what the REMOTE agent
// will present to OUR webhook (§4.3.3 Authorization header), not a caller secret.
// It is the one spec-mandated exception to "credentials never in an A2A payload";
// it is never logged, never audited, and never echoed back in PushConfig.

// PushConfigSpec describes one push-notification config to create on a remote
// agent. WebhookURL is REQUIRED (the receiver this plane mounts); Scheme/Credentials
// are what the remote will present to that webhook (e.g. "Bearer" + a single-purpose
// token — §13.2 SHOULD: unique per config, treated as a secret); Token is the A2A
// config-level correlation token; ConfigID optionally names the config (server
// assigns one otherwise).
type PushConfigSpec struct {
	AgentName   string
	AgentURL    string
	TaskID      string
	ConfigID    string
	WebhookURL  string
	Token       string
	Scheme      string
	Credentials string
}

// PushConfig is the minimal-data view of a TaskPushNotificationConfig returned by
// the remote agent: references only — the credentials member is deliberately NOT
// echoed back (it is a secret for the webhook channel, not catalog data).
type PushConfig struct {
	ID     string
	TaskID string
	URL    string
	Scheme string
}

// PushConfigPage is one page of ListTaskPushNotificationConfigs results.
type PushConfigPage struct {
	Configs       []PushConfig
	NextPageToken string
}

// wireAuthInfo is the v1.0 AuthenticationInfo wire shape (scheme REQUIRED).
type wireAuthInfo struct {
	Scheme      string `json:"scheme"`
	Credentials string `json:"credentials,omitempty"`
}

// wirePushConfig is the v1.0 TaskPushNotificationConfig wire shape (flat; url
// REQUIRED).
type wirePushConfig struct {
	Tenant         string        `json:"tenant,omitempty"`
	ID             string        `json:"id,omitempty"`
	TaskID         string        `json:"taskId,omitempty"`
	URL            string        `json:"url"`
	Token          string        `json:"token,omitempty"`
	Authentication *wireAuthInfo `json:"authentication,omitempty"`
}

// CreateTaskPushNotificationConfig registers a webhook config on a verified,
// allowlisted remote agent whose signed card advertises pushNotifications (the v1.0
// CreateTaskPushNotificationConfig method; JSON-RPC params are the
// TaskPushNotificationConfig object itself — the rpc takes the config directly,
// a2a.proto). The created config MUST persist server-side until task completion or
// explicit deletion (§3.1.7).
func (d *Delegator) CreateTaskPushNotificationConfig(ctx context.Context, spec PushConfigSpec) (PushConfig, error) {
	url := strings.TrimSpace(spec.WebhookURL)
	if url == "" {
		return PushConfig{}, fmt.Errorf("a2a: push config requires a webhook url")
	}
	if err := d.client.requireSecure(url); err != nil {
		return PushConfig{}, err
	}
	cfg := wirePushConfig{ID: spec.ConfigID, TaskID: spec.TaskID, URL: url, Token: spec.Token}
	if strings.TrimSpace(spec.Scheme) != "" {
		cfg.Authentication = &wireAuthInfo{Scheme: spec.Scheme, Credentials: spec.Credentials}
	}
	raw, err := d.callPushConfig(ctx, spec.AgentName, spec.AgentURL, methodCreatePushConfig, structToParams(cfg))
	if err != nil {
		return PushConfig{}, err
	}
	return decodePushConfig(raw)
}

// GetTaskPushNotificationConfig fetches one config by (taskId, configId) — both
// REQUIRED on the wire (GetTaskPushNotificationConfigRequest).
func (d *Delegator) GetTaskPushNotificationConfig(ctx context.Context, ref TaskRef, configID string) (PushConfig, error) {
	raw, err := d.callPushConfig(ctx, ref.AgentName, ref.AgentURL, methodGetPushConfig,
		map[string]any{"taskId": ref.TaskID, "id": configID})
	if err != nil {
		return PushConfig{}, err
	}
	return decodePushConfig(raw)
}

// ListTaskPushNotificationConfigs lists a task's active configs (the v1.0 method —
// note the plural "Configs", §9.4.7) with pageToken pagination.
func (d *Delegator) ListTaskPushNotificationConfigs(ctx context.Context, ref TaskRef, pageToken string, pageSize int) (PushConfigPage, error) {
	params := map[string]any{"taskId": ref.TaskID}
	if pageToken != "" {
		params["pageToken"] = pageToken
	}
	if pageSize > 0 {
		params["pageSize"] = pageSize
	}
	raw, err := d.callPushConfig(ctx, ref.AgentName, ref.AgentURL, methodListPushConfigs, params)
	if err != nil {
		return PushConfigPage{}, err
	}
	var lr struct {
		Configs       []json.RawMessage `json:"configs"`
		NextPageToken string            `json:"nextPageToken"`
	}
	if err := json.Unmarshal(raw, &lr); err != nil {
		return PushConfigPage{}, fmt.Errorf("a2a: decode %s result: %w", methodListPushConfigs, err)
	}
	page := PushConfigPage{NextPageToken: lr.NextPageToken}
	for _, item := range lr.Configs {
		if pc, err := decodePushConfig(item); err == nil {
			page.Configs = append(page.Configs, pc)
		}
	}
	return page, nil
}

// DeleteTaskPushNotificationConfig permanently removes one config (idempotent
// server-side, §3.1.10; the rpc returns google.protobuf.Empty).
func (d *Delegator) DeleteTaskPushNotificationConfig(ctx context.Context, ref TaskRef, configID string) error {
	_, err := d.callPushConfig(ctx, ref.AgentName, ref.AgentURL, methodDeletePushConfig,
		map[string]any{"taskId": ref.TaskID, "id": configID})
	return err
}

// callPushConfig is the shared gated path for the four config operations: verified
// card → signed pushNotifications capability → operator allowlist (AllowedAgent) —
// all deny-closed — then one JSON-RPC call (tenant echoed by callTaskGated).
func (d *Delegator) callPushConfig(ctx context.Context, agentName, agentURL, method string, params map[string]any) (json.RawMessage, error) {
	return d.callTaskGated(ctx, agentName, agentURL, method, params, func(card AgentCard) error {
		if err := requirePushNotifications(card, agentName); err != nil {
			return err
		}
		if !d.allowlist.AllowedAgent(agentName) {
			return &DenyError{Reason: "agent not on the delegation allowlist (push config)"}
		}
		return nil
	})
}

// decodePushConfig maps a TaskPushNotificationConfig result to the minimal-data
// view (credentials deliberately dropped).
func decodePushConfig(raw json.RawMessage) (PushConfig, error) {
	var pc struct {
		ID             string `json:"id"`
		TaskID         string `json:"taskId"`
		URL            string `json:"url"`
		Authentication *struct {
			Scheme string `json:"scheme"`
		} `json:"authentication"`
	}
	if err := json.Unmarshal(raw, &pc); err != nil {
		return PushConfig{}, fmt.Errorf("a2a: decode push config: %w", err)
	}
	out := PushConfig{ID: pc.ID, TaskID: pc.TaskID, URL: pc.URL}
	if pc.Authentication != nil {
		out.Scheme = pc.Authentication.Scheme
	}
	return out, nil
}

// structToParams marshals a wire struct into the map params shape callTaskGated
// expects (so tenant injection composes). Marshal of a local wire struct cannot
// fail; the decode is over our own bytes.
func structToParams(v any) map[string]any {
	raw, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{}
	}
	return m
}
