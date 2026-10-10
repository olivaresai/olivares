// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package siemforward

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestCommunityAuditForwardUnavailable(t *testing.T) {
	m := New(nil)
	if err := m.Forward(context.Background(), model.AuditEvent{}); err == nil || !strings.Contains(err.Error(), "Business") {
		t.Errorf("Forward = %v", err)
	}
	if n, err := m.ForwardDue(context.Background(), model.SystemTenantID); n != 0 || err == nil || !strings.Contains(err.Error(), "Business") {
		t.Errorf("ForwardDue = %d %v", n, err)
	}
	if NewRenderer() != nil {
		t.Error("Community has a SIEM renderer")
	}
}
