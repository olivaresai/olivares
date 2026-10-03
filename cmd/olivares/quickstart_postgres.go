// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

func parseQuickstartPostgresURL(raw string) (*url.URL, error) {
	u, err := postgresMaintenanceURL(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return nil, errors.New("--postgres needs a valid maintenance PostgreSQL URL; use file:<path> or env:<VAR> to keep its password private")
	}
	// pgx query parameters override the URL authority/path. Keep one target:
	// provisioning, generated role DSNs and TLS selection must use the same host.
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, errors.New("--postgres needs a valid maintenance PostgreSQL URL query")
	}
	if len(query["sslmode"]) > 1 || len(query["ssl"]) > 1 || query.Has("ssl") && (query.Get("ssl") != "true" || query.Has("sslmode")) {
		return nil, errors.New("PostgreSQL connection must specify one TLS mode: sslmode or ssl=true")
	}
	if query.Get("ssl") == "true" {
		query.Set("sslmode", "require")
		query.Del("ssl")
	}
	for key := range query {
		if key == "host" && u.Hostname() == "" && len(query[key]) == 1 && filepath.IsAbs(query.Get(key)) {
			continue // libpq's Unix-socket URL form has no authority host.
		}
		switch strings.TrimSpace(key) {
		case "host", "hostaddr", "port", "database", "dbname", "user", "password", "service", "servicefile":
			return nil, errors.New("--postgres PostgreSQL URL must put the server, port, database and credentials in its authority/path, not query parameters")
		}
	}
	if u.Hostname() == "" && !filepath.IsAbs(query.Get("host")) {
		return nil, errors.New("--postgres PostgreSQL URL must name a server or an absolute Unix socket directory")
	}
	if query.Get("sslmode") == "" {
		mode := "verify-full"
		if hostIsLoopback(u.Hostname()) || filepath.IsAbs(query.Get("host")) {
			mode = "prefer"
		}
		query.Set("sslmode", mode)
	}
	u.RawQuery = query.Encode()
	if _, err := pgx.ParseConfig(u.String()); err != nil {
		return nil, errors.New("--postgres needs a valid maintenance PostgreSQL URL")
	}
	return u, nil
}

// Preserve db init's libpq keyword/value DSNs while keeping one URL-based
// bootstrap path. pgx validates the normalized connection before it is used.
func postgresMaintenanceURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(raw), "postgres://") || strings.HasPrefix(strings.ToLower(raw), "postgresql://") {
		return url.Parse(raw)
	}
	settings, err := postgresKeywordSettings(raw, "database")
	if err != nil {
		return nil, err
	}
	host, port := settings.Get("host"), settings.Get("port")
	u := &url.URL{Scheme: "postgres", Path: "/" + settings.Get("dbname")}
	if settings.Has("database") {
		u.Path = "/" + settings.Get("database")
	}
	if port == "" {
		port = "5432"
	}
	if filepath.IsAbs(host) {
		u.Host = ":" + port
	} else {
		u.Host = net.JoinHostPort(host, port)
		settings.Del("host")
	}
	if settings.Get("password") != "" {
		u.User = url.UserPassword(settings.Get("user"), settings.Get("password"))
	} else if settings.Get("user") != "" {
		u.User = url.User(settings.Get("user"))
	}
	for _, key := range []string{"port", "database", "dbname", "user", "password"} {
		settings.Del(key)
	}
	u.RawQuery = settings.Encode()
	return u, nil
}

// The DSN files are the bootstrap configuration, not a second credential
// store. The engine resolves them through its existing file: secret handler.
func quickstartPostgresConfig(ctx context.Context, dataDir, maintenance string) (store.Config, error) {
	init, err := initPostgresConfig(ctx, dataDir, maintenance, store.PgProvisionSpec{}, true)
	return init.config, err
}

type postgresInit struct {
	config store.Config
	spec   store.PgProvisionSpec
	result store.PgProvisionResult
	// fresh: this call provisioned a new database (no saved configuration before).
	fresh bool
	// maintenance is the maintenance DSN this call provisioned with (empty when it
	// reused the saved configuration without one).
	maintenance string
}

// Both db init and quickstart provision and reopen this same configuration.
// Only the optional operator role/database overrides differ from the defaults.
func initPostgresConfig(ctx context.Context, dataDir, maintenance string, requested store.PgProvisionSpec, splitOwner bool) (postgresInit, error) {
	var init postgresInit
	dir := filepath.Join(dataDir, "postgres")
	appPath, ownerPath := filepath.Join(dir, "app.dsn"), filepath.Join(dir, "owner.dsn")
	_, err := os.Stat(dir)
	if os.IsNotExist(err) && maintenance == "" {
		return init, nil // ordinary SQLite quickstart
	}
	if err != nil && !os.IsNotExist(err) {
		return init, fmt.Errorf("read PostgreSQL configuration: %w", err)
	}
	if maintenance != "" && fileExistsAt(filepath.Join(dataDir, "olivares.db")) {
		return init, errors.New("this data directory already uses SQLite; choose a separate --data-dir for PostgreSQL")
	}
	fresh := os.IsNotExist(err)
	var base *url.URL
	explicitTLSMode := requested.SSLMode != ""
	if maintenance != "" {
		base, err = parseQuickstartPostgresURL(maintenance)
		if err != nil {
			return init, err
		}
		rawURL, _ := postgresMaintenanceURL(maintenance) // validated above
		explicitTLSMode = explicitTLSMode || rawURL.Query().Get("sslmode") != "" || rawURL.Query().Has("ssl")
		if requested.SSLMode != "" {
			q := base.Query()
			q.Set("sslmode", requested.SSLMode)
			base.RawQuery = q.Encode()
			base, err = parseQuickstartPostgresURL(base.String())
			if err != nil {
				return init, err
			}
		}
	}
	if fresh {
		// An operator-named role may belong to another installation. Do not
		// replace its existing password just because it was omitted here.
		namedRoles := map[string]store.PgRole{"app": requested.App, "owner": requested.Owner}
		if requested.Admin != nil {
			namedRoles["admin"] = *requested.Admin
		}
		for role, credentials := range namedRoles {
			if credentials.Name != "" && credentials.Password == "" {
				return init, fmt.Errorf("named PostgreSQL %s role requires --%s-password-file to save usable credentials without replacing an unknown existing password", role, role)
			}
		}
		var suffix [8]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return init, err
		}
		id := hex.EncodeToString(suffix[:])
		// A distinct database and role pair avoids changing another installation.
		spec := postgresSpecNames(requested, splitOwner, "_"+id)
		if spec.Admin != nil && (spec.Admin.Name == spec.App.Name || spec.Admin.Name == spec.Owner.Name) {
			return init, errors.New("PostgreSQL admin role must be separate from the application and owner roles")
		}
		if _, err := coreengine.RenderProvisionSQL(spec); err != nil {
			return init, err
		}
		roleURL := *base
		roleURL.Path = "/" + spec.Database
		q := roleURL.Query()
		for _, key := range []string{"options", "role", "search_path"} {
			q.Del(key)
		}
		roleURL.RawQuery = q.Encode()
		if err := secure.EnsureDataDir(dataDir); err != nil {
			return init, err
		}
		staging, err := os.MkdirTemp(dataDir, ".postgres-")
		if err != nil {
			return init, err
		}
		defer os.RemoveAll(staging)
		if err := secure.EnsureDataDir(staging); err != nil {
			return init, err
		}
		roles := map[string]*store.PgRole{"app": &spec.App}
		if splitOwner {
			roles["owner"] = &spec.Owner
		}
		if spec.Admin != nil {
			roles["admin"] = spec.Admin
		}
		for role, credentials := range roles {
			if credentials.Password == "" {
				var password [32]byte
				if _, err := rand.Read(password[:]); err != nil {
					return init, err
				}
				credentials.Password = hex.EncodeToString(password[:])
			}
			roleURL.User = url.UserPassword(credentials.Name, credentials.Password)
			if err := os.WriteFile(filepath.Join(staging, role+".dsn"), []byte(roleURL.String()+"\n"), 0o600); err != nil {
				return init, err
			}
		}
		if !splitOwner {
			data, err := os.ReadFile(filepath.Join(staging, "app.dsn"))
			if err != nil {
				return init, err
			}
			if err := os.WriteFile(filepath.Join(staging, "owner.dsn"), data, 0o600); err != nil {
				return init, err
			}
		}
		// Persist credentials before provisioning, so an interrupted init can be
		// retried with the same roles and passwords. Rename publishes both files.
		if err := os.Rename(staging, dir); err != nil {
			return init, fmt.Errorf("save PostgreSQL credentials: %w", err)
		}
	}
	init.config = store.Config{Engine: store.EnginePostgres, DSN: "file:" + appPath, OwnerDSN: "file:" + ownerPath}
	if fileExistsAt(filepath.Join(dir, "admin.dsn")) {
		init.config.AdminDSN = "file:" + filepath.Join(dir, "admin.dsn")
	}
	if maintenance == "" {
		return init, nil // restart without retaining or asking for the DBA credential
	}
	roles := make(map[string]*url.URL)
	roleNames := []string{"app", "owner"}
	if init.config.AdminDSN != "" {
		roleNames = append(roleNames, "admin")
	}
	for _, role := range roleNames {
		raw, err := resolveDSNRef(ctx, "PostgreSQL "+role+" DSN", "file:"+filepath.Join(dir, role+".dsn"), osGetenv)
		if err != nil {
			return init, err
		}
		u, err := parseQuickstartPostgresURL(raw)
		if err != nil {
			return init, err
		}
		if u.Host != base.Host || u.Query().Get("host") != base.Query().Get("host") {
			return init, errors.New("this data directory uses another PostgreSQL address; use its original maintenance URL or a separate --data-dir")
		}
		roles[role] = u
	}
	app, owner := roles["app"], roles["owner"]
	appPW, _ := app.User.Password()
	ownerPW, _ := owner.User.Password()
	if app.User.Username() == "" || owner.User.Username() == "" || appPW == "" || ownerPW == "" ||
		app.Path != owner.Path || app.Query().Get("sslmode") != owner.Query().Get("sslmode") {
		return init, errors.New("PostgreSQL credentials must name one database with application and owner passwords and the same TLS mode")
	}
	spec := store.PgProvisionSpec{
		Database: strings.TrimPrefix(app.Path, "/"), SSLMode: app.Query().Get("sslmode"),
		App: store.PgRole{Name: app.User.Username(), Password: appPW},
	}
	if owner.User.Username() != app.User.Username() {
		spec.Owner = store.PgRole{Name: owner.User.Username(), Password: ownerPW}
	}
	if admin := roles["admin"]; admin != nil {
		password, _ := admin.User.Password()
		if admin.Path != app.Path || password == "" || admin.Query().Get("sslmode") != spec.SSLMode {
			return init, errors.New("PostgreSQL admin credentials must name the same database and TLS mode with a password")
		}
		spec.Admin = &store.PgRole{Name: admin.User.Username(), Password: password}
	}
	if requested.Database != "" && requested.Database != spec.Database || requested.SSLMode != "" && requested.SSLMode != spec.SSLMode {
		return init, errors.New("requested database or TLS mode differs from the saved PostgreSQL configuration; use its original settings or a separate --data-dir")
	}
	if explicitTLSMode && base.Query().Get("sslmode") != spec.SSLMode {
		return init, errors.New("maintenance TLS mode differs from the saved PostgreSQL configuration; use its original settings or a separate --data-dir")
	}
	for _, pair := range []struct{ requested, saved store.PgRole }{{requested.App, spec.App}, {requested.Owner, spec.Owner}} {
		if pair.requested.Name != "" && pair.requested.Name != pair.saved.Name || pair.requested.Password != "" && pair.requested.Password != pair.saved.Password {
			return init, errors.New("requested role or password differs from the saved PostgreSQL configuration; use its original settings or a separate --data-dir")
		}
	}
	adminStaging := ""
	if requested.Admin != nil {
		if spec.Admin == nil {
			if requested.Admin.Password == "" {
				return init, errors.New("named PostgreSQL admin role requires --admin-password-file to save usable credentials without replacing an unknown existing password")
			}
			admin := *requested.Admin
			spec.Admin = &admin
			if admin.Name == spec.App.Name || admin.Name == spec.Owner.Name {
				return init, errors.New("PostgreSQL admin role must be separate from the application and owner roles")
			}
			if _, err := coreengine.RenderProvisionSQL(spec); err != nil {
				return init, err
			}
			adminURL := *app
			adminURL.User = url.UserPassword(requested.Admin.Name, requested.Admin.Password)
			file, err := os.CreateTemp(dir, ".admin-")
			if err != nil {
				return init, err
			}
			defer os.Remove(file.Name())
			_, writeErr := file.WriteString(adminURL.String() + "\n")
			closeErr := file.Close()
			if err := errors.Join(writeErr, closeErr); err != nil {
				return init, err
			}
			adminStaging = file.Name()
		} else if requested.Admin.Name != spec.Admin.Name || requested.Admin.Password != "" && requested.Admin.Password != spec.Admin.Password {
			return init, errors.New("requested admin role differs from the saved PostgreSQL configuration; use its original settings or a separate --data-dir")
		}
	}
	q := base.Query()
	q.Set("sslmode", spec.SSLMode)
	base.RawQuery = q.Encode()
	init.spec = spec
	init.maintenance = base.String()
	init.result, err = coreengine.ProvisionPostgres(ctx, base.String(), spec, true)
	if err != nil {
		return init, fmt.Errorf("PostgreSQL provisioning failed: %w", err)
	}
	if adminStaging != "" {
		if init.result.AdminPosture == nil {
			return init, errors.New("PostgreSQL admin role was not verified; existing installation remains unchanged")
		}
		if _, accepted := checkVerdict(*init.result.AdminPosture, true); !accepted {
			return init, errors.New("PostgreSQL admin role verification failed; existing installation remains unchanged")
		}
		// Optional provisioning must not activate a broken pool in a working
		// installation. Publish only after the new role has been verified.
		if err := os.Rename(adminStaging, filepath.Join(dir, "admin.dsn")); err != nil {
			return init, err
		}
		init.config.AdminDSN = "file:" + filepath.Join(dir, "admin.dsn")
	}
	init.fresh = fresh
	return init, nil
}

func postgresSpecNames(spec store.PgProvisionSpec, splitOwner bool, suffix string) store.PgProvisionSpec {
	if spec.Database == "" {
		spec.Database = "olivares" + suffix
	}
	if spec.App.Name == "" {
		spec.App.Name = "olivares_app" + suffix
	}
	if splitOwner && spec.Owner.Name == "" {
		spec.Owner.Name = "olivares_owner" + suffix
	}
	return spec
}

// postgresKeywordSettings shares libpq quoting with the bootstrap URL adapter.
func postgresKeywordSettings(raw, databaseKey string) (url.Values, error) {
	settings := url.Values{}
	for raw != "" {
		key, rest, found := strings.Cut(raw, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" || strings.ContainsAny(key, " \t\n\r\v\f") {
			return nil, errors.New("invalid PostgreSQL maintenance DSN")
		}
		raw = strings.TrimLeft(rest, " \t\n\r\v\f")
		quoted := strings.HasPrefix(raw, "'")
		if quoted {
			raw = raw[1:]
		}
		var value strings.Builder
		closed := !quoted
		for raw != "" {
			ch := raw[0]
			raw = raw[1:]
			if ch == '\\' {
				if raw == "" {
					return nil, errors.New("invalid PostgreSQL maintenance DSN")
				}
				value.WriteByte(raw[0])
				raw = raw[1:]
			} else if quoted && ch == '\'' {
				closed = true
				break
			} else if !quoted && strings.ContainsRune(" \t\n\r\v\f", rune(ch)) {
				break
			} else {
				value.WriteByte(ch)
			}
		}
		if !closed {
			return nil, errors.New("invalid PostgreSQL maintenance DSN")
		}
		if key == "dbname" {
			key = databaseKey // preserve the caller's database spelling.
		}
		settings.Set(key, value.String())
		raw = strings.TrimLeft(raw, " \t\n\r\v\f")
	}
	return settings, nil
}
