// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package bedrock

import (
	"net/http"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/awssig"
)

// awsCreds is the connector's credential triple. token is the optional STS session
// token. Values live only in memory and are NEVER logged or emitted.
type awsCreds struct {
	akid   string
	secret string
	token  string
}

// sign signs req in place with AWS Signature Version 4 (delegates to the shared
// internal/awssig signer, the same one the aws audit and cloudqueue connectors use).
// It mutates only the signing headers, never the URL or body, so it cannot turn a read
// into a write.
func sign(req *http.Request, body []byte, service, region string, creds awsCreds, t time.Time) {
	awssig.Sign(req, body, service, region,
		awssig.Creds{AKID: creds.akid, Secret: creds.secret, Token: creds.token}, t)
}
