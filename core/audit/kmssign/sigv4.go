// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package kmssign

import (
	"net/http"
	"time"

	"github.com/olivaresai/olivares/core/internal/sigv4"
)

// AWSCreds is the minimal credential triple a SigV4 signer needs. Token is the
// optional STS session token (assumed-role / IRSA). These live only in memory and
// are NEVER logged or emitted (docs/SECURITY-HARDENING.md). It aliases the shared core signer
// (core/internal/sigv4) so the custody KEK wrapper (core/secure/kmswrap)
// and this ledger signer sign requests with the SAME audited implementation.
type AWSCreds = sigv4.Credentials

// signSigV4 signs req in place with AWS Signature Version 4 for the given
// service and region. It mutates only the Authorization, X-Amz-Date and
// X-Amz-Security-Token headers — never the URL or body — so it cannot turn a
// read into a write.
func signSigV4(req *http.Request, body []byte, service, region string, creds AWSCreds, t time.Time) {
	sigv4.Sign(req, body, service, region, creds, t)
}
