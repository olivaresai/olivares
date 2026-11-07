// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// stsTimeout bounds one token exchange.
const stsTimeout = 30 * time.Second

// stsCredentials are the temporary credentials an object store issues for a person.
type stsCredentials struct {
	AccessKeyID, SecretAccessKey, SessionToken string
}

// stsResponse is the part of an AssumeRoleWithWebIdentity answer that is read: AWS, MinIO
// and Ceph RGW all answer in this shape, so no vendor library is needed.
type stsResponse struct {
	Credentials struct {
		AccessKeyID     string `xml:"AccessKeyId"`
		SecretAccessKey string `xml:"SecretAccessKey"`
		SessionToken    string `xml:"SessionToken"`
	} `xml:"AssumeRoleWithWebIdentityResult>Credentials"`
	Error struct {
		Code string `xml:"Code"`
	} `xml:"Error"`
}

// stsRefused is the storage answering that it will not issue credentials for the token
// (expired, wrong audience, role not allowed): the person's to fix, not an outage.
type stsRefused struct{ code string }

func (e stsRefused) Error() string { return "the storage refused the identity token (" + e.code + ")" }

// assumeWebIdentity exchanges a person's identity token for temporary credentials at the
// storage's own token service (the AssumeRoleWithWebIdentity call of the S3 family). The
// token travels in the request body, never the URL; the request is not signed, as the
// call defines; a redirect is not followed, so the token reaches the origin it was
// stored for and no other. sessionName is what the storage's log shows for the person.
func assumeWebIdentity(ctx context.Context, endpoint, roleARN, token, sessionName string) (stsCredentials, error) {
	form := url.Values{"Action": {"AssumeRoleWithWebIdentity"}, "Version": {"2011-06-15"}, "WebIdentityToken": {token}, "RoleSessionName": {sessionName}}
	if roleARN != "" {
		form.Set("RoleArn", roleARN)
	}
	ctx, cancel := context.WithTimeout(ctx, stsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return stsCredentials{}, errors.New("the storage's token service address is not valid")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// cli-transport-exempt: ENGINE→the object store's own token service, not a CLI-to-control-plane
	// call. The person's identity token goes to the origin it was stored for; the operator's
	// --ca-cert, --pin-sha256 and control-plane bearer answer another trust question and must
	// not ride along. The hardening this path needs is here: no redirect is followed, the
	// exchange is bounded by stsTimeout and the answer by a 1 MiB read.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err // keep the cause short: the request carries no secret in its URL
		}
		return stsCredentials{}, fmt.Errorf("the storage's token service could not be reached: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return stsCredentials{}, fmt.Errorf("the storage's token service did not finish its answer: %w", err)
	}
	var parsed stsResponse
	_ = xml.Unmarshal(body, &parsed) // an unreadable body is judged by the status below
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		// Any other answer (a missing token service, throttling) is not the person's token to fix.
		code := strings.TrimSpace(parsed.Error.Code)
		if code == "" {
			code = http.StatusText(resp.StatusCode)
		}
		return stsCredentials{}, stsRefused{code: code}
	}
	creds := stsCredentials{parsed.Credentials.AccessKeyID, parsed.Credentials.SecretAccessKey, parsed.Credentials.SessionToken}
	// A session token is always part of temporary credentials: without one the connector
	// would fall back to the engine's own AWS environment for it.
	if resp.StatusCode != http.StatusOK || creds.AccessKeyID == "" || creds.SecretAccessKey == "" || creds.SessionToken == "" {
		return stsCredentials{}, fmt.Errorf("the storage's token service answered %d without temporary credentials", resp.StatusCode)
	}
	return creds, nil
}
