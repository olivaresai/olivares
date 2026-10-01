// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package threatfeed

import (
	"net/url"
	"strings"
)

// unparsableURLMarker replaces a feed URL that cannot be parsed: the value is
// never echoed into diagnostics, because a malformed operator URL can carry the
// very credential this package promises to keep out of status output.
const unparsableURLMarker = "[feed URL not shown: unparsable]"

// StripURLUserinfo enforces the FeedStatus.FeedURL contract ("Host+path only;
// never embeds a credential") at the RENDER boundary, where it can actually be
// kept: it returns raw with any userinfo removed, so a status or diagnostic can
// never display the credential an operator may have embedded in the configured
// endpoint (conditional Basic userinfo or an opaque token, H-05 of the 26.10.x
// security backlog). The configured value used on the wire is unchanged — this
// helper is for rendering only. A userinfo-free value is returned
// byte-identical, so diagnostics for well-formed configs do not move; an
// unparseable value is replaced by a fixed marker rather than echoed.
func StripURLUserinfo(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return unparsableURLMarker
	}
	if u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}

// Redacted returns s with FeedURL passed through StripURLUserinfo — the value a
// renderer (CLI, console) must print. Every other FeedStatus field is
// minimal-data by construction (versions, dates, counts, key fingerprints).
func (s FeedStatus) Redacted() FeedStatus {
	s.FeedURL = StripURLUserinfo(s.FeedURL)
	return s
}
