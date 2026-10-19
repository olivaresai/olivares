//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import "github.com/olivaresai/olivares/core/api"

// The approval review-quality report (approval rate, time to decision and
// rubber-stamp flags per approver and per action class) is a Business
// capability. Community keeps the published route and answers 501.
func (m *Module) registerApprovalReviewQualityRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/approvals/review-quality", permApprovalRead, businessRequired)
}
