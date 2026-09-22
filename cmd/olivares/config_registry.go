// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/webaddr"
)

const (
	envConfigStrict     = "OLIVARES_CONFIG_STRICT"
	redactedConfigValue = "<redacted>"
)

type configKeyMode uint8

const (
	configKeyUnknown configKeyMode = iota
	configKeyExact
	configKeyPrefix
	configKeyTestOnly
)

// exactConfigEnvKeys is the production OLIVARES_* contract. Keep it in sync by
// running `grep -rhoE 'OLIVARES_[A-Z0-9_]+' cmd/olivares core modules | sort -u`,
// then classify runtime-constructed families and test-only sentinels in the lists
// below. Exclude registry/test literals themselves when checking for removals.
var exactConfigEnvKeys = []string{
	// the default for `--actor` on the decision-bearing verbs
	// (cmd_eventing_egress.go:353, cmd_eventing_fence.go:570).
	"OLIVARES_ACTOR",
	"OLIVARES_AGENTCORE_EXPORT_CONFIG",
	"OLIVARES_AGENT_GATEWAY_CONFIG",
	// C08-01: the environment form of --allow-cleartext (clitransport.go:38). It is
	// the ONLY way to consent to sending a credential over plain HTTP from the CLI
	// surfaces that carry no auth flags, so a run that sets it must not fail
	// --strict. Unset, empty, "0" and anything else all mean "no".
	"OLIVARES_ALLOW_CLEARTEXT",
	"OLIVARES_APPROVAL_BRIDGE_CONFIG",
	"OLIVARES_AUDIT_ARCHIVE_CONFIG",
	"OLIVARES_AUDIT_ARCHIVE_DIR",
	"OLIVARES_AUDIT_ARCHIVE_INTERVAL",
	"OLIVARES_AUDIT_ARCHIVE_RETAIN_DAYS",
	"OLIVARES_AUDIT_ARCHIVE_SEGMENT_EVENTS",
	"OLIVARES_AUDIT_ARCHIVE_SINK",
	"OLIVARES_AUDIT_LEGALHOLD_INTERVAL",
	// Private operator census (2026-09-11): private enterprise code reads this key and the
	// eight marked below through osGetenv. This key and OLIVARES_CIRCUIT_BREAKER_CONFIG are
	// strconv.ParseBool switches, OLIVARES_LOGIN_ENFORCEMENT is the login break-glass switch,
	// and the other six are file paths. Unregistered, `config validate` and `config effective
	// --strict` refused a deployment that set any of them. Registering the NAME claims nothing
	// more: build linkage, value parsing and file validity stay with those readers, and these
	// verbs display the value without opening a referenced file. OLIVARES_DURABLE and
	// OLIVARES_DURABLE_DEDUP stay unregistered: they are JetStream resource names, not keys.
	"OLIVARES_AUDIT_LEGALHOLD_RECONCILE",
	// read at boot (auditspool.go:97) to select the metadata-commitment write
	// rule. The registry's OLIVARES_AUDIT_ARCHIVE_ prefix does not reach it.
	"OLIVARES_AUDIT_META_BLINDING",
	"OLIVARES_AUDIT_SIGNING_KEY",
	"OLIVARES_AUDIT_SIGNING_KEY_FILE",
	"OLIVARES_AUDIT_SIGNING_KEY_WRAPPED_FILE",
	"OLIVARES_AUDIT_SPOOL_MAX_BYTES",
	"OLIVARES_AUDIT_SPOOL_ON_FULL",
	"OLIVARES_AUTHZEN_ALLOWED_CIDRS",
	"OLIVARES_AUTHZEN_DISABLED",
	"OLIVARES_AUTHZEN_EXPORT_DISABLED",
	"OLIVARES_AUTHZEN_SEARCH_DISABLED",
	"OLIVARES_BASE_URL",
	"OLIVARES_BUS_CONFIG",
	"OLIVARES_CAEP_TRANSMITTER_CONFIG",
	"OLIVARES_CATALOG_SIGNING_KEY",
	"OLIVARES_CATALOG_SIGNING_KEY_FILE",
	"OLIVARES_CATALOG_SIGNING_KEY_WRAPPED_FILE",
	"OLIVARES_CIRCUIT_BREAKER_CONFIG", // private operator input, see OLIVARES_AUDIT_LEGALHOLD_RECONCILE
	"OLIVARES_CLAUDE_ADMIN_ACTUATOR_CONFIG",
	"OLIVARES_CLAUDE_ADMIN_KEY",
	"OLIVARES_CLAUDE_ERASER_CONFIG",
	"OLIVARES_CLAUDE_FILES_CONFIG",
	"OLIVARES_CLAUDE_INFERENCE_KEY",
	"OLIVARES_CLAUDE_WORKSPACE_ID",
	// the CLI's own config-path override (cliconfig.go:20), used by hermetic
	// automation. A product key, so a run that sets it must not fail --strict.
	"OLIVARES_CLI_CONFIG",
	"OLIVARES_CLI_TRAMPOLINE",
	// K3 lot A (2026-09-05): the REQUESTED activation flag (on|off) parsed by
	// loadCommunicationActivationConfig, and the cursor keyring custody file
	// loaded next to the content keyring (communicationcursorkeyring.go).
	"OLIVARES_COMMUNICATION_ACTIVATION",
	"OLIVARES_COMMUNICATION_CONTENT_KEYRING_FILE",
	"OLIVARES_COMMUNICATION_CURSOR_KEYRING_FILE",
	// T9 (2026-09-11): the enterprise addon_airs constructors read this key and the four
	// marked below (cmd-overlay/olivares/wire_enterprise_addon_airs.go), just as they read
	// OLIVARES_HOOK_FIREWALL_CONFIG. Unregistered, a boot that enforced the content firewall
	// also logged its key as ignored, and `config validate` / `config effective --strict`
	// refused the deployment. Registering the NAME claims nothing more: whether the build
	// links the control and whether the referenced file is valid stay with those
	// constructors, and these verbs display the path without opening it.
	"OLIVARES_COMPUTER_USE_CONFIG",
	"OLIVARES_CONFIG_STRICT",
	"OLIVARES_CONTENT_FIREWALL_CONFIG", // T9 addon_airs input, see OLIVARES_COMPUTER_USE_CONFIG
	"OLIVARES_CONTEXT_MAX_TOKENS",
	"OLIVARES_CONTEXT_STRATEGY",
	"OLIVARES_CREDENTIAL_MINTER_CONFIG", // private operator input, see OLIVARES_AUDIT_LEGALHOLD_RECONCILE
	"OLIVARES_DATA_DIR",
	"OLIVARES_DB_MAX_CONNS",
	"OLIVARES_DEPLOY_EXECUTOR_CONFIG",
	"OLIVARES_DR_KEK_FILE",
	"OLIVARES_DR_OFFSITE_ACCESS_KEY_ID_FILE",
	"OLIVARES_DR_OFFSITE_BUCKET",
	"OLIVARES_DR_OFFSITE_ENDPOINT",
	"OLIVARES_DR_OFFSITE_PREFIX",
	"OLIVARES_DR_OFFSITE_REGION",
	"OLIVARES_DR_OFFSITE_SECRET_ACCESS_KEY_FILE",
	"OLIVARES_DR_OFFSITE_SESSION_TOKEN_FILE",
	"OLIVARES_DR_PASSPHRASE_FILE",
	"OLIVARES_DR_SCHEDULE_INTERVAL",
	"OLIVARES_DURABLE_BUS_CONFIG",
	"OLIVARES_ELICITATION_MEDIATOR_CONFIG", // T9 addon_airs input, see OLIVARES_COMPUTER_USE_CONFIG
	"OLIVARES_EMBEDDINGS_BASE_URL",
	"OLIVARES_EMBEDDINGS_DIM",
	"OLIVARES_EMBEDDINGS_GEO",
	"OLIVARES_EMBEDDINGS_KEY",
	"OLIVARES_EMBEDDINGS_MODEL",
	"OLIVARES_EMBEDDINGS_OPENAI_BASE_URL",
	"OLIVARES_EMBEDDINGS_OPENAI_COMPAT_BASE_URL",
	"OLIVARES_EMBEDDINGS_OPENAI_COMPAT_DIM",
	"OLIVARES_EMBEDDINGS_OPENAI_COMPAT_GEO",
	"OLIVARES_EMBEDDINGS_OPENAI_COMPAT_KEY",
	"OLIVARES_EMBEDDINGS_OPENAI_COMPAT_MODEL",
	"OLIVARES_EMBEDDINGS_OPENAI_DIM",
	"OLIVARES_EMBEDDINGS_OPENAI_GEO",
	"OLIVARES_EMBEDDINGS_OPENAI_KEY",
	"OLIVARES_EMBEDDINGS_OPENAI_MODEL",
	"OLIVARES_EMBEDDINGS_PROVIDER",
	"OLIVARES_EMBEDDINGS_REQUIRE",
	"OLIVARES_EMBEDDINGS_SELF_HOSTED_BASE_URL",
	"OLIVARES_EMBEDDINGS_SELF_HOSTED_DIM",
	"OLIVARES_EMBEDDINGS_SELF_HOSTED_GEO",
	"OLIVARES_EMBEDDINGS_SELF_HOSTED_KEY",
	"OLIVARES_EMBEDDINGS_SELF_HOSTED_MODEL",
	"OLIVARES_EMBEDDINGS_VOYAGE_BASE_URL",
	"OLIVARES_EMBEDDINGS_VOYAGE_DIM",
	"OLIVARES_EMBEDDINGS_VOYAGE_GEO",
	"OLIVARES_EMBEDDINGS_VOYAGE_KEY",
	"OLIVARES_EMBEDDINGS_VOYAGE_MODEL",
	"OLIVARES_EVALS_MONITOR_WINDOW",
	"OLIVARES_EVENTING_ALLOW_LOOPBACK",
	"OLIVARES_EVENTING_DISPATCH_INTERVAL",
	// read and HONORED at boot (eventingegress.go:84-100) — it is the operator
	// ceiling on where a tenant may point its event stream. Missing here, the boot that
	// had just logged "egress destination policy IN FORCE; source=OLIVARES_EVENTING_EGRESS_POLICY"
	// also logged "unrecognized OLIVARES_* env keys ignored" about the SAME key, and
	// `config effective --strict` — the documented CI/pre-production gate — REFUSED the
	// deployment (measured 2026-08-10: exit 1 with it set, 0 without). A security control
	// whose configuration fails the gate that is supposed to bless it teaches operators to
	// stop configuring it.
	"OLIVARES_EVENTING_EGRESS_POLICY",
	"OLIVARES_EVENTING_RETENTION",
	"OLIVARES_EVENTING_SECRET_KEY",
	"OLIVARES_EXECUTION_ENVIRONMENT_ID",
	"OLIVARES_EXTRA_ARGS",
	"OLIVARES_GUARDIAN_SWEEP_INTERVAL",
	"OLIVARES_HA_LEADER_GATE",
	"OLIVARES_HA_LEADER_LABEL",
	"OLIVARES_HITL_CONFIG",
	"OLIVARES_HOOK_FIREWALL_CONFIG",
	"OLIVARES_HOOK_PEP_ACCOUNT",
	"OLIVARES_HOOK_PEP_AGENT",
	"OLIVARES_HOOK_PEP_CONFIG",
	"OLIVARES_HOOK_PEP_ORG",
	"OLIVARES_HOOK_PEP_TENANT",
	"OLIVARES_HOOK_PEP_TOKEN",
	"OLIVARES_HOOK_PEP_URL",
	"OLIVARES_INCIDENTLOOP_CONFIG",
	"OLIVARES_INFERENCE_PROXY_CONFIG",
	"OLIVARES_INGEST_TOKEN",
	"OLIVARES_INSECURE",
	"OLIVARES_KEY_CUSTODY",
	"OLIVARES_KEY_WRAP",
	"OLIVARES_KEY_WRAP_AWS_KEY_ID",
	"OLIVARES_KEY_WRAP_AWS_REGION",
	"OLIVARES_KEY_WRAP_AZURE_KEY_NAME",
	"OLIVARES_KEY_WRAP_AZURE_KEY_VERSION",
	"OLIVARES_KEY_WRAP_AZURE_TOKEN",
	"OLIVARES_KEY_WRAP_AZURE_VAULT_URL",
	"OLIVARES_KEY_WRAP_GCP_KEY",
	"OLIVARES_KEY_WRAP_GCP_TOKEN",
	"OLIVARES_KEY_WRAP_GCP_TOKEN_FILE",
	"OLIVARES_LEDGER_CUSTODY",
	"OLIVARES_LEDGER_KMS_AWS_KEY_ID",
	"OLIVARES_LEDGER_KMS_AWS_REGION",
	"OLIVARES_LEDGER_KMS_AWS_SIGNING_ALG",
	"OLIVARES_LEDGER_KMS_AZURE_KEY_NAME",
	"OLIVARES_LEDGER_KMS_AZURE_KEY_VERSION",
	"OLIVARES_LEDGER_KMS_AZURE_TOKEN",
	"OLIVARES_LEDGER_KMS_AZURE_VAULT_URL",
	"OLIVARES_LEDGER_KMS_GCP_KEY",
	"OLIVARES_LEDGER_KMS_GCP_TOKEN",
	"OLIVARES_LEDGER_KMS_GCP_TOKEN_FILE",
	"OLIVARES_LEDGER_SIGNER",
	"OLIVARES_LICENSE",
	"OLIVARES_LICENSE_PATH",
	"OLIVARES_LICENSE_PUBKEY",
	"OLIVARES_LIVEINGEST_INSPECT_OBSERVED_REFS",
	"OLIVARES_LOGIN_ENFORCEMENT", // private operator input, see OLIVARES_AUDIT_LEGALHOLD_RECONCILE
	"OLIVARES_LOG_LEVEL",
	"OLIVARES_MCP_TASK_KILLSWITCH_SWEEP",
	"OLIVARES_METRICS_ALLOWED_CIDRS",
	"OLIVARES_METRICS_TOKEN",
	// The ONE explicit activation for the governed synchronous Chat dispatch
	// path (modelsguardedtext.go). Absent and "disabled" both keep it deny-closed;
	// only "development_precheck" wires it, and any other value fails startup.
	"OLIVARES_MODEL_GATEWAY_CHAT_MODE",
	"OLIVARES_MODEL_GATEWAY_PROFILES_CONFIG",
	"OLIVARES_NHI_ACTUATORS_CONFIG",
	"OLIVARES_NIS2INCIDENT_CONFIG",
	"OLIVARES_NOTIFY_CONFIG",
	"OLIVARES_NOTIFY_DISPATCH_INTERVAL",
	"OLIVARES_OIDC_CLIENT_ID",
	"OLIVARES_OIDC_CLIENT_SECRET",
	"OLIVARES_OIDC_GROUPS_CLAIM",
	"OLIVARES_OIDC_ISSUER",
	"OLIVARES_ONBOARDING_CONFIG", // private operator input, see OLIVARES_AUDIT_LEGALHOLD_RECONCILE
	"OLIVARES_ORCH_CADENCE_INTERVAL",
	"OLIVARES_ORCH_DISPATCH_CONFIG",
	"OLIVARES_ORCH_WORKFLOW_INTERVAL",
	"OLIVARES_ORCH_WORKFLOW_MAX",
	"OLIVARES_ORCH_WORKFLOW_STEPS_MAX",
	"OLIVARES_OTA_PUBKEY",
	"OLIVARES_OTEL_ENABLED",
	"OLIVARES_OTEL_ENDPOINT",
	"OLIVARES_OTEL_GENAI_COMPAT",
	"OLIVARES_OTEL_INSECURE",
	"OLIVARES_OTEL_PROTOCOL",
	"OLIVARES_OTEL_SAMPLE_RATIO",
	"OLIVARES_OTEL_SERVICE_NAME",
	"OLIVARES_PDP_CEDAR_FILE",
	"OLIVARES_PDP_ENGINE",
	"OLIVARES_PDP_OPA_PATH",
	"OLIVARES_PDP_OPA_TOKEN",
	"OLIVARES_PDP_OPA_URL",
	"OLIVARES_PIV_CONFIG",
	"OLIVARES_POLICY_MAX_STALENESS",
	"OLIVARES_POLICY_SIGNING_KEY",
	"OLIVARES_POLICY_SIGNING_KEY_FILE",
	"OLIVARES_POLICY_SIGNING_KEY_WRAPPED_FILE",
	"OLIVARES_PQC_POSTURE_CONFIG", // private operator input, see OLIVARES_AUDIT_LEGALHOLD_RECONCILE
	// The environment spelling of --public-url: the address a browser reaches this
	// console at. It has to be listed here or the engine logs "unrecognized
	// OLIVARES_* env keys ignored" about a key it is honoring in the same boot,
	// and `config effective --strict` — the documented pre-production gate —
	// REFUSES a deployment for setting it.
	"OLIVARES_PUBLIC_URL",
	"OLIVARES_RATELIMIT_CONFIG",
	"OLIVARES_RATELIMIT_STORE",
	"OLIVARES_RENDER_INSPECTOR_CONFIG", // T9 addon_airs input, see OLIVARES_COMPUTER_USE_CONFIG
	"OLIVARES_REPORTING_CONFIG",
	"OLIVARES_REPORTING_SCHEDULE_INTERVAL",
	"OLIVARES_REPORT_CACHE_DIR",
	"OLIVARES_RETENTION_GOVERNOR_CONFIG", // private operator input, see OLIVARES_AUDIT_LEGALHOLD_RECONCILE
	"OLIVARES_RETENTION_SWEEP_INTERVAL",
	"OLIVARES_RTBF_DEPTH_CONFIG", // private operator input, see OLIVARES_AUDIT_LEGALHOLD_RECONCILE
	"OLIVARES_SAML_ACS_URL",
	"OLIVARES_SAML_EMAIL_ATTRIBUTE",
	"OLIVARES_SAML_GROUPS_ATTRIBUTE",
	"OLIVARES_SAML_IDP_METADATA_URL",
	"OLIVARES_SAML_IDP_SSO_URL",
	"OLIVARES_SAML_SP_CERT_PEM",
	"OLIVARES_SAML_SP_ENTITY_ID",
	"OLIVARES_SAML_SP_KEY_PEM",
	"OLIVARES_SAML_SP_SIGN_CERT_PEM",
	"OLIVARES_SAML_SP_SIGN_KEY_PEM",
	"OLIVARES_SANDBOX_RUNTIME_CONFIG",
	"OLIVARES_SECRET_STORE_KEY",
	"OLIVARES_SERVERTOOL_EGRESS_CONFIG", // T9 addon_airs input, see OLIVARES_COMPUTER_USE_CONFIG
	"OLIVARES_SERVER_URL",
	"OLIVARES_SESSIONS_AGENT_LINK_LISTEN", // an edition built on this tree reads it, see agentLinkListenKey
	"OLIVARES_SESSIONS_MANAGED_STOP_ADMISSION_TIMEOUT",
	"OLIVARES_SESSION_BUDGET_AVAILABILITY",
	"OLIVARES_SESSION_CONTEXT_AVAILABILITY",
	"OLIVARES_SESSION_KILLSWITCH_SWEEP",
	"OLIVARES_SESSION_PEP_TOKEN_FILE",
	"OLIVARES_SESSION_PEP_URL",
	"OLIVARES_SESSION_RUNTIME_BASE_URL",
	"OLIVARES_SESSION_RUNTIME_CLAUDE_BIN",
	"OLIVARES_SESSION_RUNTIME_TOKEN_FILE",
	"OLIVARES_SESSION_RUNTIME_TOKEN_TTL",
	"OLIVARES_SESSION_RUNTIME_WIF",
	"OLIVARES_SESSION_RUNTIME_WIF_RULE",
	"OLIVARES_SIEM_FORWARD_INTERVAL",
	"OLIVARES_SOURCES_CONFIG",
	"OLIVARES_SSO_PROTOCOL",
	"OLIVARES_SSO_SECRET_KEY",
	"OLIVARES_TARGET_BINDING_KEY",
	"OLIVARES_TARGET_BINDING_KEY_FILE",
	"OLIVARES_TENANT",
	"OLIVARES_THREATINTEL_CONFIG",
	"OLIVARES_THREATINTEL_SIGNING_KEY",
	"OLIVARES_TOKEN",
	"OLIVARES_TOOL_PIN_CONFIG", // private operator input, see OLIVARES_AUDIT_LEGALHOLD_RECONCILE
	"OLIVARES_UPDATE_CHANNEL",
	"OLIVARES_UPDATE_ENDPOINT",
	"OLIVARES_UPGRADE_TOKEN",
	"OLIVARES_VECTOR_API_KEY",
	"OLIVARES_VECTOR_BACKEND",
	"OLIVARES_VECTOR_DIM",
	"OLIVARES_VECTOR_DSN",
	"OLIVARES_VECTOR_NAMESPACE",
	"OLIVARES_VECTOR_TIMEOUT",
	"OLIVARES_VOICE_CALL_CONFIG",
	"OLIVARES_VOICE_DISPATCH_CONFIG",
	"OLIVARES_WEBAUTHN_ORIGINS",
	"OLIVARES_WEBAUTHN_RPID",
	"OLIVARES_WEBAUTHN_RP_NAME",
	"OLIVARES_WIF_BASE_URL",
	"OLIVARES_WIF_REFRESH_SLACK",
	"OLIVARES_WIF_SPIFFE_SOCKET",
	"OLIVARES_WIF_TRUST_DOMAIN",
	"OLIVARES_WORK_OUTBOX_INTERVAL",
}

// prefixConfigEnvKeys covers families whose members are constructed at runtime or
// passed to child processes. Concrete members remain above so the grep-derived
// contract is reviewable; prefixes admit valid dynamically constructed members.
var prefixConfigEnvKeys = []string{
	"OLIVARES_AUDIT_ARCHIVE_",
	// the Codex hook family — the exact twin of OLIVARES_HOOK_PEP_ below, which
	// has been a registered prefix all along. The Codex PEP server config
	// (codexhookpepserver.go:59) and the six hook-client keys (cmd_codexhook.go) were
	// all reported "unrecognized" by the same boot that mounts them. One tier-1
	// integration registered and its tier-2 twin not is the shape a registry drifts in.
	"OLIVARES_CODEX_HOOK_",
	"OLIVARES_DR_OFFSITE_",
	"OLIVARES_EMBEDDINGS_",
	// Y el mismo caso otra vez, ahora con Grok (PR #1011, 2026-08-19): siete claves leidas
	// —seis en cmd_grokhook.go y OLIVARES_GROK_HOOK_PEP_CONFIG en grokhookpepserver.go— y
	// ninguna registrada, de modo que `config validate --strict` habria RECHAZADO un despliegue
	// que las pusiera. Tercera vez que la familia de un hook llega sin su prefijo. Va AQUI y no
	// junto a su gemelo de Codex porque la lista esta ORDENADA y hay un test que lo comprueba.
	"OLIVARES_GROK_HOOK_",
	"OLIVARES_HOOK_PEP_",
	"OLIVARES_KEY_WRAP_",
	"OLIVARES_LEDGER_KMS_",
	"OLIVARES_OIDC_",
	"OLIVARES_OTEL_",
	"OLIVARES_SAML_",
	"OLIVARES_SESSION_RUNTIME_",
	"OLIVARES_VECTOR_",
	"OLIVARES_WIF_",
}

var (
	testOnlyConfigEnvKeys = []string{
		"OLIVARES_DEFINITELY_UNSET_KEY_XYZ",
		"OLIVARES_E2E_MARKER_OK",
	}
	testOnlyConfigEnvPrefixes = []string{
		"OLIVARES_E2E_",
		"OLIVARES_TEST_",
	}
)

func configEnvKeyMode(key string) configKeyMode {
	if sortedContains(testOnlyConfigEnvKeys, key) || hasAnyPrefix(key, testOnlyConfigEnvPrefixes) {
		return configKeyTestOnly
	}
	if sortedContains(exactConfigEnvKeys, key) {
		return configKeyExact
	}
	if hasAnyPrefix(key, prefixConfigEnvKeys) {
		return configKeyPrefix
	}
	return configKeyUnknown
}

func sortedContains(values []string, target string) bool {
	i := sort.SearchStrings(values, target)
	return i < len(values) && values[i] == target
}

func hasAnyPrefix(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

// unknownConfigEnvKeys returns a sorted, deduplicated list of unrecognized
// OLIVARES_* keys. Values are never retained or returned.
func unknownConfigEnvKeys(environ []string) []string {
	unknown := make(map[string]struct{})
	for _, entry := range environ {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(key, "OLIVARES_") {
			continue
		}
		if configEnvKeyMode(key) == configKeyUnknown {
			unknown[key] = struct{}{}
		}
	}
	keys := make([]string, 0, len(unknown))
	for key := range unknown {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// effectiveConfigEnv returns only configured production keys. getenv resolves the
// normal environment plus the enterprise activation overlay; test-only and unknown
// keys never enter the dump.
func effectiveConfigEnv(environ []string, getenv func(string) string) map[string]string {
	names := make(map[string]struct{})
	for _, entry := range environ {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		mode := configEnvKeyMode(key)
		if mode == configKeyExact || mode == configKeyPrefix {
			names[key] = struct{}{}
		}
	}
	// An activation-manifest value is not present in os.Environ. Probe every
	// concrete key through osGetenv so active overlays appear as effective values.
	for _, key := range exactConfigEnvKeys {
		if getenv(key) != "" {
			names[key] = struct{}{}
		}
	}

	effective := make(map[string]string, len(names))
	for key := range names {
		effective[key] = redactEffectiveConfigValue(key, getenv(key))
	}
	return effective
}

// effectiveConfigEntries projects the CLI's exact registry and redaction result
// into the API value contract. The caller supplies a fresh environ/getenv pair
// on every request so activation-overlay changes stay visible without an API
// dependency on package main.
// resolvedPublicAddr is what the RUNNING invocation actually resolved, handed to
// effectiveConfigEntries by the engine's callback. The standalone `config
// effective` command has no such thing — it is a different process and cannot
// observe a running one — so it passes the zero value and the entry keeps its
// redacted requested-environment meaning.
type resolvedPublicAddr struct {
	addr   webaddr.Address
	source publicAddrSource
	// known distinguishes "this caller resolved an address" from "this caller has
	// no idea", which is not the same as "no address was declared".
	known bool
}

func effectiveConfigEntries(environ []string, getenv func(string) string) []api.EffectiveConfigEntry {
	return effectiveConfigEntriesFor(environ, getenv, resolvedPublicAddr{})
}

// effectiveConfigEntriesFor is effectiveConfigEntries with the running
// invocation's declared address, when the caller has one.
//
// THE ENGINE'S CALLBACK KNOWS WHAT THE COMMAND CANNOT. cfg.PublicAddr and
// cfg.PublicAddrSource are the values THIS process resolved at startup, so the
// row can state the address actually in force and where it came from — including
// the case where an explicit empty flag cleared an environment value that is
// still set in the environment this projection is reading. Showing that
// overridden environment value would describe a configuration the running engine
// is not using.
//
// The address is never re-read after startup: it comes from the resolution that
// already happened, so a later mutation of the environment cannot change what
// this row reports. That is the point — the row is about the running invocation.
func effectiveConfigEntriesFor(environ []string, getenv func(string) string, running resolvedPublicAddr) []api.EffectiveConfigEntry {
	envValues := make(map[string]string)
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			envValues[key] = value
		}
	}

	resolved := make(map[string]string)
	cachedGetenv := func(key string) string {
		if value, ok := resolved[key]; ok {
			return value
		}
		value := getenv(key)
		resolved[key] = value
		return value
	}
	effective := effectiveConfigEnv(environ, cachedGetenv)
	keys := make([]string, 0, len(effective))
	for key := range effective {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	entries := make([]api.EffectiveConfigEntry, 0, len(keys))
	for _, key := range keys {
		source := "env"
		envValue, inEnvironment := envValues[key]
		if !inEnvironment || envValue == "" && cachedGetenv(key) != envValue {
			source = "activation"
		}
		value := effective[key]
		if key == publicURLEnv && running.known {
			value, source = running.addr.Origin, running.source.String()
		}
		entries = append(entries, api.EffectiveConfigEntry{
			Key: key, Value: value, Redacted: value == redactedConfigValue, Source: source,
		})
	}
	// An explicit empty flag CLEARS the address, and the environment key may still
	// be set — so the key can be absent from the projection above while the
	// running invocation has a truthful thing to say about it. Say it.
	if running.known && !containsKey(keys, publicURLEnv) {
		entries = append(entries, api.EffectiveConfigEntry{
			Key: publicURLEnv, Value: running.addr.Origin, Source: running.source.String(),
		})
		sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	}
	return entries
}

func containsKey(keys []string, want string) bool {
	for _, k := range keys {
		if k == want {
			return true
		}
	}
	return false
}

func redactEffectiveConfigValue(key, value string) string {
	if secretNameKey(key) {
		return redactedConfigValue
	}
	// THE DECLARED CONSOLE ADDRESS IS SHOWN ONLY IF THE ENGINE WOULD ACCEPT IT.
	//
	// This surface reports the REQUESTED environment configuration of this host.
	// It is a separate invocation from any running engine, so it cannot know which
	// flags that engine was given, and --public-url overrides this variable — the
	// value here may therefore never have been used. What it must not do is print
	// a value the engine REFUSES, because a refused one is exactly the class that
	// can carry a credential or a token in its userinfo or query, and this output
	// is read and pasted far from the person who typed it.
	//
	// Accepted values are shown canonicalized, which is both what the engine would
	// use and a form that cannot carry userinfo, a query or a fragment — Parse
	// refuses all three. A refused value is reported as redacted, not echoed and
	// not guessed at.
	if key == publicURLEnv {
		if strings.TrimSpace(value) == "" {
			return value
		}
		addr, err := webaddr.Parse(publicURLEnv, value)
		if err != nil {
			return redactedConfigValue
		}
		return addr.Origin
	}
	for _, part := range strings.Split(key, "_") {
		switch part {
		case "KEY", "TOKEN", "SECRET", "PASSPHRASE", "PEM":
			return redactedConfigValue
		}
	}
	// Reuse custody.go's strong inline-credential detector: a DSN reference/path is
	// safe to show, while a DSN carrying user:password credentials is not.
	if strings.Contains(key, "DSN") && hasInlineSecretValue(value) {
		return redactedConfigValue
	}
	return value
}
