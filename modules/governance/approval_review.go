// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/olivaresai/olivares/connectors/redact"
	"github.com/olivaresai/olivares/core/model"
)

// ApprovalReview is display-only: the reviewed tool and its proven masked facts.
// It is never decoded into execution input or authority. Old requests omit it.
type ApprovalReview struct {
	Tool string `json:"tool"`
	Text string `json:"text"`
}

func normalizeApprovalReview(review *ApprovalReview, masks []redact.GeneratedMaskSpan) (*ApprovalReview, error) {
	if review == nil {
		if len(masks) != 0 {
			return nil, errors.New("review mask provenance requires a review")
		}
		return nil, nil
	}
	if strings.TrimSpace(review.Tool) == "" || len(review.Tool) > maxMatchLen || !utf8.ValidString(review.Tool) || redact.Clean(review.Tool) != review.Tool {
		return nil, errors.New("invalid review tool")
	}
	if strings.TrimSpace(review.Text) == "" || len(review.Text) > maxApprovalReasonLen || !utf8.ValidString(review.Text) {
		return nil, errors.New("invalid or oversized review text")
	}
	text, err := redact.CleanMasked(review.Text, masks)
	if err != nil {
		return nil, err
	}
	if len(text) > maxApprovalReasonLen {
		return nil, errors.New("review text too long")
	}
	// Normalize a copy: caller-owned text and the original generated offsets
	// remain available for proving exactly what the reviewer will see.
	return &ApprovalReview{Tool: review.Tool, Text: text}, nil
}

func storedApprovalReview(rec model.Record) *ApprovalReview {
	text := rec.String(colApprovalReview)
	if text == "" {
		return nil
	}
	var review *ApprovalReview
	if json.Unmarshal([]byte(text), &review) != nil {
		return nil
	}
	return review
}
