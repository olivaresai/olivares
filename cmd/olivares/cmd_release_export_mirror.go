// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/release"
)

// export-mirror is the CUSTOMER side of the air-gap story, and it is deliberately
// NOT scripts/export-update-bundle.sh.
//
// That script is the VENDOR ceremony: it starts from a release directory of
// goreleaser archives and SIGNS the manifest with the private OTA key. A customer
// has neither. What a customer has is a licence token and a network-facing host,
// and what they need is to walk the licensed `/download` gate once and carry the
// result to machines that will never reach it.
//
// The output format is not invented here: it is exactly what `upgrade --bundle`
// already consumes (bundleSource in cmd_upgrade_source.go) — manifest.json,
// manifest.json.sig and the platform archives by their bare leaf names. Producing
// anything else would be a second format for the same job.
//
// Nothing is re-signed. The signature that ships is the vendor's, and the mirror
// verifies it BEFORE writing anything, so a mirror that completed is a mirror whose
// bytes were checked against the embedded key on the machine that had the network.

type exportMirrorOptions struct {
	endpoint  string
	token     string
	channel   string
	set       string
	out       string
	pubkey    string
	platforms []string
	timeout   time.Duration
	force     bool
}

func newReleaseExportMirrorCmd() *cobra.Command {
	o := &exportMirrorOptions{}
	cmd := &cobra.Command{
		Use:   "export-mirror",
		Short: "Mirror the entitled manifest and artifacts from the licensed gate into an air-gap bundle",
		Long: "export-mirror walks the licensed /download gate with your token and writes an air-gap\n" +
			"bundle that `olivares upgrade --bundle` installs with NO network at all.\n\n" +
			"It fetches the signed per-channel manifest for the entitled --set, verifies that\n" +
			"signature against the embedded (or --pubkey) OTA key BEFORE anything is written, then\n" +
			"downloads each platform archive the manifest names and checks every one against the\n" +
			"digest the signed manifest carries. Nothing is re-signed: what ships is the vendor's\n" +
			"signature, so an air-gapped install verifies byte-identically to an online one.\n\n" +
			"This is the customer half of the air-gap path. The vendor half — building and SIGNING\n" +
			"a bundle from a release directory — is scripts/export-update-bundle.sh and needs the\n" +
			"private OTA key, which a customer does not have and does not need.",
		Example: "  olivares release export-mirror --token $TOKEN --set biz+reg --out ./mirror\n" +
			"  olivares release export-mirror --token $TOKEN --set biz --channel security \\\n" +
			"      --platform linux/amd64 --platform linux/arm64 --out mirror.tar.gz",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runExportMirror(cmd, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.endpoint, "endpoint", "", "licensed worker base URL (required)")
	f.StringVar(&o.token, "token", "", "licence download token (required)")
	f.StringVar(&o.channel, "channel", release.ChannelStable, "release channel: stable | security")
	f.StringVar(&o.set, "set", "", "entitled set slug, e.g. biz+reg (required: the gate never defaults it)")
	f.StringVar(&o.out, "out", "", "output directory, or a path ending in .tar.gz (required)")
	f.StringVar(&o.pubkey, "pubkey", "", "base64 Ed25519 OTA public key (default: the key embedded in this binary)")
	f.StringSliceVar(&o.platforms, "platform", nil, "os/arch to mirror; repeatable (default: every platform the manifest names)")
	// Per REQUEST, not for the whole run: http.Client.Timeout bounds one request/response,
	// and the first version's help said "for the whole mirror", which it never was.
	f.DurationVar(&o.timeout, "timeout", 10*time.Minute, "HTTP timeout for each gate request")
	f.BoolVar(&o.force, "force", false, "replace a non-empty --out (it is refused otherwise)")
	return cmd
}

func runExportMirror(cmd *cobra.Command, o *exportMirrorOptions) error {
	// Every required flag is named in ONE refusal rather than one per run: an operator
	// wiring this into a pipeline should learn the whole shape from the first failure.
	var missing []string
	if strings.TrimSpace(o.endpoint) == "" {
		missing = append(missing, "--endpoint")
	}
	if strings.TrimSpace(o.token) == "" {
		missing = append(missing, "--token")
	}
	if strings.TrimSpace(o.set) == "" {
		missing = append(missing, "--set")
	}
	if strings.TrimSpace(o.out) == "" {
		missing = append(missing, "--out")
	}
	if len(missing) > 0 {
		return fmt.Errorf("export-mirror needs %s", strings.Join(missing, ", "))
	}
	if !release.ValidChannel(o.channel) {
		return fmt.Errorf("unknown channel %q", o.channel)
	}

	pub, keyLabel, err := resolveReleaseKey(o.pubkey)
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	// cli-transport-exempt: a release MIRROR fetch, same class as the OTA download in
	// cmd_upgrade.go. Its trust anchor is the Ed25519 manifest signature verified with the
	// embedded (or --pubkey) OTA key BEFORE a single byte is written, not TLS. It reaches
	// the licensed worker release endpoint (--endpoint), NOT the operator control plane,
	// so the CLI client context and its pins do not apply and must not be attached.
	client := &http.Client{Timeout: o.timeout}

	// 1) The manifest and its signature, then VERIFY before a single byte is written.
	//    Writing first and verifying after would leave a half-mirror on disk that a
	//    later run cannot tell from a good one.
	manifestBytes, err := fetchMirror(ctx, client, o, "manifest", "", "")
	if err != nil {
		return fmt.Errorf("fetch the signed manifest: %w", err)
	}
	sigBytes, err := fetchMirror(ctx, client, o, "manifest.sig", "", "")
	if err != nil {
		return fmt.Errorf("fetch the manifest signature: %w", err)
	}
	m, err := release.VerifyManifest(manifestBytes, sigBytes, pub)
	if err != nil {
		return fmt.Errorf("the manifest failed verification against the %s OTA key (nothing written): %w", keyLabel, err)
	}

	// M-03: the signature proves the vendor wrote this manifest, NOT that it is the
	// manifest we asked for. The gate can answer a `security` request with a perfectly
	// signed `stable` manifest, and without this the command mirrored it and then PRINTED
	// "channel security" — the export lies about itself while every digest checks out.
	// The consumer does compare (cmd_upgrade.go), so the damage stopped at install time;
	// that is a reason to fix it here, not a reason to leave it.
	if m.Channel != o.channel {
		return fmt.Errorf("asked the gate for channel %q and it returned a manifest signed for %q "+
			"(nothing written)", o.channel, m.Channel)
	}

	wanted, err := selectPlatforms(m, o.platforms)
	if err != nil {
		return err
	}

	dir, finish, err := openMirrorOut(o.out, o.force)
	if err != nil {
		return err
	}

	// 2) One gate call per platform: the gate serves THE archive for
	//    (token, os, arch, channel) and derives the set from the grants itself, so
	//    `set` steers the manifest key only and is not sent here.
	for _, a := range wanted {
		name := filepath.Base(a.Filename)
		if name != a.Filename || strings.ContainsAny(a.Filename, `/\`) {
			return fmt.Errorf("manifest artifact name %q must be a bare filename", a.Filename)
		}
		// L-02: the bundle layout reserves two names. An artifact called `manifest.json`
		// would overwrite the metadata the consumer verifies FIRST, and the run would
		// still exit 0 — a publisher typo becoming an unreadable bundle silently.
		if name == "manifest.json" || name == "manifest.json.sig" {
			return fmt.Errorf("manifest artifact name %q collides with the bundle metadata", name)
		}
		blob, err := fetchMirror(ctx, client, o, "", a.OS, a.Arch)
		if err != nil {
			return fmt.Errorf("fetch %s/%s: %w", a.OS, a.Arch, err)
		}
		if err := release.VerifyArtifactSHA256(blob, a.SHA256); err != nil {
			return fmt.Errorf("%s (%s/%s) does not match the digest the signed manifest carries: %w", name, a.OS, a.Arch, err)
		}
		if err := writeNew(filepath.Join(dir, name), blob); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "export-mirror: %s/%s  %s  (%d bytes, digest OK)\n", a.OS, a.Arch, name, len(blob))
	}

	// 3) The manifest pair goes last on purpose: bundleSource reads manifest.json
	//    first, so a bundle interrupted mid-download has no manifest and is refused
	//    as incomplete rather than installed as a partial mirror.
	if err := writeNew(filepath.Join(dir, "manifest.json"), manifestBytes); err != nil {
		return fmt.Errorf("write manifest.json: %w", err)
	}
	if err := writeNew(filepath.Join(dir, "manifest.json.sig"), sigBytes); err != nil {
		return fmt.Errorf("write manifest.json.sig: %w", err)
	}

	out, err := finish()
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"export-mirror: %d platform(s), channel %s, set %s, verified against the %s OTA key -> %s\n",
		len(wanted), o.channel, o.set, keyLabel, out)
	fmt.Fprintf(cmd.OutOrStdout(), "export-mirror: install offline with `olivares upgrade --bundle %s`\n", out)
	return nil
}

// fetchMirror performs one gate request. `kind` empty means the platform archive,
// and only then are os/arch sent; `set` rides with manifest kinds only, which is the
// gate's own split (a client-supplied set on the binary path would be a second
// derivation competing with the token).
func fetchMirror(ctx context.Context, client *http.Client, o *exportMirrorOptions, kind, goos, goarch string) ([]byte, error) {
	base, err := url.Parse(strings.TrimRight(o.endpoint, "/") + "/download")
	if err != nil {
		return nil, fmt.Errorf("bad --endpoint: %w", err)
	}
	q := base.Query()
	q.Set("token", o.token)
	q.Set("channel", o.channel)
	if kind != "" {
		q.Set("kind", kind)
		q.Set("set", o.set)
	} else {
		q.Set("os", goos)
		q.Set("arch", goarch)
	}
	base.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "olivares-export-mirror")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// The gate names the reason AND the remedy in its 403s (C03-20); forwarding
		// its body is the difference between "403" and "renew to restore downloads".
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("gate returned %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxArtifactBytes))
}

// selectPlatforms resolves --platform against what the SIGNED manifest names. An
// unknown platform is a refusal, not an empty mirror: a bundle that silently carries
// nothing for the machine it was built for is discovered air-gapped, where there is
// no second chance.
func selectPlatforms(m release.Manifest, want []string) ([]release.Artifact, error) {
	if len(want) == 0 {
		out := append([]release.Artifact(nil), m.Artifacts...)
		if len(out) == 0 {
			return nil, fmt.Errorf("the signed manifest names no artifacts; nothing to mirror")
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].OS != out[j].OS {
				return out[i].OS < out[j].OS
			}
			return out[i].Arch < out[j].Arch
		})
		return out, nil
	}
	var picked []release.Artifact
	for _, w := range want {
		goos, goarch, ok := strings.Cut(strings.TrimSpace(w), "/")
		if !ok || goos == "" || goarch == "" {
			return nil, fmt.Errorf("--platform %q must be os/arch, e.g. linux/amd64", w)
		}
		found := false
		for _, a := range m.Artifacts {
			if a.OS == goos && a.Arch == goarch {
				picked = append(picked, a)
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("the signed manifest names no artifact for %s/%s", goos, goarch)
		}
	}
	return picked, nil
}

// openMirrorOut ALWAYS stages into a fresh private directory and publishes at the end.
//
// The first version wrote straight into `--out` when it was a directory, and the sol max
// contrast defeated the interruption invariant with a directed sequence: refresh an
// existing bundle, let the second platform fail its digest, and the OLD manifest plus the
// untouched second artifact still verify while the first no longer matches. The result is
// a MIXED directory that the real consumer accepts. The refusal was honest and the disk
// was not.
//
// Staging is not belt-and-braces here, it is the whole guarantee: the destination never
// holds a half-mirror because it is never written into. The tar path already worked this
// way, which is why the contrast found it sound — this makes the directory path the same
// shape rather than inventing a second discipline.
//
// A non-empty destination is REFUSED rather than merged or silently replaced. Replacing
// would destroy a good bundle on a failed refresh; merging is what produced the mixed
// directory. `--force` is the operator saying the old one is expendable.
func openMirrorOut(out string, force bool) (dir string, finish func() (string, error), err error) {
	esTar := strings.HasSuffix(out, ".tar.gz")

	padre := filepath.Dir(out)
	if err := os.MkdirAll(padre, 0o755); err != nil {
		return "", nil, fmt.Errorf("create the parent of --out: %w", err)
	}
	// Staging is a SIBLING of the destination so the final rename stays on one file
	// system: a rename across devices fails, and falling back to a copy would give up the
	// atomicity this exists for.
	staging, err := os.MkdirTemp(padre, ".olivares-mirror-*")
	if err != nil {
		return "", nil, fmt.Errorf("staging directory for %s: %w", out, err)
	}

	ocupado, err := destinoOcupado(out, esTar)
	if err != nil {
		_ = os.RemoveAll(staging)
		return "", nil, err
	}
	if ocupado && !force {
		_ = os.RemoveAll(staging)
		return "", nil, fmt.Errorf("--out %s already exists and is not empty; "+
			"pass --force to replace it (a refresh that fails partway would otherwise leave "+
			"a mixture of the old bundle and the new one)", out)
	}

	// Unconditional cleanup, NOT inside the success closure: the first version cleaned up
	// only when finish() was reached, so any fetch or digest failure left the staging
	// directory and the licensed bytes it already held behind on disk.
	hecho := false
	limpia := func() {
		if !hecho {
			_ = os.RemoveAll(staging)
		}
	}

	return staging, func() (string, error) {
		defer limpia()
		if esTar {
			tmp := out + ".tmp"
			if err := tarGzDir(staging, tmp); err != nil {
				_ = os.Remove(tmp)
				return "", err
			}
			// Rename LAST: creating the archive at its public name meant an interruption
			// while packing destroyed the previous output and left a truncated gzip in
			// its place.
			if err := os.Rename(tmp, out); err != nil {
				_ = os.Remove(tmp)
				return "", fmt.Errorf("publish %s: %w", out, err)
			}
			return out, nil
		}
		viejo := ""
		if ocupado {
			viejo = out + ".replaced-" + fmt.Sprint(os.Getpid())
			if err := os.Rename(out, viejo); err != nil {
				return "", fmt.Errorf("move the existing --out aside: %w", err)
			}
		} else if _, err := os.Lstat(out); err == nil {
			// An EMPTY destination still has to go before the rename, and this is not a
			// portability nicety: measured here, rename(dir -> existing empty dir) fails
			// with EEXIST rather than replacing it. Creating --out in advance is a normal
			// thing for a pipeline to do, so leaving this out made the ordinary case fail.
			// os.Remove refuses a non-empty directory, so it cannot destroy anything that
			// destinoOcupado would have called occupied.
			if err := os.Remove(out); err != nil {
				return "", fmt.Errorf("clear the empty --out before publishing: %w", err)
			}
		}
		if err := os.Rename(staging, out); err != nil {
			// The old bundle is NOT deleted here, and the deferred cleanup must not delete
			// it either: at this point the destination is empty and the only complete
			// bundle on disk is the one we moved aside. Destroying it to keep the
			// directory tidy would turn a failed refresh into data loss — which is the
			// very failure this whole staging design exists to prevent.
			if viejo != "" {
				if back := os.Rename(viejo, out); back != nil {
					return "", fmt.Errorf("publish %s: %w — AND the previous bundle could not be "+
						"put back; it is at %s: %v", out, err, viejo, back)
				}
			}
			return "", fmt.Errorf("publish %s: %w (the previous bundle was restored)", out, err)
		}
		if viejo != "" {
			_ = os.RemoveAll(viejo)
		}
		hecho = true
		return out, nil
	}, nil
}

// destinoOcupado says whether --out already holds something a publish would destroy. An
// empty directory is NOT occupied: creating the output path in advance is a normal thing
// for a pipeline to do, and refusing it would make the flag hostile.
func destinoOcupado(out string, esTar bool) (bool, error) {
	fi, err := os.Stat(out)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat --out: %w", err)
	}
	if esTar || !fi.IsDir() {
		return true, nil
	}
	entradas, err := os.ReadDir(out)
	if err != nil {
		return false, fmt.Errorf("read --out: %w", err)
	}
	return len(entradas) > 0, nil
}

// writeNew writes a file that must NOT already exist, which is also what stops a write
// from following a symlink.
//
// `os.WriteFile` follows a pre-existing symlink at the target, so an output directory
// carrying a planted link made the mirrored bytes land OUTSIDE it while the command
// returned success — measured by the sol max contrast with a directed probe.
//
// O_CREATE|O_EXCL is the whole guard, and O_NOFOLLOW is deliberately NOT used: O_EXCL
// already fails with EEXIST when the path exists AS A SYMLINK, dangling or not, because
// the exclusive create does not resolve the final component. Adding O_NOFOLLOW would buy
// nothing here and would not compile at all for windows, where `syscall` has no such
// constant — checked, not assumed, and this binary does ship for windows (knownGOOS).
//
// It also turns a duplicate leaf name in the signed manifest into a refusal rather than a
// silent last-writer-wins.
func writeNew(ruta string, datos []byte) error {
	f, err := os.OpenFile(ruta, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(datos); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func tarGzDir(dir, out string) error {
	f, err := os.Create(out)
	if err != nil {
		return fmt.Errorf("create %s: %w", out, err)
	}
	defer func() { _ = f.Close() }()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		// Flat, name-only headers: the bundle reader joins bare leaf names, and a
		// tar carrying paths is how an extractor is talked out of its own directory.
		hdr := &tar.Header{Name: e.Name(), Mode: 0o644, Size: info.Size(), ModTime: info.ModTime(), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		src, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		if _, err := io.Copy(tw, src); err != nil {
			_ = src.Close()
			return err
		}
		if err := src.Close(); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return f.Close()
}
