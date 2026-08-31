// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/secure/kmswrap"
)

// Key-custody governance: the OPTIONAL customer-managed KEK (CMEK) that
// wraps engine-persisted secrets at rest — the per-event audit signing key, the
// catalog signing key and sealed operator config files — plus the DECLARED
// custody assertions that make a posture regression fail the boot instead of
// silently downgrading (the OLIVARES_EMBEDDINGS_REQUIRE precedent, docs/SECURITY-HARDENING.md:
// never a silent gap).
//
//	OLIVARES_KEY_WRAP = "" (none, default) | "aws-kms" | "gcp-kms" | "azure-kv"
//
// aws-kms: OLIVARES_KEY_WRAP_AWS_REGION, _AWS_KEY_ID (a SYMMETRIC_DEFAULT KMS
//
//	key — id, ARN or alias). Credentials from the standard AWS_ACCESS_KEY_ID /
//	AWS_SECRET_ACCESS_KEY / AWS_SESSION_TOKEN env (same model as the ledger
//	signer); AWS_ENDPOINT_URL_KMS (the standard SDK override) points at a
//	KMS-compatible endpoint when set.
//
// gcp-kms: OLIVARES_KEY_WRAP_GCP_KEY (a full cryptoKeys resource name — NOT a
//
//	cryptoKeyVersion: decrypt is key-scoped and survives KEK rotation), token
//	from OLIVARES_KEY_WRAP_GCP_TOKEN_FILE (re-read per call) or _GCP_TOKEN.
//
// azure-kv: OLIVARES_KEY_WRAP_AZURE_VAULT_URL, _AZURE_KEY_NAME (an RSA key;
//
//	wrap alg RSA-OAEP-256), optional _AZURE_KEY_VERSION (empty = the backend
//	resolves and pins the current version), token from _AZURE_TOKEN_FILE or
//	_AZURE_TOKEN.
//
// Custody assertions (declared vs actual, fail-closed):
//
//	OLIVARES_KEY_CUSTODY    = "" | "byok" | "cmek"  — the audit signing key MUST
//	  be customer-provisioned (byok: env/mounted Secret) or KEK-wrapped
//	  (cmek: sealed envelope). Minting, or a custody mode other than the one
//	  declared, refuses the boot.
//	OLIVARES_LEDGER_CUSTODY = "" | "hyok"           — ledger checkpoints MUST be
//	  signed by the off-box KMS/HSM key (OLIVARES_LEDGER_SIGNER).
const (
	envKeyWrap = "OLIVARES_KEY_WRAP"
	// envKeyWrapOld declares the KEK identity a custody envelope was sealed under
	// BEFORE a migration — the same namespace shape as envKeyWrap, one level down
	// (OLIVARES_KEY_WRAP_OLD_AWS_REGION, …). Declaring it declares a migration:
	// the next ceremony OPENS with this identity and SEALS under the configured
	// one. Only the CLI ceremonies read it; the boot path never does.
	envKeyWrapOld     = "OLIVARES_KEY_WRAP_OLD"
	envKeyCustody     = "OLIVARES_KEY_CUSTODY"
	envLedgerCustody  = "OLIVARES_LEDGER_CUSTODY"
	envAuditWrapped   = "OLIVARES_AUDIT_SIGNING_KEY_WRAPPED_FILE"
	envCatalogWrapped = "OLIVARES_CATALOG_SIGNING_KEY_WRAPPED_FILE"
	envPolicyWrapped  = "OLIVARES_POLICY_SIGNING_KEY_WRAPPED_FILE"
)

// kmsCallTimeout bounds every boot/CLI KEK round-trip (the checkpointer's 30s
// precedent): a hung KMS must fail the operation, not wedge the boot forever.
const kmsCallTimeout = 30 * time.Second

// keyWrapConfig is the parsed OLIVARES_KEY_WRAP_* surface. It is parsed once and
// turned into a backend per use, because the Azure unwrap path may need to
// re-pin the KEK version recorded in the envelope being opened.
type keyWrapConfig struct {
	kind string
	// envPrefix is the env NAMESPACE this identity was parsed from
	// (envKeyWrap for the configured KEK, envKeyWrapOld for a declared migration
	// source). It is kept because the AWS credential lookup happens when the
	// wrapper is BUILT rather than when the config is parsed, so it has to know
	// which namespace's overrides to prefer.
	envPrefix string

	awsRegion string
	awsKeyID  string

	gcpKey   string
	gcpToken kmswrap.TokenSource

	azureVaultURL string
	azureKeyName  string
	azureVersion  string
	azureToken    kmswrap.TokenSource
}

// loadKeyWrapConfig parses OLIVARES_KEY_WRAP — the KEK the engine and every
// ceremony SEAL under. (nil, nil) when none is configured; an unknown kind or an
// incomplete backend config is an error (a custody config typo must never
// silently mean "no custody").
//
// This is the ONLY custody config the boot path ever reads. The migration
// namespace is parsed by the same code through a different prefix, but only the
// CLI ceremonies call for it (loadOldKeyWrapConfig, cmd_keys.go): a running
// engine has exactly one custody root, and adding a second one to the env must
// not be able to change that.
func loadKeyWrapConfig() (*keyWrapConfig, error) {
	return parseKeyWrapConfig(envKeyWrap)
}

// parseKeyWrapConfig parses one KEK identity out of an env NAMESPACE: prefix is
// the variable naming the backend kind, and every backend setting hangs off it
// (`<prefix>_AWS_REGION`, `<prefix>_AZURE_VAULT_URL`, …). The prefix reaches the
// ERROR MESSAGES too — an operator who mistyped a variable has to be told the
// name they actually have to fix, not the name of the namespace they are not
// using.
//
// Names are read EXACTLY, never by prefix scan, so declaring a second namespace
// cannot change the meaning of a config that does not use one.
func parseKeyWrapConfig(prefix string) (*keyWrapConfig, error) {
	get := func(suffix string) string { return strings.TrimSpace(os.Getenv(prefix + suffix)) }
	kind := strings.TrimSpace(os.Getenv(prefix))
	switch kind {
	case "":
		return nil, nil
	case "aws-kms":
		c := &keyWrapConfig{kind: kind, envPrefix: prefix,
			awsRegion: get("_AWS_REGION"),
			awsKeyID:  get("_AWS_KEY_ID"),
		}
		if c.awsRegion == "" || c.awsKeyID == "" {
			return nil, fmt.Errorf("%s=aws-kms needs %s_AWS_REGION and %s_AWS_KEY_ID", prefix, prefix, prefix)
		}
		return c, nil
	case "gcp-kms":
		ts, err := wrapTokenSource(prefix + "_GCP_TOKEN")
		if err != nil {
			return nil, err
		}
		c := &keyWrapConfig{kind: kind, envPrefix: prefix, gcpKey: get("_GCP_KEY"), gcpToken: ts}
		if c.gcpKey == "" {
			return nil, fmt.Errorf("%s=gcp-kms needs %s_GCP_KEY (a cryptoKeys resource name)", prefix, prefix)
		}
		return c, nil
	case "azure-kv":
		ts, err := wrapTokenSource(prefix + "_AZURE_TOKEN")
		if err != nil {
			return nil, err
		}
		c := &keyWrapConfig{kind: kind, envPrefix: prefix,
			azureVaultURL: get("_AZURE_VAULT_URL"),
			azureKeyName:  get("_AZURE_KEY_NAME"),
			azureVersion:  get("_AZURE_KEY_VERSION"),
			azureToken:    ts,
		}
		if c.azureVaultURL == "" || c.azureKeyName == "" {
			return nil, fmt.Errorf("%s=azure-kv needs %s_AZURE_VAULT_URL and %s_AZURE_KEY_NAME", prefix, prefix, prefix)
		}
		return c, nil
	default:
		return nil, fmt.Errorf("%s=%q unknown (use \"\"|aws-kms|gcp-kms|azure-kv)", prefix, kind)
	}
}

// wrapTokenSource builds a bearer TokenSource from <prefix>_FILE (re-read each
// call so the operator's refresher keeps it fresh) or <prefix> (static) — the
// exact model of the ledger signer's tokenSourceFromEnv.
func wrapTokenSource(prefix string) (kmswrap.TokenSource, error) {
	ts, err := tokenSourceFromEnv(prefix)
	if err != nil {
		return nil, err
	}
	return kmswrap.TokenSource(ts), nil
}

// wrapper builds the KEK backend for WRAP-side operations (sealing, minting,
// rewrapping): it targets the CONFIGURED KEK as-is.
func (c *keyWrapConfig) wrapper() (secure.KeyWrapper, error) {
	return c.wrapperPinned("", "")
}

// wrapperFor builds the KEK backend for UNWRAP-side operations on a specific
// envelope, pinning what the envelope RECORDS over what the env configures —
// unwrap must target the key/version that actually wrapped the DEK:
//
//   - Azure: unwrapKey addressed at a different version than the wrapping one
//     fails, so the version recorded in the envelope's key id wins — even over a
//     configured _AZURE_KEY_VERSION (which otherwise points rewrap's open side
//     at the POST-rotation version and bricks the documented rewrap ceremony).
//     The recorded id must reference the configured vault and key name; anything
//     else is a custody mismatch reported loudly, not silently tried.
//   - AWS: Decrypt pinned to an ALIAS resolves at call time, so after the
//     operator repoints the alias (manual rotation) an alias-pinned unwrap
//     throws IncorrectKeyException; the envelope records the RESOLVED key ARN
//     (kmswrap.AWS.KeyID after wrap), so unwrap pins that ARN. A tampered
//     recorded ARN fails closed at the KMS (wrong key ⇒ error), never open.
//   - GCP: decrypt is key-scoped and version-auto-detecting — nothing to pin.
func (c *keyWrapConfig) wrapperFor(e *secure.SealedEnvelope) (secure.KeyWrapper, error) {
	if e == nil {
		return c.wrapperPinned("", "")
	}
	switch {
	case c.kind == "azure-kv" && e.Provider == kmswrap.ProviderAzure:
		vault, name, version, err := kmswrap.ParseAzureKeyID(e.KeyID)
		if err != nil {
			// Not an Azure key identifier at all: nothing to pin, and nothing that
			// can be compared to the configured KEK either.
			return c.wrapperPinned("", "")
		}
		// The vault/key comparison comes FIRST, before the version is considered.
		// ParseAzureKeyID models an EMPTY version as valid, so an id naming another
		// vault without a version used to skip this check entirely and be answered
		// with a wrapper for the configured key — silently trying a different KEK,
		// which is exactly what this branch says it never does (Codex contrast
		// 2026-08-06, F-03).
		if !strings.EqualFold(vault, strings.TrimSuffix(c.azureVaultURL, "/")) || !strings.EqualFold(name, c.azureKeyName) {
			return nil, fmt.Errorf("sealed envelope was wrapped by Azure KEK %s, but the KEK being used to open it is %s/keys/%s — point %s_AZURE_* at that key, or, to MIGRATE custody between vaults, declare the source vault in %s_AZURE_* and run `keys rewrap`", e.KeyID, c.azureVaultURL, c.azureKeyName, c.envPrefix, envKeyWrapOld)
		}
		if version == "" {
			// Same vault and key, no recorded version: the configured pin (or the
			// current version) is the only answer available.
			return c.wrapperPinned("", "")
		}
		return c.wrapperPinned(version, "")
	case c.kind == "aws-kms" && e.Provider == kmswrap.ProviderAWS:
		// Pin whatever CONCRETE key identity the envelope recorded — a key ARN or a
		// bare key id. The test used to be `HasPrefix("arn:")`, which meant a
		// recorded bare key id lost to a configured alias even though AWS accepts
		// "key id, key ARN or alias" and the wrapper records the configured id
		// verbatim when an Encrypt response carries no KeyId. An ALIAS is the one
		// thing not worth pinning: it is late-bound and moves on rotation, which is
		// the failure the re-pin exists to prevent — and "alias" means both of its
		// accepted spellings, which is why this asks isAWSAlias rather than testing
		// a prefix.
		if recorded := strings.TrimSpace(e.KeyID); recorded != "" &&
			!isAWSAlias(recorded) && recorded != c.awsKeyID {
			return c.wrapperPinned("", recorded)
		}
	}
	return c.wrapperPinned("", "")
}

// isAWSAlias reports whether an AWS KMS key identifier names an ALIAS rather than
// a concrete key. Both accepted alias forms count: the bare name `alias/Example`
// and the alias ARN `arn:aws:kms:eu-west-1:111:alias/Example`. An alias is
// late-bound — it resolves at call time and moves when the operator repoints it —
// so it is never what an envelope should pin.
func isAWSAlias(keyID string) bool {
	return strings.HasPrefix(keyID, "alias/") || strings.Contains(keyID, ":alias/")
}

// awsEnv resolves one AWS credential/endpoint setting for THIS identity: the
// namespaced override first (`<prefix>_AWS_ACCESS_KEY_ID`), then the standard AWS
// variable. The namespaced form exists because a migration BETWEEN AWS accounts
// needs two different principals in one process, and the standard AWS_* names can
// only hold one. Everything else keeps working untouched: with no override set,
// this is the plain os.Getenv it replaced.
//
// The lookup stays here, at wrapper-BUILD time, rather than moving into the
// parser — the ledger-signer model this borrowed from re-reads credentials per
// use so an operator's refresher keeps them fresh, and caching them at parse time
// would silently break that.
func (c *keyWrapConfig) awsEnv(suffix, std string) string {
	if c.envPrefix != "" {
		if v := strings.TrimSpace(os.Getenv(c.envPrefix + suffix)); v != "" {
			return v
		}
	}
	return os.Getenv(std)
}

func (c *keyWrapConfig) wrapperPinned(azureVersion, awsKeyARN string) (secure.KeyWrapper, error) {
	switch c.kind {
	case "aws-kms":
		keyID := c.awsKeyID
		if awsKeyARN != "" {
			keyID = awsKeyARN
		}
		return kmswrap.NewAWS(kmswrap.AWSConfig{
			Region: c.awsRegion,
			KeyID:  keyID,
			Creds: kmswrap.AWSCreds{
				AccessKeyID:     c.awsEnv("_AWS_ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID"),
				SecretAccessKey: c.awsEnv("_AWS_SECRET_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY"),
				SessionToken:    c.awsEnv("_AWS_SESSION_TOKEN", "AWS_SESSION_TOKEN"),
			},
			Endpoint: strings.TrimSpace(c.awsEnv("_AWS_ENDPOINT_URL_KMS", "AWS_ENDPOINT_URL_KMS")),
		})
	case "gcp-kms":
		return kmswrap.NewGCP(kmswrap.GCPConfig{KeyName: c.gcpKey, Token: c.gcpToken})
	case "azure-kv":
		v := c.azureVersion
		if azureVersion != "" {
			v = azureVersion // the envelope's recorded wrapping version wins
		}
		return kmswrap.NewAzure(kmswrap.AzureConfig{
			VaultURL: c.azureVaultURL, KeyName: c.azureKeyName, KeyVersion: v, Token: c.azureToken,
		})
	default:
		return nil, fmt.Errorf("no key wrapper configured")
	}
}

// describe is the non-secret posture label logged at boot and shown by
// `keys status`.
func (c *keyWrapConfig) describe() string {
	switch c.kind {
	case "aws-kms":
		return "aws-kms " + c.awsKeyID
	case "gcp-kms":
		return "gcp-kms " + c.gcpKey
	case "azure-kv":
		id := c.azureVaultURL + "/keys/" + c.azureKeyName
		if c.azureVersion != "" {
			id += "/" + c.azureVersion
		}
		return "azure-kv " + id
	default:
		return "none"
	}
}

// openSealedEnvelope unwraps a sealed envelope under the configured KEK with the
// boot/CLI timeout. It is the single unwrap choke point: key loads and sealed
// operator configs both go through it.
func openSealedEnvelope(ctx context.Context, cfg *keyWrapConfig, e *secure.SealedEnvelope, purpose string) ([]byte, error) {
	w, err := cfg.wrapperFor(e)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, kmsCallTimeout)
	defer cancel()
	return e.Open(cctx, w, purpose)
}

// sealedCfgErr records the FIRST sealed-config custody failure, checked by the
// boot/collector entry points AFTER all loaders ran. Why a deferred check
// instead of erroring in each loader: the 12 operator-config loaders share a
// warn-and-degrade contract for an absent/IGNORABLE config, and none can fail
// the boot from where they run — but sealing a config is an explicit custody
// opt-in, so an envelope that CANNOT BE OPENED (revoked KEK, KMS outage,
// custody typo) must refuse the boot rather than silently running the
// subsystem unconfigured (e.g. sandbox isolation downgrading to the in-proc
// mock — exactly the silent gap docs/SECURITY-HARDENING.md forbids).
var (
	sealedCfgMu  sync.Mutex
	sealedCfgErr error
)

func noteSealedConfigFailure(path string, err error) error {
	wrapped := fmt.Errorf("sealed operator config %s cannot be opened: %w", path, err)
	sealedCfgMu.Lock()
	if sealedCfgErr == nil {
		sealedCfgErr = wrapped
	}
	sealedCfgMu.Unlock()
	return wrapped
}

// sealedConfigFailure returns the first sealed-config custody failure, or nil.
// boot() (and the collector command) fail closed on it after config loading.
func sealedConfigFailure() error {
	sealedCfgMu.Lock()
	defer sealedCfgMu.Unlock()
	return sealedCfgErr
}

// resetSealedConfigFailure clears the recorded failure (tests only).
func resetSealedConfigFailure() {
	sealedCfgMu.Lock()
	sealedCfgErr = nil
	sealedCfgMu.Unlock()
}

// --- advisory detection of CLEARTEXT secrets in an UNSEALED operator config.
//
// readOperatorConfig records (the PATH only, never the value) when a plaintext
// operator config appears to carry a secret on disk while it could be sealed
// (`keys seal`, CMEK) or externalized as a `<scheme>:<locator>` reference. boot()
// and the collector drain this AFTER loading and emit one advisory WARN per file:
// the loaders' warn-and-degrade contract covered an absent/ignorable config, never
// "you left a live credential in cleartext", which was otherwise silent at boot
// (docs/SECURITY-HARDENING.md: never a silent gap). It NEVER fails the boot — cleartext + mode 0600
// on an encrypted volume is a legitimate, if weaker, posture — the operator simply
// hears it once.
var (
	unsealedSecretMu    sync.Mutex
	unsealedSecretPaths = map[string]bool{}
)

func noteUnsealedSecretConfig(path string) {
	unsealedSecretMu.Lock()
	unsealedSecretPaths[path] = true
	unsealedSecretMu.Unlock()
}

// drainUnsealedSecretConfigs returns the recorded paths (sorted, deduplicated) and
// clears the set, so the next boot in the same process re-evaluates from scratch.
func drainUnsealedSecretConfigs() []string {
	unsealedSecretMu.Lock()
	defer unsealedSecretMu.Unlock()
	paths := make([]string, 0, len(unsealedSecretPaths))
	for p := range unsealedSecretPaths {
		paths = append(paths, p)
	}
	unsealedSecretPaths = map[string]bool{}
	sort.Strings(paths)
	return paths
}

// resetUnsealedSecretConfigs clears the recorded paths. Called at boot/collector
// start so a config removed since a prior boot does not leak a stale WARN, and used
// by tests.
func resetUnsealedSecretConfigs() {
	unsealedSecretMu.Lock()
	unsealedSecretPaths = map[string]bool{}
	unsealedSecretMu.Unlock()
}

// warnUnsealedSecretConfigs emits one advisory WARN per operator config that
// carried cleartext secrets unsealed. The wording adapts to whether a CMEK KEK is
// configured: with one, sealing is a single command away; without, the operator is
// pointed at configuring a KEK or externalizing the secret as a reference.
func warnUnsealedSecretConfigs(log *slog.Logger) {
	paths := drainUnsealedSecretConfigs()
	if len(paths) == 0 {
		return
	}
	kekConfigured := false
	if kw, err := loadKeyWrapConfig(); err == nil && kw != nil {
		kekConfigured = true
	}
	for _, p := range paths {
		if kekConfigured {
			log.Warn("operator config holds apparent cleartext secret(s) and is NOT sealed, though a customer-managed KEK is configured — seal it with `olivares keys seal` so the secrets are encrypted at rest (docs/08 §5)", "path", p)
		} else {
			log.Warn("operator config holds apparent cleartext secret(s) on disk — for production seal it under a customer-managed KEK (`olivares keys seal` with OLIVARES_KEY_WRAP), or externalize each secret as a file:/env:/store: reference; at minimum keep the file mode 0600 on an encrypted volume (docs/08 §5)", "path", p)
		}
	}
}

// Strong cleartext-secret VALUE shapes, matched anywhere in a config value.
// reProviderKey requires a LEFT token boundary (start-of-string or a non-alphanumeric)
// before `sk-`, so an ordinary identifier that merely contains the letters s-k-hyphen
// mid-word (di-sk-, ta-sk-, ri-sk-, fla-sk-, …) is NOT mistaken for an API key; the body
// keeps `-`/`_` so real keys (sk-ant-api03-…, sk-proj-…) still match.
var (
	reProviderKey   = regexp.MustCompile(`(^|[^A-Za-z0-9])sk-[A-Za-z0-9_-]{16,}`) // OpenAI/Anthropic-style API keys
	reAWSAccessKey  = regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)         // an AWS access key id
	reInlineCredURL = regexp.MustCompile(`://[^/\s:@]+:[^/\s@]+@`)                // user:password@host in a URL/DSN
)

// configHasInlineSecret reports whether an UNSEALED operator config appears to
// carry a cleartext secret on disk. It is deliberately conservative — strong
// indicators only — and skips `<scheme>:<locator>` references (the file:/env:/store:
// externalization path) and filesystem paths, so a config that keeps its secrets
// OUT of the file is not flagged. It returns only whether a secret exists, never
// the value, and is cheap enough to run on every config load.
func configHasInlineSecret(b []byte) bool {
	// A PEM PRIVATE KEY block is unambiguous; a public certificate/CA is
	// "BEGIN CERTIFICATE", which this substring does NOT match.
	if bytes.Contains(b, []byte("PRIVATE KEY-----")) {
		return true
	}
	var v any
	if err := json.Unmarshal(b, &v); err == nil {
		return jsonHasInlineSecret(v, "")
	}
	// Non-JSON payload (e.g. a PEM bundle): scan for strong secret VALUE shapes.
	return hasInlineSecretValue(string(b))
}

func hasInlineSecretValue(s string) bool {
	return reProviderKey.MatchString(s) || reAWSAccessKey.MatchString(s) || reInlineCredURL.MatchString(s)
}

// jsonHasInlineSecret walks a decoded JSON value. A string leaf is a secret when
// its VALUE matches a strong secret shape, or when its FIELD NAME implies a secret
// AND the value is a literal (non-empty, not a reference, not a path/placeholder).
func jsonHasInlineSecret(v any, key string) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if jsonHasInlineSecret(val, k) {
				return true
			}
		}
	case []any:
		for _, val := range t {
			if jsonHasInlineSecret(val, key) { // array elements inherit the parent key
				return true
			}
		}
	case string:
		if hasInlineSecretValue(t) {
			return true
		}
		if secretNameKey(key) && isLiteralSecretValue(t) {
			return true
		}
	}
	return false
}

// secretNameKey reports whether a JSON field name strongly implies a secret VALUE.
// *file/*path/*ref/*url/*uri keys hold LOCATIONS (not secrets) and are excluded.
func secretNameKey(key string) bool {
	k := strings.ToLower(key)
	switch {
	case strings.HasSuffix(k, "file"), strings.HasSuffix(k, "path"),
		strings.HasSuffix(k, "ref"), strings.HasSuffix(k, "url"), strings.HasSuffix(k, "uri"):
		return false
	}
	for _, n := range []string{
		"client_secret", "clientsecret", "secret_access_key", "aws_secret",
		"private_key", "privatekey", "passphrase", "password", "passwd",
	} {
		if strings.Contains(k, n) {
			return true
		}
	}
	return false
}

// isLiteralSecretValue reports whether s is a non-empty literal that is neither a
// `<scheme>:<locator>` reference, a filesystem path, a placeholder, nor a flag/toggle
// value — so a field whose NAME contains a secret word but whose VALUE is a switch
// (e.g. reset_password_enabled:"true", password_policy_min_length:"8") is not flagged.
func isLiteralSecretValue(s string) bool {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return false
	case secret.IsReference(s): // file:/env:/store:/vault:/aws-secretsmanager:/…
		return false
	case strings.HasPrefix(s, "/"), strings.HasPrefix(s, "./"), strings.HasPrefix(s, "~"):
		return false
	case s == "********", strings.HasPrefix(s, "<"), strings.HasPrefix(s, "${"):
		return false
	case isFlagLiteral(s):
		return false
	}
	return true
}

// isFlagLiteral reports whether s is an enum/bool/number toggle value rather than a
// credential — the value side of a policy/flag field (…_enabled, …_min_length, has_…).
func isFlagLiteral(s string) bool {
	switch strings.ToLower(s) {
	case "true", "false", "on", "off", "yes", "no", "enabled", "disabled", "none", "null":
		return true
	}
	for _, r := range s { // an all-digits count/length/threshold is not a secret
		if r < '0' || r > '9' {
			return false
		}
	}
	return true // reached only when every rune was a digit (s is non-empty here)
}

// readOperatorConfig reads an operator JSON config file, transparently opening
// it when it is a CMEK-sealed envelope (`keys seal`). Plaintext files behave
// exactly as before — sealing is per-file opt-in. A SEALED file that cannot be
// opened (no KEK configured, KMS refused, tampered envelope) is both surfaced
// to the caller AND recorded as a custody failure that fails the boot closed
// (sealedConfigFailure). Optional absence is handled before this function; once
// a path is supplied, an unopenable config is fatal.
func readOperatorConfig(path string) ([]byte, error) {
	b, err := os.ReadFile(path) //nolint:gosec // operator-provided config path
	if err != nil {
		return nil, err
	}
	if !secure.IsSealedEnvelope(b) {
		// the file is plaintext. If it appears to carry a cleartext secret on
		// disk, record the path so boot()/the collector can WARN once after loading
		// (advisory, never fatal — see warnUnsealedSecretConfigs). This is the only
		// behavior change: the bytes are returned exactly as before.
		if configHasInlineSecret(b) {
			noteUnsealedSecretConfig(path)
		}
		return b, nil
	}
	e, err := secure.DecodeSealedEnvelope(b)
	if err != nil {
		return nil, noteSealedConfigFailure(path, err)
	}
	cfg, err := loadKeyWrapConfig()
	if err != nil {
		return nil, noteSealedConfigFailure(path, err)
	}
	if cfg == nil {
		return nil, noteSealedConfigFailure(path, fmt.Errorf("no KEK is configured (%s) — the envelope cannot be opened without the customer-managed key", envKeyWrap))
	}
	pt, err := openSealedEnvelope(context.Background(), cfg, e, secure.PurposeOperatorConfig)
	if err != nil {
		return nil, noteSealedConfigFailure(path, err)
	}
	return pt, nil
}

// loadOperatorJSONConfig reads and unmarshals a JSON file that the operator explicitly
// selected through envName. Callers handle an unset environment variable before calling
// this helper. Once a path was supplied, every read or syntax error is fatal: silently
// omitting requested governance wiring would leave the process less governed than the
// operator intended.
func loadOperatorJSONConfig(envName, path string, dst any) error {
	b, err := readOperatorConfig(path)
	if err != nil {
		return fmt.Errorf("%s is set to %q but the file cannot be read; refusing to start instead of silently omitting operator configuration: %w", envName, path, err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return fmt.Errorf("%s is set to %q but the file contains invalid JSON; refusing to start instead of silently omitting operator configuration: %w", envName, path, err)
	}
	return nil
}

// loadOperatorInlineJSONConfig unmarshals JSON supplied directly through envName.
// Callers handle an unset value before calling. It is the inline counterpart of
// loadOperatorJSONConfig: once an operator supplies the control, syntax errors are
// fatal rather than silently degrading to an unwired subsystem.
func loadOperatorInlineJSONConfig(envName, raw string, dst any) error {
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return fmt.Errorf("%s contains invalid inline JSON; refusing to start instead of silently omitting operator configuration: %w", envName, err)
	}
	return nil
}

// externalKeyCustodyConfigured reports whether the audit signing key comes from
// an external custody source (BYOK env/file; or a CMEK envelope)
// instead of the data dir. DR uses it: under external custody the data dir
// holds no *-signing.key to escrow, BY DESIGN — the customer custodies the key
// (their Secret / their KMS envelope), so a bundle without key material is the
// correct outcome there, not a packaging failure.
func externalKeyCustodyConfigured() bool {
	return strings.TrimSpace(os.Getenv(envAuditKey)) != "" ||
		strings.TrimSpace(os.Getenv(envAuditKeyFile)) != "" ||
		strings.TrimSpace(os.Getenv(envAuditWrapped)) != ""
}

// orDash renders an absent value as a dash — for structured logs, and for the
// Source plan/diff table, where an empty column is indistinguishable from a
// value the renderer dropped.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// custodyAssertions is the declared posture parsed from the environment.
type custodyAssertions struct {
	auditKey string // "" | "byok" | "cmek"
	ledger   string // "" | "hyok"
}

func loadCustodyAssertions() (custodyAssertions, error) {
	a := custodyAssertions{
		auditKey: strings.TrimSpace(os.Getenv(envKeyCustody)),
		ledger:   strings.TrimSpace(os.Getenv(envLedgerCustody)),
	}
	switch a.auditKey {
	case "", "byok", "cmek":
	default:
		return a, fmt.Errorf("%s=%q unknown (use \"\"|byok|cmek)", envKeyCustody, a.auditKey)
	}
	switch a.ledger {
	case "", "hyok":
	default:
		return a, fmt.Errorf("%s=%q unknown (use \"\"|hyok)", envLedgerCustody, a.ledger)
	}
	return a, nil
}

// verify fails the boot when the ACTUAL custody does not satisfy the DECLARED
// posture. auditMode is the mode loadAuditSigningKey actually used; offBox
// reports whether checkpoints are signed off-box.
func (a custodyAssertions) verify(auditMode string, offBox bool) error {
	switch a.auditKey {
	case "byok":
		if auditMode != custodyModeBYOKEnv && auditMode != custodyModeBYOKFile {
			return fmt.Errorf("%s=byok but the audit signing key came from %q — provision it via %s/%s (a minted or KEK-wrapped key does not satisfy a declared BYOK posture)", envKeyCustody, auditMode, envAuditKey, envAuditKeyFile)
		}
	case "cmek":
		if auditMode != custodyModeCMEK {
			return fmt.Errorf("%s=cmek but the audit signing key came from %q — provision a sealed envelope via %s + %s", envKeyCustody, auditMode, envAuditWrapped, envKeyWrap)
		}
	}
	if a.ledger == "hyok" && !offBox {
		return fmt.Errorf("%s=hyok but ledger checkpoints are signed ON-BOX — configure the off-box signer (OLIVARES_LEDGER_SIGNER, see ledgersigner.go)", envLedgerCustody)
	}
	return nil
}
