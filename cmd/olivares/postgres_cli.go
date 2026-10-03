// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PostgreSQL service files use literal values, not conninfo quoting or URIs.
// The temporary directory and file keep credentials private until the child exits.
func postgresServiceFile(dsn string) (string, func(), error) {
	body, err := postgresServiceEntry(dsn)
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "olivares-pg-service-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "pg_service.conf")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

func postgresServiceEntry(dsn string) ([]byte, error) {
	refused := errors.New("PostgreSQL DSN cannot be represented safely in a private service file; use direct connection settings without nested services, line breaks, trailing whitespace or overlong values")
	settings := url.Values{}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil || u.Fragment != "" {
			return nil, refused
		}
		if u.User != nil {
			settings.Set("user", u.User.Username())
			if password, ok := u.User.Password(); ok {
				settings.Set("password", password)
			}
		}
		if u.Host != "" {
			var hosts, ports []string
			for _, host := range strings.Split(u.Host, ",") {
				part, err := url.Parse("postgres://" + host)
				if err != nil {
					return nil, refused
				}
				hosts = append(hosts, part.Hostname())
				ports = append(ports, part.Port())
			}
			settings.Set("host", strings.Join(hosts, ","))
			if strings.Trim(strings.Join(ports, ","), ",") != "" {
				settings.Set("port", strings.Join(ports, ","))
			}
		}
		if u.Path != "" && u.Path != "/" {
			settings.Set("dbname", strings.TrimPrefix(u.Path, "/"))
		}
		// libpq percent-decodes URI query values; '+' remains a literal plus.
		for _, pair := range strings.Split(u.RawQuery, "&") {
			if pair == "" {
				continue
			}
			key, value, ok := strings.Cut(pair, "=")
			if !ok {
				return nil, refused
			}
			key, err = url.PathUnescape(key)
			if err != nil {
				return nil, refused
			}
			value, err = url.PathUnescape(value)
			if err != nil {
				return nil, refused
			}
			if key == "ssl" && value == "true" {
				key, value = "sslmode", "require"
			}
			settings.Set(key, value)
		}
	} else {
		var err error
		settings, err = postgresKeywordSettings(strings.TrimSpace(dsn), "dbname")
		if err != nil {
			return nil, refused
		}
	}
	var keys []string
	for key, values := range settings {
		value := values[len(values)-1]
		// libpq rejects nested services, strips trailing whitespace, and bounds
		// each physical service-file line to fewer than 1023 bytes including LF.
		if key == "service" || strings.HasPrefix(key, "ldap") || key == "" || strings.ContainsAny(key, "= \t\r\n\v\f\x00[]#") || strings.ContainsAny(value, "\r\n\x00") || strings.TrimRight(value, " \t\v\f") != value || len(key)+len(value)+2 >= 1023 {
			return nil, refused
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var body strings.Builder
	body.WriteString("[olivares]\n")
	for _, key := range keys {
		body.WriteString(key + "=" + settings.Get(key) + "\n")
	}
	return []byte(body.String()), nil
}
