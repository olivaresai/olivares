// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const evidenceMetricName = "git.host.evidence"

// handleGitHubEvidence maps a check or workflow delivery onto one metric
// sample. A payload that is not JSON is 400. A delivery with no repository,
// head, name, or status is accepted and ignored.
func (s *Source) handleGitHubEvidence(r *http.Request, w http.ResponseWriter, event string, body []byte, sink sdk.Sink) {
	sample, ok, err := parseGitHubEvidence(event, body)
	if err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if ok {
		if err := sink.Emit(r.Context(), sample); err != nil {
			http.Error(w, "emit error", http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

func parseGitHubEvidence(event string, body []byte) (model.MetricSample, bool, error) {
	var ev struct {
		Repository repoRef `json:"repository"`
		CheckRun   *struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HeadSHA    string `json:"head_sha"`
		} `json:"check_run"`
		CheckSuite *struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HeadSHA    string `json:"head_sha"`
			App        struct {
				Name string `json:"name"`
			} `json:"app"`
		} `json:"check_suite"`
		WorkflowRun *struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HeadSHA    string `json:"head_sha"`
		} `json:"workflow_run"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		return model.MetricSample{}, false, err
	}
	var name, status, conclusion, head string
	switch event {
	case "check_run":
		if ev.CheckRun == nil {
			return model.MetricSample{}, false, nil
		}
		name = ev.CheckRun.Name
		status = ev.CheckRun.Status
		conclusion = ev.CheckRun.Conclusion
		head = ev.CheckRun.HeadSHA
	case "check_suite":
		if ev.CheckSuite == nil {
			return model.MetricSample{}, false, nil
		}
		name = ev.CheckSuite.App.Name
		if name == "" {
			name = "check_suite"
		}
		status = ev.CheckSuite.Status
		conclusion = ev.CheckSuite.Conclusion
		head = ev.CheckSuite.HeadSHA
	case "workflow_run":
		if ev.WorkflowRun == nil {
			return model.MetricSample{}, false, nil
		}
		name = ev.WorkflowRun.Name
		status = ev.WorkflowRun.Status
		conclusion = ev.WorkflowRun.Conclusion
		head = ev.WorkflowRun.HeadSHA
	default:
		return model.MetricSample{}, false, nil
	}
	if ev.Repository.FullName == "" || head == "" || name == "" || status == "" {
		return model.MetricSample{}, false, nil
	}
	return evidenceSample(ev.Repository.FullName, head, name, status, conclusion, event), true, nil
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
