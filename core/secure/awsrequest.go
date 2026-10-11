// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secure

import (
	"github.com/olivaresai/olivares/core/internal/sigv4"
	"net/http"
	"time"
)

type AWSCredentials = sigv4.Credentials

func SignAWSRequest(req *http.Request, body []byte, service, region string, creds AWSCredentials, at time.Time) {
	sigv4.Sign(req, body, service, region, creds, at)
}
