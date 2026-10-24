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
// and the host's CA bundles (git's included: it ignores SSL_CERT_FILE) only when
// the session brings its own CA.
func TestProxyEnvironmentReplacesTheHostWayOut(t *testing.T) {
	host := []string{"PATH=/usr/bin", "HTTPS_PROXY=http://corp:3128", "http_proxy=http://corp:3128", "All_Proxy=socks5://corp",
		"no_proxy=internal", "SSL_CERT_FILE=/etc/corp.pem", "NODE_EXTRA_CA_CERTS=/etc/corp.pem", "SSL_CERT_DIR=/etc/ssl/certs",
		"GIT_SSL_CAINFO=/etc/corp-git.pem"}
	bridge := []string{"HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "http_proxy=http://127.0.0.1:9", "https_proxy=http://127.0.0.1:9"}
	for _, tc := range []struct {
		caFile string
		want   []string
	}{
		{"", append([]string{"PATH=/usr/bin", "SSL_CERT_FILE=/etc/corp.pem", "NODE_EXTRA_CA_CERTS=/etc/corp.pem", "SSL_CERT_DIR=/etc/ssl/certs", "GIT_SSL_CAINFO=/etc/corp-git.pem"}, bridge...)},
		{"/run/ca.pem", append(append([]string{"PATH=/usr/bin", "SSL_CERT_DIR=/etc/ssl/certs"}, bridge...),
			"NODE_EXTRA_CA_CERTS=/run/ca.pem", "SSL_CERT_FILE=/run/ca.pem", "REQUESTS_CA_BUNDLE=/run/ca.pem", "CURL_CA_BUNDLE=/run/ca.pem",
			"GIT_SSL_CAINFO=/run/ca.pem")},
	} {
		if got := proxyEnvironment(host, "http://127.0.0.1:9", tc.caFile, nil); !slices.Equal(got, tc.want) {
			t.Errorf("caFile %q:\n got %q\nwant %q", tc.caFile, got, tc.want)
		}
	}
}

// A tool that calls its own server on a loopback port reaches only that port
// directly (#3082): NO_PROXY names it with its port, and the host's no_proxy is gone.
func TestProxyEnvironmentExemptsOnlyTheNamedLoopbackPorts(t *testing.T) {
	host := []string{"PATH=/usr/bin", "no_proxy=internal", "NO_PROXY=10.0.0.0/8"}
	want := []string{"PATH=/usr/bin", "HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "http_proxy=http://127.0.0.1:9", "https_proxy=http://127.0.0.1:9",
		"NO_PROXY=127.0.0.1:4096", "no_proxy=127.0.0.1:4096"}
	if got := proxyEnvironment(host, "http://127.0.0.1:9", "", []int{4096}); !slices.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}
