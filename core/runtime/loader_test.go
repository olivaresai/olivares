// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/sdk"
)

// These tests pin the S142 LoadSourcePluginVerified contract WITHOUT a real
// plugin binary (the heavyweight out-of-process path is plugin_e2e_test.go):
// a tiny executable shell script is enough, because everything under test —
// the malformed-pin refusal and go-plugin's SecureConfig checksum gate — fires
// BEFORE the gRPC handshake. Hermetic: no network, no go toolchain.

// writeFakePlugin writes an executable script that records its execution by
// creating sentinel, so a test can assert whether the "plugin" ever ran.

// execCapableDir devuelve un directorio donde un binario REALMENTE se puede ejecutar, o salta
// diciendo por que. No es lo mismo que un directorio donde se puede escribir.
//
// ⛔ MEDIDO EL 2026-08-19 EN ci-runner-8. Estos casos usaban t.TempDir(), que cae bajo TMPDIR,
// y el paso de CI habia tenido que caer a `_work/_temp` —su diagnostico por candidato lo dice:
// «candidato /dev/shm: NO EJECUTA (rc=127)»—. Ahi el plugin no arranca, y el test fallo con
//
//	loader_test.go:196: the binary never executed despite a matching digest
//	                    (the pin should gate, not block)
//
// que acusa al PIN de bloquear cuando el pin hizo su trabajo: lo que no se pudo fue ejecutar.
// Un test que no distingue «el codigo bloquea» de «esta maquina no ejecuta» acusa al codigo,
// porque es la unica de las dos que sabe nombrar.
//
// Prueba candidatos EJECUTANDO uno de verdad —igual que scripts/lib/exec-workdir.sh hace para
// el shell, y por la misma razon: `test -x` responde por el bit, no por el montaje— y si
// ninguno sirve SALTA en vez de dar un rojo que apunta al sitio equivocado.
// primerAncestroSinPaso devuelve el primer directorio de la cadena hasta / que NO concede el bit
// de busqueda a «otros», o "" si todos lo conceden. Un uid distinto del dueno necesita ese bit en
// CADA componente para llegar al binario; uno solo que falte da EACCES, y ese EACCES se lee igual
// que un montaje noexec.
func primerAncestroSinPaso(dir string) string {
	p, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	for {
		fi, err := os.Stat(p)
		if err != nil {
			return p
		}
		if fi.Mode().Perm()&0o001 == 0 {
			return p
		}
		padre := filepath.Dir(p)
		if padre == p {
			return ""
		}
		p = padre
	}
}

func execCapableDir(t *testing.T) string {
	t.Helper()
	candidatos := []string{os.Getenv("OLIVARES_GATE_BINDIR"), t.TempDir(), "/dev/shm", os.Getenv("HOME"), "/var/tmp", "/tmp"}
	var porque []string
	for _, base := range candidatos {
		if base == "" {
			continue
		}
		dir, err := os.MkdirTemp(base, "x")
		if err != nil {
			porque = append(porque, fmt.Sprintf("%s: no puedo crear dentro (%v)", base, err))
			continue
		}
		// os.MkdirTemp crea 0700, y el hijo enjaulado tiene que ATRAVESAR este directorio. Sin
		// esto, la comprobacion de ancestros de abajo rechaza el directorio que acaba de crear
		// —lo mide antes de que nadie lo abra— y todos los candidatos salen descartados.
		//
		// ⛔ 0733, NO 0711, y la diferencia la pago yo: con 0711 el hijo ATRAVIESA pero no puede
		// CREAR, y estos tests detectan la ejecucion por un fichero centinela que el propio
		// plugin escribe. Bajo root —los runners de CI corren como uid 0 y esta caja no—
		// `plugjail` baja a un uid dedicado, asi que el `touch` del centinela moria con
		// «Permission denied» y el test declaraba «the binary never executed» **con el binario
		// ya ejecutado y su salida en el log**. Medido el 2026-08-19 en la corrida 32254169591:
		// «plugin started … plugin process exited» y, en medio, el touch denegado.
		// El bit de escritura para OTROS es exactamente lo que el centinela necesita; se sigue
		// negando el LISTADO, que es lo que 0711 protegia.
		//
		// ⛔ Y QUE NADIE «ALINEE» ESTO CON PRODUCCION, que es 0711 y esta BIEN. La diferencia es
		// deliberada y el motivo esta medido: `cmd/olivares/firstparty/embed.go` concede el
		// minimo que hace posible el exec, y un plugin de verdad NO escribe en su directorio de
		// extraccion — `core/runtime/plugjail/plugjail_linux.go` no fija `Dir`, ni `Chdir`, ni
		// `TMPDIR`, asi que el hijo hereda el cwd del padre y go-plugin crea su socket en
		// `os.TempDir()`. Quien necesita escribir aqui es el CENTINELA de estos tests, que es un
		// artefacto del arnes. Igualar los dos modos reintroduce el rojo de la corrida 32254169591
		// (si se baja este a 0711) o ensancha produccion sin motivo (si se sube aquel a 0733).
		_ = os.Chmod(dir, 0o733)
		probe := filepath.Join(dir, "p")
		if err := os.WriteFile(probe, []byte("#!/bin/sh\nexit 7\n"), 0o755); err != nil {
			porque = append(porque, fmt.Sprintf("%s: no puedo escribir (%v)", base, err))
			_ = os.RemoveAll(dir)
			continue
		}
		// EJECUTARLO, no mirarle el bit: un montaje noexec deja el bit puesto y niega el execve.
		err = exec.Command(probe).Run()
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 7 {
			// ⛔ Y ATRAVESABLE POR OTRO UID, que es la mitad que faltaba. La sonda de arriba
			// responde por el usuario ACTUAL; el plugin lo lanza plugjail bajo un uid dedicado
			// no-root cuando el motor es root, y eso es lo que ocurre en los runners, cuyo
			// servicio corre con HOME=/root. Medido el 2026-08-19 en ci-runner-7: este helper
			// elegia t.TempDir(), que cuelga de /home/runner en 0700, la sonda pasaba —somos el
			// dueno— y el plugin no arrancaba. Es el mismo hueco que scripts/lib/exec-workdir.sh
			// ya tenia cerrado; lo cerre alli y no aqui.
			if falta := primerAncestroSinPaso(dir); falta != "" {
				porque = append(porque, fmt.Sprintf("%s: ejecuta, pero %s no deja pasar a otro uid", base, falta))
				_ = os.RemoveAll(dir)
				continue
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			return dir
		}
		porque = append(porque, fmt.Sprintf("%s: NO EJECUTA (%v)", base, err))
		_ = os.RemoveAll(dir)
	}
	t.Skipf("ningun directorio candidato permite EJECUTAR un binario, asi que este caso no puede "+
		"medir si el pin deja pasar: %s", strings.Join(porque, " · "))
	return ""
}

func writeFakePlugin(t *testing.T, dir string) (bin, sentinel string) {
	t.Helper()
	sentinel = filepath.Join(dir, "executed")
	bin = filepath.Join(dir, "fake-source")
	script := fmt.Sprintf("#!/bin/sh\ntouch %q\nexit 0\n", sentinel)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, sentinel
}

// TestLoadSourcePluginVerifiedMalformedDigest: a supplied-but-unusable pin must
// refuse BEFORE any file access or exec (deny-closed: it never degrades to an
// unpinned launch). The path intentionally does not exist — if the loader
// touched the filesystem before validating the pin, the error would be about
// the missing file, not the digest.
func TestLoadSourcePluginVerifiedMalformedDigest(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	for _, bad := range []string{
		"",                                  // no pin at all
		"deadbeef",                          // too short
		strings.Repeat("z", 64),             // right length, not hex
		strings.Repeat("a", 63),             // odd length
		"sha256:" + strings.Repeat("a", 64), // prefix is the GATE's job; the runtime takes raw hex
	} {
		err := rt.LoadSourcePluginVerified(missing, sdk.Config{}, "tenant-x", bad)
		if err == nil {
			t.Fatalf("digest %q: malformed pin must refuse, got nil error", bad)
		}
		if !strings.Contains(err.Error(), "not a sha256 hex digest") {
			t.Errorf("digest %q: error must explain the unusable pin, got %v", bad, err)
		}
	}
}

func TestLoadContentSourcePluginVerifiedMalformedDigest(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	for _, bad := range []string{
		"",
		"deadbeef",
		strings.Repeat("z", 64),
		strings.Repeat("a", 63),
		"sha256:" + strings.Repeat("a", 64),
	} {
		_, err := rt.LoadContentSourcePluginVerified(missing, sdk.Config{}, "tenant-x", bad)
		if err == nil {
			t.Fatalf("digest %q: malformed pin must refuse, got nil error", bad)
		}
		if !strings.Contains(err.Error(), "not a sha256 hex digest") {
			t.Errorf("digest %q: error must explain the unusable pin, got %v", bad, err)
		}
	}
}

// TestLoadSourcePluginVerifiedChecksumMismatch: a well-formed pin that does not
// match the on-disk binary makes go-plugin refuse to launch (the exec-time
// integrity gate) — the binary must never run.
func TestLoadSourcePluginVerifiedChecksumMismatch(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	bin, sentinel := writeFakePlugin(t, execCapableDir(t))

	wrong := strings.Repeat("0", 64) // valid hex, not the script's digest
	err := rt.LoadSourcePluginVerified(bin, sdk.Config{}, "tenant-x", wrong)
	if err == nil {
		t.Fatal("checksum mismatch must refuse to launch, got nil error")
	}
	if !errors.Is(err, goplugin.ErrChecksumsDoNotMatch) {
		t.Errorf("error must be the go-plugin checksum refusal, got %v", err)
	}
	if _, statErr := os.Stat(sentinel); statErr == nil {
		t.Error("the plugin RAN despite a checksum mismatch (the exec-time pin is broken)")
	}
}

func TestLoadContentSourcePluginVerifiedChecksumMismatch(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	bin, sentinel := writeFakePlugin(t, execCapableDir(t))

	wrong := strings.Repeat("0", 64)
	_, err := rt.LoadContentSourcePluginVerified(bin, sdk.Config{}, "tenant-x", wrong)
	if err == nil {
		t.Fatal("checksum mismatch must refuse to launch, got nil error")
	}
	if !errors.Is(err, goplugin.ErrChecksumsDoNotMatch) {
		t.Errorf("error must be the go-plugin checksum refusal, got %v", err)
	}
	if _, statErr := os.Stat(sentinel); statErr == nil {
		t.Error("the plugin RAN despite a checksum mismatch (the exec-time pin is broken)")
	}
}

// TestDispenseOutputPluginVerifiedMalformedDigest: the external OUTPUT twin
// of the source pin — a supplied-but-unusable digest refuses BEFORE any file access
// or exec, never degrading to an unpinned launch.
func TestDispenseOutputPluginVerifiedMalformedDigest(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	for _, bad := range []string{
		"",
		"deadbeef",
		strings.Repeat("z", 64),
		strings.Repeat("a", 63),
		"sha256:" + strings.Repeat("a", 64),
	} {
		conn, client, err := rt.DispenseOutputPluginVerified(missing, bad)
		if err == nil {
			t.Fatalf("digest %q: malformed pin must refuse, got nil error", bad)
		}
		if conn != nil || client != nil {
			t.Errorf("digest %q: a refused pin must not dispense a connector/client", bad)
		}
		if !strings.Contains(err.Error(), "not a sha256 hex digest") {
			t.Errorf("digest %q: error must explain the unusable pin, got %v", bad, err)
		}
	}
}

// TestDispenseOutputPluginVerifiedChecksumMismatch: a well-formed pin that
// does not match the on-disk binary makes go-plugin refuse to launch — the external
// output binary must never run on a mismatch.
func TestDispenseOutputPluginVerifiedChecksumMismatch(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	bin, sentinel := writeFakePlugin(t, execCapableDir(t))

	wrong := strings.Repeat("0", 64) // valid hex, not the script's digest
	conn, client, err := rt.DispenseOutputPluginVerified(bin, wrong)
	if err == nil {
		t.Fatal("checksum mismatch must refuse to launch, got nil error")
	}
	if conn != nil || client != nil {
		t.Error("a checksum mismatch must not dispense a connector/client")
	}
	if !errors.Is(err, goplugin.ErrChecksumsDoNotMatch) {
		t.Errorf("error must be the go-plugin checksum refusal, got %v", err)
	}
	if _, statErr := os.Stat(sentinel); statErr == nil {
		t.Error("the plugin RAN despite a checksum mismatch (the exec-time pin is broken)")
	}
}

// TestLoadSourcePluginVerifiedCorrectDigestReachesExec: with the CORRECT digest
// the checksum gate passes and the binary actually executes (the sentinel
// appears); the load still fails afterwards — the script is not a real
// go-plugin server, so the handshake dies — but with a NON-checksum error.
// Together with the mismatch test this proves the pin, and only the pin, gates
// exec.
func TestLoadSourcePluginVerifiedCorrectDigestReachesExec(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	bin, sentinel := writeFakePlugin(t, execCapableDir(t))
	raw, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)

	err = rt.LoadSourcePluginVerified(bin, sdk.Config{}, "tenant-x", hex.EncodeToString(sum[:]))
	if err == nil {
		t.Fatal("a non-handshaking script must fail to load (it is not a plugin)")
	}
	if errors.Is(err, goplugin.ErrChecksumsDoNotMatch) {
		t.Errorf("correct digest must pass the checksum gate, got %v", err)
	}
	if _, statErr := os.Stat(sentinel); statErr != nil {
		t.Errorf("no sentinel at %s: the binary did not execute, OR it executed and could not "+
			"write there (a jailed uid needs w+x on the directory). The pin must gate, not block. "+
			"stat: %v", sentinel, statErr)
	}
}

func TestLoadContentSourcePluginVerifiedCorrectDigestReachesExec(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	bin, sentinel := writeFakePlugin(t, execCapableDir(t))
	raw, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)

	_, err = rt.LoadContentSourcePluginVerified(bin, sdk.Config{}, "tenant-x", hex.EncodeToString(sum[:]))
	if err == nil {
		t.Fatal("a non-handshaking script must fail to load (it is not a plugin)")
	}
	if errors.Is(err, goplugin.ErrChecksumsDoNotMatch) {
		t.Errorf("correct digest must pass the checksum gate, got %v", err)
	}
	if _, statErr := os.Stat(sentinel); statErr != nil {
		t.Errorf("no sentinel at %s: the binary did not execute, OR it executed and could not "+
			"write there (a jailed uid needs w+x on the directory). The pin must gate, not block. "+
			"stat: %v", sentinel, statErr)
	}
}
