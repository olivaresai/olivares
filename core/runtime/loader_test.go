// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/runtime/confine"
	"github.com/olivaresai/olivares/sdk"
	sdkplugin "github.com/olivaresai/olivares/sdk/plugin"
)

// The runtime's command is the real shared helper, served by this test binary.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == confine.HelperArg {
		os.Exit(confine.RunHelper(os.Args[2:]))
	}
	if os.Getenv(sdkplugin.Handshake.MagicCookieKey) == sdkplugin.Handshake.MagicCookieValue {
		plugins := goplugin.PluginSet{sdkplugin.OutputPluginName: &sdkplugin.OutputPlugin{Impl: &ownedOutput{}}}
		switch filepath.Base(os.Args[0]) { // the restart tests copy this binary under a fixture's name
		case streamOutputBinary:
			plugins = goplugin.PluginSet{sdkplugin.OutputPluginName: &sdkplugin.OutputPlugin{Impl: &streamOutput{}}}
		case streamSourceBinary:
			plugins = goplugin.PluginSet{sdkplugin.SourcePluginName: &sdkplugin.SourcePlugin{Impl: &streamSource{}}}
		}
		goplugin.Serve(&goplugin.ServeConfig{HandshakeConfig: sdkplugin.Handshake, Plugins: plugins, GRPCServer: goplugin.DefaultGRPCServer})
		os.Exit(0)
	}
	// Root CI drops plugin credentials before re-exec. Serve the helper from
	// an owned traversable copy, rather than Go's private build directory.
	if os.Geteuid() == 0 && confine.Probe().Mode == confine.ModeLandlock && os.Getenv("OLIVARES_OWNED_LOADER_TEST") != "1" {
		dir, err := os.MkdirTemp("/tmp", "olivares-loader-test-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		self, err := os.Executable()
		if err == nil {
			var image []byte
			image, err = os.ReadFile(self)
			if err == nil {
				err = os.Chmod(dir, 0o755)
			}
			if err == nil {
				err = os.WriteFile(filepath.Join(dir, "engine"), image, 0o755)
			}
		}
		code := 1
		if err == nil {
			cmd := exec.Command(filepath.Join(dir, "engine"), os.Args[1:]...)
			cmd.Env = append(os.Environ(), "OLIVARES_OWNED_LOADER_TEST=1")
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			err = cmd.Run()
			if err == nil {
				code = 0
			} else if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			}
		}
		_ = os.RemoveAll(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// These tests pin the S142 LoadSourcePluginVerified contract WITHOUT a real
// plugin binary (the heavyweight out-of-process path is plugin_e2e_test.go):
// a tiny executable shell script is enough, because everything under test —
// the malformed-pin refusal and go-plugin's SecureConfig checksum gate — fires
// BEFORE the gRPC handshake. Hermetic: no network, no go toolchain.

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
		// A dedicated UID must traverse every parent. The plugin directory is
		// read-only under Landlock; execution is observed on inherited stdout,
		// not by writing a sentinel into that directory.
		_ = os.Chmod(dir, 0o711)
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

// writeFakePlugin reports execution on stdout using shell builtins only.
// It also checks original $0, sibling reads and the owned writable TMPDIR. A
// handshake refusal retains that first output line, so execution is observable
// without granting write access to the executable's directory.
func writeFakePlugin(t *testing.T, dir string) (bin, sentinel string) {
	t.Helper()
	bin = filepath.Join(dir, "fake-source")
	sentinel = "plugin-executed:" + bin
	resource := filepath.Join(dir, "sibling")
	if err := os.WriteFile(resource, []byte("sibling-ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\n[ \"$0\" = %q ] || exit 8\nIFS= read -r sibling < %q || exit 9\n[ \"$sibling\" = sibling-ok ] || exit 10\nif [ -n \"$TMPDIR\" ]; then if [ -n \"$TMPDIR\" ]; then printf scratch > \"$TMPDIR/executed\" || exit 11; fi; fi\nprintf '%%s\\n' %q\nexit 0\n", bin, resource, sentinel)
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
	if strings.Contains(err.Error(), sentinel) {
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
	if strings.Contains(err.Error(), sentinel) {
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
	if strings.Contains(err.Error(), sentinel) {
		t.Error("the plugin RAN despite a checksum mismatch (the exec-time pin is broken)")
	}
}

// TestLoadSourcePluginVerifiedCorrectDigestReachesExec: with the CORRECT digest
// the checksum gate passes and the binary actually executes (the execution marker
// appears in the handshake refusal); the load still fails afterwards — the script is not a real
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
	if !strings.Contains(err.Error(), sentinel) {
		t.Errorf("matching digest did not reach original-path script with sibling read and scratch write: %v", err)
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
	if !strings.Contains(err.Error(), sentinel) {
		t.Errorf("matching digest did not reach original-path script with sibling read and scratch write: %v", err)
	}
}

func TestVerifiedPluginRetainsOriginalAdmissionAndPath(t *testing.T) {
	t.Run("checksum-before-format-inspection", func(t *testing.T) {
		bin := filepath.Join(execCapableDir(t), "invalid-elf")
		if err := os.WriteFile(bin, []byte("\x7fELFnot-an-ELF-header"), 0o755); err != nil {
			t.Fatal(err)
		}
		rt := runtime.New(runtime.Options{Logger: quiet()})
		err := rt.LoadSourcePluginVerified(bin, sdk.Config{}, "tenant-x", strings.Repeat("0", 64))
		if !errors.Is(err, goplugin.ErrChecksumsDoNotMatch) {
			t.Fatalf("wrong digest lost original checksum refusal: %v", err)
		}
	})
	for _, mode := range []string{"relative-path", "resolved-PATH"} {
		t.Run(mode, func(t *testing.T) {
			dir := execCapableDir(t)
			bin, sentinel := writeFakePlugin(t, dir)
			program := filepath.Base(bin)
			expectedZero := program
			if mode == "relative-path" {
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				program, err = filepath.Rel(cwd, bin)
				if err != nil {
					t.Fatal(err)
				}
				expectedZero = program
			} else {
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				// exec.Command resolves Path on PATH; the kernel gives a shebang
				// interpreter that resolved script path as $0.
				expectedZero = bin
			}
			script := fmt.Sprintf("#!/bin/sh\n[ \"$0\" = %q ] || exit 8\nprintf '%%s\\n' %q\n", expectedZero, sentinel)
			if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(script))
			rt := runtime.New(runtime.Options{Logger: quiet()})
			err := rt.LoadSourcePluginVerified(program, sdk.Config{}, "tenant-x", hex.EncodeToString(sum[:]))
			if err == nil || errors.Is(err, goplugin.ErrChecksumsDoNotMatch) || !strings.Contains(err.Error(), sentinel) {
				t.Fatalf("original path/argv/checksum behavior changed: %v", err)
			}
		})
	}
}

type ownedOutput struct{}

func (*ownedOutput) Descriptor() sdk.Descriptor             { return sdk.Descriptor{Name: "owned-output"} }
func (*ownedOutput) Open(context.Context, sdk.Config) error { return nil }
func (*ownedOutput) Close(context.Context) error            { return nil }
func (*ownedOutput) Notify(_ context.Context, n sdk.Notification) error {
	// Observe an actual RPC method running inside the confined SDK child.
	if _, err := os.ReadFile(n.Fields["denied_file"]); !os.IsPermission(err) {
		return fmt.Errorf("ungranted file read: %v", err)
	}
	return os.WriteFile(filepath.Join(os.TempDir(), "notified"), []byte(n.Title), 0o600)
}

func TestVerifiedOwnedSDKPluginStillHandshakes(t *testing.T) {
	if confine.Probe().Mode != confine.ModeLandlock {
		t.Skip(confine.Probe().Reason)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(execCapableDir(t), "owned-output")
	if err := os.WriteFile(bin, image, 0o711); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(image)
	secret := filepath.Join(t.TempDir(), "synthetic-ungranted")
	if err := os.WriteFile(secret, []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	rt := runtime.New(runtime.Options{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	conn, client, err := rt.DispenseOutputPluginVerified(bin, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = conn.Close(t.Context())
		client.Kill()
		rt.RunPluginCleanup(client)
		rt.RunPluginCleanup(client)
	}()
	if conn.Descriptor().Name != "owned-output" {
		t.Fatalf("wrong descriptor: %+v", conn.Descriptor())
	}
	if err := conn.Open(t.Context(), sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Notify(t.Context(), sdk.Notification{Title: "owned RPC proof", Fields: map[string]string{"denied_file": secret}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "landlock=true") || !strings.Contains(logs.String(), "no_new_privs=true") {
		t.Fatalf("successful handshake did not record native controls: %s", logs.String())
	}
	t.Logf("owned SDK output binary_sha256=%x; admitted checksum/AutoMTLS/Describe/Open/Notify/denied-file/scratch-write PASS", sum)
}
