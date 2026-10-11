// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package audit_test

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
)

func TestCommunityAuditExportUnavailable(t *testing.T) {
	ev := model.AuditEvent{Seq: 1, Hash: make([]byte, 32), PrevHash: make([]byte, 32), MetaCommitment: make([]byte, 32)}
	for _, f := range audit.Formats() {
		if text, err := audit.FormatEvent(ev, f); err == nil || !strings.Contains(err.Error(), "Business") || text != "" {
			t.Errorf("format %s = (%q, %v), want Business refusal", f, text, err)
		}
	}
	dir := t.TempDir()
	if sink, err := audit.NewDirSink(dir); err == nil || !strings.Contains(err.Error(), "Business") || sink != nil {
		t.Errorf("directory sink = (%v, %v), want Business refusal", sink, err)
	}
	if rep, err := audit.VerifyArchiveDir(context.Background(), dir, audit.ArchiveVerifyOptions{}); err == nil || !strings.Contains(err.Error(), "Business") || rep.OK {
		t.Errorf("archive verify = (%+v, %v), want Business refusal", rep, err)
	}
}
