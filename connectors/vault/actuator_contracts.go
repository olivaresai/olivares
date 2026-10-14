// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vault

import (
	"net/http"
)

// Doer is the minimal HTTP capability the actuator needs (the Exchanger
// precedent). *http.Client satisfies it; tests inject a stub.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}
