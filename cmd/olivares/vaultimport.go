// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/auth"
)

// vaultRateWait is how long a bulk verb waits when the engine rate limits it. The engine's
// reveal budget refills one item every three seconds, so a shorter wait only asks again too early.
var vaultRateWait = 3 * time.Second

const (
	vaultImportOlivares = "olivares"
	vaultImportSources  = "bitwarden, keepass, 1password, chrome, proton or olivares"
	vaultExportFormat   = "olivares-vault"

	maxVaultImportBytes = 32 << 20
	maxVaultLabelLen    = 100 // leaves room for the "-N" of a repeated title under the name limit
	maxVaultTitleLen    = 1024
	// The longest label the engine keeps: a secret name holds at most 256 bytes, and the
	// vault/<user-id>/ prefix takes the prefix and a 36-character id and a slash of them.
	maxVaultOwnLabelLen = 256 - len(auth.PersonalVaultPrefix) - 36 - 1
	maxVaultKindLen     = 32
)

type vaultImportItem struct {
	Label       string
	Description string
	Item        auth.VaultItem
}

type vaultImportFailure struct {
	Row    int    `json:"row"`
	Reason string `json:"reason"`
}

type vaultImportParse struct {
	Rows        int
	Items       []vaultImportItem
	Failed      []vaultImportFailure
	Unsupported map[string]int // rows of a kind the vault has no item for, by kind
	Dropped     map[string]int // data an item has no field for, by name: counted, never stored
}

type vaultImportResult struct {
	Source          string               `json:"source"`
	DryRun          bool                 `json:"dry_run"`
	Rows            int                  `json:"rows"`
	Items           int                  `json:"items"`
	Types           map[string]int       `json:"types"`
	Written         int                  `json:"written"`
	SkippedExisting int                  `json:"skipped_existing"`
	Unsupported     map[string]int       `json:"unsupported"`
	NotKept         map[string]int       `json:"not_kept"`
	Failed          []vaultImportFailure `json:"failed"`
}

// vaultImportCells is one export row in the vault's own terms.
type vaultImportCells struct {
	kind                               string // "" or "login": a login, authenticator or note by what it holds; "note"; any other is not supported
	title, user, pass, url, note, totp string
	notKept                            []string
}

// vaultImportLayout is one program's export: the columns that prove the file is that program's,
// and how a row of it reads. cell returns "" for a column the row does not have.
type vaultImportLayout struct {
	required []string
	row      func(cell func(string) string) vaultImportCells
}

var vaultImportLayouts = map[string]vaultImportLayout{
	"bitwarden": {
		required: []string{"type", "name", "login_password"},
		row: func(c func(string) string) vaultImportCells {
			// Several websites are one per line; the first is the login's, the rest have no field.
			uris := strings.Split(strings.ReplaceAll(c("login_uri"), "\r", ""), "\n")
			cells := vaultImportCells{
				kind: vaultImportKind(c("type")), title: c("name"), note: c("notes"),
				user: c("login_username"), pass: c("login_password"), totp: c("login_totp"), url: uris[0],
			}
			cells.drop("folder", c("folder"))
			cells.drop("custom fields", c("fields"))
			cells.drop("extra websites", strings.Join(uris[1:], ""))
			return cells
		},
	},
	"keepass": {
		required: []string{"title", "password"},
		row: func(c func(string) string) vaultImportCells {
			cells := vaultImportCells{title: c("title"), user: c("username"), pass: c("password"), url: c("url"), note: c("notes"), totp: c("totp")}
			cells.drop("group", c("group"))
			return cells
		},
	},
	"1password": {
		required: []string{"title", "password"},
		row: func(c func(string) string) vaultImportCells {
			cells := vaultImportCells{title: c("title"), user: c("username"), pass: c("password"), url: c("url"), note: c("notes"), totp: c("otpauth")}
			cells.drop("tags", c("tags"))
			return cells
		},
	},
	"chrome": {
		required: []string{"name", "password"},
		row: func(c func(string) string) vaultImportCells {
			return vaultImportCells{title: c("name"), user: c("username"), pass: c("password"), url: c("url"), note: c("note")}
		},
	},
	"proton": {
		required: []string{"type", "name", "password"},
		row: func(c func(string) string) vaultImportCells {
			cells := vaultImportCells{
				kind: vaultImportKind(c("type")), title: c("name"), user: c("username"), pass: c("password"),
				url: c("url"), note: c("note"), totp: c("totp"),
			}
			// The email is the login's name when there is no username; with both, the email has no field.
			if cells.user == "" {
				cells.user = c("email")
			} else if c("email") != cells.user {
				cells.drop("email", c("email"))
			}
			cells.drop("vault", c("vault"))
			return cells
		},
	},
}

// vaultImportKind is a row's type cell as a short lowercase word. Anything else is "other", so
// a file cannot put its own text, or an escape sequence, in the report.
func vaultImportKind(cell string) string {
	kind := strings.ToLower(strings.TrimSpace(cell))
	if len(kind) > maxVaultKindLen || strings.ContainsFunc(kind, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != ' ' && r != '_' && r != '-'
	}) {
		return "other"
	}
	return kind
}

func (c *vaultImportCells) drop(what, value string) {
	if value != "" {
		c.notKept = append(c.notKept, what)
	}
}

// item is what the row holds, by what is in it: a username, password or website makes a login,
// an authenticator key alone an authenticator item, text alone a note. The zero item means
// the row holds nothing.
func (c vaultImportCells) item() auth.VaultItem {
	if c.kind == "note" {
		return auth.VaultItem{Type: auth.VaultNote, Note: c.note}
	}
	switch {
	case c.user != "" || c.pass != "" || c.url != "":
		return auth.VaultItem{Type: auth.VaultLogin, Username: c.user, Password: c.pass, URL: c.url, Note: c.note, TOTP: c.totp}
	case c.totp != "":
		return auth.VaultItem{Type: auth.VaultTOTP, Note: c.note, TOTP: c.totp}
	case c.note != "":
		return auth.VaultItem{Type: auth.VaultNote, Note: c.note}
	}
	return auth.VaultItem{}
}

// vaultExportDoc is the file `vault export` writes and `vault import --from olivares` reads.
type vaultExportDoc struct {
	Format string            `json:"format"`
	Items  []vaultExportItem `json:"items"`
}

type vaultExportItem struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	auth.VaultItem
}

// parseVaultImport reads an export file into the items its rows mean. A file that is not the
// layout it was said to be is a wrong invocation (exit 2) and its message repeats nothing of
// the file; a row that cannot be an item is a failure of that row, with the field at fault but
// never a value.
func parseVaultImport(source string, data []byte) (vaultImportParse, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if source == vaultImportOlivares {
		return parseVaultExport(data)
	}
	layout, ok := vaultImportLayouts[source]
	if !ok {
		return vaultImportParse{}, sentence(exitcode.Usage, "Unknown source %q. Use %s.", source, vaultImportSources)
	}
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1 // a short row reads as empty cells
	records, err := reader.ReadAll()
	var syntax *csv.ParseError
	switch {
	case errors.As(err, &syntax):
		return vaultImportParse{}, sentence(exitcode.Usage, "The file is not valid CSV (line %d).", syntax.Line)
	case err != nil:
		return vaultImportParse{}, sentence(exitcode.Usage, "The file could not be read as CSV.")
	case len(records) == 0:
		return vaultImportParse{}, sentence(exitcode.Usage, "The file is empty.")
	}
	column := map[string]int{}
	for i, name := range records[0] {
		name = strings.ToLower(strings.TrimSpace(name))
		if _, dup := column[name]; !dup {
			column[name] = i
		}
	}
	for _, name := range layout.required {
		if _, ok := column[name]; !ok {
			return vaultImportParse{}, sentence(exitcode.Usage, "This is not a %s export: it has no %q column.", source, name)
		}
	}

	out := newVaultImportParse()
	labels := newVaultLabels()
	for i, record := range records[1:] {
		row := i + 2 // the header is row 1
		out.Rows++
		cells := layout.row(func(name string) string {
			if j, ok := column[name]; ok && j < len(record) {
				return record[j]
			}
			return ""
		})
		switch cells.kind {
		case "", "login", "note":
		default:
			out.Unsupported[cells.kind]++
			continue
		}
		title := strings.TrimSpace(cells.title)
		if title == "" {
			out.fail(row, "the title is empty")
			continue
		}
		if cells.kind == "note" {
			cells.drop("login fields of a note", cells.user+cells.pass+cells.url+cells.totp)
		}
		item := cells.item()
		if !out.check(row, item, "") {
			continue
		}
		label, description := vaultImportLabel(title, labels)
		if err := out.keep(label, description, item, cells.notKept); err != nil {
			return vaultImportParse{}, err
		}
	}
	return out, nil
}

// parseVaultExport reads the file `vault export` wrote.
func parseVaultExport(data []byte) (vaultImportParse, error) {
	wrong := sentence(exitcode.Usage, "This is not an Olivares vault export (olivares vault export).")
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var doc vaultExportDoc
	if err := dec.Decode(&doc); err != nil || doc.Format != vaultExportFormat {
		return vaultImportParse{}, wrong
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return vaultImportParse{}, wrong
	}
	out := newVaultImportParse()
	labels := newVaultLabels()
	for i, it := range doc.Items {
		row := i + 1
		out.Rows++
		// An exported label is already a valid name of this vault: it comes back as it went out.
		switch {
		case it.Label == "" || strings.TrimSpace(it.Label) != it.Label || auth.ValidateSecretName(it.Label) != "":
			out.fail(row, "the label is not a valid item name")
			continue
		case len(it.Label) > maxVaultOwnLabelLen:
			out.fail(row, "the label is too long")
			continue
		case labels.taken[it.Label]:
			out.fail(row, "the label appears twice")
			continue
		case !out.check(row, it.VaultItem, it.Description):
			continue
		}
		labels.taken[it.Label] = true
		if err := out.keep(it.Label, it.Description, it.VaultItem, nil); err != nil {
			return vaultImportParse{}, err
		}
	}
	return out, nil
}

func newVaultImportParse() vaultImportParse {
	return vaultImportParse{Failed: []vaultImportFailure{}, Unsupported: map[string]int{}, Dropped: map[string]int{}}
}

func (p *vaultImportParse) fail(row int, reason string) {
	p.Failed = append(p.Failed, vaultImportFailure{Row: row, Reason: reason})
}

// check reports whether the engine will take the item, so nothing it would refuse is sent, and
// records why not when it will not.
func (p *vaultImportParse) check(row int, item auth.VaultItem, description string) bool {
	if item.Type == "" {
		p.fail(row, "the row holds no username, password, note or authenticator key")
		return false
	}
	doc, err := json.Marshal(item)
	if err == nil {
		_, err = auth.ParseVaultItem(string(doc))
	}
	if err == nil {
		err = auth.ValidateVaultDescription(description)
	}
	if err != nil {
		p.fail(row, vaultItemReason(err))
		return false
	}
	return true
}

// keep adds a checked item. More than a vault can hold is refused at once: no other row can
// make room, and a hostile file could otherwise be parsed for as long as it is long.
func (p *vaultImportParse) keep(label, description string, item auth.VaultItem, notKept []string) error {
	for _, what := range notKept {
		p.Dropped[what]++
	}
	p.Items = append(p.Items, vaultImportItem{Label: label, Description: description, Item: item})
	if len(p.Items) > auth.MaxVaultItemsPerOwner {
		return sentence(exitcode.Err, "The file holds more than %d items, but a vault holds at most %d. Nothing was imported.",
			auth.MaxVaultItemsPerOwner, auth.MaxVaultItemsPerOwner)
	}
	return nil
}

// vaultLabels hands out the labels of one import.
type vaultLabels struct {
	taken map[string]bool
	next  map[string]int // where a repeated base resumes numbering
}

func newVaultLabels() *vaultLabels {
	return &vaultLabels{taken: map[string]bool{}, next: map[string]int{}}
}

// claim returns base, or base-2, base-3 ... for a repeated one.
func (l *vaultLabels) claim(base string) string {
	label, n := base, max(l.next[base], 2)
	for ; l.taken[label]; n++ {
		label = base + "-" + strconv.Itoa(n)
	}
	l.next[base] = n
	l.taken[label] = true
	return label
}

// vaultImportLabel turns a program's title into a valid, unused label. A title that is not
// itself that label is kept as the description, so the list still shows what the program
// called it; control characters in it become spaces, so a title cannot forge a row of the list.
func vaultImportLabel(title string, labels *vaultLabels) (label, description string) {
	title = strings.TrimSpace(title)
	var b strings.Builder
	for _, r := range title {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	base := strings.Trim(b.String(), "-._")
	if len(base) > maxVaultLabelLen {
		base = strings.Trim(base[:maxVaultLabelLen], "-._")
	}
	if base == "" {
		base = "item"
	}
	label = labels.claim(base)
	if title == label {
		return label, ""
	}
	line := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, title)
	return label, truncateUTF8(line, maxVaultTitleLen)
}

// truncateUTF8 cuts s to at most n bytes without splitting a character.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// vaultCounts is a count map as one line, by name: "alias 2, card 1".
func vaultCounts(m map[string]int) string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = name + " " + strconv.Itoa(m[name])
	}
	return strings.Join(parts, ", ")
}
