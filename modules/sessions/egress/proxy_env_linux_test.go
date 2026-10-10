// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package egress

import (
	"slices"
	"testing"
)

// The bridge replaces the host's way out: every host proxy variable in any case,
// and the host's CA bundles only when the session brings its own CA.
func TestProxyEnvironmentReplacesTheHostWayOut(t *testing.T) {
	host := []string{"PATH=/usr/bin", "HTTPS_PROXY=http://corp:3128", "http_proxy=http://corp:3128", "All_Proxy=socks5://corp",
		"no_proxy=internal", "SSL_CERT_FILE=/etc/corp.pem", "NODE_EXTRA_CA_CERTS=/etc/corp.pem", "SSL_CERT_DIR=/etc/ssl/certs"}
	bridge := []string{"HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "http_proxy=http://127.0.0.1:9", "https_proxy=http://127.0.0.1:9"}
	for _, tc := range []struct {
		caFile string
		want   []string
	}{
		{"", append([]string{"PATH=/usr/bin", "SSL_CERT_FILE=/etc/corp.pem", "NODE_EXTRA_CA_CERTS=/etc/corp.pem", "SSL_CERT_DIR=/etc/ssl/certs"}, bridge...)},
		{"/run/ca.pem", append(append([]string{"PATH=/usr/bin", "SSL_CERT_DIR=/etc/ssl/certs"}, bridge...),
			"NODE_EXTRA_CA_CERTS=/run/ca.pem", "SSL_CERT_FILE=/run/ca.pem", "REQUESTS_CA_BUNDLE=/run/ca.pem", "CURL_CA_BUNDLE=/run/ca.pem")},
	} {
		if got := proxyEnvironment(host, "http://127.0.0.1:9", tc.caFile); !slices.Equal(got, tc.want) {
			t.Errorf("caFile %q:\n got %q\nwant %q", tc.caFile, got, tc.want)
		}
	}
}
