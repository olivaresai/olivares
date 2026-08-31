// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/olivaresai/olivares/core/runtime/plugjail"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkplugin "github.com/olivaresai/olivares/sdk/plugin"
)

// LoadSourcePlugin launches the plugin executable at path, dispenses its source
// connector over gRPC and registers it exactly like an in-process source. The
// plugin process is tracked and killed on Stop. If the plugin crashes later,
// go-plugin tears the connection down; the source's Gather returns an error and
// it is marked failed (failure isolation).
func (r *Runtime) LoadSourcePlugin(path string, cfg sdk.Config, tenant string) error {
	// secure=nil: first-party plugin binaries are extracted from the host's own
	// go:embed set (cmd/olivares/firstparty) — their provenance IS the release
	// build the engine itself shipped in, so there is no separate artifact to pin.
	// External (third-party) binaries go through LoadSourcePluginVerified instead.
	raw, client, err := r.dispense(path, sdkplugin.SourcePluginMap(), sdkplugin.SourcePluginName, nil)
	if err != nil {
		return err
	}
	conn, ok := raw.(sdk.SourceConnector)
	if !ok {
		client.Kill()
		return fmt.Errorf("runtime: plugin %q did not dispense a SourceConnector (%T)", path, raw)
	}
	if err := r.AddSource(conn, cfg, tenant); err != nil {
		client.Kill()
		return err
	}
	r.trackClient(client)
	r.linkSourceClient(conn.Descriptor().Name, client)
	return nil
}

// LoadSourcePluginVerified launches an EXTERNAL (third-party) source-connector
// plugin with its sha256 pinned at exec time — the S142 external-plugin path.
// sha256Hex is the operator-pinned artifact digest (lowercase hex, normalized by
// the admission gate); it is decoded into a go-plugin SecureConfig, which makes
// go-plugin RE-HASH the binary on disk immediately before exec and refuse to
// launch on any mismatch — so the verified bytes are the executed bytes (the
// TOCTOU pin: a binary swapped between verification and launch never runs).
// A malformed digest is refused outright without touching the file: a
// supplied-but-unusable pin refuses, it never degrades to an unpinned launch.
//
// Signature verification (the Sigstore/DSSE attestation over this digest against
// the operator's trust policy) happens in the composition root BEFORE this call
// (cmd/olivares/externalplugins.go, admitExternalPlugin) — this method
// enforces the integrity pin, not the trust decision. After the pin, the flow is
// identical to LoadSourcePlugin: dispense over gRPC (AutoMTLS), register, track.
func (r *Runtime) LoadSourcePluginVerified(path string, cfg sdk.Config, tenant string, sha256Hex string) error {
	sum, err := hex.DecodeString(sha256Hex)
	if err != nil || len(sum) != sha256.Size {
		return fmt.Errorf("runtime: external plugin %q: pinned digest is not a sha256 hex digest (a supplied-but-unusable pin refuses, never degrades to an unpinned launch)", path)
	}
	secure := &goplugin.SecureConfig{Checksum: sum, Hash: sha256.New()}
	raw, client, err := r.dispense(path, sdkplugin.SourcePluginMap(), sdkplugin.SourcePluginName, secure)
	if err != nil {
		return err
	}
	conn, ok := raw.(sdk.SourceConnector)
	if !ok {
		client.Kill()
		return fmt.Errorf("runtime: plugin %q did not dispense a SourceConnector (%T)", path, raw)
	}
	if err := r.AddSource(conn, cfg, tenant); err != nil {
		client.Kill()
		return err
	}
	r.trackClient(client)
	r.linkSourceClient(conn.Descriptor().Name, client)
	return nil
}

// LoadOutputPlugin launches the plugin at path, dispenses its output connector
// and registers it like an in-process output.
func (r *Runtime) LoadOutputPlugin(path string, cfg sdk.Config, types []event.Type) error {
	conn, client, err := r.DispenseOutputPlugin(path)
	if err != nil {
		return err
	}
	if err := r.AddOutput(conn, cfg, types); err != nil {
		client.Kill()
		return err
	}
	r.trackClient(client)
	return nil
}

// DispenseOutputPlugin launches the plugin at path and dispenses its output
// connector WITHOUT registering it as a bus output — the caller owns Open and use
// (the notify-destination path calls the connector's Notify directly). The
// returned client is NOT yet tracked: the caller must either TrackOutputPlugin it
// (so Stop closes the connector and kills the subprocess) or Kill it on a failed
// Open.
func (r *Runtime) DispenseOutputPlugin(path string) (sdk.OutputConnector, *goplugin.Client, error) {
	// secure=nil: like LoadSourcePlugin, output plugins are first-party embedded
	// binaries (the notify composition has no external-plugin wiring).
	raw, client, err := r.dispense(path, sdkplugin.OutputPluginMap(), sdkplugin.OutputPluginName, nil)
	if err != nil {
		return nil, nil, err
	}
	conn, ok := raw.(sdk.OutputConnector)
	if !ok {
		client.Kill()
		return nil, nil, fmt.Errorf("runtime: plugin %q did not dispense an OutputConnector (%T)", path, raw)
	}
	return conn, client, nil
}

// DispenseOutputPluginVerified is the EXTERNAL (third-party) twin of
// DispenseOutputPlugin: it launches the output-connector plugin at path with its
// sha256 pinned at exec time and dispenses the connector WITHOUT registering it as
// a bus output (the external notify-destination path — the caller owns Open,
// Notify and teardown, exactly like DispenseOutputPlugin). sha256Hex is the
// operator-pinned digest the composition root already verified via a Sigstore/DSSE
// attestation (cmd/olivares/externalplugins.go, admitExternalPlugin); here it is
// decoded into a go-plugin SecureConfig so go-plugin RE-HASHES the binary on disk
// immediately before exec and refuses to launch on any mismatch — the verified
// bytes are the executed bytes (the TOCTOU pin). A malformed digest is refused
// outright without touching the file: a supplied-but-unusable pin refuses, it never
// degrades to an unpinned launch (the LoadSourcePluginVerified posture). Signature
// verification happens BEFORE this call; this method enforces the integrity pin.
// The returned client is NOT yet tracked: the caller must TrackOutputPlugin it on a
// successful Open or Kill it on failure.
func (r *Runtime) DispenseOutputPluginVerified(path string, sha256Hex string) (sdk.OutputConnector, *goplugin.Client, error) {
	sum, err := hex.DecodeString(sha256Hex)
	if err != nil || len(sum) != sha256.Size {
		return nil, nil, fmt.Errorf("runtime: external output plugin %q: pinned digest is not a sha256 hex digest (a supplied-but-unusable pin refuses, never degrades to an unpinned launch)", path)
	}
	secure := &goplugin.SecureConfig{Checksum: sum, Hash: sha256.New()}
	raw, client, err := r.dispense(path, sdkplugin.OutputPluginMap(), sdkplugin.OutputPluginName, secure)
	if err != nil {
		return nil, nil, err
	}
	conn, ok := raw.(sdk.OutputConnector)
	if !ok {
		client.Kill()
		return nil, nil, fmt.Errorf("runtime: plugin %q did not dispense an OutputConnector (%T)", path, raw)
	}
	return conn, client, nil
}

// TrackOutputPlugin registers a successfully opened, standalone output plugin for
// full lifecycle management: Stop calls the SDK Close method first, then kills the
// subprocess. A nil connector/client is ignored defensively (and lets lifecycle
// tests exercise Close without constructing a real go-plugin client).
func (r *Runtime) TrackOutputPlugin(conn sdk.OutputConnector, client *goplugin.Client) {
	r.mu.Lock()
	if r.stopped {
		// The runtime is already stopping and has snapshotted its teardown set, so a
		// plugin appended now would never be Closed/Killed by Stop — an orphan. Tear it
		// down here instead (Kill + release the confinement), the same self-healing a
		// reconcile racing shutdown needs.
		r.mu.Unlock()
		if client != nil {
			client.Kill()
		}
		r.RunPluginCleanup(client)
		return
	}
	if conn != nil {
		r.standaloneOutputs = append(r.standaloneOutputs, conn)
	}
	if client != nil {
		r.clients = append(r.clients, client)
	}
	r.mu.Unlock()
}

// UntrackOutputPlugin reverses TrackOutputPlugin: it removes conn/client from the
// Stop-teardown slices so a live-reloaded external output destination — whose
// caller Closes the connector and Kills the subprocess itself the moment it is
// swapped out — is not Closed/Killed a second time by Stop. Kill and Close are both
// idempotent, so this is hygiene (an accurate slice, no confusing double teardown),
// mirroring untrackClient for source plugins. A nil conn/client is ignored.
func (r *Runtime) UntrackOutputPlugin(conn sdk.OutputConnector, client *goplugin.Client) {
	r.mu.Lock()
	if conn != nil {
		for i, x := range r.standaloneOutputs {
			if x == conn {
				r.standaloneOutputs = append(r.standaloneOutputs[:i], r.standaloneOutputs[i+1:]...)
				break
			}
		}
	}
	if client != nil {
		for i, x := range r.clients {
			if x == client {
				r.clients = append(r.clients[:i], r.clients[i+1:]...)
				break
			}
		}
	}
	r.mu.Unlock()
}

// dispense starts a plugin process and returns the dispensed implementation plus
// its client (so the caller can Kill it on failure or track it for Stop). secure,
// when non-nil, is the S142 exec-time integrity pin for EXTERNAL binaries:
// go-plugin re-hashes the file right before launching it and refuses to start on
// a checksum mismatch (goplugin.ErrChecksumsDoNotMatch). First-party callers pass
// nil (embedded binaries — see LoadSourcePlugin).
func (r *Runtime) dispense(path string, plugins goplugin.PluginSet, name string, secure *goplugin.SecureConfig) (any, *goplugin.Client, error) {
	// confine the plugin subprocess. Env scoping is the load-bearing control — the
	// plugin must NOT inherit the engine environment (every connector secret + KMS/signing
	// key). Dedicated uid + cgroup ceilings apply on Linux and degrade honestly elsewhere;
	// the per-launch attestation records the real level. SkipHostEnv stops go-plugin from
	// re-adding os.Environ() on top of plugjail's scoped env.
	cmd := exec.Command(path) // #nosec G204 -- operator-admitted, digest-pinned plugin binary (externalplugins.go); confined below
	att, cleanup, jerr := plugjail.Apply(cmd, plugjail.Default(filepath.Base(path)))
	if jerr != nil {
		return nil, nil, fmt.Errorf("runtime: confine plugin %q: %w", path, jerr)
	}
	r.log.Info("plugin launched under confinement",
		"plugin", att.Plugin, "level", att.Level, "env_scoped", att.EnvScoped,
		"dedicated_uid", att.DedicatedUID, "cgroup", att.Cgroup,
		"platform", att.Platform, "degraded", att.Degraded)

	client := goplugin.NewClient(&goplugin.ClientConfig{
		HandshakeConfig:  sdkplugin.Handshake,
		Plugins:          plugins,
		Cmd:              cmd,
		SkipHostEnv:      true, // C1: never inherit the engine env; plugjail set the scoped env
		AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
		// AutoMTLS: the engine mints a one-time certificate pair per plugin launch
		// and pins it on both ends, so the localhost subprocess gRPC channel
		// (engine<->connector, the in-host "collector" boundary) is mutually
		// authenticated and encrypted with zero operator setup. Closes the
		// loopback interposition window; the magic cookie is not a security
		// boundary (sdk/plugin/handshake.go). docs/SECURITY-HARDENING.md, §6.
		AutoMTLS: true,
		// SecureConfig (nil for first-party): checksum-pins the executable at exec
		// time (S142). The decision of WHAT digest to pin — and whether the binary's
		// attestation verifies — was made by the composition root before this point.
		SecureConfig: secure,
	})
	rpc, err := client.Client()
	plugjail.CloseSpawnFD(cmd) // the CgroupFD (if any) is done at spawn; don't hold it for the plugin's lifetime
	if err != nil {
		// Kill BEFORE cleanup so the uid is released (in cleanup) only after the subprocess is
		// dead — client.Kill() blocks until exit; cleanup's cgroup reap can be a no-op when no
		// cgroup is delegated, so the uid would otherwise be freed while the process is still
		// live and a racing launch could become co-resident at that uid (F8).
		client.Kill()
		cleanup()
		return nil, nil, fmt.Errorf("runtime: connect plugin %q: %w%s", path, err, noexecHint(path, err))
	}
	raw, err := rpc.Dispense(name)
	if err != nil {
		client.Kill() // as above: kill (blocks until exit) BEFORE cleanup releases the uid (F8)
		cleanup()
		return nil, nil, fmt.Errorf("runtime: dispense %q from plugin %q: %w", name, path, err)
	}
	// Success: the plugin is running in its confinement. Record its cgroup cleanup
	// KEYED BY CLIENT so a LIVE teardown (external-output reload/remove, source
	// live-remove) can run it immediately via RunPluginCleanup; otherwise Stop drains
	// whatever is left. Refuse if the runtime is already stopping — a reconcile racing
	// shutdown must never launch an orphan past Stop's teardown snapshot. Both the
	// stopped check and Stop's snapshot are under r.mu, so this is race-free.
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		client.Kill() // kill (blocks until exit) BEFORE cleanup releases the uid (F8)
		if cleanup != nil {
			cleanup()
		}
		return nil, nil, fmt.Errorf("runtime: refusing to launch plugin %q: the runtime is stopping", path)
	}
	if cleanup != nil {
		r.pluginCleanupByClient[client] = cleanup
	}
	r.mu.Unlock()
	return raw, client, nil
}

// RunPluginCleanup runs and unregisters the confinement cleanup (cgroup.kill over the
// whole subtree + RemoveAll of the cgroup dir) for one plugin client — the live
// teardown of an external output destination or a source after its
// client is Killed, so a reload/remove reclaims the confinement immediately instead of
// leaking it until Stop and orphaning any process the plugin forked inside its cgroup.
// It is idempotent (a no-op if the client was never tracked or already reclaimed) and
// nil-safe.
func (r *Runtime) RunPluginCleanup(client *goplugin.Client) {
	if client == nil {
		return
	}
	r.mu.Lock()
	fn := r.pluginCleanupByClient[client]
	delete(r.pluginCleanupByClient, client)
	r.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (r *Runtime) trackClient(c *goplugin.Client) {
	r.mu.Lock()
	r.clients = append(r.clients, c)
	r.mu.Unlock()
}

// linkSourceClient records that a source is backed by the out-of-process plugin
// client c, so a live remove Kills exactly that subprocess. The client is
// ALSO tracked in r.clients for Stop's blanket teardown (trackClient); a live
// remove untracks it there first so it is never Killed twice. Pre-Start plugin
// sources are linked here too, so a source added at boot can still be removed
// live (its name resolves through srcIndex).
func (r *Runtime) linkSourceClient(name string, c *goplugin.Client) {
	r.mu.Lock()
	if reg, ok := r.srcIndex[name]; ok {
		reg.client = c
	}
	r.mu.Unlock()
}

// untrackClient removes c from r.clients so a live-removed plugin source's
// subprocess is not Killed a second time by Stop. (goplugin Kill is idempotent,
// but keeping the slice accurate avoids a confusing double teardown.)
func (r *Runtime) untrackClient(c *goplugin.Client) {
	r.mu.Lock()
	for i, x := range r.clients {
		if x == c {
			r.clients = append(r.clients[:i], r.clients[i+1:]...)
			break
		}
	}
	r.mu.Unlock()
}

// noexecHint distinguishes the causes around a refused exec: EACCES may mean a
// missing execute/search bit or a noexec mount, while ENOEXEC may mean an invalid
// executable format. The diagnostic names the extraction mount and both supported
// relocation controls (TMPDIR and data-dir) when the mount remains a candidate.
//
// ⛔ NO ES COSMETICO. Medido el 2026-08-18 en CI: `examples` murio en un runner con
// «fork/exec …: permission denied» sobre un fichero que el extractor deja en 0700 a proposito
// (cmd/olivares/firstparty/embed.go:90, «force 0700 so the subprocess is launchable»). Con el bit
// puesto, el mensaje senala al sitio equivocado y manda a revisar permisos que ya son correctos;
// la causa era el montaje. Reproducido con control positivo y negativo: el MISMO fichero 0700
// falla en un tmpfs `noexec` y corre en uno normal.
//
// It stays deliberately cheap and quiet when uncertain: if EACCES arrives but
// the mode cannot be read, it says nothing. ENOEXEC gets a separate format/mount
// diagnostic because permission-bit inspection cannot explain that errno.
func noexecHint(path string, err error) string {
	return noexecHintFor(path, err, os.Geteuid())
}

// noexecHintFor recibe el euid como ARGUMENTO en vez de leerlo del proceso, y no es ceremonia:
// las tres causas que distingue dependen de si el motor es root, y una bateria no corre como
// root. Con `os.Geteuid()` leido dentro, los casos que importan quedaban INCOMPROBABLES en la
// maquina donde se escriben — la misma vacuidad que se midio el 2026-08-19 en
// cmd/olivares/firstparty, donde borrar entero un `os.Chmod` dejaba su test en verde.
func noexecHintFor(path string, err error, euid int) string {
	if errors.Is(err, syscall.ENOEXEC) {
		return fmt.Sprintf(" (exec returned ENOEXEC for %s: verify the executable format and the mount containing %s; if that mount forbids execution, set TMPDIR to an executable mount or place --data-dir / OLIVARES_DATA_DIR on an executable, writable mount)",
			path, filepath.Dir(path))
	}
	if !errors.Is(err, syscall.EACCES) && !errors.Is(err, os.ErrPermission) {
		return ""
	}

	// ⛔ Y EL BIT QUE SE MIRA DEPENDE DE QUIEN VA A ATRAVESAR. Lo encontro otro carril
	// contrastando este mismo instrumento: el bucle preguntaba por `0o100`, el bit del DUENO,
	// mientras el fallo que explica es de OTRO uid — con el motor como root, plugjail baja el
	// hijo a un uid dedicado no-root ANTES del execve. Un directorio 0700 tiene el bit del
	// dueno puesto, asi que el bucle NO disparaba.
	//
	// No mordia mientras la extraccion dejaba TODO en 0700, porque la comprobacion del binario
	// disparaba primero. Mordia justo al aplicar el arreglo A MEDIAS —binario 0711, directorios
	// aun 0700—, y entonces este mensaje llegaba a afirmar «todos sus directorios son
	// atravesables»: falso para el uid enjaulado, y apuntando lejos de la causa precisamente
	// cuando el diagnostico es lo unico que queda.
	bitBusqueda, deQuien := os.FileMode(0o100), "su dueno"
	if euid == 0 {
		bitBusqueda, deQuien = 0o001, "el uid dedicado no-root bajo el que plugjail lanza el plugin"
	}

	// ⛔ LOS ANCESTROS VAN PRIMERO, y el orden es el arreglo. Un directorio sin bit de BUSQUEDA no
	// solo impide ejecutar lo que hay dentro: impide hasta hacerle `stat`. La primera version
	// miraba el modo del binario antes que nada y salia por «no he podido mirar» justo en el caso
	// que venia a explicar. Lo destapo su propio test, no una corrida.
	//
	// Y la distincion importa porque las DOS causas dan el mismo EACCES —reproducido el 2026-08-19
	// con un intermedio en `drw-------`: rc=126, texto identico al de un montaje noexec— pero solo
	// una se arregla desde el codigo que creo el directorio.
	for dir := filepath.Dir(path); ; {
		d, statErr := os.Stat(dir)
		if statErr == nil && d.IsDir() && d.Mode().Perm()&bitBusqueda == 0 {
			return fmt.Sprintf(" (el directorio %s es %v: no concede bit de BUSQUEDA a %s, y sin atravesarlo execve responde EACCES igual que un montaje noexec)",
				dir, d.Mode().Perm(), deQuien)
		}
		padre := filepath.Dir(dir)
		if padre == dir {
			break
		}
		dir = padre
	}

	fi, statErr := os.Stat(path)
	if statErr != nil || fi.Mode().Perm()&0o111 == 0 {
		return "" // sin bit de ejecucion: el mensaje de siempre ya apunta bien
	}
	// ⛔ Y LA TERCERA CAUSA, que es la que de verdad muerde y la que estas dos primeras versiones
	// atribuyeron mal al montaje: si el motor corre como ROOT, plugjail baja el hijo a un uid
	// DEDICADO NO-ROOT (plugjail_linux.go:37) antes del execve. Un binario 0700 y unos directorios
	// 0700 pertenecen al motor, no a ese uid — asi que el hijo no puede ni atravesar ni ejecutar, y
	// el kernel responde EACCES: el mismo «permission denied» que un noexec.
	//
	// Se distingue sin adivinar: si somos root y el modo no da permiso al OTRO, esa es la causa.
	if euid == 0 && fi.Mode().Perm()&0o001 == 0 {
		return fmt.Sprintf(" (el motor corre como ROOT, asi que el plugin se lanza bajo un uid DEDICADO no-root; %s es %v y no concede permiso a ese uid — de ahi el EACCES. No es el montaje: es que la extraccion escribe 0700 y el jail cambia de identidad antes de ejecutar)",
			path, fi.Mode().Perm())
	}

	return fmt.Sprintf(" (the binary has an execute bit (%v) and its directory chain is traversable; the mount containing %s is probably noexec. Set TMPDIR to an executable mount, or place --data-dir / OLIVARES_DATA_DIR on an executable, writable mount)",
		fi.Mode().Perm(), filepath.Dir(path))
}
