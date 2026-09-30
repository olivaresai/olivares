// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"errors"
	"fmt"
	"net/http"
)

var ErrInspectionCatalogInvalid = errors.New("mcp: invalid or incomplete inspection catalog")

// NewHTTPInspectionClient reuses the connector's read-only initialize/list
// client with an explicitly supplied governed HTTP client. It neither obtains
// OAuth credentials nor calls tools. Headers are copied, not retained by alias.
// Inspection is bounded to eight pages and 128 tools; a truncated or repeating
// catalog is an error rather than a successful partial observation.
func NewHTTPInspectionClient(endpoint string, headers map[string]string, client *http.Client) (*Client, error) {
	if client == nil || client.Transport == nil {
		return nil, fmt.Errorf("mcp: inspection requires an explicit HTTP transport")
	}
	h := make(map[string]string, len(headers))
	for k, v := range headers {
		h[k] = v
	}
	return &Client{t: &httpTransport{url: endpoint, headers: h, client: client}, inspection: true}, nil
}
