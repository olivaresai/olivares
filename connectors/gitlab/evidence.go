// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"encoding/json"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

const evidenceMetricName = "git.host.evidence"

func parseGitLabEvidence(event string, body []byte) (model.MetricSample, bool, error) {
	switch event {
	case "Pipeline Hook":
		var ev struct {
			ObjectAttributes struct {
				SHA    string `json:"sha"`
				Status string `json:"status"`
			} `json:"object_attributes"`
			Project projectRef `json:"project"`
		}
		if err := json.Unmarshal(body, &ev); err != nil {
			return model.MetricSample{}, false, err
		}
		if ev.Project.PathWithNamespace == "" || ev.ObjectAttributes.SHA == "" || ev.ObjectAttributes.Status == "" {
			return model.MetricSample{}, false, nil
		}
		return evidenceSample(ev.Project.PathWithNamespace, ev.ObjectAttributes.SHA, "pipeline", ev.ObjectAttributes.Status, gitlabConclusion(ev.ObjectAttributes.Status), "pipeline"), true, nil
	case "Job Hook":
		var ev struct {
			SHA         string     `json:"sha"`
			BuildName   string     `json:"build_name"`
			BuildStatus string     `json:"build_status"`
			Project     projectRef `json:"project"`
		}
		if err := json.Unmarshal(body, &ev); err != nil {
			return model.MetricSample{}, false, err
		}
		if ev.Project.PathWithNamespace == "" || ev.SHA == "" || ev.BuildName == "" || ev.BuildStatus == "" {
			return model.MetricSample{}, false, nil
		}
		return evidenceSample(ev.Project.PathWithNamespace, ev.SHA, ev.BuildName, ev.BuildStatus, gitlabConclusion(ev.BuildStatus), "job"), true, nil
	default:
		return model.MetricSample{}, false, nil
	}
}

func gitlabConclusion(status string) string {
	switch status {
	case "success", "failed", "canceled", "skipped":
		return status
	default:
		return ""
	}
}

func evidenceSample(repo, head, name, status, conclusion, event string) model.MetricSample {
	return model.MetricSample{
		Name:        evidenceMetricName,
		Value:       1,
		Unit:        "1",
		SubjectKind: "git.repository",
		SubjectRef:  repo,
		OccurredAt:  time.Now().UTC(),
		Dimensions: map[string]string{
			"repository": repo,
			"head_sha":   head,
			"name":       name,
			"status":     status,
			"conclusion": conclusion,
			"event":      event,
		},
	}
}
