// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import (
	"bytes"
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

func TestPersistFailureStoresHashedDetailAndMinimalSubjectRef(t *testing.T) {
	h := newHarness(t, fakeSandbox{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	m := New(WithClock(fixedClock{now: now}))

	detail := "probe-id|complied|raw response fragment"
	sum := sha256.Sum256([]byte(detail))
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := m.persistFailure(context.Background(), sc, sdkmodel.SeverityCritical, familyInjection, "external-agent",
			"Red-team failure", detail)
		return err
	}); err != nil {
		t.Fatalf("persistFailure: %v", err)
	}
	findings := h.coreFindings(tenant, findingKindRedteam)
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(findings))
	}
	f := findings[0]
	if f.SubjectID != "" {
		t.Fatalf("SubjectID = %q, want zero for external ref", f.SubjectID)
	}
	if got := f.Metadata["subject_ref"]; got != "external-agent" {
		t.Fatalf("subject_ref = %v, want external-agent", got)
	}
	if !bytes.Equal(f.DetailHash, sum[:]) {
		t.Fatalf("DetailHash = %x, want sha256(detail)", f.DetailHash)
	}
	if f.OccurredAt.Time() != now {
		t.Fatalf("OccurredAt = %s, want %s", f.OccurredAt.Time(), now)
	}
}

func TestParseIDOrZero(t *testing.T) {
	id := model.NewID()
	if got := parseIDOrZero(id.String()); got != id {
		t.Fatalf("parseIDOrZero(valid) = %q, want %q", got, id)
	}
	if got := parseIDOrZero("agent-ref"); got != "" {
		t.Fatalf("parseIDOrZero(external) = %q, want zero", got)
	}
}

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() model.Timestamp {
	return model.NewTimestamp(c.now)
}
