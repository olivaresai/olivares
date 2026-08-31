// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"fmt"
)

// DurableTaskPeerReport is the bounded, content-free projection of one
// strictly validated GetTaskResult. Result payloads and peer metadata never
// cross this adapter seam; Digest commits to the complete canonical report.
type DurableTaskPeerReport struct {
	Status         string
	StatusReason   string
	ResultDigest   string
	TTLMs          *int64
	PollIntervalMs *int64
	Terminal       bool
	InputRequests  []DurableTaskInputRef
}

// BuildDurableTaskGetParams creates the exact Tasks-extension params used by
// the connector's own reconciliation path. The extension is implemented only
// for the stateless 2026-07-28 protocol revision; older wire shapes are not
// inferred.
func BuildDurableTaskGetParams(taskID, protocolVersion string) ([]byte, error) {
	if err := validateTaskID(taskID); err != nil {
		return nil, fmt.Errorf("mcp: durable tasks/get params: %w", err)
	}
	if protocolVersion != revision20260728 {
		return nil, fmt.Errorf(
			"mcp: durable tasks/get requires protocol revision %s", revision20260728,
		)
	}
	params, err := taskRequestParams(tasksRequestMeta(protocolVersion), struct {
		TaskID string `json:"taskId"`
	}{TaskID: taskID})
	if err != nil {
		return nil, err
	}
	return json.Marshal(params)
}

// ParseDurableTaskGetResult reuses the connector's single strict
// GetTaskResult validator. Callers must not persist err.Error(): malformed peer
// bodies may contain peer-controlled member names. DurableTaskResultDefectClass
// exposes the stable, payload-free classification when one is needed.
func ParseDurableTaskGetResult(taskID string, raw json.RawMessage) (DurableTaskPeerReport, error) {
	report, err := strictGetTaskResult(taskID, raw)
	if err != nil {
		return DurableTaskPeerReport{}, err
	}
	return DurableTaskPeerReport{
		Status: report.Status, StatusReason: report.Reason,
		ResultDigest: report.Digest, TTLMs: cloneInt64(report.TTLMs),
		PollIntervalMs: cloneInt64(report.PollIntervalMs),
		Terminal:       taskStatusTerminal(report.Status),
		InputRequests:  cloneDurableTaskInputRefs(report.InputRequests),
	}, nil
}

// DurableTaskResultDefectClass returns the connector-owned stable class for a
// strict peer-result failure. It never includes peer-controlled text.
func DurableTaskResultDefectClass(err error) string {
	return taskDefectClass(err)
}
