// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"context"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// configSelfHosted is the environment config.type for a self-hosted sandbox (the only kind
// with an outbound-polling work queue worth observing).
const configSelfHosted = "self_hosted"

// Environment is a CMA execution environment (env_...). A self_hosted environment acts as
// an outbound-polling work queue: sessions assigned to it are enqueued as work items that an
// operator-run worker claims. The connector inventories the environment and OBSERVES its
// queue; it is never a worker (it never claims/acks/heartbeats/executes work).
type Environment struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Config struct {
		Type string `json:"type"`
	} `json:"config"`
	ArchivedAt string `json:"archived_at"`
	CreatedAt  string `json:"created_at"`
}

func (e Environment) selfHosted() bool { return e.Config.Type == configSelfHosted }

// WorkQueueStats is the queue state for a self-hosted environment (GET .../work/stats).
type WorkQueueStats struct {
	Depth          int    `json:"depth"`            // items waiting to be claimed
	Pending        int    `json:"pending"`          // claimed but not acknowledged
	OldestQueuedAt string `json:"oldest_queued_at"` // RFC3339 or empty
	WorkersPolling int    `json:"workers_polling"`  // workers seen polling in the last 30s
	Type           string `json:"type"`
}

// WorkItem is one unit of work in a self-hosted environment (work_...), wrapping a session.
type WorkItem struct {
	ID   string `json:"id"`
	Data struct {
		ID   string `json:"id"` // the session id
		Type string `json:"type"`
	} `json:"data"`
	EnvironmentID   string `json:"environment_id"`
	State           string `json:"state"` // queued|starting|active|stopping|stopped
	LatestHeartbeat string `json:"latest_heartbeat_at"`
	CreatedAt       string `json:"created_at"`
}

type environmentPage struct {
	Data    []Environment `json:"data"`
	HasMore bool          `json:"has_more"`
	LastID  string        `json:"last_id"`
}

type workListPage struct {
	Data     []WorkItem `json:"data"`
	NextPage string     `json:"next_page"`
}

// fetchEnvironments lists the workspace's environments.
func (c *client) fetchEnvironments(ctx context.Context) ([]Environment, error) {
	var out []Environment
	after := ""
	for i := 0; i < c.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		var page environmentPage
		if err := c.getJSON(ctx, "/v1/environments", listQuery("after_id", after), &page); err != nil {
			return out, err
		}
		out = append(out, page.Data...)
		if !page.HasMore || page.LastID == "" {
			break
		}
		after = page.LastID
	}
	return out, nil
}

// fetchWorkStats reads a self-hosted environment's queue statistics (org-API-key call; the
// worker-only poll/ack/heartbeat endpoints are never touched).
func (c *client) fetchWorkStats(ctx context.Context, envID string) (WorkQueueStats, error) {
	var stats WorkQueueStats
	err := c.getJSON(ctx, "/v1/environments/"+envID+"/work/stats", nil, &stats)
	return stats, err
}

// fetchWorkItems lists a self-hosted environment's current work items (a single bounded
// page, newest first), for the environment→session topology.
func (c *client) fetchWorkItems(ctx context.Context, envID string) ([]WorkItem, error) {
	var page workListPage
	if err := c.getJSON(ctx, "/v1/environments/"+envID+"/work", listQuery("", ""), &page); err != nil {
		return nil, err
	}
	return page.Data, nil
}

// environmentEdge places an environment under its workspace in the access map.
func environmentEdge(e Environment, workspaceRef string, at time.Time) model.EdgeObservation {
	return environmentEdgeByID(e.ID, workspaceRef, at)
}

// environmentEdgeByID is environmentEdge from a bare environment id — used for
// operator-pinned environment_ids, which skip discovery (fix: pinned environments
// previously produced stats/work edges but never appeared in the inventory themselves).
func environmentEdgeByID(envID, workspaceRef string, at time.Time) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind:   originWorkspace,
		OriginRef:    labelRef(workspaceRef, "workspace"),
		ResourceKind: kindEnvironment,
		ResourceRef:  redact.Clean(envID),
		Mode:         model.ModeRead,
		Source:       model.SignalCMA,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   at,
	}
}

// workItemEdge maps a work item to the environment→session execution edge (the env runs the
// session's tools). ok is false when the item names no session.
func workItemEdge(item WorkItem, at time.Time) (model.EdgeObservation, bool) {
	if item.Data.ID == "" {
		return model.EdgeObservation{}, false
	}
	return model.EdgeObservation{
		OriginKind:   originEnvironment,
		OriginRef:    redact.Clean(item.EnvironmentID),
		ResourceKind: kindManagedAgent,
		ResourceRef:  redact.Clean(item.Data.ID),
		Mode:         model.ModeReadWrite,
		Source:       model.SignalCMA,
		Confidence:   model.ConfidenceAttributed,
		ToolRef:      redact.Clean(item.ID),
		ObservedAt:   at,
	}, true
}

// workQueueFinding raises a backlog posture finding when a self-hosted queue has work
// waiting at or above the operator threshold. With NO workers polling the backlog is a
// silent starvation (HIGH: queued sessions never run); with workers present it is a
// capacity heads-up (LOW). ok is false when the queue is below threshold (no finding).
func workQueueFinding(envID string, stats WorkQueueStats, threshold int, at time.Time) (model.FindingReport, bool) {
	if stats.Depth < threshold {
		return model.FindingReport{}, false
	}
	sev := model.SeverityLow
	title := "CMA self-hosted work queue has a backlog"
	if stats.WorkersPolling == 0 {
		sev = model.SeverityHigh
		title = "CMA self-hosted work queue backlog with NO workers polling (sessions are starving)"
	}
	return model.FindingReport{
		Kind:        findingPosture,
		Severity:    sev,
		SubjectKind: kindEnvironment,
		SubjectRef:  redact.Clean(envID),
		Title:       title,
		DetailHash:  redact.Hash(fmt.Sprintf("work_queue env=%s depth=%d pending=%d workers_polling=%d oldest=%s; a self-hosted queue with no connected worker silently starves sessions (CMA self-hosted sandboxes)", envID, stats.Depth, stats.Pending, stats.WorkersPolling, stats.OldestQueuedAt)),
		OccurredAt:  at,
	}, true
}
