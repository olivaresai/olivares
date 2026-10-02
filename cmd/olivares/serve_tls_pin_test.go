// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/secure"
)

// TestServerInfoNamesThePinOfTheServedCertificate: the console's "Command line" panel
// prints `olivares login --server <address> --pin-sha256 <pin>` for a self-signed
// engine, and until now only the engine's start log carried that pin. server-info names
// it, as tls_pin_sha256: the pin of the certificate a client actually receives, the same
// value the start line logs (secure.SPKIPin of tls.crt) and --pin-sha256 accepts.
func TestServerInfoNamesThePinOfTheServedCertificate(t *testing.T) {
	dataDir := t.TempDir()
	opts := serveOptions{
		listen: bindAnnounceFreeAddr(t), grpcListen: bindAnnounceFreeAddr(t), dataDir: dataDir,
		engine: "sqlite", checkpointInterval: 0,
	}
	var (
		reported, received string
		probeErr           error
	)
	res := runBindAnnounce(t, opts, &bindAnnounceBuffer{}, serveAnnounce(false), func(base string) {
		client, tr := bindAnnounceClient(bindAnnounceProbeWait)
		defer tr.CloseIdleConnections()
		resp, err := client.Get(base + "/v1/server-info")
		if err != nil {
			probeErr = err
			return
		}
		defer func() { _ = resp.Body.Close() }()
		var info struct {
			Pin string `json:"tls_pin_sha256"`
		}
		probeErr = json.NewDecoder(resp.Body).Decode(&info)
		reported = info.Pin
		if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
			sum := sha256.Sum256(resp.TLS.PeerCertificates[0].RawSubjectPublicKeyInfo)
			received = base64.RawStdEncoding.EncodeToString(sum[:])
		}
	})
	if !res.answered {
		t.Fatalf("the engine never served: %v", res.err)
	}
	if probeErr != nil {
		t.Fatalf("server-info over TLS: %v", probeErr)
	}
	logged, err := secure.SPKIPin(filepath.Join(dataDir, "tls.crt"))
	if err != nil {
		t.Fatal(err)
	}
	if reported == "" || reported != received || reported != logged {
		t.Fatalf("server-info tls_pin_sha256 = %q; the served certificate's pin is %q and the start line's %q", reported, received, logged)
	}
}
