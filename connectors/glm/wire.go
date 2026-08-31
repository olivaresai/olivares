// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package glm

import (
	"errors"
	"strings"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// wire.go intentionally declares no /models response body: GLM's models-list schema
// is undocumented. Gather uses GET /models only as a liveness/entitlement probe with
// out=nil and discards the body.

func isUnavailable(err error) bool {
	var apiErr *modelprovider.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == 401 || apiErr.Status == 403
	}
	msg := err.Error()
	return strings.Contains(msg, "status 401") || strings.Contains(msg, "status 403")
}
