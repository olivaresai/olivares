// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/url"
	"strings"
)

// Repository sources use HTTPS without ambient credentials, redirects, hooks,
// filters, submodules or local transports. The agent receives only the checkout.
func validateRepositorySource(source, ref string) error {
	u, err := url.Parse(source)
	if err != nil || len(source) > 2048 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path == "" || u.Opaque != "" {
		return badRequest("repository_url must be an HTTPS repository URL without credentials, query or fragment")
	}
	if len(ref) > 512 || strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\x00\r\n:") {
		return badRequest("invalid repository_ref")
	}
	return nil
}

func prepareRepositorySource(ctx context.Context, dir, source, ref string) error {
	if err := validateRepositorySource(source, ref); err != nil {
		return err
	}
	if ref == "" {
		ref = "HEAD"
	}
	if _, err := gitOK(ctx, dir, "init", "--quiet", "."); err != nil {
		return badRequest("could not initialize the repository checkout")
	}
	// gitRun supplies an isolated environment and bounds the command and its output.
	_, code, _, err := gitRunWithHome(ctx, dir, dir, "-c", "protocol.https.allow=always", "-c", "http.followRedirects=false", "fetch", "--quiet", "--depth=1", "--", source, ref)
	if err != nil {
		return err
	}
	if code != 0 {
		return badRequest("could not fetch the requested repository and ref")
	}

	if _, err := gitOK(ctx, dir, "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
		return badRequest("could not check out the requested repository ref")
	}
	if _, err := gitOK(ctx, dir, "remote", "add", "origin", source); err != nil {
		return badRequest("could not record the repository origin")
	}
	return nil
}
