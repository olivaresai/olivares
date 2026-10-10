// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/olivaresai/olivares/core/runtime/confine"
	"github.com/olivaresai/olivares/core/runtime/plugjail"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkplugin "github.com/olivaresai/olivares/sdk/plugin"
)

// LoadSourcePlugin launches the plugin executable at path, dispenses its source
// connector over gRPC and registers it exactly like an in-process source. The
// plugin process is tracked and killed on Stop. If the plugin process dies later,
// the source's Gather returns an error, the engine stays up (failure isolation) and
// the gather loop starts the same binary again (restartDeadPlugin).
func (r *Runtime) LoadSourcePlugin(path string, cfg sdk.Config, tenant string) error {
	// secure=nil: first-party plugin binaries are extracted from the host's own
	// go:embed set (cmd/olivares/firstparty) — their provenance IS the release
	// build the engine itself shipped in, so there is no separate artifact to pin.
	// External (third-party) binaries go through LoadSourcePluginVerified instead.
	return r.loadSourcePlugin("", pluginLaunch{path: path}, cfg, tenant)
}

// LoadSourcePluginNamed is LoadSourcePlugin with the registration name supplied by
// the composition root, so the SAME first-party plugin binary can back two roster
// rows: each launch is its own subprocess with its own config, its own name and
// its own lifecycle. The plugin's Descriptor is left completely alone — it is the
// component identity that validation, diagnosis and admission need, and it is
// recorded beside the registration name rather than wrapped or faked.
func (r *Runtime) LoadSourcePluginNamed(name, path string, cfg sdk.Config, tenant string) error {
	if err := validRegistrationName(name); err != nil {
		return err
	}
	return r.loadSourcePlugin(name, pluginLaunch{path: path}, cfg, tenant) // no pin: first-party, as above
}

// LoadSourcePluginVerified launches an EXTERNAL (third-party) source-connector
// plugin with its sha256 pinned at exec time — the S142 external-plugin path.
// sha256Hex is the operator-pinned artifact digest (lowercase hex, normalized by
// the admission gate); it is decoded into a go-plugin SecureConfig, which makes
// go-plugin RE-HASH the binary on disk immediately before exec and refuse to
// launch on a checksum mismatch. The path hash-then-exec window is unchanged;
// execution is not sealed against a path replacement after that check.
// A malformed digest is refused outright without touching the file: a
// supplied-but-unusable pin refuses, it never degrades to an unpinned launch.
//
// Signature verification (the Sigstore/DSSE attestation over this digest against
// the operator's trust policy) happens in the composition root BEFORE this call
// (cmd/olivares/externalplugins.go, admitExternalPlugin) — this method
// enforces the integrity pin, not the trust decision. After the pin, the flow is
// identical to LoadSourcePlugin: dispense over gRPC (AutoMTLS), register, track.
func (r *Runtime) LoadSourcePluginVerified(path string, cfg sdk.Config, tenant string, sha256Hex string) error {
	launch, err := pinnedLaunch(path, sha256Hex)
	if err != nil {
		return err
	}
	return r.loadSourcePlugin("", launch, cfg, tenant)
}

// LoadSourcePluginVerifiedNamed is LoadSourcePluginVerified with an explicit
// registration name. The admission posture is untouched: the digest is decoded
// first and a malformed pin refuses without touching the file, then go-plugin
// re-hashes the binary at exec. Naming the SOURCE changes nothing about verifying
// the COMPONENT.
func (r *Runtime) LoadSourcePluginVerifiedNamed(name, path string, cfg sdk.Config, tenant string, sha256Hex string) error {
	if err := validRegistrationName(name); err != nil {
		return err
	}
	launch, err := pinnedLaunch(path, sha256Hex)
	if err != nil {
		return err
	}
	return r.loadSourcePlugin(name, launch, cfg, tenant)
}

// loadSourcePlugin is the shared launch+register path. An empty regName is the
// LEGACY, name-less mode used only by the deprecated entry points above: there the
// connector's Descriptor name is the registration identity, exactly as before. The
// exported Named variants reject an empty name before reaching here, so a
// configured source can never fall back to its descriptor by accident.
func (r *Runtime) loadSourcePlugin(regName string, launch pluginLaunch, cfg sdk.Config, tenant string) error {
	conn, client, err := r.launchSource(launch)
	if err != nil {
		return err
	}
	name := regName
	if name == "" {
		name = conn.Descriptor().Name
		err = r.AddSource(conn, cfg, tenant)
	} else {
		err = r.AddSourceNamed(name, conn, cfg, tenant)
	}
	if err != nil {
		// A refused registration reaps the subprocess AND releases its
		// confinement: a rejected load must leave no forked child and no cgroup dir.
		client.Kill()
		r.RunPluginCleanup(client)
		return err
	}
	r.trackClient(client)
	r.linkSourceClient(name, client, launch)
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
// Open. The connector is supervised: when a Notify fails because the plugin process
// died, the runtime starts the same binary again (supervisedOutput), so the client
// the caller holds is only the first process.
//
// secure=nil: like LoadSourcePlugin, output plugins are first-party embedded
// binaries (the notify composition has no external-plugin wiring).
func (r *Runtime) DispenseOutputPlugin(path string) (sdk.OutputConnector, *goplugin.Client, error) {
	return r.dispenseOutput(pluginLaunch{path: path})
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
// digest is checked at the original path before launch; execution is not sealed.
// A malformed digest is refused
// outright without touching the file: a supplied-but-unusable pin refuses, it never
// degrades to an unpinned launch (the LoadSourcePluginVerified posture). Signature
// verification happens BEFORE this call; this method enforces the integrity pin.
// The returned client is NOT yet tracked: the caller must TrackOutputPlugin it on a
// successful Open or Kill it on failure. A restart of a dead plugin checks the pin
// again (see DispenseOutputPlugin).
func (r *Runtime) DispenseOutputPluginVerified(path string, sha256Hex string) (sdk.OutputConnector, *goplugin.Client, error) {
	launch, err := pinnedLaunch(path, sha256Hex)
	if err != nil {
		return nil, nil, err
	}
	return r.dispenseOutput(launch)
}

// dispenseOutput launches an output plugin and wraps its connector so a dead
// process is started again.
func (r *Runtime) dispenseOutput(launch pluginLaunch) (sdk.OutputConnector, *goplugin.Client, error) {
	conn, client, err := r.launchOutput(launch)
	if err != nil {
		return nil, nil, err
	}
	return &supervisedOutput{rt: r, launch: launch, first: client, conn: conn, client: client}, client, nil
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
// go-plugin's checksum check hashes the original path before launching it and refuses to start on
// a checksum mismatch (goplugin.ErrChecksumsDoNotMatch). First-party callers pass
// nil (embedded binaries — see LoadSourcePlugin).
func (r *Runtime) dispense(path string, plugins goplugin.PluginSet, name string, secure *goplugin.SecureConfig) (any, *goplugin.Client, error) {
	// confine the plugin subprocess. Env scoping is the load-bearing control — the
	// plugin must NOT inherit the engine environment (every connector secret + KMS/signing
	// key). Dedicated uid + cgroup ceilings apply on Linux and degrade honestly elsewhere;
	// the per-launch attestation records the real level. SkipHostEnv stops go-plugin from
	// re-adding os.Environ() on top of plugjail's scoped env.
	cmd := exec.Command(path) // #nosec G204 -- operator-admitted, digest-pinned plugin binary (externalplugins.go); confined below
	pluginPath := cmd.Path    // exec.Command's original PATH resolution, before helper wrapping
	att, cleanup, jerr := plugjail.Apply(cmd, plugjail.Default(filepath.Base(path)))
	if jerr != nil {
		return nil, nil, fmt.Errorf("runtime: confine plugin %q: %w", path, jerr)
	}
	// The hash still covers the original plugin path, not the re-exec helper.
	// ponytail: path hash-then-exec window as in go-plugin, sealed execution if a threat model needs it
	if secure != nil && confine.IsWrapped(cmd) {
		ok, err := secure.Check(pluginPath)
		if err != nil || !ok {
			plugjail.CloseSpawnFD(cmd)
			cleanup()
			if err != nil {
				return nil, nil, fmt.Errorf("runtime: connect plugin %q: error verifying checksum: %s", path, err)
			}
			return nil, nil, fmt.Errorf("runtime: connect plugin %q: %w", path, goplugin.ErrChecksumsDoNotMatch)
		}
		secure = nil // go-plugin must not hash the engine/helper in place of the plugin
	}

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
	att = plugjail.ConfirmLaunch(cmd, att)
	r.log.Info("plugin launched under confinement",
		"plugin", att.Plugin, "level", att.Level, "env_scoped", att.EnvScoped,
		"dedicated_uid", att.DedicatedUID, "cgroup", att.Cgroup,
		"landlock", att.Landlock, "no_new_privs", att.NoNewPrivs,
		"platform", att.Platform, "degraded", att.Degraded)
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

// linkSourceClient records that the source REGISTERED UNDER name is backed by the
// out-of-process plugin client c, started as launch (so the supervisor can restart
// it the same way), and so a live remove Kills exactly that
// subprocess — and, when two rows run the same binary, exactly the right one of the
// two. The client is
// ALSO tracked in r.clients for Stop's blanket teardown (trackClient); a live
// remove untracks it there first so it is never Killed twice. Pre-Start plugin
// sources are linked here too, so a source added at boot can still be removed
// live (its name resolves through srcIndex).
func (r *Runtime) linkSourceClient(name string, c *goplugin.Client, launch pluginLaunch) {
	r.mu.Lock()
	if reg, ok := r.srcIndex[name]; ok {
		reg.client, reg.plugin = c, launch
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
// A 0700 executable can still fail on a noexec mount. When its permission
// bits are correct, pointing only at chmod sends the operator to the wrong cause.
// The same executable succeeds on an executable mount and fails on noexec.
//
// It stays deliberately cheap and quiet when uncertain: if EACCES arrives but
// the mode cannot be read, it says nothing. ENOEXEC gets a separate format/mount
// diagnostic because permission-bit inspection cannot explain that errno.
func noexecHint(path string, err error) string {
	return noexecHintFor(path, err, os.Geteuid())
}

// noexecHintFor takes an explicit euid so tests can exercise both the root
// engine's jailed child and the unprivileged engine without requiring root.
func noexecHintFor(path string, err error, euid int) string {
	if errors.Is(err, syscall.ENOEXEC) {
		return fmt.Sprintf(" (exec returned ENOEXEC for %s: verify the executable format and the mount containing %s; if that mount forbids execution, set TMPDIR to an executable mount or place --data-dir / OLIVARES_DATA_DIR on an executable, writable mount)",
			path, filepath.Dir(path))
	}
	if !errors.Is(err, syscall.EACCES) && !errors.Is(err, os.ErrPermission) {
		return ""
	}

	// Check the search bit for the uid that will traverse the directory.
	// With a root engine, plugjail switches to a dedicated non-root uid
	// before execve. Owner-search on a 0700 directory does not help that uid,
	// even when the binary itself has already been changed to 0711.
	bitBusqueda, deQuien := os.FileMode(0o100), "its owner"
	if euid == 0 {
		bitBusqueda, deQuien = 0o001, "the dedicated non-root uid plugjail launches the plugin under"
	}

	// Check ancestors first: missing directory-search permission can block
	// stat on the binary itself. Both this and noexec produce EACCES, but
	// only the former is fixed by the code that creates the directory.
	for dir := filepath.Dir(path); ; {
		d, statErr := os.Stat(dir)
		if statErr == nil && d.IsDir() && d.Mode().Perm()&bitBusqueda == 0 {
			return fmt.Sprintf(" (directory %s is %v: it grants no search bit to %s, and without traversing it execve answers EACCES just like a noexec mount)",
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
		return "" // without an execute bit, the original error already points at the cause
	}
	// A root engine's dedicated non-root child cannot execute a 0700
	// binary owned by the engine. The resulting EACCES is not mount evidence.
	if euid == 0 && fi.Mode().Perm()&0o001 == 0 {
		return fmt.Sprintf(" (the engine runs as ROOT, so the plugin is launched under a DEDICATED non-root uid; %s is %v and grants no permission to that uid — hence the EACCES. It is not the mount: the extraction writes 0700 and the jail switches identity before executing)",
			path, fi.Mode().Perm())
	}

	return fmt.Sprintf(" (the binary has an execute bit (%v) and its directory chain is traversable; the mount containing %s is probably noexec. Set TMPDIR to an executable mount, or place --data-dir / OLIVARES_DATA_DIR on an executable, writable mount)",
		fi.Mode().Perm(), filepath.Dir(path))
}
