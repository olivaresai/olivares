// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package bedrockkb

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

type fakeDoer struct {
	fn func(req *http.Request) (int, any)
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	status, payload := f.fn(req)
	b, _ := json.Marshal(payload)
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(b)),
		Header:     make(http.Header),
	}, nil
}

type fakeSink struct {
	observations []model.Observation
}

func (s *fakeSink) Emit(_ context.Context, obs model.Observation) error {
	s.observations = append(s.observations, obs)
	return nil
}

func newTestSource(t *testing.T, kbID string, doerFn func(req *http.Request) (int, any)) *Source {
	t.Helper()
	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		cfgAccessKeyID:     "AKIATEST",
		cfgSecretAccessKey: "testsecret",
		cfgRegion:          "us-east-1",
		cfgKnowledgeBaseID: kbID,
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	s.SetDoer(&fakeDoer{fn: doerFn})
	return s
}

func TestGatherEmitsHealthAndEdgesOnSuccess(t *testing.T) {
	s := newTestSource(t, "kb-123", func(req *http.Request) (int, any) {
		if !strings.Contains(req.URL.Path, "/knowledgebases/kb-123/retrieve") {
			t.Fatalf("unexpected path: %s", req.URL.Path)
		}
		return http.StatusOK, map[string]any{
			"retrievalResults": []any{
				map[string]any{
					"content": map[string]any{"text": "chunk 1"},
					"location": map[string]any{
						"type":       "S3",
						"s3Location": map[string]any{"uri": "s3://bucket/doc1.pdf"},
					},
					"score": 0.95,
				},
				map[string]any{
					"content": map[string]any{"text": "chunk 2"},
					"location": map[string]any{
						"type":       "S3",
						"s3Location": map[string]any{"uri": "s3://bucket/doc2.pdf"},
					},
					"score": 0.82,
				},
				map[string]any{
					"content": map[string]any{"text": "chunk 3"},
					"location": map[string]any{
						"type":       "S3",
						"s3Location": map[string]any{"uri": "s3://bucket/doc1.pdf"},
					},
					"score": 0.70,
				},
			},
		}
	})

	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}

	// Expect: 1 knowledge_retrieval finding + 2 edge observations (2 unique sources).
	if len(sink.observations) != 3 {
		t.Fatalf("expected 3 observations, got %d: %+v", len(sink.observations), sink.observations)
	}

	finding, ok := sink.observations[0].(model.FindingReport)
	if !ok {
		t.Fatalf("first observation should be FindingReport, got %T", sink.observations[0])
	}
	if finding.Kind != "knowledge_retrieval" {
		t.Fatalf("finding kind = %q, want knowledge_retrieval", finding.Kind)
	}
	if !strings.Contains(finding.Title, "3 results") {
		t.Fatalf("finding title = %q, want mention of 3 results", finding.Title)
	}
	if !strings.Contains(finding.Title, "2 unique sources") {
		t.Fatalf("finding title = %q, want mention of 2 unique sources", finding.Title)
	}

	// Check edge observations.
	edge1, ok := sink.observations[1].(model.EdgeObservation)
	if !ok {
		t.Fatalf("second observation should be EdgeObservation, got %T", sink.observations[1])
	}
	if edge1.OriginRef != "kb-123" || edge1.ResourceKind != "knowledge_document" {
		t.Fatalf("edge1 = %+v, want origin=kb-123, resourceKind=knowledge_document", edge1)
	}
}

func TestGatherEmitsHealthFindingOnError(t *testing.T) {
	s := newTestSource(t, "kb-fail", func(req *http.Request) (int, any) {
		return http.StatusForbidden, map[string]any{"message": "access denied"}
	})

	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}

	if len(sink.observations) != 1 {
		t.Fatalf("expected 1 health finding, got %d", len(sink.observations))
	}
	finding, ok := sink.observations[0].(model.FindingReport)
	if !ok {
		t.Fatalf("observation should be FindingReport, got %T", sink.observations[0])
	}
	if finding.Kind != "health" {
		t.Fatalf("finding kind = %q, want health", finding.Kind)
	}
	if finding.Severity != model.SeverityMedium {
		t.Fatalf("finding severity = %v, want medium", finding.Severity)
	}
}

func TestOpenRejectsEmptyKBID(t *testing.T) {
	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		cfgAccessKeyID:     "AKIATEST",
		cfgSecretAccessKey: "testsecret",
	}}
	if err := s.Open(context.Background(), cfg); err == nil {
		t.Fatal("open should fail with empty knowledge_base_id")
	}
}

func TestOpenRejectsMissingCredentials(t *testing.T) {
	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		cfgKnowledgeBaseID: "kb-123",
	}}
	if err := s.Open(context.Background(), cfg); err == nil {
		t.Fatal("open should fail with missing credentials")
	}
}

func TestRetrieveSignsRequest(t *testing.T) {
	var authHeader string
	s := newTestSource(t, "kb-sign", func(req *http.Request) (int, any) {
		authHeader = req.Header.Get("Authorization")
		return http.StatusOK, map[string]any{"retrievalResults": []any{}}
	})

	sink := &fakeSink{}
	_ = s.Gather(context.Background(), sink)

	if !strings.HasPrefix(authHeader, "AWS4-HMAC-SHA256") {
		t.Fatalf("expected SigV4 Authorization header, got %q", authHeader)
	}
}

func TestGatherEmitsNoEdgesForEmptyResults(t *testing.T) {
	s := newTestSource(t, "kb-empty", func(req *http.Request) (int, any) {
		return http.StatusOK, map[string]any{"retrievalResults": []any{}}
	})

	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}

	// 1 finding (0 results, 0 sources), 0 edges.
	if len(sink.observations) != 1 {
		t.Fatalf("expected 1 observation (finding only), got %d", len(sink.observations))
	}
}
