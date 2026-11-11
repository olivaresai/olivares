// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secret

import (
	"strings"
	"unicode"
)

// Redacted stands in a read for a saved value the read must not return. A write
// that sends it back where the read put it keeps the stored value (KeepRedacted).
const Redacted = "<redacted>"

// credentialWords mark a query parameter, flag or environment name as naming a
// credential when one of the name's words is one of them.
var credentialWords = map[string]bool{
	"key": true, "apikey": true, "sig": true, "signature": true, "auth": true, "authorization": true,
	"bearer": true, "pass": true, "passwd": true, "password": true, "passphrase": true, "pwd": true,
	"credential": true, "credentials": true, "token": true, "secret": true,
}

// handleWords end a name that holds a handle to a credential or describes it,
// not the credential itself: --key-file, SIG_PATH, auth-mode.
var handleWords = map[string]bool{
	"file": true, "path": true, "dir": true, "ref": true, "url": true, "uri": true, "endpoint": true, "name": true,
	"id": true, "hint": true, "type": true, "mode": true, "method": true, "scheme": true, "count": true,
	"limit": true, "ttl": true, "enabled": true,
}

// IsCredentialName reports whether a query parameter, flag or environment name
// names a credential: one of its words is a credential word and its last word does
// not name a handle. Words split at any character that is not a letter or digit and
// where a lower-case letter meets an upper-case one; case does not matter.
func IsCredentialName(name string) bool {
	words := nameWords(name)
	if len(words) == 0 || handleWords[words[len(words)-1]] {
		return false
	}
	for _, word := range words {
		if credentialWords[word] {
			return true
		}
	}
	return false
}

func nameWords(name string) []string {
	var words []string
	var word strings.Builder
	lower := false
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) || unicode.IsUpper(r) && lower {
			if word.Len() > 0 {
				words = append(words, strings.ToLower(word.String()))
				word.Reset()
			}
		}
		lower = unicode.IsLower(r) || unicode.IsDigit(r)
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			word.WriteRune(r)
		}
	}
	if word.Len() > 0 {
		words = append(words, strings.ToLower(word.String()))
	}
	return words
}

// assignment is one name=value in a string; value spans s[start:end].
type assignment struct {
	name       string
	start, end int
}

// assignments lists every name=value in s, in order. A name is the run of letters,
// digits, '_', '-' and '.' before '='; a value runs to the next '&', ';', ',', '#' or
// space, so it may hold further assignments (--upstream=https://h/?key=v).
func assignments(s string) []assignment {
	var out []assignment
	for i := 0; i < len(s); i++ {
		if s[i] != '=' {
			continue
		}
		begin := i
		for begin > 0 && isNameByte(s[begin-1]) {
			begin--
		}
		end := i + 1
		for end < len(s) && !strings.ContainsRune("&;,# \t", rune(s[end])) {
			end++
		}
		if begin < i {
			out = append(out, assignment{name: s[begin:i], start: i + 1, end: end})
		}
	}
	return out
}

func isNameByte(b byte) bool {
	return b == '_' || b == '-' || b == '.' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// userinfo returns the bounds of a URL's userinfo in s (the text between "://" and
// the last '@' of the authority), or ok=false when s holds none.
func userinfo(s string) (start, end int, ok bool) {
	i := strings.Index(s, "://")
	if i < 0 {
		return 0, 0, false
	}
	start = i + 3
	authority := s[start:]
	if stop := strings.IndexAny(authority, "/?# \t"); stop >= 0 {
		authority = authority[:stop]
	}
	at := strings.LastIndexByte(authority, '@')
	if at <= 0 {
		return 0, 0, false
	}
	return start, start + at, true
}

// ContainsNamedCredential reports whether a value carries a credential by name: URL
// userinfo, or a value under a credential name (key=, sig=, --api-key=, API_KEY=).
// A value that is the Redacted marker carries nothing. It returns only a boolean
// so a refusal never copies the value.
func ContainsNamedCredential(value string) bool {
	if start, end, ok := userinfo(value); ok && value[start:end] != Redacted {
		return true
	}
	for _, a := range assignments(value) {
		if v := value[a.start:a.end]; v != "" && v != Redacted && IsCredentialName(a.name) {
			return true
		}
	}
	return false
}

// RedactValue returns a value as a read may show it: URL userinfo and every URL
// query value, and every value under a credential name, become Redacted.
func RedactValue(value string) string {
	if start, end, ok := userinfo(value); ok {
		value = value[:start] + Redacted + value[end:]
	}
	query := -1
	if i := strings.Index(value, "://"); i >= 0 {
		if q := strings.IndexByte(value[i:], '?'); q >= 0 {
			query = i + q
		}
	}
	var out strings.Builder
	last := 0
	for _, a := range assignments(value) {
		if a.start < last {
			continue // inside a value already redacted
		}
		inQuery := query >= 0 && a.start > query && !strings.ContainsAny(value[query:a.start], "# \t")
		if v := value[a.start:a.end]; v == "" || v == Redacted || !inQuery && !IsCredentialName(a.name) {
			continue
		}
		out.WriteString(value[last:a.start])
		out.WriteString(Redacted)
		last = a.end
	}
	out.WriteString(value[last:])
	return out.String()
}

// credentialFlag is a flag without its own value whose name names a credential:
// the argument after it is its value (--api-key VALUE).
func credentialFlag(arg string) bool {
	return strings.HasPrefix(arg, "-") && !strings.Contains(arg, "=") && IsCredentialName(arg)
}

// RedactArgs returns command arguments as a read may show them: RedactValue of
// each, and the argument after a credential flag becomes Redacted.
func RedactArgs(args []string) []string {
	if args == nil {
		return nil
	}
	out := make([]string, len(args))
	for i, arg := range args {
		out[i] = RedactValue(arg)
		if i > 0 && credentialFlag(args[i-1]) && !strings.HasPrefix(arg, "-") {
			out[i] = Redacted
		}
	}
	return out
}

// CredentialArgument returns the position of the first argument that carries a
// credential (ContainsNamedCredential, or the value after a credential flag), or -1.
func CredentialArgument(args []string) int {
	for i, arg := range args {
		if ContainsNamedCredential(arg) || i > 0 && credentialFlag(args[i-1]) && !strings.HasPrefix(arg, "-") && arg != Redacted {
			return i
		}
	}
	return -1
}

// KeepRedacted returns the value a write stores. A value without the marker is the
// client's own. A value equal to the stored value's redacted read (storedRedacted)
// keeps the stored value. Any other value holding the marker has nothing to keep:
// ok is false, and the marker is never stored as a value.
func KeepRedacted(incoming, stored, storedRedacted string) (string, bool) {
	if !strings.Contains(incoming, Redacted) {
		return incoming, true
	}
	if storedRedacted != "" && incoming == storedRedacted {
		return stored, true
	}
	return "", false
}
