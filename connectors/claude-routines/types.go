// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package clauderoutines

// trigger is the API response type for a single Claude Code Routine (scheduled
// trigger). The Prompt field is NEVER stored or logged — only hashed for
// posture fingerprinting.
type trigger struct {
	ID                  string `json:"id"` // trig_*
	Name                string `json:"name"`
	CronExpression      string `json:"cron_expression,omitempty"`
	RunOnceAt           string `json:"run_once_at,omitempty"`
	Enabled             bool   `json:"enabled"`
	EndedReason         string `json:"ended_reason,omitempty"`
	NextRunAt           string `json:"next_run_at,omitempty"`
	CreatedAt           string `json:"created_at"`
	PersistentSessionID string `json:"persistent_session_id,omitempty"`
	Prompt              string `json:"prompt"` // NEVER stored — only hashed
	CreateNewSession    bool   `json:"create_new_session_on_fire,omitempty"`
}

// listTriggersResponse is the paginated list envelope returned by the triggers
// API.
type listTriggersResponse struct {
	Triggers   []trigger `json:"triggers"`
	NextCursor string    `json:"next_cursor,omitempty"`
}
