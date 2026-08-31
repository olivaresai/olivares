// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package release

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/olivaresai/olivares/core/sigbundle"
)

// manifest.go is the TUF-lite update manifest for `olivares upgrade`: a
// signed, per-channel JSON descriptor of "what is the current release on this
// channel, for which platforms, at which digests, and may this binary jump to it".
// It layers VERSION + CHANNEL + ROLLOUT + ANTI-ROLLBACK on top of the artifact
// integrity primitives (checksums + Ed25519) already in verify.go.
//
// Trust model (identical anchor to the checksums manifest): the signer produces
// `manifest.json` and a detached Ed25519 signature over its EXACT bytes; the
// verifier checks the signature against the embedded (or operator-supplied) release
// key BEFORE unmarshalling, so a tampered manifest never reaches the decision
// logic. There is no canonicalisation step to disagree on — we sign and verify the
// verbatim served bytes. A build with no key and no --pubkey fails closed.
//
// The manifest is NOT a second authorization system: it decides upgrade ELIGIBILITY
// (newer? min-version reachable? in the rollout cohort? a security release?), and
// the artifact bytes are still bound to a signed SHA-256 before execution.

// Channel names. These are the three semantic channels (design): the default
// GA line, the out-of-band security line, and the backport-only long-term line.
const (
	ChannelStable   = "stable"
	ChannelSecurity = "security"
	ChannelLTS      = "lts"
)

// Channels lists the recognized channels in escalating-stability order.
var Channels = []string{ChannelStable, ChannelSecurity, ChannelLTS}

// ValidChannel reports whether name is a recognized channel.
func ValidChannel(name string) bool {
	for _, c := range Channels {
		if c == name {
			return true
		}
	}
	return false
}

// ManifestSchemaVersion is the current on-the-wire schema. The verifier rejects a
// manifest it does not understand (a newer major schema) rather than guess.
const ManifestSchemaVersion = 1

// Manifest is the signed per-channel update descriptor.
type Manifest struct {
	SchemaVersion int        `json:"schema_version"`
	Channel       string     `json:"channel"`
	Version       string     `json:"version"`               // semver of the release this manifest points at
	MinVersion    string     `json:"min_version,omitempty"` // a direct jump requires current >= this
	ReleasedAt    time.Time  `json:"released_at"`
	Security      bool       `json:"security,omitempty"`   // this release carries a security fix
	Advisories    []string   `json:"advisories,omitempty"` // OSV/CVE ids fixed (surfaced by --check + console)
	EOLAt         *time.Time `json:"eol_at,omitempty"`     // channel/line end-of-life (LTS), informational
	Expires       *time.Time `json:"expires,omitempty"`    // freshness bound (TUF timestamp-lite): a manifest past this is refused
	Notes         string     `json:"notes,omitempty"`      // short human note or URL
	Rollout       Rollout    `json:"rollout"`
	Artifacts     []Artifact `json:"artifacts"`
	// Revoked is the license CRL this channel carries (D4=D, design §5.2).
	// It rides in the OTA manifest ON PURPOSE: the manifest is signed by the OTA
	// key, whose custody is independent from the license-signing key — so a
	// compromised license key can never sign itself back out of revocation.
	// Omitted on a manifest published before (and on channels with nothing
	// revoked): absent means "no revocations", and such manifests stay valid.
	// The field was added BEFORE the first public release, so no deployed
	// verifier predates it (ParseManifest's DisallowUnknownFields would have
	// rejected it there).
	Revoked *RevokedSet `json:"revoked,omitempty"`
}

// RevokedSet is the flat license CRL. Scale doctrine (design §5.2): this
// is DOZENS of entries at most — revocation is for chargebacks, leaked blobs and
// key compromise, not seat management. maxRevokedEntries is a plausibility
// ceiling; if a real deployment ever approaches it, the design gets re-evaluated
// (bloom filters, delta CRLs) rather than the cap silently raised.
//
// What the OPEN binary does with this is DISPLAY/attestation only (`olivares
// license verify` reports "revoked"). Since B10 removed the user cap there is no
// seat lift left to fall back from, so revocation has no behavioral consumer in
// this repository at all. License checks cannot be turned into an authorization
// bypass — that trust property is unchanged by revocation.
type RevokedSet struct {
	// Serials revokes single licenses by their unique serial (Claims.Serial).
	// Legacy blobs signed before serials existed carry none and can only be
	// revoked by holder or epoch.
	Serials []string `json:"serials,omitempty"`
	// HolderIDs revokes EVERY license of an organization (Claims.HolderID) —
	// the chargeback/leak response when serials are unknown.
	HolderIDs []string `json:"holder_ids,omitempty"`
	// LicenseKeyEpoch is the key-compromise fence (the fencing pattern
	// applied to licensing): licenses whose IssuedAt is BEFORE this Unix time
	// (seconds, UTC) are invalid regardless of serial. 0 means no fence. After
	// a compromise the ceremony sets the epoch to the compromise time and the
	// worker re-issues the legitimate park with fresh IssuedAt.
	LicenseKeyEpoch int64 `json:"license_key_epoch,omitempty"`
}

// Empty reports whether the set revokes nothing at all.
func (r *RevokedSet) Empty() bool {
	return r == nil || (len(r.Serials) == 0 && len(r.HolderIDs) == 0 && r.LicenseKeyEpoch == 0)
}

// maxRevokedEntries bounds each CRL list (see RevokedSet doc — dozens expected;
// this is a plausibility ceiling, not a target).
const maxRevokedEntries = 1024

// Stale reports whether the manifest is past its freshness bound (if set). A stale
// manifest is REFUSED by upgrade and reported as a failed check by the console. This
// is the anti-FREEZE defense: an attacker replaying an old (still validly-signed)
// manifest to hide a newer release — or to keep offering a superseded, vulnerable
// version — cannot do so indefinitely, because the publisher re-signs a short-lived
// manifest (the TUF timestamp role, minimally). An omitted Expires means no freshness
// check (back-compat; the publisher opts in with a re-signing cadence).
func (m Manifest) Stale(now time.Time) bool {
	return m.Expires != nil && now.After(*m.Expires)
}

// Rollout is the publisher-controlled staged-release window. Percentage is a
// POINTER so an omitted rollout means "everyone" (100), distinct from an explicit
// 0 ("paused — nobody yet"). A node self-selects deterministically (RolloutBucket)
// so a partial rollout needs no server-side cohort state.
type Rollout struct {
	Percentage *int      `json:"percentage,omitempty"`
	StartAt    time.Time `json:"start_at,omitempty"`
}

// Artifact is one downloadable binary for a platform, bound to a signed digest.
type Artifact struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
	// Variant names the SUBSET of the product this archive carries, and the name is
	// deliberately GENERIC: this repository is AGPL and must not learn commercial
	// vocabulary ("add-on set", "SKU") to describe a supply-chain field. Empty means
	// "the one archive this platform publishes", which is every manifest community
	// builds today — and `omitempty` keeps those bytes IDENTICAL, so nothing already
	// signed changes meaning by this field existing.
	//
	// It is added NOW on purpose. `Revoked` above records the rule: a field added
	// after the first public release is rejected by every deployed verifier, because
	// ParseManifest sets DisallowUnknownFields. Measured 2026-08-19: this repository
	// has ZERO release tags and ZERO published releases, so the window is open today
	// and closes with the first one.
	Variant  string `json:"variant,omitempty"`
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size,omitempty"`
	Cosign   string `json:"cosign,omitempty"` // optional cosign bundle ref (supply-chain, online-only)
}

// ManifestError classes for callers that branch on why a manifest was rejected.
type ManifestError struct{ msg string }

func (e *ManifestError) Error() string { return e.msg }

func manifestErr(format string, a ...any) error {
	return &ManifestError{msg: fmt.Sprintf(format, a...)}
}

// ParseManifest unmarshals and validates a manifest's shape (NOT its signature —
// callers use VerifyManifest for the trusted path). It rejects an unknown schema,
// a missing/unknown channel, an unparseable version, and a manifest with no
// artifacts, because a manifest we cannot fully understand is not one to act on.
func ParseManifest(b []byte) (Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields() // a field we do not know might change the meaning of one we do
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, manifestErr("release: manifest is not valid JSON: %v", err)
	}
	if m.SchemaVersion != ManifestSchemaVersion {
		return Manifest{}, manifestErr("release: manifest schema_version %d is unsupported (this build understands %d — upgrade this binary first)", m.SchemaVersion, ManifestSchemaVersion)
	}
	if !ValidChannel(m.Channel) {
		return Manifest{}, manifestErr("release: manifest channel %q is not one of %s", m.Channel, strings.Join(Channels, "|"))
	}
	if _, err := ParseVersion(m.Version); err != nil || strings.TrimSpace(m.Version) == "" {
		return Manifest{}, manifestErr("release: manifest version %q is not a valid semver", m.Version)
	}
	if strings.TrimSpace(m.MinVersion) != "" {
		if _, err := ParseVersion(m.MinVersion); err != nil {
			return Manifest{}, manifestErr("release: manifest min_version %q is not a valid semver", m.MinVersion)
		}
	}
	if m.Rollout.Percentage != nil && (*m.Rollout.Percentage < 0 || *m.Rollout.Percentage > 100) {
		return Manifest{}, manifestErr("release: manifest rollout.percentage %d is out of range 0..100", *m.Rollout.Percentage)
	}
	if len(m.Artifacts) == 0 {
		return Manifest{}, manifestErr("release: manifest lists no artifacts")
	}
	for i, a := range m.Artifacts {
		if a.OS == "" || a.Arch == "" || a.Filename == "" {
			return Manifest{}, manifestErr("release: manifest artifact %d is missing os/arch/filename", i)
		}
		sum := strings.ToLower(strings.TrimSpace(a.SHA256))
		if len(sum) != 2*sha256.Size || !isHex(sum) {
			return Manifest{}, manifestErr("release: manifest artifact %q has a non-SHA-256 digest %q", a.Filename, a.SHA256)
		}
		m.Artifacts[i].SHA256 = sum
	}
	if err := m.Revoked.validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// validate applies the structural bounds to a manifest's CRL. An entry that is
// empty, padded, control-laden or duplicated is not a serial/holder this
// publisher would ever list — and an empty string in particular must never
// reach matching code, where it could pair with a legacy claim's empty field.
func (r *RevokedSet) validate() error {
	if r == nil {
		return nil
	}
	check := func(kind string, list []string) error {
		if len(list) > maxRevokedEntries {
			return manifestErr("release: manifest revokes %d %s (bound %d) — the CRL is a dozens-scale list; re-evaluate the design before raising the cap", len(list), kind, maxRevokedEntries)
		}
		seen := make(map[string]struct{}, len(list))
		for i, v := range list {
			switch {
			case strings.TrimSpace(v) == "":
				return manifestErr("release: revoked %s %d is empty — an empty entry could match a legacy claim's empty field", kind, i)
			case v != strings.TrimSpace(v):
				return manifestErr("release: revoked %s %q carries surrounding whitespace", kind, v)
			case hasControlChars(v):
				return manifestErr("release: revoked %s %d contains control characters", kind, i)
			}
			if _, dup := seen[v]; dup {
				return manifestErr("release: revoked %s %q is listed twice", kind, v)
			}
			seen[v] = struct{}{}
		}
		return nil
	}
	if err := check("serials", r.Serials); err != nil {
		return err
	}
	if err := check("holder_ids", r.HolderIDs); err != nil {
		return err
	}
	if r.LicenseKeyEpoch < 0 {
		return manifestErr("release: revoked.license_key_epoch %d is negative (want Unix seconds UTC, 0 = no fence)", r.LicenseKeyEpoch)
	}
	return nil
}

// ManifestSigningInput returns the exact bytes an update-manifest signature covers:
// the domain tag followed by the verbatim manifest bytes. Producers sign this;
// VerifyManifest verifies over it. Keeping it in one place means the generator and
// the verifier can never disagree on what was signed.
//
// The domain tag (sigbundle.TagUpdateManifest, "olivares.update-manifest.v1\n") lives
// in the shared core/sigbundle registry. OTA has a keypair independent from licensing;
// the per-type tag remains defense in depth among signed
// OTA/advisory/bundle messages and prevents future key-custody mistakes from becoming
// cross-protocol replay. The signing input stays byte-identical to the legacy form.
func ManifestSigningInput(manifest []byte) []byte {
	return sigbundle.SigningInput(sigbundle.TagUpdateManifest, manifest)
}

// SignManifest returns a detached Ed25519 signature over the domain-separated
// manifest message (ManifestSigningInput).
func SignManifest(manifest []byte, priv ed25519.PrivateKey) []byte {
	return ed25519.Sign(priv, ManifestSigningInput(manifest))
}

// VerifyManifest authenticates a manifest OFFLINE: it verifies the detached
// signature over the DOMAIN-SEPARATED manifest bytes against pub, THEN
// parses/validates. A nil key fails closed (ErrNoKey). This is the single trusted
// entry point the updater calls — a manifest that does not verify never reaches
// decision logic, and a signature minted for another protocol never verifies here.
func VerifyManifest(manifest, sig []byte, pub ed25519.PublicKey) (Manifest, error) {
	if err := VerifySignature(ManifestSigningInput(manifest), sig, pub); err != nil {
		return Manifest{}, err
	}
	return ParseManifest(manifest)
}

// CrossCheckChecksums binds every digest this manifest claims to the SAME
// filename's entry in a checksums.txt that the caller authenticated OUT OF BAND —
// in the public pipeline, the cosign/Sigstore signature goreleaser produces over
// checksums.txt (and, transitively, the SLSA provenance whose subject is that same
// file). It is the missing link between the two halves of the supply chain: the
// OTA manifest is signed off-box by the release custodian, checksums.txt is signed
// in CI by the build identity, and NOTHING previously forced the two to agree.
//
// Threat it closes: an actor with `contents: write` on the release (or a
// compromised runner) replaces the unsigned draft manifest between the build and
// the signing ceremony, pointing a platform entry at a malicious archive's digest.
// The custodian would sign valid-looking JSON, the signature would verify, and
// clients would OTA to that digest. Running this check before signing — and again
// after, in the protected dispatch — makes that substitution a hard failure,
// because the substituted digest cannot also appear in the cosign-signed
// checksums.txt.
//
// It is deliberately STRICT in both directions that matter: every manifest
// artifact must be listed (ErrNotInManifest) and every listed digest must match
// (ErrDigestDisagreement). checksums.txt legitimately covers MORE files than the
// manifest (FIPS variants, SBOMs, packages), so extra entries are not an error.
//
// This is a supply-chain cross-check, not a replacement for the client-side
// binding: `upgrade` still recomputes the artifact digest before execution.
func (m Manifest) CrossCheckChecksums(checksums []byte) error {
	if len(m.Artifacts) == 0 {
		return manifestErr("release: manifest lists no artifacts to cross-check")
	}
	// A digest that agrees is not enough: checksums.txt covers the FIPS variant and
	// every other platform, so the filename must also be the one this platform and
	// version is supposed to carry (CheckArtifactNaming).
	if err := m.CheckArtifactNaming(); err != nil {
		return err
	}
	sums, err := ParseChecksums(checksums)
	if err != nil {
		return err
	}
	for _, a := range m.Artifacts {
		want, ok := sums[a.Filename]
		if !ok {
			return fmt.Errorf("%w: %q is claimed by the manifest but absent from the signed checksums", ErrNotInManifest, a.Filename)
		}
		got := strings.ToLower(strings.TrimSpace(a.SHA256))
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			return fmt.Errorf("%w: %s (manifest=%s checksums=%s)", ErrDigestDisagreement, a.Filename, got, want)
		}
	}
	return nil
}

// ExpectedArtifactName is the canonical release archive name for a platform:
// `olivares_<version>_<os>_<arch>.tar.gz`. It is what scanArtifacts records and
// what .goreleaser.yaml's base `archives` entry emits (the FIPS variant carries an
// extra `_fips_` segment and is deliberately NOT an OTA target).
func ExpectedArtifactName(version, goos, goarch, variant string) string {
	if variant == "" {
		return fmt.Sprintf("olivares_%s_%s_%s.tar.gz", version, goos, goarch)
	}
	return fmt.Sprintf("olivares_%s_%s_%s_%s.tar.gz", version, variant, goos, goarch)
}

// CheckArtifactNaming binds every artifact's FILENAME to the platform (and the
// version) it declares.
//
// Threat it closes: the digest cross-check only asks "is this filename→digest pair
// in the signed checksums.txt?". checksums.txt covers MORE files than the manifest —
// notably the FIPS variant and every other platform — so a substituted manifest can
// keep every digest honest and simply re-point `os:linux/arch:amd64` at
// `olivares_<v>_fips_linux_amd64.tar.gz` (or at the darwin archive, or at a previous
// version's archive). Digest agrees, cosign agrees, the custodian signs, and the
// fleet OTAs onto a binary the publisher never meant to ship on that platform. The
// only defense is refusing to accept a filename that does not match the platform and
// version the artifact entry claims.
//
// It also refuses two entries for the SAME os/arch: ArtifactFor returns the first
// match, so a duplicate pair means the manifest says two different things about one
// platform and the reader picks by accident.
//
// ⛔ AND THE DUPLICATE KEY STAYS (os, arch) — NOT (os, arch, variant), which is what
// the 08-15 spec asked for (SPEC-CORTE-POR-CONJUNTO-EN-LA-DESCARGA-2026-08-15.md:270).
// Widening it would make two entries for linux/amd64 legal, one per variant, and
// ArtifactFor (below) selects by os/arch alone and returns the FIRST match: the reader
// would pick a subset BY ACCIDENT. That is precisely the failure this refusal exists to
// prevent, re-opened one axis deeper — the same shape as the threat being closed here.
//
// So one manifest carries one variant, and a 16-SKU release publishes 16 manifests. The
// alternative (one manifest, many variants) is defensible, but it moves the choice into
// ArtifactFor and therefore into every caller, and that is a client-seam decision this
// ceremony-side change must not make on its own. Named here rather than silently skipped
// because the row that ordered this work requires adversarial review before it lands.
//
// ⛔ NI EL EJE `set` ENTRA EN EL NOMBRE — declarado NO NECESARIO el 2026-08-19 (fila C02-02),
// porque la fila llegaba con una premisa que el código desmiente. C02-03/C02-04 publican
// `enterprise/<version>/<set>/olivares_<v>_<os>_<arch>.tar.gz`: el conjunto viaja en la CLAVE R2 y
// el basename no cambia, y se preguntó si el contrato OTA obliga a nombrarlo también en el fichero.
// No obliga, y las tres razones son comprobables:
//
//  1. ExpectedArtifactName tiene UN llamador de producción — la línea de abajo, dentro de esta
//     misma función. Es una COMPROBACIÓN de ceremonia, no un compositor de rutas: no hay descarga
//     que pueda cambiar por lo que diga.
//  2. El párrafo de arriba ya rehúsa ensanchar la clave de duplicado a (os, arch, variant), y el
//     conjunto es exactamente el mismo eje un paso más allá. Un manifiesto lleva un conjunto, así
//     que el conjunto YA está desambiguado por CUÁL manifiesto se lee. Meterlo en el nombre sin
//     ensanchar la clave sería decorativo; ensancharla está rehusado ahí arriba con su motivo.
//  3. Y la premisa —«el motor pedirá la ruta vieja»— es falsa en el carril enterprise, porque el
//     motor NO PIDE NINGUNA RUTA: gatedSource.fetchArtifact ignora el artefacto (parámetro `_`) y
//     downloadGated emite GET <endpoint>/download?token&os&arch&channel&kind
//     (cmd/olivares/cmd_upgrade_source.go:104 · cmd/olivares/cmd_upgrade.go:745-760). Sin versión,
//     sin conjunto, sin clave. El layout del publicador es INVISIBLE para el motor, así que no
//     existe ninguna «ruta vieja» que pueda pedir. Quien sí compone ruta es communitySource
//     (`<base>/<channel>/<filename>`, :77), y ése es el carril comunitario, que no lleva conjuntos.
//
// ⇒ Lo que esta declaración deja FIJADO para quien venga: el cliente manda cuatro cosas —token, os,
// arch, channel— y nada más, así que **cualquier** derivación de la clave R2 tiene que ser función
// de esas cuatro. Una derivación que necesite la versión o el conjunto como entrada explícita del
// cliente no es implementable sin cambiar el protocolo, y ese límite vive aquí para que no se
// vuelva a descubrir desde el lado del nombre del fichero.
//
// This lives on the SUPPLY-CHAIN seam (CrossCheckChecksums), not in ParseManifest:
// the client trusts the publisher's signature for filenames, whereas the ceremony is
// exactly the moment that signature has not been applied yet.
func (m Manifest) CheckArtifactNaming() error {
	seen := make(map[string]string, len(m.Artifacts))
	for _, a := range m.Artifacts {
		plat := a.OS + "/" + a.Arch
		if prev, dup := seen[plat]; dup {
			return manifestErr("release: manifest lists %s twice (%q and %q) — one platform, two answers", plat, prev, a.Filename)
		}
		seen[plat] = a.Filename
		// os/arch are attacker-controlled strings that go on to compose a filename a
		// caller joins onto a directory. Every real GOOS/GOARCH is lowercase
		// alphanumeric; anything else has no business reaching a path.
		// The variant composes a FILENAME exactly as os/arch do, so it earns the same
		// shape check. Empty is the community case and legal; isLowerAlnum rejects the
		// empty string, so it cannot be applied unconditionally.
		if a.Variant != "" && !isLowerAlnum(a.Variant) {
			return manifestErr("release: manifest artifact %q declares variant %q, which is not a plausible variant name "+
				"(lowercase letters and digits only — it composes a filename)", a.Filename, a.Variant)
		}
		if !isLowerAlnum(a.OS) || !isLowerAlnum(a.Arch) {
			return manifestErr("release: manifest artifact %q declares os/arch %q/%q, which is not a plausible GOOS/GOARCH", a.Filename, a.OS, a.Arch)
		}
		want := ExpectedArtifactName(m.Version, a.OS, a.Arch, a.Variant)
		if a.Filename != want {
			return manifestErr("release: artifact for %s is named %q but this release's archive for that platform is %q "+
				"(a manifest may not point a platform at another platform's, another version's, or a VARIANT (_fips_) archive)",
				plat, a.Filename, want)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Policy plausibility — the fields the cross-check does NOT bind
// ---------------------------------------------------------------------------

// The digest cross-check binds a manifest to the artifacts CI built and cosign
// signed. It says nothing about the manifest's POLICY, which the custodian's
// signature covers just the same and which decides, fleet-wide:
//
//   - `expires`   — how long a mirror may keep serving THIS manifest (anti-freeze);
//   - `min_version` — who is allowed to jump to this release at all;
//   - `rollout`   — who actually receives it, and from when;
//   - `security`/`advisories` — whether the unattended timer treats it as urgent;
//   - `notes`     — free text the operator is shown.
//
// Under the very threat model the cross-check exists for (an actor with
// `contents: write` swapping the draft asset before the ceremony), an attacker who
// leaves every digest untouched still gets a fleet-wide kill switch: `min_version:
// 99.0.0` means nobody may ever upgrade again, `rollout.percentage: 0` (or a
// far-future `start_at`) silently suppresses the unattended security auto-update, and
// `expires: 2999-…` re-opens the anti-freeze hole permanently — all with a valid
// signature. These bounds make each of those a REFUSAL instead of an `OK:`.
//
// They are plausibility bounds, not authorization: a legitimate publisher never needs
// values outside them, and every bound has an explicit, audited escape hatch.

// DefaultMaxFreshnessWindow caps `expires - released_at` (and `expires - now`).
//
// The documented production window is 2160h = 90 days
// (docs/RELEASE-GO-LIVE-RUNBOOK.md §7.2, repo variable OLIVARES_MANIFEST_EXPIRES_IN),
// chosen so the off-box HSM ceremony is a quarterly obligation at worst. 4320h = 180
// days is that documented default with a 2x margin: it lets an operator legitimately
// double the window (a long LTS quiet period, an air-gap bundle that must survive a
// slow shipping cycle) with no code change, while still bounding how long a hostile
// mirror can freeze the fleet on one manifest to half a year rather than to forever.
// Anything beyond it is not a publishing choice, it is the anti-freeze defense being
// switched off, and it must be a deliberate --max-expires-in override on the record.
const DefaultMaxFreshnessWindow = 4320 * time.Hour

// DefaultMaxClockSkew is how far into the future `released_at` may sit before the
// manifest is treated as forged. A release is stamped by the generator at build time;
// a day covers the worst realistic skew between the CI runner, the ceremony host and
// the verifier without letting a forward-dated stamp buy an attacker extra freshness.
const DefaultMaxClockSkew = 24 * time.Hour

// maxNotesLen / maxAdvisories / maxAdvisoryLen bound the free-text fields. They are
// shown to a human (the custodian, then every operator) — an unbounded field is a
// place to hide a wall of text around a hostile URL.
const (
	maxNotesLen    = 512
	maxAdvisories  = 32
	maxAdvisoryLen = 128
)

// PolicyBounds are the plausibility limits applied to a manifest's policy fields.
// The zero value is NOT usable — start from DefaultPolicyBounds.
type PolicyBounds struct {
	// MaxFreshnessWindow caps expires-released_at and expires-now.
	MaxFreshnessWindow time.Duration
	// MaxClockSkew is the tolerated forward drift of released_at.
	MaxClockSkew time.Duration
	// AllowNoExpiry permits a manifest with no freshness bound at all. Off by
	// default: a production manifest without `expires` disables anti-freeze.
	AllowNoExpiry bool
	// AllowPausedRollout permits rollout.percentage == 0 (or a future start_at) on
	// a SECURITY manifest, which otherwise silently suppresses the unattended
	// security auto-update the whole channel exists for.
	AllowPausedRollout bool
}

// DefaultPolicyBounds returns the fail-closed bounds a release ceremony uses.
func DefaultPolicyBounds() PolicyBounds {
	return PolicyBounds{
		MaxFreshnessWindow: DefaultMaxFreshnessWindow,
		MaxClockSkew:       DefaultMaxClockSkew,
	}
}

// ErrImplausiblePolicy classifies every policy-bound refusal, so a caller can tell
// "the manifest is not the release" (ErrDigestDisagreement) from "the manifest is the
// release but its policy is not one this publisher would ever issue".
var ErrImplausiblePolicy = errors.New("release: manifest policy is outside plausible bounds")

// CheckPolicy applies the plausibility bounds to the manifest's policy fields at
// time now. It returns every advisory warning it found (things a custodian must LOOK
// at but which are legitimate) and a joined error naming every fatal violation.
//
// It is fail-closed and exhaustive on purpose: it reports ALL violations rather than
// the first, because a substituted manifest usually moves several levers at once and
// a custodian repairing them one round-trip at a time learns less.
func (m Manifest) CheckPolicy(now time.Time, b PolicyBounds) (warnings []string, err error) {
	if b.MaxFreshnessWindow <= 0 {
		b.MaxFreshnessWindow = DefaultMaxFreshnessWindow
	}
	if b.MaxClockSkew <= 0 {
		b.MaxClockSkew = DefaultMaxClockSkew
	}
	var fatal []error
	refuse := func(format string, a ...any) {
		fatal = append(fatal, fmt.Errorf("%s", fmt.Sprintf(format, a...)))
	}
	warn := func(format string, a ...any) { warnings = append(warnings, fmt.Sprintf(format, a...)) }

	// --- released_at: the anchor every other time bound is measured from. -------
	switch {
	case m.ReleasedAt.IsZero():
		refuse("released_at is missing — every time bound in this manifest is measured from it")
	case m.ReleasedAt.After(now.Add(b.MaxClockSkew)):
		refuse("released_at %s is %s in the FUTURE (tolerated skew %s) — a forward-dated stamp buys freshness the release never had",
			m.ReleasedAt.UTC().Format(time.RFC3339), m.ReleasedAt.Sub(now).Round(time.Minute), b.MaxClockSkew)
	}

	// --- expires: the anti-freeze bound, in BOTH directions. --------------------
	switch {
	case m.Expires == nil && !b.AllowNoExpiry:
		refuse("no freshness bound (expires): a mirror can serve this validly-signed manifest FOREVER and pin the fleet to a superseded, possibly vulnerable version — regenerate with --expires-in")
	case m.Expires == nil:
		warn("expires is ABSENT and was explicitly allowed: anti-freeze is DISABLED for this manifest")
	case m.Stale(now):
		refuse("already expired at %s — a manifest born stale is dead on arrival", m.Expires.UTC().Format(time.RFC3339))
	default:
		exp := m.Expires.UTC()
		if !m.ReleasedAt.IsZero() && !exp.After(m.ReleasedAt) {
			refuse("expires %s is not after released_at %s — the manifest is stale the moment it is published",
				exp.Format(time.RFC3339), m.ReleasedAt.UTC().Format(time.RFC3339))
		}
		if !m.ReleasedAt.IsZero() {
			if win := exp.Sub(m.ReleasedAt); win > b.MaxFreshnessWindow {
				refuse("freshness window is %s (expires %s, released_at %s), beyond the %s bound — this is the anti-freeze defense being switched off, not a publishing choice",
					win.Round(time.Hour), exp.Format(time.RFC3339), m.ReleasedAt.UTC().Format(time.RFC3339), b.MaxFreshnessWindow)
			}
		}
		if rem := exp.Sub(now); rem > b.MaxFreshnessWindow {
			refuse("expires %s is %s away, beyond the %s bound — a mirror could freeze the fleet on it for that long",
				exp.Format(time.RFC3339), rem.Round(time.Hour), b.MaxFreshnessWindow)
		}
	}

	// --- min_version: the fleet-wide "nobody may upgrade" lever. ----------------
	if mv := strings.TrimSpace(m.MinVersion); mv != "" {
		minV, perr := ParseVersion(mv)
		if perr != nil {
			refuse("min_version %q is not a valid semver", mv)
		} else if tgt, terr := ParseVersion(m.Version); terr == nil && Compare(minV, tgt) >= 0 {
			// EQUAL is a kill switch too, not just GREATER: MinTooOld is
			// Compare(current, min) < 0, so min_version == version fails every node
			// that is not already on the release — and one that IS already on it
			// short-circuits at IsUpToDate long before min_version is read. The
			// equal case therefore has no legitimate use and blocks the whole fleet
			// exactly like a greater one, with an otherwise honest manifest.
			refuse("min_version %s is not BELOW the release it gates (%s): no deployment that still needs this upgrade can satisfy it, so this manifest permanently blocks the whole fleet from upgrading",
				mv, m.Version)
		}
	}

	// --- rollout: the "nobody actually receives it" lever. ----------------------
	securityRelease := m.Security || m.Channel == ChannelSecurity
	if p := m.Rollout.Percentage; p != nil {
		switch {
		case *p < 0 || *p > 100:
			refuse("rollout.percentage %d is out of range 0..100", *p)
		case *p == 0 && securityRelease && !b.AllowPausedRollout:
			refuse("rollout.percentage is 0 on a SECURITY release: the unattended `upgrade --if-eligible` timer this channel exists for would install it for NOBODY (pass --allow-paused-rollout if the pause is deliberate)")
		case *p == 0 && !b.AllowPausedRollout:
			// A staged rollout is legitimate on any channel, but the exact value 0 —
			// "reaches nobody at all" — is not something a publisher issues: it is
			// indistinguishable from a substituted manifest that suppresses the whole
			// release while every digest stays honest. Refuse it everywhere and make
			// a deliberate pause say so out loud.
			refuse("rollout.percentage is 0: this manifest reaches NOBODY, suppressing the release for the entire fleet (pass --allow-paused-rollout if the pause is deliberate)")
		case *p == 0:
			warn("rollout.percentage is 0: this manifest reaches NOBODY until it is re-issued")
		case *p < 100:
			warn("rollout.percentage is %d: only that share of the fleet will take this release", *p)
		}
	}
	if st := m.Rollout.StartAt; !st.IsZero() {
		stU := st.UTC()
		switch {
		case m.Expires != nil && !stU.Before(m.Expires.UTC()):
			refuse("rollout.start_at %s is at or after expires %s: the rollout can never begin before the manifest dies — nobody ever upgrades",
				stU.Format(time.RFC3339), m.Expires.UTC().Format(time.RFC3339))
		case !m.ReleasedAt.IsZero() && stU.Sub(m.ReleasedAt) > b.MaxFreshnessWindow:
			refuse("rollout.start_at %s is %s after released_at, beyond the %s bound — a far-future start suppresses the rollout as effectively as percentage 0",
				stU.Format(time.RFC3339), stU.Sub(m.ReleasedAt).Round(time.Hour), b.MaxFreshnessWindow)
		case stU.After(now) && securityRelease && !b.AllowPausedRollout:
			refuse("rollout.start_at %s is in the FUTURE on a SECURITY release: no node upgrades until then, including the unattended timer (pass --allow-paused-rollout if the delay is deliberate)",
				stU.Format(time.RFC3339))
		case stU.After(now):
			warn("rollout.start_at %s is in the future: nothing rolls out until then", stU.Format(time.RFC3339))
		}
	}

	// --- security / advisories coherence. ---------------------------------------
	if m.Channel == ChannelSecurity && !m.Security {
		refuse("channel is %q but security is false: the generator sets security=true for this channel, so these bytes were not produced by it", ChannelSecurity)
	}
	if securityRelease && len(m.Advisories) == 0 {
		refuse("a security release carries no advisories: `security check`, the console and the operator have nothing to act on, and stripping the advisory list is how a substituted manifest hides WHAT it claims to fix")
	}
	if !securityRelease && len(m.Advisories) > 0 {
		warn("advisories are listed (%s) but security is false: clients will not prioritize this release", strings.Join(m.Advisories, ", "))
	}
	if n := len(m.Advisories); n > maxAdvisories {
		refuse("%d advisories listed (bound %d)", n, maxAdvisories)
	}
	for i, a := range m.Advisories {
		switch {
		case strings.TrimSpace(a) == "":
			refuse("advisory %d is empty", i)
		case len(a) > maxAdvisoryLen:
			refuse("advisory %d is %d chars (bound %d)", i, len(a), maxAdvisoryLen)
		case hasControlChars(a):
			refuse("advisory %d contains control characters — a terminal-escape payload in a field printed to the custodian's terminal", i)
		}
	}

	// --- notes: free text shown to a human; the classic phishing carrier. -------
	if m.Notes != "" {
		switch {
		case len(m.Notes) > maxNotesLen:
			refuse("notes is %d chars (bound %d)", len(m.Notes), maxNotesLen)
		case hasControlChars(m.Notes):
			refuse("notes contains control characters — a terminal-escape payload can hide or fake the lines around it")
		default:
			warn("notes is operator-visible free text and is verified by NOTHING — read it: %q", m.Notes)
			if strings.Contains(strings.ToLower(m.Notes), "http://") {
				warn("notes carries a plaintext http:// URL — never publish one; confirm it is the URL you meant")
			}
		}
	}

	// --- revoked: the license CRL — fleet-wide licensing policy the OTA signature
	//     covers. Structural bounds live in ParseManifest; here the levers.
	if !m.Revoked.Empty() {
		if e := m.Revoked.LicenseKeyEpoch; e > 0 {
			et := time.Unix(e, 0).UTC()
			if et.After(now.Add(b.MaxClockSkew)) {
				refuse("revoked.license_key_epoch %s is in the FUTURE (tolerated skew %s): it would invalidate licenses the worker is legitimately issuing right now — a compromise fence points at the PAST compromise time",
					et.Format(time.RFC3339), b.MaxClockSkew)
			} else {
				warn("revoked.license_key_epoch %s: legitimately-issued licenses dated before it are invalidated on binaries that trust the CURRENT license anchor — this is the key-compromise response, but it does NOT stop a still-held compromised key from minting post-epoch blobs; full remediation is the anchor rotation + fleet binary upgrade. Confirm the O03 rotation happened and the worker re-issued the park", et.Format(time.RFC3339))
			}
		}
		if n := len(m.Revoked.Serials) + len(m.Revoked.HolderIDs); n > 0 {
			warn("this manifest revokes %d license(s)/holder(s): every enterprise deployment that pulls it starts that seat-lift grace clock", n)
		}
	}

	// --- eol_at: informational, but a past date is a contradiction. -------------
	if m.EOLAt != nil && m.EOLAt.Before(now) {
		warn("eol_at %s is already past: this line is declared end-of-life", m.EOLAt.UTC().Format(time.RFC3339))
	}

	if len(fatal) > 0 {
		return warnings, fmt.Errorf("%w:\n  - %s", ErrImplausiblePolicy, strings.Join(errStrings(fatal), "\n  - "))
	}
	return warnings, nil
}

func errStrings(errs []error) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Error())
	}
	return out
}

// isLowerAlnum reports whether s is a non-empty run of lowercase ASCII letters and
// digits — the shape every GOOS/GOARCH value takes.
func isLowerAlnum(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// hasControlChars reports whether s carries any Unicode control character —
// including NUL, newline, tab and ESC. Every field it guards is a single-line human
// string, so there is no legitimate control character in any of them.
func hasControlChars(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// PolicyField is one policy value rendered for human review before signing.
type PolicyField struct {
	Name  string
	Value string
	// Alert marks a value that changes fleet-wide behavior in a restricting or
	// operator-visible direction (a gate, a partial rollout, free text). It is
	// legitimate — it is what the custodian must consciously confirm.
	Alert bool
}

// PolicySummary renders EVERY policy field the custodian's signature will cover, so
// "it printed OK:" is never the whole of the review. Silence about a field is what
// let a substituted `min_version`/`rollout`/`notes` through unread.
func (m Manifest) PolicySummary(now time.Time) []PolicyField {
	f := []PolicyField{
		{Name: "schema_version", Value: fmt.Sprintf("%d", m.SchemaVersion)},
		{Name: "channel", Value: m.Channel, Alert: m.Channel == ChannelSecurity},
		{Name: "version", Value: m.Version},
		{Name: "released_at", Value: m.ReleasedAt.UTC().Format(time.RFC3339)},
	}
	minV := "none (any version may jump directly to this release)"
	if mv := strings.TrimSpace(m.MinVersion); mv != "" {
		minV = mv + "  <- deployments BELOW this are refused the upgrade"
	}
	f = append(f, PolicyField{Name: "min_version", Value: minV, Alert: strings.TrimSpace(m.MinVersion) != ""})

	expires := "NONE — anti-freeze DISABLED (a mirror can serve this forever)"
	alertExp := true
	if m.Expires != nil {
		expires = fmt.Sprintf("%s (in %s)", m.Expires.UTC().Format(time.RFC3339), m.Expires.Sub(now).Round(time.Hour))
		alertExp = false
	}
	f = append(f, PolicyField{Name: "expires", Value: expires, Alert: alertExp})

	rollout := "100% (omitted — the whole fleet)"
	alertRollout := false
	if p := m.Rollout.Percentage; p != nil {
		rollout = fmt.Sprintf("%d%%", *p)
		alertRollout = *p != 100
		if *p == 0 {
			rollout += "  <- NOBODY receives this release"
		}
	}
	if st := m.Rollout.StartAt; !st.IsZero() {
		rollout += fmt.Sprintf(", start_at %s", st.UTC().Format(time.RFC3339))
		if st.After(now) {
			rollout += "  <- IN THE FUTURE: nothing rolls out until then"
			alertRollout = true
		}
	}
	f = append(f, PolicyField{Name: "rollout", Value: rollout, Alert: alertRollout})

	f = append(f,
		PolicyField{Name: "security", Value: fmt.Sprintf("%t", m.Security), Alert: m.Security},
		PolicyField{Name: "advisories", Value: cndStr(len(m.Advisories) == 0, "none", strings.Join(m.Advisories, ", ")), Alert: len(m.Advisories) > 0},
	)
	if m.EOLAt != nil {
		f = append(f, PolicyField{Name: "eol_at", Value: m.EOLAt.UTC().Format(time.RFC3339), Alert: true})
	}
	revoked := "none"
	if !m.Revoked.Empty() {
		parts := []string{fmt.Sprintf("%d serial(s), %d holder(s)", len(m.Revoked.Serials), len(m.Revoked.HolderIDs))}
		if m.Revoked.LicenseKeyEpoch > 0 {
			parts = append(parts, fmt.Sprintf("license_key_epoch %s  <- pre-epoch licenses invalid on the current anchor (full fix = anchor rotation + upgrade)",
				time.Unix(m.Revoked.LicenseKeyEpoch, 0).UTC().Format(time.RFC3339)))
		}
		revoked = strings.Join(parts, ", ")
	}
	f = append(f, PolicyField{Name: "revoked", Value: revoked, Alert: !m.Revoked.Empty()})
	f = append(f, PolicyField{
		Name:  "notes",
		Value: cndStr(m.Notes == "", "none", fmt.Sprintf("%q  <- operator-visible free text, verified by NOTHING", m.Notes)),
		Alert: m.Notes != "",
	})
	platforms := make([]string, 0, len(m.Artifacts))
	for _, a := range m.Artifacts {
		platforms = append(platforms, fmt.Sprintf("%s/%s=%s", a.OS, a.Arch, a.Filename))
	}
	sort.Strings(platforms)
	f = append(f, PolicyField{Name: "artifacts", Value: fmt.Sprintf("%d — %s", len(m.Artifacts), strings.Join(platforms, ", "))})
	return f
}

func cndStr(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

// ArtifactFor returns the artifact for the given os/arch, or false if the manifest
// carries none (this platform is not part of the release).
func (m Manifest) ArtifactFor(goos, goarch string) (Artifact, bool) {
	for _, a := range m.Artifacts {
		if a.OS == goos && a.Arch == goarch {
			return a, true
		}
	}
	return Artifact{}, false
}

// Platforms returns the sorted "os/arch" list the manifest ships, for diagnostics.
func (m Manifest) Platforms() []string {
	out := make([]string, 0, len(m.Artifacts))
	for _, a := range m.Artifacts {
		out = append(out, a.OS+"/"+a.Arch)
	}
	sort.Strings(out)
	return out
}

// rolloutPercent returns the effective rollout percentage (omitted => 100 = full).
func (r Rollout) rolloutPercent() int {
	if r.Percentage == nil {
		return 100
	}
	return *r.Percentage
}

// RolloutBucket maps an install identity to a stable bucket in [0,100) for a given
// target version. A node is in the rollout cohort iff bucket < rollout.percentage.
// It is deterministic (same install+version => same bucket) so a staged rollout
// needs no central cohort registry, and versioned so consecutive releases reshuffle
// the fleet (a node unlucky this release is not always last).
func RolloutBucket(installID, version string) int {
	h := sha256.Sum256([]byte(installID + "\x00" + version))
	return int(binary.BigEndian.Uint32(h[:4]) % 100)
}

// Eligible reports whether the node identified by installID is inside the manifest's
// rollout cohort at time now (respecting start_at). A full (100) or omitted rollout
// is always eligible; a paused (0) rollout never is.
func (m Manifest) Eligible(installID string, now time.Time) bool {
	// start_at gates the whole rollout — before it, no node upgrades, even at 100%.
	if !m.Rollout.StartAt.IsZero() && now.Before(m.Rollout.StartAt) {
		return false
	}
	pct := m.Rollout.rolloutPercent()
	if pct >= 100 {
		return true
	}
	if pct <= 0 {
		return false
	}
	return RolloutBucket(installID, m.Version) < pct
}

// UpgradePlan is the decision the updater renders from a verified manifest against
// the running version. It NEVER mutates anything; the command layer acts on it.
type UpgradePlan struct {
	Current Version
	Target  Version
	Channel string
	// CurrentKnown reports whether Current is a real position in the release
	// ordering. It is FALSE for an unstamped build (IsUnstamped), where Current is
	// the zero Version — a parse result, not a position. Every ordering predicate
	// below is meaningless when this is false, and the upgrade path refuses on it
	// BEFORE reading them.
	CurrentKnown bool
	Direction    int // +1 forward (upgrade), 0 same version, -1 backward (would be a rollback)
	MinVersion   Version
	MinTooOld    bool // current < min_version: a direct jump is not allowed
	Eligible     bool // inside the rollout cohort
	Security     bool // target carries a security fix
	Advisories   []string
	Artifact     Artifact
	HasArtifact  bool
}

// PlanUpgrade builds the decision for currentVersionStr moving to m, for goos/goarch
// (empty => this runtime), identified by installID at time now. It surfaces every
// gate; it does not enforce policy (anti-rollback/min-version refusal is the caller's,
// so `--force-rollback` can override with an audit trail).
func (m Manifest) PlanUpgrade(currentVersionStr, goos, goarch, installID string, now time.Time) (UpgradePlan, error) {
	cur, err := ParseVersion(currentVersionStr)
	if err != nil {
		return UpgradePlan{}, err
	}
	tgt, err := ParseVersion(m.Version)
	if err != nil {
		return UpgradePlan{}, err
	}
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	plan := UpgradePlan{
		Current:      cur,
		Target:       tgt,
		Channel:      m.Channel,
		CurrentKnown: !IsUnstamped(currentVersionStr),
		Direction:    Compare(tgt, cur),
		Eligible:     m.Eligible(installID, now),
		Security:     m.Security,
		Advisories:   append([]string(nil), m.Advisories...),
	}
	// MinTooOld is an ordering claim, so it is only computed when there IS an
	// ordering. Deriving it from an unstamped build's zero Version is what made the
	// min-version gate fire on every source build — the zero is below every
	// minimum. When the current version is unknown the caller must refuse on
	// CurrentKnown, not read a comparison that was never meaningful.
	if mv := strings.TrimSpace(m.MinVersion); mv != "" {
		plan.MinVersion, _ = ParseVersion(mv)
		plan.MinTooOld = plan.CurrentKnown && Compare(cur, plan.MinVersion) < 0
	}
	plan.Artifact, plan.HasArtifact = m.ArtifactFor(goos, goarch)
	return plan, nil
}

// IsRollback reports whether acting on this plan would move to a lower version.
// An unknown current version yields FALSE — not because the move is forward, but
// because "lower than an unknown" is not a claim this type is allowed to make.
// Callers must refuse on CurrentKnown first; this only ensures that one that forgets
// cannot be handed a fabricated ordering.
func (p UpgradePlan) IsRollback() bool { return p.CurrentKnown && p.Direction < 0 }

// IsUpToDate reports whether the target equals the running version. Unknown current
// version yields FALSE, for the same reason as IsRollback.
func (p UpgradePlan) IsUpToDate() bool { return p.CurrentKnown && p.Direction == 0 }
