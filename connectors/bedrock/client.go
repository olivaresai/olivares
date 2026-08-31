// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package bedrock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// contentTypeAWSJSON is the AWS JSON 1.1 content type CloudWatch Logs and Cost Explorer
// use on their JSON-protocol POSTs.
const contentTypeAWSJSON = "application/x-amz-json-1.1"

// awsJSONPost issues one SigV4-signed AWS-JSON-1.1 POST and decodes the JSON response.
// CloudWatch Logs FilterLogEvents and Cost Explorer GetCostAndUsage are both reads, but
// the AWS-JSON protocol mandates POST to "/" with the request carried in the body and
// the operation named in X-Amz-Target. The body is signed as part of the canonical
// request; sign mutates only headers, so this cannot become a write.
func (s *Source) awsJSONPost(ctx context.Context, endpoint, target, service, region string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	url := strings.TrimRight(endpoint, "/") + "/"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentTypeAWSJSON)
	req.Header.Set("X-Amz-Target", target)
	sign(req, raw, service, region, s.cfg.creds, time.Now())

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		// The status alone is reported; the body may echo a request token, so it is
		// never embedded in the error (minimal-data).
		return fmt.Errorf("bedrock: %s returned status %d", target, resp.StatusCode)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("bedrock: decode %s response: %w", target, err)
	}
	return nil
}

// bedrockGet issues one SigV4-signed GET to the Bedrock control plane and decodes the
// JSON response. The path may carry a query string (signed as part of the canonical
// request). It is read-only: a GET with no body, signed for the regional "bedrock"
// service.
func (s *Source) bedrockGet(ctx context.Context, path string, out any) error {
	endpoint := strings.TrimRight(s.cfg.bedrockEndpoint, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	sign(req, nil, bedrockSigningService, s.cfg.region, s.cfg.creds, time.Now())

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bedrock: GET %s returned status %d", reqPathForError(path), resp.StatusCode)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("bedrock: decode %s response: %w", reqPathForError(path), err)
	}
	return nil
}

// reqPathForError returns the path without its query string, so an error never echoes a
// (potentially token-bearing) nextToken cursor.
func reqPathForError(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		return path[:i]
	}
	return path
}
