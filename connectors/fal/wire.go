// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package fal

// wire.go holds the JSON wire shapes the fal.ai connector reads. Only the minimal-data
// fields the connector maps are declared — key inventory METADATA (never a key value),
// and the queue request STATUS/metrics needed to meter cost (never the generated media
// output). Two verification tiers (honest, per):
//
//   - VERIFIED queue API (queue.fal.run): the submit/poll surface is documented
//     ([docs.fal.ai/model-apis]); the status response carries status + metrics
//     (inference_time), which is the billable signal fal itself exposes.
//   - UNVERIFIED-OFFLINE key management (rest.fal.ai): fal key issuance/rotation is
//     primarily dashboard-driven and its REST shape is not fully public. The connector
//     models a plausible list shape, the path is operator-overridable, and a 403/404
//     degrades to an honest "sales-gated/UNVERIFIED" posture finding.

// keysListResponse is the API-key inventory list (the control point). The API returns
// only a masked partial, never the secret. Cursor pagination via has_more + last_id.
type keysListResponse struct {
	Data    []falKey `json:"data"`
	HasMore bool     `json:"has_more"`
	LastID  string   `json:"last_id"`
}

// falKey is one API key's inventory metadata. created_at is RFC3339; masked is the
// safe-to-display partial; scope/status are governance metadata. There is deliberately
// NO field that could carry the usable secret (docs/SECURITY-HARDENING.md).
type falKey struct {
	ID        string `json:"id"`
	Alias     string `json:"alias"`
	Scope     string `json:"scope"`
	Status    string `json:"status"`
	Masked    string `json:"masked"`
	CreatedAt string `json:"created_at"`
}

// queueStatus is the fal queue request status response. status is one of IN_QUEUE,
// IN_PROGRESS, COMPLETED; metrics carries inference_time (seconds) when COMPLETED — the
// billable compute signal the connector meters. The generated output is NEVER read
// (the connector reads the STATUS endpoint, not the result endpoint).
type queueStatus struct {
	Status        string        `json:"status"`
	RequestID     string        `json:"request_id"`
	QueuePosition int64         `json:"queue_position"`
	Metrics       *queueMetrics `json:"metrics"`
}

// queueMetrics carries the billable compute signal fal exposes on completion.
type queueMetrics struct {
	InferenceTime float64 `json:"inference_time"`
}
