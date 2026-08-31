// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"bytes"
	"io"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/awssig"
	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// sigV4Doer signs each outbound request with AWS Signature Version 4 before delegating
// to an inner transport. It is the forward transport for the Claude-Platform-on-AWS
// surface (service aws-external-anthropic), where the inference plane authenticates with
// SigV4 + IAM rather than an x-api-key (surfaces.go). It reads the request body to
// compute the SigV4 payload hash and restores it so the inner client re-sends it intact;
// it strips any x-api-key header (that surface does not use one) so an empty key never
// rides along. Credentials live only in memory and are never logged (docs/SECURITY-HARDENING.md).
type sigV4Doer struct {
	inner   modelprovider.Doer
	creds   awssig.Creds
	service string
	region  string
	now     func() time.Time
}

// NewSigV4Doer returns a Doer that SigV4-signs every request for (service, region) with
// the given credentials, then delegates to inner (nil ⇒ http.DefaultClient). akid/secret
// are the AWS access key id / secret; token is the optional STS session token. It is the
// seam the composition root wires for a Claude-Platform-on-AWS proxy — the AGPL
// side passes plain strings so it need not import this connector's internal signer.
func NewSigV4Doer(inner modelprovider.Doer, akid, secret, token, service, region string, now func() time.Time) modelprovider.Doer {
	if now == nil {
		now = time.Now
	}
	return &sigV4Doer{
		inner:   inner,
		creds:   awssig.Creds{AKID: akid, Secret: secret, Token: token},
		service: service,
		region:  region,
		now:     now,
	}
}

func (d *sigV4Doer) Do(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		body = b
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	}
	// This surface authenticates with SigV4, not an Anthropic key: drop any x-api-key so
	// an empty/foreign key header never travels with the signed request.
	req.Header.Del("x-api-key")
	awssig.Sign(req, body, d.service, d.region, d.creds, d.now())
	inner := d.inner
	if inner == nil {
		inner = http.DefaultClient
	}
	return inner.Do(req)
}
