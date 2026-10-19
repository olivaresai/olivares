// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A personal vault item is the JSON document a person's vault/<user-id>/<label> entry
// seals. The store, the sealer, the ledger and the owner rule are the secret store's
// (secret_store.go); this file adds only what a person's items need on top: the
// document and its checks, the authenticator code (RFC 6238, over totp.go's
// primitives), and the password generator. Errors name the field at fault and never
// repeat a value: a refusal is shown to the person and logged.

// The kinds of vault item.
const (
	VaultLogin = "login"
	VaultNote  = "note"
	VaultTOTP  = "totp"
)

const (
	maxVaultItemBytes = 64 << 10
	maxVaultShortLen  = 1024 // username, password
	maxVaultLinkLen   = 2048 // url, authenticator secret or link

	maxVaultDescription   = 1024 // the non-secret line a list shows
	MaxVaultItemsPerOwner = 500

	minTOTPSeedBytes = 10 // 80 bits: the shortest seed common issuers hand out
	maxTOTPSeedBytes = 128
)

var (
	// ErrVaultItem is returned for a document that is not a valid vault item.
	ErrVaultItem = errors.New("auth: invalid vault item")
	// ErrVaultNoTOTP is returned when an item holds no authenticator secret.
	ErrVaultNoTOTP = errors.New("auth: this vault item has no authenticator secret")
	// ErrPasswordOptions is returned for a generator request that cannot be satisfied.
	ErrPasswordOptions = errors.New("auth: invalid password options")
)

func vaultErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrVaultItem, fmt.Sprintf(format, args...))
}

// VaultItem is the document sealed in a personal vault entry. A login holds a
// username and/or a password, and may carry an authenticator secret; a note holds
// text only; an authenticator item holds the secret (and may carry a note).
type VaultItem struct {
	Type     string `json:"type"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	URL      string `json:"url,omitempty"`
	Note     string `json:"note,omitempty"`
	// TOTP is a base32 secret or the otpauth://totp/ link a service's QR code holds.
	TOTP string `json:"totp,omitempty"`
}

// VaultCode is one authenticator code and how long it stays valid. It carries no seed.
type VaultCode struct {
	Code        string `json:"code"`
	SecondsLeft int    `json:"seconds_left"`
	Period      int    `json:"period"`
	Digits      int    `json:"digits"`
}

// ParseVaultItem reads the document a person stored. It is strict: one JSON object,
// the known fields, a known type and the fields that type needs.
func ParseVaultItem(raw string) (VaultItem, error) {
	if len(raw) > maxVaultItemBytes {
		return VaultItem{}, vaultErr("the item is larger than %d KiB", maxVaultItemBytes>>10)
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var it VaultItem
	if err := dec.Decode(&it); err != nil {
		if strings.HasPrefix(err.Error(), "json: unknown field") {
			return VaultItem{}, vaultErr("the item has a field this vault does not know")
		}
		return VaultItem{}, vaultErr("the item is not a JSON object with a known type")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return VaultItem{}, vaultErr("the item has more than one JSON document")
	}
	if err := it.Validate(); err != nil {
		return VaultItem{}, err
	}
	return it, nil
}

// Validate reports why the item cannot be stored, or nil.
func (it VaultItem) Validate() error {
	hasSecretFields := it.Username != "" || it.Password != "" || it.URL != ""
	switch it.Type {
	case VaultLogin:
		if it.Username == "" && it.Password == "" {
			return vaultErr("a login needs a username or a password")
		}
	case VaultNote:
		if it.Note == "" {
			return vaultErr("a note needs text")
		}
		if hasSecretFields || it.TOTP != "" {
			return vaultErr("a note holds only text")
		}
	case VaultTOTP:
		if it.TOTP == "" {
			return vaultErr("an authenticator item needs its secret")
		}
		if hasSecretFields {
			return vaultErr("an authenticator item holds only its secret and a note")
		}
	default:
		return vaultErr("the type must be %s, %s or %s", VaultLogin, VaultNote, VaultTOTP)
	}
	for _, c := range []struct {
		field, value string
		max          int
	}{{"username", it.Username, maxVaultShortLen}, {"password", it.Password, maxVaultShortLen},
		{"url", it.URL, maxVaultLinkLen}, {"totp", it.TOTP, maxVaultLinkLen}} {
		if len(c.value) > c.max {
			return vaultErr("the %s is longer than %d bytes", c.field, c.max)
		}
	}
	// A password inside a link would sit in a shell's argv and history, and in the list.
	if urlCarriesCredentials(it.URL) {
		return vaultErr("the website must not carry a user name or password; store them as fields")
	}
	if it.TOTP != "" {
		p, err := parseTOTPSecret(it.TOTP)
		if err != nil {
			return err
		}
		clear(p.seed)
	}
	return nil
}

// urlCarriesCredentials reports whether a website has a user name or password before its host,
// whether or not it parses and whether or not it has a scheme: a link that url.Parse refuses
// ("https://ann:pw@bank.example:x") or reads as an opaque path ("ann:pw@bank.example") hides
// them from the parsed form, not from a shell's history.
func urlCarriesCredentials(s string) bool {
	if _, rest, ok := strings.Cut(s, "://"); ok {
		s = rest
	}
	if end := strings.IndexAny(s, "/?#"); end >= 0 {
		s = s[:end]
	}
	return strings.Contains(s, "@")
}

// Hint is the non-secret label a list shows for the item: its kind, and "+totp" when a
// login carries an authenticator secret. It is stored as the entry's hint, so a list
// never opens a value.
func (it VaultItem) Hint() string {
	if it.Type == VaultLogin && it.TOTP != "" {
		return VaultLogin + "+" + VaultTOTP
	}
	return it.Type
}

// totpParams is an item's authenticator: the seed and how its codes are made.
type totpParams struct {
	seed      []byte
	algorithm string
	digits    int
	period    int
}

// parseTOTPSecret reads a base32 secret (spaces, dashes and case ignored) or an
// otpauth://totp/ link, whose algorithm, digits and period override the defaults.
func parseTOTPSecret(s string) (totpParams, error) {
	p := totpParams{algorithm: TOTPDefaultAlgorithm, digits: TOTPDefaultDigits, period: TOTPDefaultPeriod}
	secret := strings.TrimSpace(s)
	if strings.HasPrefix(strings.ToLower(secret), "otpauth://") {
		u, err := url.Parse(secret)
		if err != nil || !strings.EqualFold(u.Host, "totp") {
			// url.Error repeats the whole link, seed included: say nothing of it.
			return totpParams{}, vaultErr("the authenticator link must be an otpauth://totp/ link")
		}
		q := u.Query()
		secret = q.Get("secret")
		if v := q.Get("algorithm"); v != "" {
			p.algorithm = strings.ToUpper(v)
		}
		var ok bool
		if p.digits, ok = queryInt(q, "digits", p.digits); !ok {
			return totpParams{}, vaultErr("the authenticator digits must be 6, 7 or 8")
		}
		if p.period, ok = queryInt(q, "period", p.period); !ok {
			return totpParams{}, vaultErr("the authenticator period must be between 10 and 300 seconds")
		}
	}
	if _, err := totpHash(p.algorithm); err != nil {
		return totpParams{}, vaultErr("the authenticator algorithm must be SHA1, SHA256 or SHA512")
	}
	if p.digits < 6 || p.digits > 8 {
		return totpParams{}, vaultErr("the authenticator digits must be 6, 7 or 8")
	}
	if p.period < 10 || p.period > 300 {
		return totpParams{}, vaultErr("the authenticator period must be between 10 and 300 seconds")
	}
	clean := strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "=", "").Replace(secret))
	seed, err := totpB32.DecodeString(clean)
	if err != nil {
		return totpParams{}, vaultErr("the authenticator secret is not base32")
	}
	if len(seed) < minTOTPSeedBytes || len(seed) > maxTOTPSeedBytes {
		return totpParams{}, vaultErr("the authenticator secret must be %d to %d bytes", minTOTPSeedBytes, maxTOTPSeedBytes)
	}
	p.seed = seed
	return p, nil
}

// queryInt reads an integer link parameter; absent keeps def, malformed is not ok.
func queryInt(q url.Values, key string, def int) (int, bool) {
	v := q.Get(key)
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	return n, err == nil
}

// TOTPCode is the item's current authenticator code at time at.
func (it VaultItem) TOTPCode(at time.Time) (VaultCode, error) {
	if it.TOTP == "" {
		return VaultCode{}, ErrVaultNoTOTP
	}
	p, err := parseTOTPSecret(it.TOTP)
	if err != nil {
		return VaultCode{}, err
	}
	defer clear(p.seed)
	return totpCodeAt(p.algorithm, p.seed, p.digits, p.period, at)
}

// totpCodeAt is the RFC 6238 code for seed at time at (hotp over the time step), and
// the seconds left in its step.
func totpCodeAt(algorithm string, seed []byte, digits, period int, at time.Time) (VaultCode, error) {
	newHash, err := totpHash(algorithm)
	if err != nil {
		return VaultCode{}, err
	}
	unix := at.Unix()
	if unix < 0 || period <= 0 || digits <= 0 {
		return VaultCode{}, vaultErr("the authenticator cannot make a code for that time")
	}
	return VaultCode{
		Code:        hotp(newHash, seed, uint64(unix/int64(period)), digits),
		SecondsLeft: period - int(unix%int64(period)),
		Period:      period,
		Digits:      digits,
	}, nil
}

// PutVault stores a person's item under vault/<user-id>/<label> of an organization's
// secrets: the item is validated, sealed as its normalized document, and listed with
// its kind as the hint. Whether the actor owns the name is the caller's decision, as
// for Reveal.
func (s *SecretStore) PutVault(ctx context.Context, actor Principal, scope model.TenantID, name string, item VaultItem, description string) (SecretView, error) {
	name = strings.TrimSpace(name)
	if scope == GlobalSecretScope || !strings.HasPrefix(name, PersonalVaultPrefix) {
		return SecretView{}, fmt.Errorf("%w: a vault item lives under %s<user-id>/ in an organization's secrets", ErrBadSecretName, PersonalVaultPrefix)
	}
	if err := item.Validate(); err != nil {
		return SecretView{}, err
	}
	if err := ValidateVaultDescription(description); err != nil {
		return SecretView{}, err
	}
	doc, err := vaultDocument(item)
	if err != nil {
		return SecretView{}, err
	}
	if err := s.checkVaultRoom(ctx, scope, name); err != nil {
		return SecretView{}, err
	}
	return s.put(ctx, actor, scope, name, doc, description, nil, item.Hint())
}

// ValidateVaultDescription bounds the non-secret line a list shows for an item. Every write
// that can set it asks: a description-only edit has no item to parse.
func ValidateVaultDescription(description string) error {
	if len(description) > maxVaultDescription {
		return vaultErr("the description is longer than %d bytes", maxVaultDescription)
	}
	return nil
}

// vaultDocument is the document PutVault seals. It is held to the bound ParseVaultItem
// applies on the way back, measured on the encoded form: json.Marshal would write each of
// < > & as six bytes and push an admitted item past the bound it must be read under.
func vaultDocument(item VaultItem) (string, error) {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(item); err != nil {
		return "", err
	}
	doc := strings.TrimSuffix(buf.String(), "\n")
	if len(doc) > maxVaultItemBytes {
		return "", vaultErr("the item is larger than %d KiB", maxVaultItemBytes>>10)
	}
	return doc, nil
}

// checkVaultRoom refuses a NEW item once its owner already keeps MaxVaultItemsPerOwner:
// every member may now write to the organization's secret scope, and a list reads it whole.
// Replacing an item that exists is always allowed.
//
// ponytail: counts by listing the scope, apart from the write, so concurrent creates can pass
// the check together (the API's per-person write limit bounds how many); a store query by name
// prefix in the write's own transaction replaces it if a tenant's rows ever make this slow.
func (s *SecretStore) checkVaultRoom(ctx context.Context, scope model.TenantID, name string) error {
	views, err := s.List(ctx, scope)
	if err != nil {
		return err
	}
	owner, _, _ := strings.Cut(strings.TrimPrefix(name, PersonalVaultPrefix), "/")
	own := PersonalVaultPrefix + owner + "/"
	count := 0
	for _, v := range views {
		if v.Name == name {
			return nil
		}
		if strings.HasPrefix(v.Name, own) {
			count++
		}
	}
	if count >= MaxVaultItemsPerOwner {
		return vaultErr("a vault holds at most %d items", MaxVaultItemsPerOwner)
	}
	return nil
}

// VaultTOTP opens a vault item and returns its current authenticator code. Like
// Reveal it is one ledger entry (secret.totp: the actor and the entry, never the
// code or the seed) or no code at all. The seed stays in the store.
func (s *SecretStore) VaultTOTP(ctx context.Context, actor Principal, scope model.TenantID, name string, at time.Time) (VaultCode, error) {
	row, value, err := s.open(ctx, scope, name)
	if err != nil {
		return VaultCode{}, err
	}
	defer clear(value)
	item, err := ParseVaultItem(string(value))
	if err != nil {
		return VaultCode{}, err
	}
	code, err := item.TOTPCode(at)
	if err != nil {
		return VaultCode{}, err
	}
	if err := s.st.AuthMutate(ctx, func(as store.AuthScope) error {
		return auditAct(ctx, as, actor, "secret.totp", secretEntryKind, row.ID)
	}); err != nil {
		return VaultCode{}, err
	}
	return code, nil
}

// The password generator.

// The length bounds and the default of a generated password.
const (
	MinPasswordLength     = 8
	MaxPasswordLength     = 128
	DefaultPasswordLength = 20
)

const (
	passwordLower   = "abcdefghijklmnopqrstuvwxyz"
	passwordUpper   = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	passwordDigits  = "0123456789"
	passwordSymbols = "!@#$%^&*()-_=+[]{}:,.?"
)

// PasswordOptions chooses the length and character classes of a generated password.
type PasswordOptions struct {
	Length  int  `json:"length"`
	Lower   bool `json:"lower"`
	Upper   bool `json:"upper"`
	Digits  bool `json:"digits"`
	Symbols bool `json:"symbols"`
}

// DefaultPasswordOptions is 20 characters from all four classes.
func DefaultPasswordOptions() PasswordOptions {
	return PasswordOptions{Length: DefaultPasswordLength, Lower: true, Upper: true, Digits: true, Symbols: true}
}

// GeneratePassword returns a random password with at least one character of every
// chosen class. Every draw is uniform (crypto/rand through big.Int, no modulo bias).
func GeneratePassword(o PasswordOptions) (string, error) {
	if o.Length < MinPasswordLength || o.Length > MaxPasswordLength {
		return "", fmt.Errorf("%w: the length must be %d to %d", ErrPasswordOptions, MinPasswordLength, MaxPasswordLength)
	}
	var classes []string
	for _, c := range []struct {
		on       bool
		alphabet string
	}{{o.Lower, passwordLower}, {o.Upper, passwordUpper}, {o.Digits, passwordDigits}, {o.Symbols, passwordSymbols}} {
		if c.on {
			classes = append(classes, c.alphabet)
		}
	}
	if len(classes) == 0 {
		return "", fmt.Errorf("%w: choose at least one kind of character", ErrPasswordOptions)
	}
	all := strings.Join(classes, "")
	out := make([]byte, o.Length)
	for i := range out {
		alphabet := all
		if i < len(classes) {
			alphabet = classes[i]
		}
		n, err := randBelow(len(alphabet))
		if err != nil {
			return "", err
		}
		out[i] = alphabet[n]
	}
	for i := len(out) - 1; i > 0; i-- { // Fisher-Yates, so the guaranteed ones are not first
		j, err := randBelow(i + 1)
		if err != nil {
			return "", err
		}
		out[i], out[j] = out[j], out[i]
	}
	return string(out), nil
}

func randBelow(n int) (int, error) {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, fmt.Errorf("auth: read password entropy: %w", err)
	}
	return int(v.Int64()), nil
}
