// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeconsole

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func fixedClock() time.Time { return time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC) }

// routeDoer answers requests from a handler keyed by path, recording every request
// so a test can assert the connector is GET-only.
type routeDoer struct {
	t       *testing.T
	reqs    []*http.Request
	handler func(*http.Request) (int, string)
}

func (d *routeDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	st, body := d.handler(req)
	return &http.Response{StatusCode: st, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

type captureSink struct{ obs []model.Observation }

func (s *captureSink) Emit(_ context.Context, o model.Observation) error {
	s.obs = append(s.obs, o)
	return nil
}

func TestGatherEmitsPostureFindingEvenOffline(t *testing.T) {
	s := New()
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{}); err != nil { // no admin_key => offline
		t.Fatal(err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	// Two structural posture findings: the SSO/SCIM blind spot + the model-access gap.
	if len(sink.obs) != 2 {
		t.Fatalf("emitted %d observations, want 2", len(sink.obs))
	}
	f, ok := sink.obs[0].(model.FindingReport)
	if !ok {
		t.Fatalf("emitted %T, want FindingReport", sink.obs[0])
	}
	if f.Kind != "iam_posture" || f.Severity != model.SeverityMedium || f.SubjectKind != "claude_org" {
		t.Errorf("finding = %+v", f)
	}
	// Recalibrated wording: still names the SSO + SCIM blind spot, and now records that
	// groups + custom roles ARE listable via the ce-user-management beta.
	if f.DetailHash == "" || !strings.Contains(f.Title, "SSO") || !strings.Contains(f.Title, "SCIM") {
		t.Errorf("finding title not the honest blind-spot statement: %q", f.Title)
	}
	if !strings.Contains(f.Title, "groups") || !strings.Contains(f.Title, "custom roles") {
		t.Errorf("blind-spot finding not recalibrated for the RBAC beta: %q", f.Title)
	}
	// Second finding: the model-access (entitlements) Console-only gap (E2).
	gap, ok := sink.obs[1].(model.FindingReport)
	if !ok {
		t.Fatalf("second obs %T, want FindingReport", sink.obs[1])
	}
	if !strings.Contains(gap.Title, "model access") || !strings.Contains(gap.Title, "Console-only") {
		t.Errorf("model-access gap finding not emitted: %q", gap.Title)
	}
}
