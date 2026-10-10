// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/secure"
)

func TestOperatorTLSPublicPathKeepsServing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		key    bool
		bundle bool
	}{
		{name: "certificate"},
		{name: "certificate-with-private-block", bundle: true},
		{name: "private-key", key: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir, material := t.TempDir(), t.TempDir()
			fixtureCert, fixtureKey := filepath.Join(material, "fixture.crt"), filepath.Join(material, "fixture.key")
			if _, _, err := secure.EnsureTLSCert(fixtureCert, fixtureKey); err != nil {
				t.Fatal(err)
			}
			certBytes, err := os.ReadFile(fixtureCert)
			if err != nil {
				t.Fatal(err)
			}
			keyBytes, err := os.ReadFile(fixtureKey)
			if err != nil {
				t.Fatal(err)
			}
			if tc.bundle {
				certBytes = append(certBytes, keyBytes...)
			}
			publicDir := filepath.Join(dataDir, "public")
			if err := os.Mkdir(publicDir, 0o755); err != nil {
				t.Fatal(err)
			}
			cert, key := filepath.Join(publicDir, "tls.crt"), filepath.Join(dataDir, "operator.key")
			if tc.key {
				cert, key = filepath.Join(dataDir, "operator.crt"), filepath.Join(publicDir, "tls.crt")
			}
			for path, data := range map[string][]byte{cert: certBytes, key: keyBytes} {
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			opts := serveOptions{
				listen: bindAnnounceFreeAddr(t), grpcListen: bindAnnounceFreeAddr(t), dataDir: dataDir,
				engine: "sqlite", checkpointInterval: 0, tlsCert: cert, tlsKey: key,
			}
			res := runBindAnnounce(t, opts, &bindAnnounceBuffer{}, serveAnnounce(false), nil)
			if !res.answered {
				t.Errorf("operator TLS path stopped the engine: %v", res.err)
			}
			for path, before := range map[string][]byte{cert: certBytes, key: keyBytes} {
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(after, before) {
					t.Error("operator TLS material was rewritten")
				}
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Error("operator TLS material permissions changed")
				}
			}
			requireBindAnnounceRebindable(t, "operator TLS compatibility", opts.listen, opts.grpcListen)
		})
	}
}
