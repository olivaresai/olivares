// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package siemforward

import "github.com/olivaresai/olivares/modules/eventing"

// Community has no SIEM renderer; Forward and ForwardDue answer the Business
// error (audit_export_community.go) and leave the forward cursor as stored.
func NewRenderer() eventing.SinkRenderer { return nil }
