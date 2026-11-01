// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// sourceItem asks the engine to read the issue or pull request page for the folder's
// workspace, with the repository credential an administrator registered for it. The
// engine owns every rule about the page and the credential; its sentence is the answer.
func (c *agentClientConfig) sourceItem(ctx context.Context, workspaceRef, page string) (map[string]any, error) {
	q := url.Values{"workspace_ref": {workspaceRef}, "url": {page}}
	_, b, err := c.do(ctx, "GET", sessionSourceItemsPath+"?"+q.Encode(), nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	item := map[string]any{}
	if err := json.Unmarshal(b, &item); err != nil {
		return nil, err
	}
	return item, nil
}

// sourceItemPrompt is the first message of a session started from an item: its
// title, its body, its page, then whatever the person typed as the prompt.
func sourceItemPrompt(item map[string]any, prompt string) string {
	parts := []string{strings.TrimSpace(str(item, "title"))}
	for _, part := range []string{str(item, "body"), str(item, "url"), prompt} {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "\n\n")
}
