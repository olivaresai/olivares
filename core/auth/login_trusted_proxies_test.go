// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"strings"
	"testing"
)

func TestTrustedLoginProxyCIDRs(t *testing.T) {
	for _, raw := range []string{"", " \t ", "10.1.2.3/8", " 10.0.0.0/8 , 2001:db8::/32 ", "10.0.0.0/8,10.1.2.3/8", "::ffff:10.0.0.0/104"} {
		t.Run(raw, func(t *testing.T) {
			trust, err := ParseTrustedLoginProxies(raw)
			if err != nil {
				t.Fatal(err)
			}
			want := "198.51.100.8"
			if strings.TrimSpace(raw) == "" {
				want = "10.1.2.3"
			}
			if got := trust.clientAddress("10.1.2.3", []string{"198.51.100.8"}); got != want {
				t.Fatalf("parsed trust selected %q, want %q", got, want)
			}
		})
	}
	for _, raw := range []string{"not-a-cidr", "10.0.0.0", "10.0.0.0/33", "2001:db8::/129", "fe80::1%eth0/64", ",10.0.0.0/8", "10.0.0.0/8,", "10.0.0.0/8,,::1/128", "10.0.0.0/8,private-invalid-marker"} {
		t.Run(raw, func(t *testing.T) {
			trust, err := ParseTrustedLoginProxies(raw)
			if err == nil {
				t.Fatal("invalid or incomplete CIDR list accepted")
			}
			if strings.Contains(err.Error(), "private-invalid-marker") {
				t.Fatal("configuration error quoted its input")
			}
			if got := trust.clientAddress("10.1.2.3", []string{"198.51.100.8"}); got != "10.1.2.3" {
				t.Fatal("failed parse returned a partially trusted set")
			}
		})
	}
}

func TestTrustedLoginProxyAddressSyntax(t *testing.T) {
	trust, err := ParseTrustedLoginProxies("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"[198.51.100.8]", "[198.51.100.8]:80", "[2001:db8::8", "2001:db8::8]", "[2001:db8::8]]", "[2001:db8::8]:", "[2001:db8::8]:http", "198.51.100.8:", "198.51.100.8:-1", "proxy.example:80", "", " \t ", "fe80::8%eth0"} {
		t.Run(value, func(t *testing.T) {
			if got := trust.clientAddress("10.0.0.1", []string{value}); got != "10.0.0.1" {
				t.Fatalf("malformed forwarded address selected %q, want peer fallback", got)
			}
		})
	}
	values := make([]string, 64)
	for i := range values {
		values[i] = "10.1.1.1"
	}
	values[0] = "198.51.100.8"
	if got := trust.clientAddress("10.0.0.1", values); got != "198.51.100.8" {
		t.Fatalf("64 repeated header values selected %q", got)
	}
	values = append(values, "10.1.1.1")
	if got := trust.clientAddress("10.0.0.1", values); got != "10.0.0.1" {
		t.Fatalf("65 repeated header values selected %q, want peer fallback", got)
	}
}
