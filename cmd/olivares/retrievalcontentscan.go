// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"strings"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/modules/knowledge"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// coreRetrievalScanner is the OPEN (AGPL) scanner: it runs the textscan
// injection markers and the deny-closed HIGH-severity posture on each retrieved
// chunk before it reaches the caller. The ENTERPRISE depth (the three
// deterministic detectors) is injected via the optional deepScanner; when nil,
// only the core markers run.
//
// Honesty: verified-deployed at the retrieval return point, never "impossible
// to bypass". The heuristics raise the cost of an attack and leave evidence;
// they do not prove the content is safe (Contract).
type coreRetrievalScanner struct {
	deepScanner knowledge.RetrievalContentScanner // enterprise depth; nil in community
}

func (s *coreRetrievalScanner) ScanChunk(ctx context.Context, text, sourceKind, sourceRef string) knowledge.RetrievalScanVerdict {
	text = strings.TrimSpace(text)
	if text == "" {
		return knowledge.RetrievalScanVerdict{}
	}

	// Core: textscan injection markers — HIGH severity → deny-closed block.
	markers := mcpc.ScanRetrievedInjection(text)
	var highMarkers []string
	for _, m := range markers {
		if mcpc.RetrievedMarkerSeverity(m).AtLeast(sdkmodel.SeverityHigh) {
			highMarkers = append(highMarkers, m)
		}
	}
	if len(highMarkers) > 0 {
		return knowledge.RetrievalScanVerdict{
			Blocked: true, Markers: highMarkers,
			Reason: "injection markers detected in retrieved content (deny-closed)",
		}
	}

	// Enterprise depth: delegate to the deep scanner when wired.
	if s.deepScanner != nil {
		return s.deepScanner.ScanChunk(ctx, text, sourceKind, sourceRef)
	}

	// Advisory: LOW/MEDIUM markers are findings, not blocks (precedent).
	if len(markers) > 0 {
		return knowledge.RetrievalScanVerdict{
			Blocked: false, Markers: markers,
			Reason: "low/medium injection markers (advisory, not blocked)",
		}
	}
	return knowledge.RetrievalScanVerdict{}
}
