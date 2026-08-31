// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFindGRPCFilesSkipsThrowawayExportCopies fija el control que faltaba cuando este gate
// acusó al árbol por copias de sí mismo.
//
// Medido el 2026-08-21: `lint:export --check` deja su árbol en `.export-tmp/tmp.XXXX/public/`
// y no siempre lo limpia — se encontraron 117 MB de una corrida anterior. `findGRPCFiles` no
// lo saltaba, así que el gate reportó «2 difference(s) between the tree and the published
// guides» nombrando `.export-tmp/.../api_grpc.pb.go`. El árbol estaba limpio; lo sucio era el
// residuo. Un gate que mide una copia de usar y tirar acusa al contenido equivocado.
//
// El test cubre las DOS direcciones a propósito: que el residuo se salte NO puede valer como
// coartada para dejar de ver el fichero real que hay debajo.
func TestFindGRPCFilesSkipsThrowawayExportCopies(t *testing.T) {
	root := t.TempDir()

	real := filepath.Join(root, "core", "api", "genpb", "apiv1")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	if err := os.WriteFile(filepath.Join(real, "api_grpc.pb.go"), []byte("package apiv1\n"), 0o644); err != nil {
		t.Fatalf("write real: %v", err)
	}

	copia := filepath.Join(root, ".export-tmp", "tmp.ABC123", "public", "core", "api", "genpb", "apiv1")
	if err := os.MkdirAll(copia, 0o755); err != nil {
		t.Fatalf("mkdir copia: %v", err)
	}
	if err := os.WriteFile(filepath.Join(copia, "api_grpc.pb.go"), []byte("package apiv1\n"), 0o644); err != nil {
		t.Fatalf("write copia: %v", err)
	}

	got, err := findGRPCFiles(root)
	if err != nil {
		t.Fatalf("findGRPCFiles: %v", err)
	}

	quiero := "core/api/genpb/apiv1/api_grpc.pb.go"
	if len(got) != 1 || got[0] != quiero {
		t.Fatalf("el walk devolvió %v; quería exactamente [%s] — ni el residuo de .export-tmp\n"+
			"ni, en la otra dirección, un walk que se saltara también el fichero real", got, quiero)
	}
}

// TestFindGRPCFilesStillWalksWithoutResidue es el control negativo del anterior: sin
// `.export-tmp` delante, el mismo árbol devuelve el mismo fichero. Sin él, un `findGRPCFiles`
// que devolviera siempre una lista vacía pasaría el test de arriba.
func TestFindGRPCFilesStillWalksWithoutResidue(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "sdk", "plugin", "genpb", "olivaresv1")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(real, "v1_grpc.pb.go"), []byte("package olivaresv1\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := findGRPCFiles(root)
	if err != nil {
		t.Fatalf("findGRPCFiles: %v", err)
	}
	quiero := "sdk/plugin/genpb/olivaresv1/v1_grpc.pb.go"
	if len(got) != 1 || got[0] != quiero {
		t.Fatalf("el walk devolvió %v; quería [%s]", got, quiero)
	}
}
