// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// THE REGRESSION THAT HAD NO TEST, and the one the whole class turns on.
//
// `guardMetadataSplit(appRole, ownerRole) bool` answered false to two different
// questions — "the operator configured one role" and "the operator configured two and
// this boot could not read one of them" — and three defenses downstream read that one
// false as the first. The measurement that found it (an internal design note (not shipped)
// 2026-08-04-c4-limites-medicion-independiente.md, C4-07 and C4-12) put the consumer in
// one line: `ownerRole != "" && appRole != "" && ownerRole != appRole`.
//
// These tests are written against the PROPERTY, not the implementation: an unresolved
// role must not be classifiable as a topology, whichever leg went unread. A version of
// this file that only checked `guardMetadataTopologyOf` against a table of inputs would
// pass over a `resolveGuardMetadataPosture` that ignored the verdict, so the refusal is
// asserted at the consumer too.

func TestAnUnresolvedRoleIsNotATopology(t *testing.T) {
	t.Parallel()

	// The empty string is what the old encoding used for BOTH "no owner" and "unread
	// owner". Every row below that sets Known=false is a case the old bool could not
	// express at all.
	cases := []struct {
		name  string
		roles guardRoles
		want  guardMetadataTopology
	}{{
		name:  "no owner configured is an ANSWER, not a gap",
		roles: guardRoles{App: guardRoleFact{Role: "app", Known: true}},
		want:  guardTopologySingleRole,
	}, {
		name: "owner configured but resolving to the same role is single-role",
		roles: guardRoles{
			App: guardRoleFact{Role: "app", Known: true}, Owner: guardRoleFact{Role: "app", Known: true},
			OwnerConfigured: true,
		},
		want: guardTopologySingleRole,
	}, {
		name: "two distinct resolved roles are the split",
		roles: guardRoles{
			App: guardRoleFact{Role: "app", Known: true}, Owner: guardRoleFact{Role: "owner", Known: true},
			OwnerConfigured: true,
		},
		want: guardTopologySplit,
	}, {
		// C4-07: the owner leg. This is the case the adjudication described.
		name: "owner configured and its role UNREAD is unknown, never single-role",
		roles: guardRoles{
			App: guardRoleFact{Role: "app", Known: true}, Owner: guardRoleFact{Known: false},
			OwnerConfigured: true,
		},
		want: guardTopologyUnknown,
	}, {
		// C4-12: the app leg, which the measurement showed is reached far more often —
		// under AllowPrivilegedRole a failed posture read left posture.Role empty and
		// guardMetadataSplit returned false through THIS arm, so the owner leg never
		// decided anything.
		name: "owner configured and the APP role UNREAD is unknown too",
		roles: guardRoles{
			App: guardRoleFact{Known: false}, Owner: guardRoleFact{Role: "owner", Known: true},
			OwnerConfigured: true,
		},
		want: guardTopologyUnknown,
	}, {
		name: "neither leg resolved is unknown",
		roles: guardRoles{
			App: guardRoleFact{Known: false}, Owner: guardRoleFact{Known: false}, OwnerConfigured: true,
		},
		want: guardTopologyUnknown,
	}, {
		// Known=true with an empty name is not a role. ConnRolePosture cannot produce it
		// today, but the type can hold it, and deny-closed means the type's illegal state
		// is refused rather than compared.
		name: "a Known but NAMELESS owner role is not an answer either",
		roles: guardRoles{
			App: guardRoleFact{Role: "app", Known: true}, Owner: guardRoleFact{Role: "", Known: true},
			OwnerConfigured: true,
		},
		want: guardTopologyUnknown,
	}, {
		name: "a Known but NAMELESS app role is not an answer either",
		roles: guardRoles{
			App: guardRoleFact{Role: "", Known: true}, Owner: guardRoleFact{Role: "owner", Known: true},
			OwnerConfigured: true,
		},
		want: guardTopologyUnknown,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := guardMetadataTopologyOf(tc.roles); got != tc.want {
				t.Fatalf("guardMetadataTopologyOf(%+v) = %v, want %v", tc.roles, got, tc.want)
			}
		})
	}
}

// TestTheTwoCausesOfNotSplitAreDistinguishable states the property the old signature made
// unrepresentable, and it is deliberately NOT a table: it is the one assertion that would
// have failed against `guardMetadataSplit`.
//
// A configured-but-unread owner and a deliberately single-role deployment are both "not
// split". They must not be the SAME value, because the first is a boundary this boot
// cannot vouch for and the second is a boundary the operator declined to draw.
func TestTheTwoCausesOfNotSplitAreDistinguishable(t *testing.T) {
	t.Parallel()

	declinedTheSplit := guardRoles{App: guardRoleFact{Role: "app", Known: true}}
	askedForTheSplitAndCouldNotTell := guardRoles{
		App:             guardRoleFact{Role: "app", Known: true},
		Owner:           guardRoleFact{Known: false},
		OwnerConfigured: true,
	}

	declined := guardMetadataTopologyOf(declinedTheSplit)
	couldNotTell := guardMetadataTopologyOf(askedForTheSplitAndCouldNotTell)

	if declined == couldNotTell {
		t.Fatalf("the deliberate single-role topology and an UNRESOLVED one classify the same (%v). "+
			"That is the defect: three defenses read this verdict, and a deployment that asked for the "+
			"hardened posture would run with all three off while the log announced single-role", declined)
	}
	if declined != guardTopologySingleRole {
		t.Fatalf("a deployment with no owner DSN is single-role by configuration, got %v", declined)
	}
	if couldNotTell != guardTopologyUnknown {
		t.Fatalf("a configured owner whose role could not be read is UNKNOWN, got %v", couldNotTell)
	}
}

// TestTheUnresolvedTopologyRefusesTheBoot asserts at the CONSUMER, because a verdict
// nobody acts on is not a defense. resolveGuardMetadataPosture returns (hardened bool,
// err); the old code returned (false, nil) here, which is precisely "pass".
//
// It needs no server: the refusal happens before the first query, which is itself the
// point — this boot has nothing to ask the server ABOUT.
func TestTheUnresolvedTopologyRefusesTheBoot(t *testing.T) {
	t.Parallel()

	dia, ok := dialect.NewForAppRole(store.EnginePostgres, "app")
	if !ok {
		t.Fatal("postgres dialect for a named app role")
	}

	unresolved := guardRoles{
		App:             guardRoleFact{Role: "app", Known: true},
		Owner:           guardRoleFact{Known: false},
		OwnerConfigured: true,
	}
	// A querier that fails every statement: if the refusal did NOT happen first, the
	// error would come from here and the assertions below would catch the difference.
	hardened, err := resolveGuardMetadataPosture(context.Background(), refusingQuerier{}, dia, unresolved)
	if err == nil {
		t.Fatal("an unresolved topology must REFUSE; returning (hardened=false, nil) is the pass that " +
			"switched off the re-assert, the SET ROLE closure and the per-boot verification at once")
	}
	if hardened {
		t.Fatal("a refusal must not also claim the hardened posture")
	}
	if !errors.Is(err, store.ErrAppendOnlyACLOpen) {
		t.Fatalf("the refusal must carry the ACL sentinel so callers can classify it, got %v", err)
	}
	if !strings.Contains(err.Error(), "OWNER pool") {
		t.Fatalf("the refusal must name WHICH leg went unread, got %q", err.Error())
	}
	if strings.Contains(err.Error(), "refusing-querier") {
		t.Fatalf("the refusal must happen BEFORE any statement is issued, got %q", err.Error())
	}

	// And the two answerable topologies must still be answerable, or this test would pass
	// against a function that refused everything.
	single := guardRoles{App: guardRoleFact{Role: "app", Known: true}}
	if hardened, err := resolveGuardMetadataPosture(context.Background(), refusingQuerier{}, dia, single); err != nil || hardened {
		t.Fatalf("a deliberate single-role deployment must resolve without a server: hardened=%v err=%v", hardened, err)
	}
}

// TestAnUnknownRoleIsNotTheDefaultRole pins the second half of the same class: the
// dialect constructor used to substitute DefaultAppRole for an empty role, so "nobody
// could read the role" became "the role is olivares_app". Every revoke this dialect
// renders is gated on its target existing, so that substitution turned a protective
// statement into a silent success.
func TestAnUnknownRoleIsNotTheDefaultRole(t *testing.T) {
	t.Parallel()

	if _, ok := dialect.NewForAppRole(store.EnginePostgres, ""); ok {
		t.Fatal("an EMPTY app role must not yield a Postgres dialect: it used to fall back to " +
			"DefaultAppRole, which aims the control-plane revoke at a role that may not exist and " +
			"reports success (MEASURED: the v6 DO block returns success and revokes nothing)")
	}
	// The deliberate default is still available — it is a choice a caller makes by name,
	// not a fallback the constructor makes on a caller's behalf.
	if _, ok := dialect.New(store.EnginePostgres); !ok {
		t.Fatal("dialect.New must still build the conventional default binding")
	}
	// SQLite has no role layer, so an empty role is meaningless rather than dangerous.
	if _, ok := dialect.NewForAppRole(store.EngineSQLite, ""); !ok {
		t.Fatal("SQLite has no role layer; an empty role must not refuse it")
	}
}

// TestBindableRefusesToLeakAnUnknownRole covers the conversion itself, which is the only
// place a caller can turn a fact back into a bare string.
func TestBindableRefusesToLeakAnUnknownRole(t *testing.T) {
	t.Parallel()

	// An unread role must render empty EVEN IF the struct happens to carry a stale name:
	// a name read from a previous attempt is not an answer to this one.
	if got := (guardRoleFact{Role: "leftover", Known: false}).bindable(); got != "" {
		t.Fatalf("an unknown role must render as the empty string that NewForAppRole refuses, got %q", got)
	}
	if got := (guardRoleFact{Role: "app", Known: true}).bindable(); got != "app" {
		t.Fatalf("a known role must render as itself, got %q", got)
	}
}

// refusingQuerier fails every statement, so a test can prove a decision was made without
// asking the server anything.
type refusingQuerier struct{}

func (refusingQuerier) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("refusing-querier: this test must not reach the server")
}

func (refusingQuerier) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, errors.New("refusing-querier: this test must not reach the server")
}
