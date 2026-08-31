// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package secretref

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/awssig"
	"github.com/olivaresai/olivares/connectors/internal/httpx"
)

// awsReader resolves `aws-secretsmanager:<SecretId>` (a name or ARN, optional
// `#<jsonKey>`) against AWS Secrets Manager via GetSecretValue (a SigV4-signed
// POST — the one backend httpx's GET-only client cannot serve). With `#<jsonKey>`
// the SecretString is parsed as JSON and the named key returned (the common
// "secret is a JSON document" case); without it the whole SecretString (or the
// base64-decoded SecretBinary) is returned.
//
//	aws-secretsmanager:prod/gdrive-token         (whole SecretString)
//	aws-secretsmanager:prod/gdrive#token         (the "token" key of a JSON secret)
//
// Engine config (the standard AWS chain, like the ledger signer / CMEK KEK):
//
//	OLIVARES_SECRETREF_AWS_REGION | AWS_REGION | AWS_DEFAULT_REGION
//	AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY / AWS_SESSION_TOKEN
//	AWS_ENDPOINT_URL_SECRETS_MANAGER — endpoint override (e.g. a VPC endpoint), optional
type awsReader struct {
	region   string
	endpoint string
	creds    awssig.Creds
	doer     httpx.Doer
	now      func() time.Time
}

func newAWSReader(getenv func(string) string, doer httpx.Doer) (Reader, bool) {
	region := firstEnv(getenv, "OLIVARES_SECRETREF_AWS_REGION", "AWS_REGION", "AWS_DEFAULT_REGION")
	akid := firstEnv(getenv, "AWS_ACCESS_KEY_ID")
	secret := firstEnv(getenv, "AWS_SECRET_ACCESS_KEY")
	if region == "" || akid == "" || secret == "" {
		return nil, false
	}
	endpoint := firstEnv(getenv, "AWS_ENDPOINT_URL_SECRETS_MANAGER")
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://secretsmanager.%s.amazonaws.com", region)
	}
	return &awsReader{
		region:   region,
		endpoint: strings.TrimRight(endpoint, "/"),
		creds:    awssig.Creds{AKID: akid, Secret: secret, Token: firstEnv(getenv, "AWS_SESSION_TOKEN")},
		doer:     doer,
	}, true
}

func (r *awsReader) Resolve(ctx context.Context, locator string) ([]byte, error) {
	secretID, jsonKey, hasKey := strings.Cut(locator, "#")
	secretID = strings.TrimSpace(secretID)
	if secretID == "" {
		return nil, fmt.Errorf("aws-secretsmanager: empty SecretId")
	}
	body, _ := json.Marshal(map[string]string{"SecretId": secretID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint+"/", strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("aws-secretsmanager: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "secretsmanager.GetSecretValue")
	req.Header.Set("Accept", "application/json")
	awssig.Sign(req, body, "secretsmanager", r.region, r.creds, r.clock())

	resp, err := defaultClient(r.doer).Do(req)
	if err != nil {
		return nil, fmt.Errorf("aws-secretsmanager: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// The error body carries an AWS __type/message — non-secret diagnostics.
		return nil, fmt.Errorf("aws-secretsmanager: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		SecretString string `json:"SecretString"`
		SecretBinary string `json:"SecretBinary"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("aws-secretsmanager: decode response: %w", err)
	}
	value, err := r.materialize(out.SecretString, out.SecretBinary)
	if err != nil {
		return nil, err
	}
	if hasKey && strings.TrimSpace(jsonKey) != "" {
		return extractJSONKey(value, strings.TrimSpace(jsonKey), "aws-secretsmanager")
	}
	return value, nil
}

func (r *awsReader) materialize(secretString, secretBinary string) ([]byte, error) {
	if secretString != "" {
		return []byte(secretString), nil
	}
	if secretBinary != "" {
		dec, err := base64.StdEncoding.DecodeString(secretBinary)
		if err != nil {
			return nil, fmt.Errorf("aws-secretsmanager: decode SecretBinary: %w", err)
		}
		return dec, nil
	}
	return nil, fmt.Errorf("aws-secretsmanager: secret has neither SecretString nor SecretBinary")
}

func (r *awsReader) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// extractJSONKey returns one string field of a JSON-document secret. It never
// includes a value in an error.
func extractJSONKey(doc []byte, key, backend string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(doc, &fields); err != nil {
		return nil, fmt.Errorf("%s: secret is not a JSON object, cannot select #%s", backend, key)
	}
	return selectField(fields, key, true, backend)
}
