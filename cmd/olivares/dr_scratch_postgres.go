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
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/olivaresai/olivares/core/dr"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
)

// A PostgreSQL bundle is verified the way a SQLite one is: by restoring it into a
// place nobody uses and re-verifying the ledger there. For SQLite that place is a
// temporary directory; for PostgreSQL it is a throwaway database with its own
// roles, created through the same provisioning `olivares db init` runs and dropped
// when the check ends. `dr verify` and `dr drill` share this one mechanism.

// scratchPostgres is one throwaway estate: a database plus an owner, an
// application and an admin role, all with random names and passwords that exist
// only in this process.
type scratchPostgres struct {
	flags drFlags // dsn, ownerDSN and adminDSN of the throwaway database

	maintenance string // the caller's maintenance DSN, used only to drop
	database    string
	roles       []string
}

// resolveScratchDSN accepts only a file: or env: reference, the rule
// --superuser-dsn already follows: a DBA credential never travels in the argument
// list. An empty value stays empty.
func resolveScratchDSN(ctx context.Context, flagName, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !strings.HasPrefix(value, "file:") && !strings.HasPrefix(value, "env:") {
		return "", fmt.Errorf("%s requires a file: or env: reference; keep the credential out of command arguments", flagName)
	}
	return resolveDSNRef(ctx, flagName, value, osGetenv)
}

// scratchConnectParams are the only query parameters of the maintenance DSN that
// the scratch DSNs inherit: how to reach and secure the server. pgx (and libpq)
// apply query parameters AFTER the userinfo and the path, so inheriting user,
// password, dbname, service or options would let the maintenance credentials
// override the scratch roles, and pg_restore of an untrusted dump would run as the
// DBA in the maintenance database instead of as the throwaway owner.
var scratchConnectParams = []string{"sslmode", "sslrootcert", "sslcert", "sslkey", "connect_timeout"}

// scratchRoleDSN is the DSN of one scratch role on the scratch database, built
// from the maintenance URL's scheme and host and the allowlisted parameters only.
func scratchRoleDSN(base *url.URL, userinfo *url.Userinfo, database string) string {
	query := url.Values{}
	for _, key := range scratchConnectParams {
		if v := base.Query().Get(key); v != "" {
			query.Set(key, v)
		}
	}
	// A space must read back as itself (the service file decodes %20, not +).
	u := url.URL{Scheme: base.Scheme, User: userinfo, Host: base.Host, Path: "/" + database, RawQuery: strings.ReplaceAll(query.Encode(), "+", "%20")}
	return u.String()
}

// checkScratchBase refuses a maintenance DSN whose meaning would not survive being
// rewritten for the scratch roles: no host (the scratch DSNs would then follow
// PGHOST, not the server the roles were provisioned on), or a query parameter the
// scratch DSNs do not inherit (host=, port=, target_session_attrs=, channel_binding=,
// sslpassword=...). Dropping those silently would aim the restore at another server
// or with weaker settings than the operator asked for; refusing says which.
func checkScratchBase(base *url.URL) error {
	if base.Host == "" {
		return errors.New("the scratch DSN must name the server in its host part (postgres://user@host:port/db), not through host= or PGHOST")
	}
	for key := range base.Query() {
		if !slices.Contains(scratchConnectParams, key) {
			return fmt.Errorf("the scratch DSN carries the query parameter %q, which the throwaway roles do not inherit; keep only %s", key, strings.Join(scratchConnectParams, ", "))
		}
	}
	return nil
}

// provisionScratchPostgres creates the throwaway estate on the server maintenance
// reaches. maintenance must be a postgres:// URL of a role allowed to create
// databases and roles (the same authority `olivares db init` asks for).
func provisionScratchPostgres(ctx context.Context, maintenance string) (*scratchPostgres, error) {
	base, err := url.Parse(maintenance)
	if err != nil || (base.Scheme != "postgres" && base.Scheme != "postgresql") {
		return nil, errors.New("the scratch DSN must be a postgres:// URL")
	}
	if err := checkScratchBase(base); err != nil {
		return nil, err
	}
	var token [6]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(token[:])
	newRole := func(kind string) (store.PgRole, error) {
		var pw [16]byte
		if _, err := rand.Read(pw[:]); err != nil {
			return store.PgRole{}, err
		}
		return store.PgRole{Name: "olvdr_" + kind + "_" + id, Password: hex.EncodeToString(pw[:])}, nil
	}
	app, err := newRole("app")
	if err != nil {
		return nil, err
	}
	owner, err := newRole("own")
	if err != nil {
		return nil, err
	}
	admin, err := newRole("adm")
	if err != nil {
		return nil, err
	}
	s := &scratchPostgres{
		maintenance: maintenance,
		database:    "olvdr_" + id,
		roles:       []string{app.Name, owner.Name, admin.Name},
	}
	s.flags = drFlags{
		engineKind: string(store.EnginePostgres),
		dsn:        scratchRoleDSN(base, url.UserPassword(app.Name, app.Password), s.database),
		ownerDSN:   scratchRoleDSN(base, url.UserPassword(owner.Name, owner.Password), s.database),
		adminDSN:   scratchRoleDSN(base, url.UserPassword(admin.Name, admin.Password), s.database),
	}
	spec := store.PgProvisionSpec{
		Database: s.database, App: app, Owner: owner, Admin: &admin,
		SSLMode: base.Query().Get("sslmode"),
	}
	if _, err := coreengine.ProvisionPostgres(ctx, maintenance, spec, true); err != nil {
		return nil, errors.Join(fmt.Errorf("provision the scratch database: %w", err), s.drop(ctx))
	}
	return s, nil
}

// scratchDropTimeout bounds the cleanup, which runs even when the command's own
// context was cancelled (an interrupt must not leave a database behind).
const scratchDropTimeout = 60 * time.Second

// drop removes the database and its roles. It is safe to call after a partial
// provision, and it reports what it could not remove so a leftover is never silent.
func (s *scratchPostgres) drop(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), scratchDropTimeout)
	defer cancel()
	conn, err := pgx.Connect(ctx, s.maintenance)
	if err != nil {
		return fmt.Errorf("scratch database %s and roles %s were NOT removed: %w",
			s.database, strings.Join(s.roles, ", "), err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var errs []error
	if _, err := conn.Exec(ctx, `DROP DATABASE IF EXISTS "`+s.database+`" WITH (FORCE)`); err != nil {
		errs = append(errs, fmt.Errorf("drop scratch database %s: %w", s.database, err))
	}
	for _, role := range s.roles {
		if _, err := conn.Exec(ctx, `DROP ROLE IF EXISTS "`+role+`"`); err != nil {
			errs = append(errs, fmt.Errorf("drop scratch role %s: %w", role, err))
		}
	}
	return errors.Join(errs...)
}

// restoreVerifyScratch restores a logical PostgreSQL bundle into the scratch
// estate and re-verifies the ledger there, exactly as `dr restore` does into a
// real target: signing keys into a scratch data dir, pg_restore as the owner, the
// restored authority closed, the engine booted, the chain, per-event signatures,
// checkpoints and tips compared with the manifest.
func restoreVerifyScratch(ctx context.Context, work string, m *dr.Manifest, cipher *dr.KeyCipher,
	scratch *scratchPostgres, pgRestorePath string, payload *drPayloadProof) (*dr.RestoreReport, error) {
	// The isolated directory inventory is restored by a DBA step on the restored
	// database. In a throwaway restore the dump is not trusted yet, and that step
	// would hand a DBA session to objects the dump defines, so it is refused here
	// rather than run: restore such a bundle for real, where the target is yours.
	if payload.inventory {
		return nil, fmt.Errorf("this bundle contains the isolated directory inventory, which a scratch restore does not restore (it needs a DBA step on the restored database); verify it with a real restore")
	}
	dataDir, err := os.MkdirTemp(work, "scratch-")
	if err != nil {
		return nil, err
	}
	if err := restoreKeys(work, dataDir, m, cipher, true); err != nil {
		return nil, err
	}
	if err := payload.unchanged(); err != nil {
		return nil, err
	}
	sf := scratch.flags
	sf.dataDir = dataDir
	if err := runPgRestore(ctx, pgRestorePath, drDDLDSN(sf), payload.path); err != nil {
		return nil, err
	}
	if err := restorePostgresAuthority(ctx, sf, ""); err != nil {
		return nil, err
	}
	eng, err := drBoot(ctx, sf)
	if err != nil {
		return nil, fmt.Errorf("boot restored store: %w", err)
	}
	defer func() { _ = eng.Close() }()
	cpv, err := eng.signer.CheckpointVerifier(ctx)
	if err != nil {
		return nil, err
	}
	return dr.RestoreVerify(ctx, eng.store, m, eng.signer.PublicKey(), cpv)
}
