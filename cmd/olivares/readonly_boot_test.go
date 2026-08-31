// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// readOnlyVerbs are invocations that only READ. None of them may leave anything
// behind in the directory it was run from.
//
// The list is short on purpose — it is a behavioral sample, not the coverage
// claim. The exhaustive check is the tree-wide sweep documented in
// which runs EVERY leaf in an empty directory
// against a built binary; this test is what keeps the regression from landing
// between such sweeps, in the ordinary `go test` a contributor runs.
var readOnlyVerbs = [][]string{
	{"sources", "ls"},
	{"secrets", "ls"},
	{"superadmin", "status"},
	{"eventing", "subscriptions", "ls", "--tenant", "00000000-0000-0000-0000-000000000000"},
	{"eventing", "egress", "status"},
	{"audit", "verify", "--tenant", "00000000-0000-0000-0000-000000000000"},
}

// TestReadOnlyCommandsDoNotInstallAnything pins the defect that opened:
// `olivares sources ls`, whose whole output is "no sources in the roster", left
// three private signing keys and a 6 MB SQLite database in ./olivares-data — in
// whatever directory it was run from, because the old default data dir was relative.
func TestReadOnlyCommandsDoNotInstallAnything(t *testing.T) {
	for _, argv := range readOnlyVerbs {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			t.Setenv("HOME", dir)
			t.Setenv("XDG_DATA_HOME", "")
			t.Setenv("OLIVARES_DATA_DIR", "")
			// ⛔ LA CADENA DE RESOLUCIÓN TIENE CUATRO ESLABONES Y ESTE TEST FIJABA DOS.
			//
			//    `--data-dir` cae a `$OLIVARES_DATA_DIR`, luego a un `./olivares-data`
			//    existente, luego a `$XDG_DATA_HOME/olivares` y por último a
			//    `~/.local/share/olivares` (`cmd_serve.go:89`). Vaciar el primero y hacer
			//    `Chdir` a un temporal cubre los dos primeros y **deja pasar los dos últimos**.
			//
			//    Medido el 2026-08-19: en esta máquina existe una instalación REAL en
			//    `~/.local/share/olivares` —7,9 MB, la dejó una corrida de consola del 08-18—,
			//    así que el test abría ESA. Fallaba con «unit … has two bootstrap receipts» en
			//    `origin/main` limpio, en cualquier sha y con o sin `-race`, mientras los
			//    runners de CI lo daban verde: allí ese directorio no existe. Dos carriles
			//    gastaron una noche buscando la diferencia en Postgres y en el paralelismo.
			//
			//    Y el filo que lo hacía invisible: la aserción final cuenta ficheros DENTRO de
			//    `dir`, así que no puede ver nada de lo que el comando haga fuera. Un test que
			//    prueba «no instala nada» no miraba donde instala.
			//
			//    ⚠ LAS DOS LÍNEAS HACEN FALTA, y lo digo porque la mutación de la primera SALE
			//    VERDE en esta caja: aquí `XDG_DATA_HOME` no está fijado, así que la cadena cae
			//    al cuarto eslabón y `HOME` sola ya lo tapa. Es una guarda con OR cuya otra rama
			//    mis condiciones no ejercitan. Con `XDG_DATA_HOME` fijado en el ambiente —lo
			//    normal en un escritorio Linux y en muchas imágenes de CI— quitar la primera
			//    línea vuelve a poner el test ROJO. Medido en las cuatro combinaciones.
			t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "xdg"))
			t.Setenv("HOME", dir)

			root := newRootCmd()
			var out, errb bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&errb)
			root.SetArgs(argv)
			_, err := root.ExecuteC()

			if err == nil {
				t.Fatalf("`olivares %s` succeeded against a directory with no installation; "+
					"it must report that there is nothing there, not build one", strings.Join(argv, " "))
			}
			if got := exitcode.From(err); got != exitcode.NotFound {
				t.Errorf("exit = %d, want %d (not found): %v", got, exitcode.NotFound, err)
			}
			left := filesUnder(t, dir)
			if len(left) > 0 {
				t.Fatalf("`olivares %s` left %d file(s) behind: %v",
					strings.Join(argv, " "), len(left), left)
			}
		})
	}
}

// TestMutatingCommandsDoNotInstallImplicitly covers the other half: a write verb
// pointed at nothing must not mint signing keys into the working directory just
// because it needed somewhere to write. Initializing is quickstart's job.
func TestMutatingCommandsDoNotInstallImplicitly(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", dir)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("OLIVARES_DATA_DIR", "")
	// Los dos eslabones que faltaban aquí también — ver el comentario largo del test de
	// arriba. Este caso los necesita por la misma razón y fallaba por la misma causa.
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "xdg"))
	t.Setenv("HOME", dir)

	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs([]string{"sources", "rm", "--name", "whatever", "--yes"})
	_, err := root.ExecuteC()
	if err == nil {
		t.Fatal("`sources rm` created an installation from nothing")
	}
	if !strings.Contains(err.Error(), "quickstart") {
		t.Errorf("the refusal must point at how to initialize, got: %v", err)
	}
	if left := filesUnder(t, dir); len(left) > 0 {
		t.Fatalf("left %d file(s) behind: %v", len(left), left)
	}
}

// TestStoreFileIsNotWorldReadable pins the mode of the store's own bytes. Until
// The engine stated no mode at all: SQLite created the database 0666 &
// ~umask, i.e. 0644 on a default umask. The 0700 data directory is what actually
// keeps other users out — this is defense in depth, and it is asserted rather
// than assumed because nothing else in the tree looks at it.
func TestStoreFileIsNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("OLIVARES_DATA_DIR", "")

	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	// An explicit --data-dir is a stated intent to initialize there, and `set`
	// is a WRITE — the path that legitimately creates the store.
	dataDir := filepath.Join(dir, "install")
	root.SetArgs([]string{"sources", "set", "--data-dir", dataDir,
		"--actor", "ana@corp.example", "--reason", "initialize a store to inspect",
		"--name", "probe", "--kind", "vault", "--tenant", "00000000-0000-0000-0000-000000000000"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("could not initialize a store to inspect: %v", err)
	}
	db := filepath.Join(dataDir, "olivares.db")
	info, err := os.Stat(db)
	if err != nil {
		t.Fatalf("no store at %s: %v", db, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("%s mode = %#o, want 0600 — the store must not depend on the "+
			"process umask to be unreadable by other users", db, perm)
	}
}

// initialisedDataDir returns an INITIALIZED, otherwise empty data directory.
//
// It exists because stopped read-only commands creating what they read: a
// test that hands `secrets ls` a bare t.TempDir() is no longer exercising an
// empty roster, it is exercising "there is no installation here". Booting once
// in write mode is the same thing `quickstart` does, minus the listener.
func initialisedDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	eng, err := boot(context.Background(), bootConfig{
		DataDir: dir, Version: "test", Logger: discardLog(),
	})
	if err != nil {
		t.Fatalf("seed data dir %s: %v", dir, err)
	}
	if cerr := eng.Close(); cerr != nil {
		t.Fatalf("close seeded engine: %v", cerr)
	}
	return dir
}

func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || path == dir {
			return err //nolint:wrapcheck // walk error is reported by the caller
		}
		rel, _ := filepath.Rel(dir, path)
		found = append(found, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return found
}
