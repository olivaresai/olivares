// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package firstparty

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// El acoplamiento con plugjail es real y no lo puede ver el compilador: el jail baja a un uid
// dedicado cuando `os.Geteuid() == 0`, y la extraccion tiene que conceder el minimo que hace
// posible el exec EXACTAMENTE en ese caso. Este test existe para que ese "exactamente en ese
// caso" sea una asercion y no un comentario.
func TestElPermisoSigueLaCondicionDelJail(t *testing.T) {
	casos := []struct {
		nombre   string
		euid     int
		dir, bin fs.FileMode
	}{
		{"root: el jail va a cambiar de uid, hace falta atravesar y ejecutar", 0, 0o711, 0o711},
		{"sin privilegios: el hijo hereda el uid, 0700 basta", 1000, 0o700, 0o700},
		{"otro uid sin privilegios cualquiera", 65534, 0o700, 0o700},
		{"euid desconocido (Windows devuelve -1) no ensancha nada", -1, 0o700, 0o700},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			d, b := ExecPerm(c.euid)
			if d != c.dir || b != c.bin {
				t.Fatalf("ExecPerm(%d) = dir %04o, bin %04o; queria dir %04o, bin %04o",
					c.euid, d, b, c.dir, c.bin)
			}
			// Lo que NO puede pasar en ninguna rama: conceder LECTURA fuera del dueno. El
			// binario deja de ser secreto al enviarse en el artefacto de release, pero
			// ensanchar la lectura no es lo que hace falta para lanzarlo, y un permiso de
			// mas que nadie necesita es un permiso que sobra.
			if d&0o044 != 0 || b&0o044 != 0 {
				t.Fatalf("ExecPerm(%d) concede lectura a grupo u otros: dir %04o, bin %04o", c.euid, d, b)
			}
		})
	}
}

// Y que extractFrom aplique de verdad lo que execPerm decide, no un literal paralelo que se
// quede atras: es el modo del fichero en disco lo que decide si el plugin arranca.
func TestLaExtraccionEscribeElModoQueDecideExecPerm(t *testing.T) {
	fsys := fstest.MapFS{"bins/conector": &fstest.MapFile{Data: []byte("#!/bin/sh\nexit 0\n")}}
	dir := filepath.Join(t.TempDir(), "extraidos")

	ruta, err := extractFrom(fsys, dir, "conector")
	if err != nil {
		t.Fatalf("extractFrom: %v", err)
	}
	dirQuiero, binQuiero := ExecPerm(os.Geteuid())

	fi, err := os.Stat(ruta)
	if err != nil {
		t.Fatalf("stat del binario: %v", err)
	}
	if got := fi.Mode().Perm(); got != binQuiero {
		t.Fatalf("binario extraido con %04o; ExecPerm(%d) dice %04o", got, os.Geteuid(), binQuiero)
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat del directorio: %v", err)
	}
	if got := di.Mode().Perm(); got != dirQuiero {
		t.Fatalf("directorio creado con %04o; ExecPerm(%d) dice %04o", got, os.Geteuid(), dirQuiero)
	}

	// El bit que arranca el plugin, dicho aparte: sin ejecucion para el dueno no hay lanzamiento
	// en ninguna configuracion, con jail o sin el.
	if fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("el binario extraido no es ejecutable por su dueno: %04o", fi.Mode().Perm())
	}
}

// EL CASO QUE ESTABA ROTO, y no lo cubria el test de arriba porque alli el directorio no
// existia todavia. Los tres llamantes reales —boot, collector y releasediag— crean el
// directorio ANTES con os.MkdirTemp, que hace 0700, y `MkdirAll` no cambia el modo de lo que
// ya existe. Es decir: el unico caso en que el modo del directorio venia de Extract era el
// que nunca ocurre.
func TestElModoSeImponeSobreUnDirectorioQueYaExisteEn0700(t *testing.T) {
	fsys := fstest.MapFS{"bins/conector": &fstest.MapFile{Data: []byte("#!/bin/sh\nexit 0\n")}}
	dir := filepath.Join(t.TempDir(), "ya-existe")
	if err := os.MkdirAll(dir, 0o700); err != nil { // exactamente lo que hace os.MkdirTemp
		t.Fatalf("preparar el directorio: %v", err)
	}

	// 0711 EXPLICITO, no ExecPerm(os.Geteuid()): asi la asercion mide el mecanismo y no el
	// uid con el que corre la suite.
	const dirQuiero = os.FileMode(0o711)
	if _, err := extractWithPerm(fsys, dir, "conector", dirQuiero, 0o711); err != nil {
		t.Fatalf("extractWithPerm: %v", err)
	}

	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != dirQuiero {
		t.Fatalf("el directorio preexistente quedo en %04o; se pidio %04o — "+
			"un plugin lanzado bajo otro uid no podria atravesarlo", got, dirQuiero)
	}
}

// Y el modo del BINARIO con la misma costura, por el mismo motivo: comprobarlo a traves de
// extractFrom con el euid ambiente hace que 0700 == 0700 y la asercion no distingue nada.
// Medido: sustituir `f.Chmod(binPerm)` por `f.Chmod(0o700)` dejaba la suite en verde.
func TestElModoDelBinarioEsElQueSePideYNoUnLiteral(t *testing.T) {
	fsys := fstest.MapFS{"bins/conector": &fstest.MapFile{Data: []byte("#!/bin/sh\nexit 0\n")}}
	dir := filepath.Join(t.TempDir(), "bin-mode")

	const binQuiero = os.FileMode(0o711)
	ruta, err := extractWithPerm(fsys, dir, "conector", 0o711, binQuiero)
	if err != nil {
		t.Fatalf("extractWithPerm: %v", err)
	}
	fi, err := os.Stat(ruta)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != binQuiero {
		t.Fatalf("el binario quedo en %04o; se pidio %04o — un uid distinto no podria ejecutarlo",
			got, binQuiero)
	}
}

// LA CONDICION COMPLETA: euid 0 **y** Linux. plugjail solo baja de uid en Linux
// (plugjail_other.go: «on a non-Linux host applies NO OS-level isolation»), asi que conceder 0711
// en macOS o Windows como root ensancharia un permiso que nadie va a usar. Sin la costura del
// GOOS esta rama seria incomprobable aqui, porque aqui SIEMPRE es linux.
func TestElEnsanchamientoEsSoloDondeElJailBajaDeUid(t *testing.T) {
	casos := []struct {
		nombre   string
		euid     int
		goos     string
		dir, bin os.FileMode
	}{
		{"root en linux: el jail baja de uid, hace falta", 0, "linux", 0o711, 0o711},
		{"root en darwin: NO baja de uid, no se ensancha", 0, "darwin", 0o700, 0o700},
		{"root en windows: idem", 0, "windows", 0o700, 0o700},
		{"sin privilegios en linux: el hijo hereda el uid", 1000, "linux", 0o700, 0o700},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			d, b := execPermFor(c.euid, c.goos)
			if d != c.dir || b != c.bin {
				t.Fatalf("execPermFor(%d, %q) = %04o/%04o; queria %04o/%04o",
					c.euid, c.goos, d, b, c.dir, c.bin)
			}
		})
	}
}
