// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudebatch

// JSON wire shapes of the Anthropic Batch and Files API responses the connector
// reads. Only the minimal-data fields the connector needs are mapped: identifiers,
// status, counts, timestamps — never request payloads, file content, or secrets
// (docs/SECURITY-HARDENING.md). The full upstream payload may carry more fields; they are ignored.

// batchListResponse is GET /v1/messages/batches. Cursor pagination via
// first_id/last_id + has_more.
type batchListResponse struct {
	Data    []batchEntry `json:"data"`
	HasMore bool         `json:"has_more"`
	FirstID string       `json:"first_id"`
	LastID  string       `json:"last_id"`
}

// batchEntry is one message batch's inventory metadata. The connector reads only
// the governance-relevant fields: identity, status, model, request counts and
// lifecycle timestamps. It never reads the request payloads (results_url content).
type batchEntry struct {
	ID                string        `json:"id"`
	Type              string        `json:"type"`
	ProcessingStatus  string        `json:"processing_status"`
	RequestCounts     requestCounts `json:"request_counts"`
	CreatedAt         string        `json:"created_at"`
	EndedAt           string        `json:"ended_at"`
	ExpiresAt         string        `json:"expires_at"`
	CancelInitiatedAt string        `json:"cancel_initiated_at"`
	ResultsURL        string        `json:"results_url"`
}

// requestCounts is the per-status breakdown of requests in a batch.
type requestCounts struct {
	Processing int64 `json:"processing"`
	Succeeded  int64 `json:"succeeded"`
	Errored    int64 `json:"errored"`
	Canceled   int64 `json:"canceled"`
	Expired    int64 `json:"expired"`
}

// totalLines returns the total number of request lines in the batch (all statuses).
func (r requestCounts) totalLines() int64 {
	return r.Processing + r.Succeeded + r.Errored + r.Canceled + r.Expired
}

// fileListResponse is GET /v1/files. Cursor pagination via first_id/last_id +
// has_more.
type fileListResponse struct {
	Data    []fileEntry `json:"data"`
	HasMore bool        `json:"has_more"`
	FirstID string      `json:"first_id"`
	LastID  string      `json:"last_id"`
}

// fileEntry is one file's inventory metadata. The connector reads identity, size,
// purpose and timestamps — never the file content (docs/SECURITY-HARDENING.md).
type fileEntry struct {
	ID           string `json:"id"`
	Filename     string `json:"filename"`
	MIMEType     string `json:"mime_type"`
	SizeBytes    int64  `json:"size_bytes"`
	Purpose      string `json:"purpose"`
	Downloadable bool   `json:"downloadable"`
	CreatedAt    string `json:"created_at"`
}
