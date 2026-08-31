// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package firstparty embeds the first-party SOURCE-connector plugin binaries into
// the single control-plane artifact and extracts them on demand, so the engine can
// launch them as ISOLATED out-of-process subprocesses (CB-1 transport B) WITHOUT
// linking their dependency trees into the core — the deps/SBOM isolation the
// distributed ingest plane is built on (ARCHITECTURE.md, S02 §1). The single binary is
// preserved: one artifact, one command, the connectors travel inside it.
//
// A release build populates bins/ via `task build:connectors`; a plain
// `go build`/`go test` ships an EMPTY set (only the committed placeholder), and the
// composition root warns honestly when a configured plugin source has no embedded
// binary — never a silent no-op (12 §5). The distributed packaging that builds the
// per-platform binaries is ; this package is the MECHANISM it populates.
package firstparty

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
)

// placeholderName is the committed file that keeps the embed pattern valid when no
// binaries are built in a plain development/test build. Release builds have an
// additional compile-time claude-source requirement in embed_binaries_release.go.
// The placeholder is never a connector and never extracted.
const placeholderName = "PLACEHOLDER"

// ErrNotEmbedded reports that no plugin binary for the requested connector is
// compiled into this build (a plain dev build, or a connector not yet packaged for
// this platform).
var ErrNotEmbedded = errors.New("firstparty: connector plugin not embedded in this build")

// Available returns the embedded connector plugin binary names (excluding the
// placeholder), for diagnostics and honest boot logging.
func Available() []string { return availableIn(bins) }

func availableIn(fsys fs.FS) []string {
	entries, err := fs.ReadDir(fsys, "bins")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || e.Name() == placeholderName {
			continue
		}
		out = append(out, e.Name())
	}
	return out
}

// Extract writes the embedded plugin binary named name to a private, unique file
// under dir (created if needed) and returns its path, marked executable — 0700 when the
// engine is unprivileged, 0711 when it is root. See ExecPerm for why the two differ.
// Unique paths let multiple configured instances of the same plugin run at once;
// reusing dir/name would try to truncate an already-running executable (ETXTBSY on
// Linux). It returns ErrNotEmbedded when no such binary is compiled in, so the
// caller warns and skips — never a silent no-op. The caller owns cleanup of dir on
// shutdown.
func Extract(dir, name string) (string, error) { return extractFrom(bins, dir, name) }

// ExecPerm returns the directory and binary modes the extracted plugin needs in order to be
// LAUNCHABLE, which is not the same question as who may read it.
//
// 0700 is right while the child runs as the extracting process. It stops being right the
// moment it does not: core/runtime/plugjail drops the plugin to a dedicated, per-launch
// non-root uid whenever the engine is root (plugjail_linux.go, `if os.Geteuid() == 0`), so
// the child is a DIFFERENT uid than the one that owns a 0700 directory and a 0700 binary.
// It can then neither traverse the directory nor exec the file, and the kernel says EACCES
// — indistinguishable at a glance from a noexec mount, which is exactly how this was
// misdiagnosed twice before core/runtime.noexecHint learned to tell the three causes apart.
//
// Measured on 2026-08-19 in mainline-ci: the `examples` job failed with
//
//	fork/exec …/claude-source-…: permission denied
//	ingest: source not wired (deny-closed) name=genai-otlp
//
// and the consequence is not limited to CI — ANY deployment whose engine runs as root
// cannot launch a first-party connector at all.
//
// So when, and ONLY when, the engine is root and the drop is therefore going to happen, the
// modes widen by the minimum that makes exec possible:
//
//	dir 0711  traverse, NOT list
//	bin 0711  execute, NOT read
//
// No read bit is granted to group or other in either case, so nothing becomes readable that
// was not; and the binary is shipped in the release artifact, so its bytes were never the
// secret. An unprivileged engine keeps 0700 exactly as before.
//
// The alternative — chown to the plugin's uid — was rejected because the uid is allocated
// per LAUNCH inside the jail, while the extraction directory is SHARED by every connector
// of a run: chowning it to one plugin's uid would lock out its siblings.
//
// ⛔ The condition here MIRRORS plugjail's. If that one changes, this must change with it,
// and TestElPermisoSigueLaCondicionDelJail exists to make the coupling visible rather than
// implicit.
func ExecPerm(euid int) (dirPerm, binPerm os.FileMode) {
	return execPermFor(euid, runtime.GOOS)
}

// execPermFor toma el euid Y el sistema operativo como argumentos, y el segundo no es adorno:
// plugjail solo baja de uid en LINUX. `core/runtime/plugjail/plugjail_other.go` lo dice de si
// mismo — *«on a non-Linux host applies NO OS-level isolation: dedicated uid, cgroup…»*— asi que
// en macOS o Windows corriendo como root NO hay ningun uid distinto que necesite atravesar nada,
// y conceder 0711 alli seria ensanchar un permiso que nadie va a usar.
//
// Encontrado revisando mi propio cambio: la primera version miraba solo el euid, «espejando la
// condicion de plugjail», y esa condicion vive en el fichero con `//go:build linux`. Espejar la
// mitad de una condicion no es espejarla.
//
// Va como argumento por lo mismo que el euid: con runtime.GOOS leido dentro, la rama no-Linux
// seria incomprobable en la maquina donde se escribe, que es Linux.
func execPermFor(euid int, goos string) (dirPerm, binPerm os.FileMode) {
	if euid == 0 && goos == "linux" {
		return 0o711, 0o711
	}
	return 0o700, 0o700
}

func extractFrom(fsys fs.FS, dir, name string) (string, error) {
	dirPerm, binPerm := ExecPerm(os.Geteuid())
	return extractWithPerm(fsys, dir, name, dirPerm, binPerm)
}

// extractWithPerm existe para que los modos sean UN ARGUMENTO y no el euid ambiente. No es
// ceremonia: la primera version del test de esta propiedad era VACUA y lo demostro una
// mutacion — con euid 1000, ExecPerm devuelve 0700, el directorio del caso ya estaba en 0700,
// y quitar entero el `os.Chmod` que el test existia para proteger lo dejaba igual de verde.
// Un test que solo puede distinguir el arreglo cuando corre como root no distingue nada en la
// maquina donde se escribe.
func extractWithPerm(fsys fs.FS, dir, name string, dirPerm, binPerm os.FileMode) (string, error) {
	if name == "" || name == placeholderName {
		return "", fmt.Errorf("%w: %q", ErrNotEmbedded, name)
	}
	data, err := fs.ReadFile(fsys, "bins/"+name)
	if err != nil {
		return "", fmt.Errorf("%w: %q", ErrNotEmbedded, name)
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return "", err
	}
	// Y SE IMPONE, no solo se pide al crear. MkdirAll NO cambia el modo de un directorio que
	// ya existe, y en la practica SIEMPRE existe: los dos llamantes reales —boot y collector—
	// lo crean antes con os.MkdirTemp, que hace 0700. Pedirle el modo a MkdirAll era pedirselo
	// al unico caso que no ocurre.
	//
	// Va AQUI y no en cada llamante a proposito. La primera version de este arreglo puso el
	// chmod en cmd/olivares/boot.go y dejo cmd/olivares/collector.go con el mismo defecto
	// intacto: dos sitios, uno actualizado. Extract es quien decide el modo del binario, asi
	// que es quien tiene que decidir el del directorio que lo contiene.
	if err := os.Chmod(dir, dirPerm); err != nil {
		return "", fmt.Errorf("plugin scratch dir %q: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, name+"-*")
	if err != nil {
		return "", err
	}
	path := f.Name()
	cleanFail := func(err error) (string, error) {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		return cleanFail(err)
	}
	// CreateTemp creates 0600; force the launchable mode chosen above.
	if err := f.Chmod(binPerm); err != nil {
		return cleanFail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}
