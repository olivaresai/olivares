// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"

	"github.com/olivaresai/olivares/core/auth"
)

const envLoginTrustedProxies = "OLIVARES_LOGIN_TRUSTED_PROXIES"

const loginTrustedProxiesFlagHelp = "comma-separated proxy CIDRs trusted for the password-login throttle's X-Forwarded-For address only (default $OLIVARES_LOGIN_TRUSTED_PROXIES; empty trusts none). An explicit empty flag clears the environment setting; policy, session and audit keep the transport peer"

// loginProxyOptions carries flag presence separately from its value. resolved is
// set before startup writes and prevents a downstream environment read.
type loginProxyOptions struct {
	value    string
	set      bool
	resolved *auth.TrustedLoginProxies
}

func (o loginProxyOptions) resolve(getenv func(string) string) (*auth.TrustedLoginProxies, error) {
	if o.resolved != nil {
		return o.resolved, nil
	}
	raw := o.value
	if !o.set {
		raw = getenv(envLoginTrustedProxies)
	}
	trust, err := auth.ParseTrustedLoginProxies(raw)
	if err != nil {
		return nil, fmt.Errorf("--login-trusted-proxies / %s: %w", envLoginTrustedProxies, err)
	}
	return &trust, nil
}
