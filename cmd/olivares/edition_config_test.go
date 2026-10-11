// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

// TestTheEditionSeamGetsTheResolvedDataDir is the witness for the one link of this chain that can
// be wrong in silence.
//
// ⛔ QUE LA COSTURA RECIBA UN `EditionConfig` LO GARANTIZA EL COMPILADOR; QUE RECIBA EL CAMPO
// CORRECTO NO LO GARANTIZA NADIE. `editionConfigFrom` podría devolver otro directorio —el de
// trabajo, el de configuración, una cadena vacía— y todo compilaría igual. La consecuencia no sería
// un fallo: sería el store privado del cockpit creado EN OTRO SITIO que los datos del motor, con su
// propio `SELECT` en verde y en el directorio equivocado.
//
// ⛔ Y ESTA COSTURA EXISTE POR UNA AUSENCIA MEDIDA. `Migrate` tiene una sola aparición en el árbol
// comercial —su propia definición—, así que un cliente que instala no tiene base del cockpit. La
// causa no era descuido: NINGUNA costura `edition*` recibía configuración, y el overlay no puede
// re-derivar el directorio sin convertirse en un segundo productor de la decisión que la base ya
// toma con `--data-dir` y su cadena de defectos.
func TestTheEditionSeamGetsTheResolvedDataDir(t *testing.T) {
	const dir = "/var/lib/olivares-elegido-por-el-operador"
	got := editionConfigFrom(bootConfig{DataDir: dir}, store.EngineSQLite, "")
	if got.DataDir != dir {
		t.Fatalf("la costura recibe %q y el operador pidió %q: el store del add-on aterrizaría "+
			"fuera de los datos del motor, y su propia comprobación saldría verde allí",
			got.DataDir, dir)
	}
	// ⛔ CONTROL POSITIVO, sin el cual lo de arriba lo satisface un mapeo que devuelva SIEMPRE esa
	// cadena. Un segundo valor distinto es lo que separa «lee el campo» de «devuelve una constante».
	const otro = "/srv/olivares"
	if second := editionConfigFrom(bootConfig{DataDir: otro}, store.EngineSQLite, ""); second.DataDir != otro {
		t.Fatalf("con otro directorio devuelve %q: el mapeo no lee su entrada", second.DataDir)
	}
	// Y el cero se propaga como cero: una edición sin directorio no debe recibir uno inventado.
	if empty := editionConfigFrom(bootConfig{}, store.EngineSQLite, ""); empty.DataDir != "" {
		t.Fatalf("sin directorio la costura recibe %q: alguien lo está inventando", empty.DataDir)
	}
}

// TestTheEditionSeamGetsTheCoreDSNTheStoreOpens: the cockpit's retirement proof is about ONE core
// store, so the seam is told which file the core opened. It is the resolved value boot hands the
// store (the default `<data dir>/olivares.db` when no --dsn was given), not the raw flag.
func TestTheEditionSeamGetsTheCoreDSNTheStoreOpens(t *testing.T) {
	const dsn = "/srv/core/olivares.db"
	if got := editionConfigFrom(bootConfig{DataDir: "/var/lib/olivares"}, store.EngineSQLite, dsn); got.CoreDSN != dsn {
		t.Fatalf("the seam receives core DSN %q and the store opened %q", got.CoreDSN, dsn)
	}
	const other = "file:/srv/other/olivares.db?_pragma=busy_timeout(1)"
	if got := editionConfigFrom(bootConfig{DataDir: "/var/lib/olivares"}, store.EngineSQLite, other); got.CoreDSN != other {
		t.Fatalf("with another DSN the seam receives %q: the mapping does not read its input", got.CoreDSN)
	}
	// A PostgreSQL URL carries the database password, and no private store is a file beside it:
	// the seam never receives it.
	const url = "postgres://olivares:hunter2@db.internal/olivares"
	if got := editionConfigFrom(bootConfig{DataDir: "/var/lib/olivares"}, store.EnginePostgres, url); got.CoreDSN != "" {
		t.Fatalf("a PostgreSQL deployment hands the edition seam a DSN: %q", got.CoreDSN)
	}
}

// TestCoreStoreInDataDir pins the one question the retirement proof asks of the layout: is the
// core file the data directory's own `olivares.db`. Every row is a layout an operator can reach
// with --data-dir and --dsn.
func TestCoreStoreInDataDir(t *testing.T) {
	data := t.TempDir()
	elsewhere := t.TempDir()
	touch := func(path string) string {
		t.Helper()
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	own := touch(filepath.Join(data, "olivares.db"))
	outside := touch(filepath.Join(elsewhere, "olivares.db"))
	sibling := touch(filepath.Join(data, "core.db"))
	if err := os.Mkdir(filepath.Join(data, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "data-link")
	if err := os.Symlink(data, link); err != nil {
		t.Fatal(err)
	}
	// A data directory whose olivares.db is a symlink to a core file kept elsewhere: the file
	// is not physically the data directory's, whichever way it is spelled.
	linkedData := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(linkedData, "olivares.db")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Dir(data))
	relData, relOwn := filepath.Base(data), filepath.Join(filepath.Base(data), "olivares.db")
	for _, c := range []struct {
		name    string
		dataDir string
		dsn     string
		want    bool
	}{
		{"the default layout", data, own, true},
		{"a file: URI for the same file", data, "file:" + own + "?_pragma=busy_timeout(5000)", true},
		{"the same file spelled through a parent", data, filepath.Join(data, "sub", "..", "olivares.db"), true},
		{"the data directory reached through a symlink", link, own, true},
		{"the same file reached through a symlink", data, filepath.Join(link, "olivares.db"), true},
		{"a relative data directory and a relative DSN", relData, relOwn, true},
		{"a relative data directory and an absolute DSN", relData, own, true},
		{"a core file outside the data directory", data, outside, false},
		{"the data directory's olivares.db a symlink to a file elsewhere", linkedData, filepath.Join(linkedData, "olivares.db"), false},
		{"the same symlinked file named by its target", linkedData, outside, false},
		{"another file inside the data directory", data, sibling, false},
		{"an in-memory core", data, ":memory:", false},
		{"no DSN", data, "", false},
		{"a destination the resolver refuses", data, own + "?vfs=unix-dotfile", false},
		{"no data directory", "", own, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := (EditionConfig{DataDir: c.dataDir, CoreDSN: c.dsn}).CoreStoreInDataDir(); got != c.want {
				t.Fatalf("CoreStoreInDataDir(data=%q, core=%q) = %v, want %v", c.dataDir, c.dsn, got, c.want)
			}
		})
	}
}
