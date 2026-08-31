// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openai

import (
	"encoding/json"
	"testing"
)

// TestVectorStoreFileCountsDecodesOpenAIWireSpelling pins the FOREIGN half of the
// discriminator documented on vsFileCounts: the Go field name is ours and was
// normalized to the US spelling, while the json tag carries OpenAI's own wire
// spelling and must not follow.
//
// The existing path DOES decode file_counts — assistants_test.go:42 serves
// testdata/vector_stores.json through it — but that fixture sets this counter to
// 0, the field's ZERO VALUE, so a broken tag and a correct one are
// indistinguishable there, and no test asserted the decoded value at all. A
// later misspell sweep "correcting" the TAG would therefore have compiled,
// stayed green, and silently zeroed the count on every real response. The
// NON-ZERO value below is the whole point.
func TestVectorStoreFileCountsDecodesOpenAIWireSpelling(t *testing.T) {
	// The fixture has to carry OpenAI's wire spelling verbatim — that spelling IS
	// the contract under test, so misspell is exempt over the payload.
	//nolint:misspell // OpenAI's wire field name, reproduced exactly on purpose.
	const payload = `{
		"id": "vs_abc",
		"name": "governed-corpus",
		"status": "completed",
		"file_counts": {
			"in_progress": 1,
			"completed": 7,
			"failed": 2,
			"cancelled": 3,
			"total": 13
		},
		"usage_bytes": 4096
	}`

	var got vectorStoreEntry
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	want := vsFileCounts{InProgress: 1, Completed: 7, Failed: 2, Canceled: 3, Total: 13}
	if got.FileCounts != want {
		t.Errorf("file_counts = %+v, want %+v", got.FileCounts, want)
	}

	// The named failure: a sweep that rewrites the tag to the US spelling leaves
	// this field at its zero value while everything else still decodes.
	if got.FileCounts.Canceled == 0 {
		t.Error("Canceled decoded as 0: the json tag no longer matches OpenAI's wire " +
			"field. That tag is their protocol, not our name — restore it and keep the " +
			"exemption on it (see the comment on vsFileCounts).")
	}
}
