// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

// cliOrgRow is the shared organization returned by first-boot setup.
type cliOrgRow struct {
	ID         string `json:"id"`
	TenantID   string `json:"tenant_id"`
	Name       string `json:"name"`
	Slug       string `json:"slug"`
	Status     string `json:"status"`
	DataRegion string `json:"data_region,omitempty"`
	CreatedAt  string `json:"created_at"`
}
