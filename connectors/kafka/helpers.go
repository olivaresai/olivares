// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package kafka

import (
	"encoding/base64"
	"net/http"

	"github.com/olivaresai/olivares/connectors/internal/redact"
)

// basicAuth returns an HTTP request decorator that applies HTTP Basic auth for the
// Schema Registry credential, or a no-op when no user is configured. The credential
// is held in the closure (in memory) and placed only into the Authorization header
// — never logged or emitted (docs/SECURITY-HARDENING.md).
func basicAuth(user, pass string) func(*http.Request) {
	if user == "" {
		return nil
	}
	cred := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
	return func(req *http.Request) {
		req.Header.Set("Authorization", "Basic "+cred)
	}
}

// hashErr returns a stable SHA-256 of an error's text so a health finding can be
// de-duplicated without ever transmitting the raw message (which can embed an
// endpoint or credential). An empty error hashes the empty string.
func hashErr(err error) string {
	if err == nil {
		return redact.Hash("")
	}
	return redact.Hash(err.Error())
}
