// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/store"
)

// TestListOrgsVisiblePostgresRejectsRevokedAdminWithTenantPreset is the real-RLS
// regression for the dangerous partial-inventory shape. The AdminDSN was valid at
// boot, then loses BYPASSRLS while inheriting a tenant GUC. Without the live posture
// check its query can return one tenant and look like a complete estate. It must
// instead return no rows, no authoritative witness, and the typed refusal a global
// readiness consumer propagates as UNKNOWN/OFF.
func TestListOrgsVisiblePostgresRejectsRevokedAdminWithTenantPreset(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pg := isolatedPG(t)
	raw, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres,
		DSN:    pg.App, AdminDSN: pg.Admin, MaxConns: 4,
	}, registerWidget)
	if err != nil {
		t.Fatalf("open PostgreSQL store: %v", err)
	}
	defer raw.Close() //nolint:errcheck

	tenant := provisionTenant(t, raw, "system-admin-revoked-"+uniqueSuffix())
	provisionTenant(t, raw, "system-admin-hidden-"+uniqueSuffix())
	ss := raw.(*sqlStore)
	adminRole := ss.directoryAdminRole.Role
	if !ss.directoryAdminRole.Known || adminRole != pg.Result.AdminPosture.Role {
		t.Fatalf("boot AdminDSN identity = %+v, want exact %q",
			ss.directoryAdminRole, pg.Result.AdminPosture.Role)
	}

	super, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatalf("open PostgreSQL superuser: %v", err)
	}
	defer super.Close() //nolint:errcheck
	// Restore both role properties before pgtest tears down its temporary role,
	// including when an assertion below fails.
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, cleanupErr := super.ExecContext(
			cleanupCtx, "ALTER ROLE "+quoteIdent(adminRole)+" BYPASSRLS",
		); cleanupErr != nil {
			t.Errorf("restore AdminDSN BYPASSRLS: %v", cleanupErr)
		}
		if _, cleanupErr := super.ExecContext(
			cleanupCtx,
			"ALTER ROLE "+quoteIdent(adminRole)+" IN DATABASE "+
				quoteIdent(pg.Database)+" RESET app.tenant_id",
		); cleanupErr != nil {
			t.Errorf("reset AdminDSN tenant preset: %v", cleanupErr)
		}
	}()

	if _, err := super.ExecContext(
		ctx,
		"ALTER ROLE "+quoteIdent(adminRole)+" IN DATABASE "+quoteIdent(pg.Database)+
			" SET app.tenant_id TO "+systemAdminTestQuoteLiteral(tenant.String()),
	); err != nil {
		t.Fatalf("preset AdminDSN tenant identity: %v", err)
	}
	if _, err := super.ExecContext(
		ctx, "ALTER ROLE "+quoteIdent(adminRole)+" NOBYPASSRLS",
	); err != nil {
		t.Fatalf("revoke AdminDSN BYPASSRLS: %v", err)
	}

	// Role/database defaults apply on login, so use a fresh one-connection pool
	// and prove the hostile preset is really present before exercising the API.
	hostileAdmin, err := openPGPinnedToEngineSchema(pg.Admin, 1)
	if err != nil {
		t.Fatalf("open revoked AdminDSN pool: %v", err)
	}
	defer hostileAdmin.Close() //nolint:errcheck
	var preset string
	if err := hostileAdmin.QueryRowContext(
		ctx, "SELECT pg_catalog.current_setting('app.tenant_id', true)",
	).Scan(&preset); err != nil {
		t.Fatalf("read hostile AdminDSN tenant preset: %v", err)
	}
	if preset != tenant.String() {
		t.Fatalf("hostile AdminDSN tenant preset = %q, want %q", preset, tenant)
	}

	goodAdmin := ss.adminDB
	ss.adminDB = hostileAdmin
	defer func() { ss.adminDB = goodAdmin }()
	visible, authoritative := -1, true
	enumErr := raw.System(ctx, func(sys store.SystemScope) error {
		orgs, complete, listErr := sys.ListOrgsVisible(ctx)
		visible, authoritative = len(orgs), complete
		return listErr
	})
	if !errors.Is(enumErr, store.ErrEnumerationNotAuthoritative) {
		t.Fatalf("revoked AdminDSN enumeration error = %v, want ErrEnumerationNotAuthoritative", enumErr)
	}
	if authoritative || visible != 0 {
		t.Fatalf("revoked AdminDSN enumeration = (%d,%t), want (0,false)",
			visible, authoritative)
	}
}

// TestListOrgsVisiblePostgresRejectsExactPinnedAdminFromForeignDatabase proves
// role equality is not database identity. The foreign AdminDSN is the exact role
// pinned by this Open and passes the live posture check; only the app↔admin
// database challenge can refuse it before foreign rows are certified as complete.
func TestListOrgsVisiblePostgresRejectsExactPinnedAdminFromForeignDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	primary := isolatedPG(t)
	foreign := isolatedPG(t)

	// Give each isolated database the same registered schema before deliberately
	// crossing the primary application DSN with the foreign administrative DSN.
	primaryStore, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres,
		DSN:    primary.App, AdminDSN: primary.Admin, MaxConns: 4,
	}, registerWidget)
	if err != nil {
		t.Fatalf("open primary PostgreSQL store: %v", err)
	}
	if err := primaryStore.Close(); err != nil {
		t.Fatalf("close primary PostgreSQL store: %v", err)
	}
	foreignStore, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres,
		DSN:    foreign.App, AdminDSN: foreign.Admin, MaxConns: 4,
	}, registerWidget)
	if err != nil {
		t.Fatalf("open foreign PostgreSQL store: %v", err)
	}
	defer foreignStore.Close() //nolint:errcheck

	// ⛔ ACTA — ESTE TEST CAMBIÓ DE PUNTO, NO DE SUJETO. Contesta al comentario de
	// arriba, que sigue ahí a propósito porque decía la intención original.
	//
	// La redacción anterior exigía que este Open TUVIERA ÉXITO y que sólo el
	// challenge app↔admin refusara, tarde y tipado, en ListOrgsVisible. El corte
	// del kernel de trabajo cross-session movió la comprobación al arranque: el
	// reconciliador de épocas de directorio enumera cross-tenant durante el Open
	// (directoryepoch.go, llamado desde store.go), y con un DSN administrativo
	// AJENO esa enumeración no puede ser autoritativa, así que el boot refusa.
	//
	// POR QUÉ EL CAMBIO SE ACEPTA EN VEZ DE REVERTIRSE, y esto es lo que el
	// comentario de arriba tiene derecho a que se le conteste:
	//
	//   · El INTERÉS del tronco era la DISTINGUIBILIDAD, no la tardanza. Su
	//     redacción pedía un refuso TIPADO para que el llamante no lo confundiera
	//     con un fallo cualquiera — y eso se conserva ENTERO: el error del Open
	//     satisface errors.Is(err, store.ErrEnumerationNotAuthoritative), medido,
	//     porque toda la cadena de envoltura usa %w hasta closeOnErr.
	//   · Un DSN administrativo que apunta a OTRA base no es una capacidad
	//     ausente: es un cableado activamente MAL. Un pool ausente degrada con
	//     testigo incompleto (directoryepoch.go lo hace y sigue haciéndolo); uno
	//     ajeno se rechaza pronto. Las dos posturas conviven porque los hechos son
	//     distintos.
	//   · La alternativa —degradar también el caso ajeno— se construyó y se MIDIÓ
	//     en hub/s823-foreign-admin-degrades: hace pasar la redacción vieja y
	//     rompe TRES tests que el propio corte escribió para fijar el rechazo
	//     temprano. Elegirla no habría alineado nada: habría cambiado de bando.
	//
	// LO QUE SE PIERDE Y SE DICE: con el boot refusando, el pin del rol
	// administrativo y el resultado de la enumeración ya no son observables desde
	// aquí. Es la consecuencia deliberada del fail-fast, no un descuido de esta
	// reescritura.
	_, err = Open(ctx, store.Config{
		Engine: store.EnginePostgres,
		DSN:    primary.App, AdminDSN: foreign.Admin, MaxConns: 4,
	}, registerWidget)
	if err == nil {
		t.Fatal("the boot accepted an AdminDSN addressing a foreign database")
	}
	if !errors.Is(err, store.ErrEnumerationNotAuthoritative) {
		t.Fatalf("foreign exact-role AdminDSN error = %v, want the typed refusal "+
			"store.ErrEnumerationNotAuthoritative — a boot that refuses without its "+
			"type is indistinguishable from any other startup failure", err)
	}
	if !strings.Contains(err.Error(), "does not address the owner database") {
		t.Fatalf("the refusal does not name the measured cause: %v", err)
	}
}

func systemAdminTestQuoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
