// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"crypto/tls"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/secure"
)

// The agent listeners an edition serves need what only serve holds: the certificate the
// HTTPS listener serves (and reloads), and the boot's own config, modules and store. This
// pins the mapping, as editionConfigFrom's witness does: a seam handed another certificate
// or a second store would compile the same.
func TestEditionAgentDepsCarryTheServedCertificateAndTheBootProducers(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if _, _, err := secure.EnsureTLSCertForHost(certPath, keyPath, "127.0.0.1", false); err != nil {
		t.Fatal(err)
	}
	loader, err := secure.NewCertificateLoader(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	st := erasureTestStore(t)
	modules := make([]api.Module, 1)
	config := EditionConfig{DataDir: dir, CoreDSN: filepath.Join(dir, coreStoreFile)}
	eng := &engine{store: st, editionModules: modules, editionConfig: config}
	getenv := func(string) string { return "" }

	deps := editionAgentDepsFrom(eng, loader, getenv, slog.Default())
	if deps.Config != config || deps.Store != st || deps.Log == nil || deps.Getenv == nil {
		t.Fatalf("deps = %+v, want the engine's config and store, a getenv and a logger", deps)
	}
	if reflect.ValueOf(deps.Modules).Pointer() != reflect.ValueOf(modules).Pointer() {
		t.Fatal("deps.Modules is not the boot's registered module list")
	}
	if deps.Certificate == nil {
		t.Fatal("a serve with TLS hands the edition no certificate")
	}
	got, err := deps.Certificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatal(err)
	}
	want, err := loader.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Certificate, want.Certificate) {
		t.Fatal("the edition's certificate is not the one the HTTPS listener serves")
	}

	if insecure := editionAgentDepsFrom(eng, nil, getenv, slog.Default()); insecure.Certificate != nil {
		t.Fatal("an --insecure serve has no certificate; the edition must not be handed one")
	}
}
