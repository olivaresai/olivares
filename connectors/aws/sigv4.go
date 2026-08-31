// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"net/http"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/awssig"
)

// The AWS SigV4 signing algorithm now lives in the shared internal/awssig package
// (used by both this audit connector and the cloudqueue messaging connector —).
// This file keeps the connector's existing internal surface (awsCreds, sign) as a
// thin shim over it, so the rest of the package and its tests are unchanged.

// awsCreds is the connector's credential triple. token is the optional STS session
// token. Values live only in memory and are NEVER logged or emitted.
type awsCreds struct {
	akid   string
	secret string
	token  string
}

// sign signs req in place with AWS Signature Version 4 (delegates to awssig). It
// mutates only the signing headers, never the URL or body, so it cannot turn a read
// into a write.
func sign(req *http.Request, body []byte, service, region string, creds awsCreds, t time.Time) {
	awssig.Sign(req, body, service, region,
		awssig.Creds{AKID: creds.akid, Secret: creds.secret, Token: creds.token}, t)
}
