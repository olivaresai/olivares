//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/olivaresai/olivares/cmd/olivares/seed"
)

// editionGraphReady waits for the access graph to carry the archive grant: every EDGE of the
// seed batch has ingested.
func (h *harness) editionGraphReady(tenant string) error {
	code, raw := h.req("GET", "/v1/m/accessmap/graph?limit=200", h.adminToken, tenant, nil)
	if code != http.StatusOK {
		return fmt.Errorf("graph = %d: %s", code, raw)
	}
	var g struct {
		Edges []map[string]any `json:"edges"`
	}
	_ = json.Unmarshal(raw, &g)
	for _, e := range g.Edges {
		if e["resource_ref"] == seed.ResArchive {
			return nil
		}
	}
	return fmt.Errorf("archive grant not yet ingested (%d edges)", len(g.Edges))
}

func editionDemoGraphStatus() int { return http.StatusOK }
