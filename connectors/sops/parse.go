// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sops

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// The recipient types this connector recognizes. They name the kind of key that
// can decrypt a SOPS file; each maps to a stable ref prefix and an edge ToolRef.
// Verified against github.com/getsops/sops (the file-metadata `sops:` block and
// the `.sops.yaml` creation_rules schema).
const (
	recipientAge     = "age"
	recipientKMS     = "kms"
	recipientGCPKMS  = "gcp_kms"
	recipientAzureKV = "azure_kv"
	recipientVault   = "hc_vault"
	recipientPGP     = "pgp"
)

// resourceKindFile is the resource kind every provisioning edge points at: the
// SOPS-encrypted file a recipient can decrypt.
const resourceKindFile = "sops.file"

// recipient is one public key identifier that can decrypt a SOPS file, extracted
// from a file's `sops:` metadata or a `.sops.yaml` rule. It carries ONLY the public
// identifier — never the per-recipient `enc` encrypted data key, which this package
// does not even read into a struct (so it cannot leak).
type recipient struct {
	// typ is the recipient kind (age|kms|gcp_kms|azure_kv|hc_vault|pgp).
	typ string
	// ref is the stable external id the edge OriginRef and the Snapshot identity
	// converge on (e.g. "sops.age:age1abc…", "sops.kms:arn:aws:kms:…").
	ref string
}

// displayName is a human label for the recipient in the secret_store inventory.
func (r recipient) displayName() string {
	switch r.typ {
	case recipientAge:
		return "SOPS age recipient"
	case recipientKMS:
		return "SOPS AWS KMS key"
	case recipientGCPKMS:
		return "SOPS GCP KMS key"
	case recipientAzureKV:
		return "SOPS Azure Key Vault key"
	case recipientVault:
		return "SOPS HashiCorp Vault key"
	case recipientPGP:
		return "SOPS PGP key"
	default:
		return "SOPS recipient"
	}
}

// fileMetadata is the cleartext SOPS metadata block embedded in an ENCRYPTED file
// (top-level `sops:` in YAML/JSON). Deliberately ABSENT: the per-recipient `enc`
// (the encrypted data key), `mac`, `lastmodified` — none are read into a field, so
// none can be emitted (docs/SECURITY-HARDENING.md). Only the PUBLIC recipient identifiers are read.
type fileMetadata struct {
	KMS       []metaKMS      `yaml:"kms"`
	GCPKMS    []metaGCPKMS   `yaml:"gcp_kms"`
	AzureKV   []metaAzureKV  `yaml:"azure_kv"`
	Vault     []metaVault    `yaml:"hc_vault"`
	Age       []metaAge      `yaml:"age"`
	PGP       []metaPGP      `yaml:"pgp"`
	KeyGroups []fileMetadata `yaml:"key_groups"`
}

// The per-recipient metadata rows. Each reads only the PUBLIC identifier; the `enc`
// data key present on every real row is intentionally NOT a field here.
type metaKMS struct {
	ARN string `yaml:"arn"`
}

type metaGCPKMS struct {
	ResourceID string `yaml:"resource_id"`
}

type metaAzureKV struct {
	VaultURL string `yaml:"vault_url"`
	Name     string `yaml:"name"`
}

type metaVault struct {
	VaultAddress string `yaml:"vault_address"`
	EnginePath   string `yaml:"engine_path"`
	KeyName      string `yaml:"key_name"`
}

type metaAge struct {
	Recipient string `yaml:"recipient"`
}

type metaPGP struct {
	FP string `yaml:"fp"`
}

// recipients flattens the metadata block (including any key_groups) into the
// distinct public recipients it names, in a stable type-then-value order.
func (m fileMetadata) recipients() []recipient {
	var out []recipient
	add := func(typ, ref string) {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return
		}
		out = append(out, recipient{typ: typ, ref: "sops." + typ + ":" + ref})
	}
	for _, k := range m.Age {
		add(recipientAge, k.Recipient)
	}
	for _, k := range m.KMS {
		add(recipientKMS, k.ARN)
	}
	for _, k := range m.GCPKMS {
		add(recipientGCPKMS, k.ResourceID)
	}
	for _, k := range m.AzureKV {
		if k.VaultURL == "" && k.Name == "" {
			continue
		}
		add(recipientAzureKV, strings.TrimRight(k.VaultURL, "/")+"/"+k.Name)
	}
	for _, k := range m.Vault {
		if k.VaultAddress == "" && k.KeyName == "" {
			continue
		}
		add(recipientVault, k.VaultAddress+k.EnginePath+"/"+k.KeyName)
	}
	for _, k := range m.PGP {
		add(recipientPGP, k.FP)
	}
	for _, g := range m.KeyGroups {
		out = append(out, g.recipients()...)
	}
	return dedupeRecipients(out)
}

// encryptedFile is the minimal view of a candidate SOPS file: a top-level `sops:`
// key marks it as encrypted, and that block is the only thing read. The rest of the
// file (the encrypted values) is never decoded into anything.
type encryptedFile struct {
	SOPS *fileMetadata `yaml:"sops"`
}

// parseEncrypted reads a YAML/JSON file's bytes and returns its SOPS recipients and
// whether the file is SOPS-encrypted (carries a top-level `sops:` block). It reads
// ONLY the `sops:` metadata — the rest of the document is left untouched. A parse
// error (not a SOPS document, or not YAML at all) yields ok=false, no error: a
// repo holds many files this connector simply ignores.
func parseEncrypted(data []byte) (recips []recipient, ok bool) {
	var f encryptedFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, false
	}
	if f.SOPS == nil {
		return nil, false
	}
	return f.SOPS.recipients(), true
}

// rulesFile is the `.sops.yaml` configuration: a list of creation_rules, each
// naming the recipients that encrypt files matching its path_regex. Note the Azure
// key in .sops.yaml is `azure_keyvault` (distinct from the file metadata's
// `azure_kv`), and `age`/`pgp` are comma/newline-separated strings here.
type rulesFile struct {
	CreationRules []creationRule `yaml:"creation_rules"`
}

type creationRule struct {
	PathRegex     string         `yaml:"path_regex"`
	Age           string         `yaml:"age"`
	PGP           string         `yaml:"pgp"`
	KMS           []ruleKMS      `yaml:"kms"`
	GCPKMS        []ruleGCPKMS   `yaml:"gcp_kms"`
	AzureKeyvault []ruleAzureKV  `yaml:"azure_keyvault"`
	VaultURI      string         `yaml:"hc_vault_transit_uri"`
	KeyGroups     []creationRule `yaml:"key_groups"`
}

type ruleKMS struct {
	ARN string `yaml:"arn"`
}

type ruleGCPKMS struct {
	ResourceID string `yaml:"resource_id"`
}

type ruleAzureKV struct {
	VaultURL string `yaml:"vaultUrl"`
	Key      string `yaml:"key"`
}

// recipients flattens one creation_rule into the distinct public recipients it
// configures. age/pgp are comma/newline-separated lists of identifiers.
func (r creationRule) recipients() []recipient {
	var out []recipient
	add := func(typ, ref string) {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return
		}
		out = append(out, recipient{typ: typ, ref: "sops." + typ + ":" + ref})
	}
	for _, a := range splitList(r.Age) {
		add(recipientAge, a)
	}
	for _, p := range splitList(r.PGP) {
		add(recipientPGP, p)
	}
	for _, k := range r.KMS {
		add(recipientKMS, k.ARN)
	}
	for _, k := range r.GCPKMS {
		add(recipientGCPKMS, k.ResourceID)
	}
	for _, k := range r.AzureKeyvault {
		if k.VaultURL == "" && k.Key == "" {
			continue
		}
		add(recipientAzureKV, strings.TrimRight(k.VaultURL, "/")+"/"+k.Key)
	}
	if uri := strings.TrimSpace(r.VaultURI); uri != "" {
		add(recipientVault, uri)
	}
	for _, g := range r.KeyGroups {
		out = append(out, g.recipients()...)
	}
	return out
}

// parseRules reads a `.sops.yaml` file's bytes into its creation_rules' recipients.
// A parse error yields ok=false, no error (an unrelated `.sops.yaml`-named file is
// simply skipped). The recipients feed the Snapshot inventory only.
func parseRules(data []byte) (recips []recipient, ok bool) {
	var f rulesFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, false
	}
	if len(f.CreationRules) == 0 {
		return nil, false
	}
	var out []recipient
	for _, r := range f.CreationRules {
		out = append(out, r.recipients()...)
	}
	return dedupeRecipients(out), true
}

// splitList splits a comma/newline-separated recipient string (age/pgp in
// .sops.yaml) into trimmed, non-empty entries.
func splitList(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// dedupeRecipients removes duplicate recipients (by ref), preserving first-seen
// order so the same file/rule never yields two edges to the same key.
func dedupeRecipients(in []recipient) []recipient {
	seen := make(map[string]struct{}, len(in))
	out := in[:0]
	for _, r := range in {
		if _, ok := seen[r.ref]; ok {
			continue
		}
		seen[r.ref] = struct{}{}
		out = append(out, r)
	}
	return out
}
