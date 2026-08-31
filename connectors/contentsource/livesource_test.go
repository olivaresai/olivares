// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package contentsource_test

import (
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
)

func TestLiveSourceEmbedsSource(t *testing.T) {
	var _ contentsource.Source = (contentsource.LiveSource)(nil)
}

func TestChangeKindConstants(t *testing.T) {
	for _, ck := range []contentsource.ChangeKind{
		contentsource.ChangeContent, contentsource.ChangeACL,
		contentsource.ChangeMetadata, contentsource.ChangeDeleted,
	} {
		if ck == "" {
			t.Fatal("empty ChangeKind constant")
		}
	}
}

func TestDeltaPageSeparatesPaginationAndResumeTokens(t *testing.T) {
	page := contentsource.DeltaPage{
		NextToken:   "page-2",
		ResumeToken: "delta-2",
	}
	if page.NextToken == page.ResumeToken {
		t.Fatalf("NextToken and ResumeToken must be independently representable")
	}
}
