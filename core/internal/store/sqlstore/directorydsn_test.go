// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// directoryTestDSNWithParam returns the PostgreSQL connection URI dsn with the
// query parameter key set to value. Every other query parameter keeps its exact
// bytes and an earlier occurrence of key is dropped, so nothing the DSN already
// says changes meaning.
//
// pgx reads URI query values the way libpq does and decodes only %XX, so key
// and value are percent-encoded with a space as %20 and a plus sign as %2B.
// url.Values.Encode cannot be used: it writes a space as '+', which that reader
// keeps as a literal plus sign. Errors never echo the DSN, which carries a
// password.
func directoryTestDSNWithParam(dsn, key, value string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", errors.New("the DSN is not a URL")
	}
	escape := func(s string) string {
		return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
	}
	var pairs []string
	for _, pair := range strings.Split(u.RawQuery, "&") {
		if pair == "" {
			continue
		}
		rawKey, _, _ := strings.Cut(pair, "=")
		// PathUnescape, like libpq, decodes %XX and leaves '+' alone.
		if name, err := url.PathUnescape(rawKey); err == nil && name == key {
			continue
		}
		pairs = append(pairs, pair)
	}
	u.RawQuery = strings.Join(append(pairs, escape(key)+"="+escape(value)), "&")
	return u.String(), nil
}

// TestDirectoryFixtureDSNsKeepLibpqMeaning pins, without a server, the query
// encoding the directory PostgreSQL fixtures depend on. pgx reads a connection
// URI the way libpq does: a query value is percent-decoded and nothing else, so
// a space has to be written as %20 and a plus sign stays a plus sign. The
// parameter a builder sets must reach the server exactly as given, and the
// parameters already in the login DSN must keep their meaning.
func TestDirectoryFixtureDSNsKeepLibpqMeaning(t *testing.T) {
	const login = "postgres://fixture:fixture@127.0.0.1:5432/olv?sslmode=disable&application_name=a+b%20c"
	const loginApplicationName = "a+b c"
	for _, tc := range []struct {
		name  string
		dsn   string
		param string
		want  string
	}{
		{
			name:  "assumed role",
			dsn:   directoryActivationTestAssumedRoleDSN(t, login, "olivares_app"),
			param: "options",
			want:  "-c role=olivares_app",
		},
		{
			name:  "default isolation",
			dsn:   directoryWriterTestDefaultIsolationDSN(t, login, "repeatable read"),
			param: "default_transaction_isolation",
			want:  "repeatable read",
		},
		{
			name:  "reserved characters",
			dsn:   directoryWriterTestDefaultIsolationDSN(t, login, "a+b c=d&e%"),
			param: "default_transaction_isolation",
			want:  "a+b c=d&e%",
		},
	} {
		cfg, err := pgconn.ParseConfig(tc.dsn)
		if err != nil {
			t.Fatalf("%s: pgx refused the fixture DSN: %v", tc.name, err)
		}
		if got := cfg.RuntimeParams[tc.param]; got != tc.want {
			t.Errorf("%s: %s = %q, want %q", tc.name, tc.param, got, tc.want)
		}
		if got := cfg.RuntimeParams["application_name"]; got != loginApplicationName {
			t.Errorf("%s: login DSN application_name = %q, want %q", tc.name, got, loginApplicationName)
		}
	}
}
