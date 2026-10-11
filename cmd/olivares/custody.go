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

	"github.com/olivaresai/olivares/core/envconfig"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/core/secure"
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
	if strings.TrimSpace(envconfig.Get(envKeyWrap)) != "" {
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
	pt, err := openCMEKOperatorConfig(context.Background(), b)
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
	return strings.TrimSpace(envconfig.Get(envAuditKey)) != "" ||
		strings.TrimSpace(envconfig.Get(envAuditKeyFile)) != "" ||
		strings.TrimSpace(envconfig.Get(envAuditWrapped)) != ""
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
		auditKey: strings.TrimSpace(envconfig.Get(envKeyCustody)),
		ledger:   strings.TrimSpace(envconfig.Get(envLedgerCustody)),
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
