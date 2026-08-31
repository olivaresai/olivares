// guardRoleReachability answers "which role can the application role act as", per major, and
// every branch of it is MEASURED rather than derived from a reading of the documentation.
//
// Measured identically on 16.14, 17.10 and 18.4, with the application role as the subject:
//
//	grant                                        SET  USAGE  MEMBER  MEMBER WITH ADMIN
//	WITH SET FALSE, INHERIT FALSE, ADMIN FALSE    f     f       t            f
//	… plus a live app -> mid -> owner, SET TRUE   t     t       t            —
//	WITH SET FALSE, INHERIT FALSE, ADMIN TRUE     f     —       t            t
//
// and on 15.18: pg_has_role(..., 'SET') is `ERROR: unrecognized privilege type: "SET"`.
//
// THREE THINGS FOLLOW, and they are why MEMBER is wrong on 16+ and right on 15:
//
//  1. an inert grant is MEMBER but neither SET nor USAGE, so MEMBER alone refuses a deployment
//     that conveys nothing — the over-rejection round two found;
//  2. SET resolves the TRANSITIVE closure. That is the property a previous attempt at this got
//     wrong: it read the three options off the DIRECT pg_auth_members row and dropped the
//     refusal when all were false, which an inert direct edge beside a live chain defeats. The
//     row says "conveys nothing" while SET ROLE owner works, and the measurement above is that
//     case: SET = t with the direct edge still inert;
//  3. ADMIN TRUE conveys no privilege today and the right to grant itself one tomorrow, so it
//     belongs in the predicate rather than in a comment.
//
// On 15 every membership carries the right to SET ROLE and there is no SET privilege to ask
// about, so MEMBER is exactly the question — and CREATEROLE, read separately, is the capability
// no membership predicate can see there.
//
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// guardmetadataacl.go closes the ACL leg for the guard control plane, which needs a
// DIFFERENT policy from every other append-only table in this schema.
//
// The existing machinery would get it exactly backwards, and that is worth spelling out
// because the wrong fix is the obvious one. verifyAppendOnlyACL demands SELECT and INSERT of
// every REGISTERED append-only table, because those are tables the runtime appends evidence
// to. Adding the three control-plane relations to that registry would therefore REQUIRE the
// application role to be able to insert gate events — the opposite of the posture this
// history needs. So they are not registered; they are discovered by the ACL scope (their
// immutability trigger calls the same function), which covers the negative half —
// UPDATE/DELETE/TRUNCATE denied — and says nothing about INSERT.
//
// This file is the missing half: INSERT denied as well, verified from the APPLICATION pool
// where the answer is meaningful.
//
// THE LIMIT IS DECLARED, and it is narrower than an earlier version of this comment claimed.
// Under a SINGLE role the INSERT posture is neither applied nor verified — it cannot be,
// because that role is the engine's own writer — so there is no detection of it at the next
// boot either. What remains there is the append-only ACL leg, which does revoke and verify
// UPDATE/DELETE/TRUNCATE on these relations because the live catalog discovers them.
//
// Where owner and application ARE different roles the posture applies, and even then it is a
// point-in-time attestation: the owner can re-grant a statement later, and boot refuses at the
// NEXT start. That is detection, not prevention.

// guardRoleFact is what this boot LEARNED about one pool's role, and the second field is
// the whole point: a role name and the absence of an answer are different facts, and a
// bare string cannot hold both.
type guardRoleFact struct {
	// Role is the role the pool authenticates as. Meaningless unless Known.
	Role string
	// Known reports whether this boot actually resolved the role. False means the
	// question was ASKED and not answered — never "answered no".
	Known bool
}

// bindable renders the role for a consumer that can only take a string, and renders an
// UNKNOWN role as the empty string on purpose: every such consumer in this package now
// refuses "" rather than substituting a default for it. The conversion is here, once, so a
// call site cannot reach past Known and read Role directly.
func (f guardRoleFact) bindable() string {
	if !f.Known {
		return ""
	}
	return f.Role
}

// guardRoles is the pair the guard control plane's posture depends on, plus the one
// configuration fact that decides whether an unanswered question matters.
type guardRoles struct {
	// App is the role the application pool authenticates as.
	App guardRoleFact
	// Owner is the role the owner pool authenticates as. Meaningless unless OwnerConfigured.
	Owner guardRoleFact
	// OwnerConfigured reports that an OwnerDSN naming a DIFFERENT endpoint was configured.
	// Without it the topology is single-role BY CONFIGURATION, which is a known answer and
	// not an unresolved one — nobody asked the server anything, and nothing needed asking.
	OwnerConfigured bool
}

// guardMetadataTopology is the answer to "which topology is this", and it has THREE
// values because the question has three answers.
//
// This type replaces a bool, and the bool is the defect. `guardMetadataSplit` returned
// false both for "the operator configured one role" and for "the operator configured two
// and this boot could not read one of them", and three defenses downstream — the v6-era
// REVOKE re-assertion, the escalation closure and the ACL verification — read that single
// false as the first. A deployment that asked for the hardened posture therefore ran with
// the posture switched off and a log line saying it was single-role.
//
// The package already states the rule this restores, twice: an incomplete answer about a
// boundary is a refusal, never a pass (see verifyGuardMetadataACL below and
// verifyAppendOnlyACL). Making the third state UNREPRESENTABLE-as-false is what stops the
// rule from depending on every future call site remembering it.
type guardMetadataTopology int

const (
	// guardTopologyUnknown: the deployment configured a separate owner and this boot could
	// not resolve one of the two roles. Not a topology — the absence of one.
	guardTopologyUnknown guardMetadataTopology = iota
	// guardTopologySingleRole: one role writes and owns. Known, and the limit is declared.
	guardTopologySingleRole
	// guardTopologySplit: two distinct roles, which is what the hardened posture INTENDS.
	guardTopologySplit
)

func (t guardMetadataTopology) String() string {
	switch t {
	case guardTopologySingleRole:
		return "single-role"
	case guardTopologySplit:
		return "split-owner"
	default:
		return "unknown"
	}
}

// guardMetadataTopologyOf classifies the deployment from what boot actually learned.
//
// It compares the resolved ROLES rather than the DSN strings, and that distinction is the
// whole content of the comparison: an OwnerDSN may differ from the app DSN in host, port or
// sslmode while authenticating as the same role, and calling that deployment hardened would
// be a claim its privileges do not support.
//
// NO OWNER CONFIGURED IS AN ANSWER, not a gap. The single-role topology is what the
// operator chose by not choosing another; nothing was asked of the server, so nothing went
// unanswered. An owner that WAS configured and could not be read is the opposite case, and
// it is the one that must not be quietly folded into the first.
//
// INTENT IS NOT ACHIEVEMENT, which is why this is not the whole decision. Two distinct role
// names say what the operator configured; whether the application role can ASSUME the other
// one is a question only the server can answer. See guardMetadataEscalations.
func guardMetadataTopologyOf(r guardRoles) guardMetadataTopology {
	if !r.OwnerConfigured {
		return guardTopologySingleRole
	}
	if !r.Owner.Known || r.Owner.Role == "" {
		return guardTopologyUnknown
	}
	// The comparison needs BOTH names. An unreadable application role leaves the split
	// unverifiable just as surely as an unreadable owner one, and the earlier code reached
	// "not split" through the app leg far more often than through the owner leg.
	if !r.App.Known || r.App.Role == "" {
		return guardTopologyUnknown
	}
	if r.App.Role == r.Owner.Role {
		return guardTopologySingleRole
	}
	return guardTopologySplit
}

// describeUnresolvedGuardRoles names WHICH leg went unanswered, because "could not resolve
// the roles" sends an operator to look at both pools when one of them answered fine.
func describeUnresolvedGuardRoles(r guardRoles) string {
	appBad := !r.App.Known || r.App.Role == ""
	ownerBad := !r.Owner.Known || r.Owner.Role == ""
	switch {
	case appBad && ownerBad:
		return "the role of EITHER pool"
	case ownerBad:
		return "the OWNER pool's role"
	default:
		return "the APPLICATION pool's role"
	}
}

// guardMetadataEscalation is one role the application role can become, and the reason that
// makes it fatal.
type guardMetadataEscalation struct {
	Role string
	Why  string
}

func (e guardMetadataEscalation) String() string { return e.Role + " (" + e.Why + ")" }

// guardMetadataEscalations lists every role the application role can SET ROLE to that could
// write the control plane.
//
// THIS IS THE HOLE TWO DISTINCT NAMES DID NOT CLOSE, and it was measured rather than reasoned
// about (PostgreSQL 15.18, one session, backend pid 52547):
//
//	app -> mid -> owner, both memberships granted, both roles NOINHERIT
//	has_table_privilege('ledger','INSERT')       = false   <- the old verifier passed here
//	pg_has_role('m_app','m_owner','USAGE')       = false
//	pg_has_role('m_app','m_owner','MEMBER')      = true
//	SET ROLE m_owner; INSERT INTO ledger ...     = INSERT 0 1
//
// So a deployment could be labeled hardened, pass the effective-privilege verification, and
// let the application role append gate events by changing role first. PostgreSQL distinguishes
// automatic inheritance from the right to SET ROLE, and only the second matters here:
// https://www.postgresql.org/docs/18/role-membership.html
//
// WHAT DISQUALIFIES A ROLE, and the three are separate because each is reachable on its own:
//
//  1. It is a superuser. Every table ACL is bypassed, and no revoke can reach it.
//  2. It OWNS one of the three relations. An owner whose privileges were revoked can grant
//     them back to itself, so ownership is a capability even when the privilege bits are off —
//     which is precisely why this cannot be folded into the has_table_privilege test.
//  3. It holds INSERT, UPDATE, DELETE or TRUNCATE on one of them, however acquired: directly,
//     through a group, or through PUBLIC. has_table_privilege is what sees all three.
//
// AND FOURTH, PER MAJOR: CREATEROLE. The closure above answers "which role can this one
// become"; it cannot answer "which role can this one CREATE a membership in". On PostgreSQL 15
// the official GRANT page (https://www.postgresql.org/docs/15/sql-grant.html) says a role with
// CREATEROLE may grant or revoke membership in ANY non-superuser role — so a CREATEROLE
// application role passes this check and then hands itself the owner. That is an attribute, not
// a membership, and it is read as one. From 16 the model changed: CREATEROLE only permits
// granting roles the grantor holds WITH ADMIN OPTION
// (https://www.postgresql.org/docs/16/role-attributes.html), so the attribute alone stops being
// the hazard and refusing on it would refuse a deployment that is safe.
//
// THE OTHER HALF OF THE 16 CHANGE — a membership granted WITH SET FALSE, INHERIT FALSE, ADMIN
// FALSE conveys nothing, while pg_has_role(..., 'MEMBER') still reports it — is applied as a
// DOWNGRADE after the closure rather than inside its predicate, because a refinement that can
// REMOVE a refusal must fail closed when it errors.
//
// WHAT IS MEASURED, corrected in round twenty-one because this paragraph had gone stale: it used
// to say "this repository has 15 and nothing else" and that everything here was measured on 15.18
// alone. Both stopped being true when the matrix arrived. The whole path is now exercised on
// 15.18, 16.14, 17.10 and 18.4 by TestPostgresTheEscalationPredicateIsRightOnEveryMajor
// (guardmajormatrix_pg_test.go), and this file's own header records that matrix. A comment that
// pleads an absent server, on a repository that has four of them, argues for a caution the
// evidence no longer supports.
func guardMetadataEscalations(ctx context.Context, q dialect.Querier, appRole string, major int) ([]guardMetadataEscalation, error) {
	tables := dialect.GuardControlPlaneTables()
	list, args := tableParams([]any{dialect.EngineSchema, appRole}, tables)
	// #nosec G202 -- `list` is tableParams' output: ONLY "$3,$4,…" placeholders; `reach` is one of two compile-time constants of this package. The relation names travel as bound args, the schema as $1 and the role as $2
	q1 := guardReachableCTE(major) + `SELECT r.rolname, r.rolsuper, r.rolcreaterole, c.relname,
       c.relowner = r.oid,
       pg_catalog.has_table_privilege(r.oid, c.oid, 'INSERT'),
       pg_catalog.has_table_privilege(r.oid, c.oid, 'UPDATE'),
       pg_catalog.has_table_privilege(r.oid, c.oid, 'DELETE'),
       pg_catalog.has_table_privilege(r.oid, c.oid, 'TRUNCATE')
FROM pg_catalog.pg_roles r
CROSS JOIN pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1
  AND c.relkind IN ('r','p')
  AND c.relname IN (` + list + `)
  AND r.rolname <> $2
  AND ` + guardRoleReachability(major) + `
ORDER BY r.rolname, c.relname`
	rows, err := q.QueryContext(ctx, q1, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: guard metadata ACL: read the application role's membership closure: %w", err)
	}
	defer rows.Close()

	// Keyed by role so a role that fails on all three relations is reported once. The reason
	// kept is the FIRST in the order above, which is the most severe: being a superuser is not
	// usefully described by also listing which privileges that implies.
	reasons := map[string]string{}
	var order []string
	for rows.Next() {
		var role, relation string
		var super, createrole, owns, ins, upd, del, trunc bool
		if err := rows.Scan(&role, &super, &createrole, &relation, &owns, &ins, &upd, &del, &trunc); err != nil {
			return nil, fmt.Errorf("sqlstore: guard metadata ACL: read the application role's membership closure: %w", err)
		}
		var why string
		switch {
		case super:
			why = "a superuser, which bypasses every table ACL"
		case owns:
			why = "the owner of " + relation + ", so it can grant itself any privilege it lacks"
		case createrole && major < 16:
			why = "assumable and holds CREATEROLE, which on PostgreSQL " + fmt.Sprint(major) +
				" permits granting membership in any non-superuser role — including the owner of " + relation
		default:
			var held []string
			for _, p := range []struct {
				name string
				ok   bool
			}{{"INSERT", ins}, {"UPDATE", upd}, {"DELETE", del}, {"TRUNCATE", trunc}} {
				if p.ok {
					held = append(held, p.name)
				}
			}
			if len(held) == 0 {
				continue
			}
			why = "holds " + strings.Join(held, ",") + " on " + relation
		}
		if _, seen := reasons[role]; !seen {
			order = append(order, role)
			reasons[role] = why
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: guard metadata ACL: read the application role's membership closure: %w", err)
	}
	out := make([]guardMetadataEscalation, 0, len(order))
	for _, role := range order {
		out = append(out, guardMetadataEscalation{Role: role, Why: reasons[role]})
	}
	// AND THE APPLICATION ROLE'S OWN ATTRIBUTES, which the closure above cannot see: it excludes
	// the app role by name, because the question it answers is "which OTHER role can this one
	// become". CREATEROLE is not a membership — it is the power to CREATE one — so on 15 it
	// defeats the whole check by acting after it. Its own DML is deliberately NOT read here:
	// this runs BEFORE reconcileGuardMetadataACL, so a privilege the reconcile is about to
	// revoke would refuse a database that is one statement away from being correct.
	// verifyGuardMetadataACL is what asks that question, afterwards.
	if major < 16 {
		self, serr := guardRoleHasCreateRole(ctx, q, appRole)
		if serr != nil {
			return nil, serr
		}
		if self {
			out = append(out, guardMetadataEscalation{
				Role: appRole,
				Why: "itself: it holds CREATEROLE, and on PostgreSQL " + fmt.Sprint(major) +
					" that permits granting itself membership in any non-superuser role after this check has passed",
			})
		}
	}
	return out, nil
}

// guardRoleReachability is the reachability predicate, and it BRANCHES on the major.
//
// THE PREVIOUS SENTENCE HERE SAID IT WAS "the SAME on every major", and that stopped being true
// when the 16+ arm below was added. It is corrected rather than deleted because the reasoning it
// carried is still the reason the two arms look the way they do.
//
// The natural 16+ predicate is `pg_has_role(role, 'SET')`, which is exactly what the new
// membership model calls for — and when this was first written, this repository had PostgreSQL 15
// and nothing else. Putting it on the critical path of every split-topology boot from 16 onward
// would have been SQL that had never been executed; measured on 15, `pg_has_role(..., 'SET')` is
// `ERROR: unrecognized privilege type: "SET" (SQLSTATE 22023)`, which is what a wrong guess costs.
// MEMBER and USAGE are the two names that predate 15, and they are what the pre-16 arm uses.
//
// From 16 the predicate reads the transitive closure in guardReachableCTE instead, because the
// membership model that major introduced makes a single pg_has_role answer over-report — which is
// the finding that forced the CTE. The 16+ refinement of MEMBERSHIP is still applied AFTERWARDS
// as a downgrade that fails closed — see guardMembershipConveysNothing. A refinement that errors
// leaves the refusal standing; a predicate that errors takes the boot with it.
//
// It is a function returning a COMPILE-TIME constant — never runtime data — so it is safe to
// concatenate into the statement, which is what the #nosec above records.
func guardRoleReachability(major int) string {
	if major >= 16 {
		return `r.oid IN (SELECT oid FROM guard_reachable)`
	}
	return `(pg_catalog.pg_has_role($2, r.oid, 'MEMBER') OR pg_catalog.pg_has_role($2, r.oid, 'USAGE'))`
}

// guardReachableCTE is the TRANSITIVE closure the 16+ predicate needs, and the reason it is a
// recursive CTE rather than three calls to pg_has_role is a measured escalation.
//
// THE HOLE A DIRECT PREDICATE LEAVES. `pg_has_role(app, owner, 'SET')` answers "can app become
// owner NOW". It does not answer "can app arrange to". Measured identically on 16.14 and 18.4:
//
//	GRANT mid   TO app WITH SET FALSE, INHERIT FALSE, ADMIN TRUE;
//	GRANT owner TO mid WITH SET TRUE;
//	  pg_has_role(app, owner, 'SET')                      = f
//	  pg_has_role(app, owner, 'MEMBER WITH ADMIN OPTION') = f     <- "not reachable"
//	SET ROLE app; GRANT mid TO app WITH SET TRUE;         -- its ADMIN on mid permits this
//	  pg_has_role(app, owner, 'SET')                      = t     <- and now it is
//
// So a deployment passes the check and the application role reaches the owner afterwards, with
// no further grant from anybody. ADMIN on an INTERMEDIARY is a capability over everything that
// intermediary can reach, and only a closure sees that.
//
// EVERY HOP USES THE SAME TEST — set OR inherit OR admin — because at every hop the three mean
// the same three things: may assume it, holds its privileges, or may grant itself either. A
// membership with all three false conveys nothing and is correctly excluded, which is what
// keeps the false positive round two found from coming back.
//
// It is a COMPILE-TIME constant of this package and binds $2, which is why concatenating it is
// safe; see the #nosec above.
func guardReachableCTE(major int) string {
	if major < 16 {
		return ""
	}
	return `WITH RECURSIVE guard_reachable(oid) AS (
    SELECT m.roleid
    FROM pg_catalog.pg_auth_members m
    JOIN pg_catalog.pg_roles a ON a.oid = m.member
    WHERE a.rolname = $2
      AND (m.set_option OR m.inherit_option OR m.admin_option)
  UNION
    SELECT m.roleid
    FROM pg_catalog.pg_auth_members m
    JOIN guard_reachable g ON g.oid = m.member
    WHERE (m.set_option OR m.inherit_option OR m.admin_option)
)
`
}

// THE 16+ DOWNGRADE WAS WRITTEN AND THEN WITHDRAWN, and the reason is worth keeping.
//
// From 16 a membership granted WITH SET FALSE, INHERIT FALSE, ADMIN FALSE conveys nothing, so
// refusing on pg_has_role(..., 'MEMBER') alone is a false positive. The obvious fix — read the
// three options off the DIRECT pg_auth_members row and drop the refusal when all are false —
// was implemented, and round three found it unsound: with a direct inert edge app->owner AND a
// live chain app->mid->owner where every hop has SET TRUE, the direct row says "conveys
// nothing" and the refusal disappears while `SET ROLE owner` still works. A refinement that can
// REMOVE a refusal must be right about the whole graph, not about one edge of it.
//
// The server has a predicate that answers it — pg_has_role(app, role, 'SET') resolves the
// transitive closure (https://www.postgresql.org/docs/16/functions-info.html,
// https://www.postgresql.org/docs/16/sql-set-role.html) — but it cannot be spelled on 15, where
// it is `unrecognized privilege type: "SET" (SQLSTATE 22023)`. That is why the answer is not one
// predicate for all majors.
//
// WHAT ACTUALLY SHIPS, corrected in round twenty-one because this block described a state the code
// left behind. It said the 16+ refinement "was withdrawn", that "the conservative predicate stands
// on every major", and that closing it needed a certified 16+ run that did not exist. None of
// those hold now: guardRoleReachability BRANCHES, and from 16 it resolves reachability through the
// recursive closure in guardReachableCTE rather than through a single pg_has_role answer — which
// is precisely what makes the inert-direct-edge-plus-live-chain case come out right. The certified
// run exists too: TestPostgresTheEscalationPredicateIsRightOnEveryMajor exercises the three
// outcomes on 15.18, 16.14, 17.10 and 18.4.
//
// What is kept from the original reasoning, because it is still the rule: a refinement that can
// REMOVE a refusal must be right about the whole GRAPH and not about one edge of it. Round three
// measured the counter-example — an inert direct edge app->owner alongside a live chain
// app->mid->owner — and the one-edge form said "conveys nothing" while `SET ROLE owner` still
// worked. The closure is the answer to that, not an approximation of it.

func guardRoleHasCreateRole(ctx context.Context, q dialect.Querier, role string) (bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT rolcreaterole FROM pg_catalog.pg_roles WHERE rolname = $1`, role)
	if err != nil {
		return false, fmt.Errorf("sqlstore: guard metadata ACL: read the attributes of role %q: %w", role, err)
	}
	defer rows.Close()
	if !rows.Next() {
		// A role the server does not know is not a role that can escalate. The append-only ACL
		// leg reports the same absence in its own terms, so failing here would double it.
		return false, rows.Err()
	}
	var createrole bool
	if err := rows.Scan(&createrole); err != nil {
		return false, fmt.Errorf("sqlstore: guard metadata ACL: read the attributes of role %q: %w", role, err)
	}
	return createrole, rows.Err()
}

// postgresMajorVia reads the server major through a Querier.
//
// postgresServerMajor needs QueryRowContext, which the migration executor does not expose. The
// answer is the same one; only the handle differs.
func postgresMajorVia(ctx context.Context, q dialect.Querier) (int, error) {
	rows, err := q.QueryContext(ctx, "SHOW server_version_num")
	if err != nil {
		return 0, fmt.Errorf("sqlstore: read server_version_num: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, fmt.Errorf("sqlstore: read server_version_num: %w", err)
		}
		return 0, fmt.Errorf("sqlstore: read server_version_num: the server returned no row")
	}
	var raw string
	if err := rows.Scan(&raw); err != nil {
		return 0, fmt.Errorf("sqlstore: read server_version_num: %w", err)
	}
	raw = strings.TrimSpace(raw)
	var n int
	if _, err := fmt.Sscanf(raw, "%d", &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("sqlstore: server_version_num %q is not a positive integer", raw)
	}
	return n / 10000, rows.Err()
}

// resolveGuardMetadataPosture decides which posture this boot is in, and REFUSES a split
// topology whose separation the server does not actually enforce.
//
// It refuses rather than quietly downgrading to the single-role warning, and the reason is
// what the two outcomes would mean to the operator. Downgrading would leave a deployment that
// was configured for the hardened posture running with a log line — while
// verifyGuardMetadataACL, asked only about the application role's own privileges, kept
// reporting that the boundary holds. That is a guarantee stated over a database that does not
// provide it, which is the exact class of claim this whole rollout exists to make impossible.
//
// The single-role topology is NOT subjected to this. There the limit is already declared in
// full — that role writes the ledger by design — so a role it could additionally assume adds
// no capability it does not already have.
//
// AND IT REFUSES AN UNRESOLVED TOPOLOGY, which is the case this function used to answer
// "not hardened" to. A boot that configured a separate owner and then could not read one of
// the two roles does not know which topology it is in; returning false there switched off
// the re-assertion, the escalation closure and the verification at once, and announced the
// deployment as single-role while the operator had configured two. The refusal is the same
// rule verifyGuardMetadataACL states below — an incomplete answer about a boundary is a
// refusal, never a pass — applied to the question that SELECTS the boundary.
func resolveGuardMetadataPosture(ctx context.Context, q dialect.Querier, dia dialect.Dialect, roles guardRoles) (bool, error) {
	if dia.Name() != store.EnginePostgres {
		return false, nil
	}
	switch guardMetadataTopologyOf(roles) {
	case guardTopologyUnknown:
		return false, fmt.Errorf(
			"sqlstore: refusing to start: %w — this deployment configures a separate owner role (--owner-dsn), and this boot could not resolve %s. "+
				"Which posture the guard control plane is in depends on comparing the two roles, so an unread role leaves the boundary UNDETERMINED — and an undetermined boundary is not the single-role one. "+
				"Treating it as single-role is what would silently switch off the control plane's revoke, its SET ROLE closure and its per-boot verification on a deployment that asked for all three. "+
				"Grant SELECT on pg_roles to both roles (only the RLS attributes need it; the identity does not), or remove --owner-dsn to declare the single-role topology deliberately",
			store.ErrAppendOnlyACLOpen, describeUnresolvedGuardRoles(roles))
	case guardTopologySingleRole:
		return false, nil
	case guardTopologySplit:
		// Falls through to the escalation closure below.
	}
	appRole := roles.App.Role
	// THE MAJOR DECIDES THE QUESTION, not just the answer: 15 and 16 disagree about what a
	// membership conveys and about what CREATEROLE permits. Reading it here rather than
	// threading it in keeps the two callers — the reconcile and the verification — asking the
	// same server the same thing.
	major, err := postgresMajorVia(ctx, q)
	if err != nil {
		return false, err
	}
	escalations, err := guardMetadataEscalations(ctx, q, appRole, major)
	if err != nil {
		return false, err
	}
	if len(escalations) == 0 {
		return true, nil
	}
	rendered := make([]string, 0, len(escalations))
	for _, e := range escalations {
		rendered = append(rendered, e.String())
	}
	return false, fmt.Errorf(
		"sqlstore: refusing to start: %w — the application role %q can SET ROLE to %v. "+
			"This deployment configures a separate owner role, and the posture that buys is that runtime traffic cannot append to the history of which schema changes were authorized. "+
			"A role the application can assume defeats it without touching a single privilege of the application role itself, so the boundary would be verified and absent at the same time. "+
			"Revoke the membership (REVOKE <role> FROM %s), or point --dsn at a role that is a member of nothing",
		store.ErrAppendOnlyACLOpen, appRole, rendered, appRole)
}

// reconcileGuardMetadataACL re-asserts the control plane's revoke on every boot.
//
// It runs on the OWNER pool inside the migration lock, because only a table's owner may
// administer its ACL. Ownership is CHECKED first, through the same helper the append-only
// reconcile uses, because PostgreSQL does not error when a role revokes privileges it did not
// grant: it warns and reports success. A reconcile that inferred "applied" from a clean exit
// would silently do nothing whenever the owner pool is not the owner, and the verification
// that follows would then read as "re-asserted and held" when nothing was re-asserted.
func reconcileGuardMetadataACL(ctx context.Context, ownerDB dialect.Execer, dia dialect.Dialect, hardened bool, roles guardRoles) error {
	if dia.Name() != store.EnginePostgres {
		return nil
	}
	if !hardened {
		// SINGLE ROLE: the revoke is NOT applied, and the reason is that applying it would
		// brick the engine rather than harden it. The owner pool is the application pool here,
		// so the role this would revoke INSERT from is the very role that appends gate events —
		// the rollout would disable its own writer and then fail 42501 opening its own rollout.
		//
		// Announced once per boot rather than silent, because the LIMIT is real and an operator
		// deciding whether this deployment is adversarially sound needs to know which posture
		// it is in.
		//
		// AND IT NO LONGER TELLS AN OPERATOR TO DO WHAT THEY ALREADY DID. This line used to
		// end with "Configure a separate owner role (--owner-dsn)" unconditionally, so a
		// deployment that HAD configured one — and reached here because both DSNs resolve to
		// the SAME role — was sent to set a flag that was already set, while the real cause
		// (two DSNs, one role) went unnamed. The remedy has to name the case it is the remedy
		// for; pointing at the wrong knob is worse than pointing at none, because the operator
		// checks the knob, finds it set, and concludes the warning is noise.
		if roles.OwnerConfigured {
			slog.Warn("store: the guard control plane's history is written by the same role the application uses, so it is durable against a crash but NOT resistant to that role: it can append events. An owner DSN IS configured, but both pools authenticate as the SAME role, so the split buys nothing — point --owner-dsn at a role that is not the --dsn role",
				"role", roles.Owner.Role, "relations", dialect.GuardControlPlaneTables())
			return nil
		}
		slog.Warn("store: the guard control plane's history is written by the same role the application uses, so it is durable against a crash but NOT resistant to that role: it can append events. Configure a separate owner role (--owner-dsn) to close that gap",
			"relations", dialect.GuardControlPlaneTables())
		return nil
	}
	// THE POSITIVE LINE, which dialect/guardeventfence.go asks for by name: "there is no
	// positive SplitOwner line … an operator cannot tell 'resolved SplitOwner' from 'never
	// looked' by reading the log". Only the single-role branch above said anything, so its
	// ABSENCE carried two meanings at once — hardened, or a boot that never got here. One
	// line at Info makes the hardened posture assertable from the log instead of inferable
	// from a silence.
	slog.Info("store: the guard control plane is in the SPLIT-OWNER posture: the application role is denied INSERT/UPDATE/DELETE/TRUNCATE on the history of which schema changes were authorized, re-asserted now and verified before serving",
		"app_role", roles.App.Role, "owner_role", roles.Owner.Role, "relations", dialect.GuardControlPlaneTables())
	tables := dialect.GuardControlPlaneTables()
	present, err := existingTables(ctx, ownerDB, tables)
	if err != nil {
		return fmt.Errorf("sqlstore: guard metadata ACL: list tables: %w", err)
	}
	live := make([]string, 0, len(tables))
	for _, t := range tables {
		if present[t] {
			live = append(live, t)
		}
	}
	if len(live) == 0 {
		// Nothing to do, and nothing to hide: on SQLite this function returned already, and on
		// PostgreSQL the preflight refuses a partial control plane, so reaching here with none
		// of the three present means this boot has not created them yet.
		return nil
	}
	if err := requireTableOwnership(ctx, ownerDB, live); err != nil {
		return err
	}
	stmts := dia.GuardMetadataACLStmts()
	if len(stmts) != len(tables) {
		return fmt.Errorf("sqlstore: guard metadata ACL: the dialect returned %d statements for %d relations",
			len(stmts), len(tables))
	}
	for i, stmt := range stmts {
		if !present[tables[i]] {
			continue
		}
		if _, err := ownerDB.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("sqlstore: guard metadata ACL on %q: %w", tables[i], err)
		}
	}
	return nil
}

// verifyGuardMetadataACL refuses to serve when the application role can write the control
// plane.
//
// It reads EFFECTIVE privileges from the application pool, which is the only reading that
// accounts for a direct grant, a group role, PUBLIC and ownership alike. A name-targeted
// REVOKE does not strip a privilege held through a group, and only has_table_privilege sees
// that — which is exactly why the reconcile above does not make this check redundant.
//
// SELECT is PERMITTED and not required. An operator reading the rollout's history through the
// application role is legitimate; being unable to is not a failure, because the engine writes
// and reads this history on the owner pool.
func verifyGuardMetadataACL(ctx context.Context, db *sql.DB, dia dialect.Dialect, hardened bool) error {
	if dia.Name() != store.EnginePostgres {
		return nil
	}
	if !hardened {
		// Nothing to verify, and demanding it would be worse than nothing: under a single role
		// the engine's own writer is the application role, so requiring INSERT to be absent
		// would refuse every default deployment. The reconcile above states the limit.
		return nil
	}
	tables := dialect.GuardControlPlaneTables()
	present, err := existingTables(ctx, db, tables)
	if err != nil {
		return fmt.Errorf("sqlstore: guard metadata ACL verification: list tables: %w", err)
	}
	live := make([]string, 0, len(tables))
	for _, t := range tables {
		if present[t] {
			live = append(live, t)
		}
	}
	// ALL THREE, OR IT IS A REFUSAL — never a pass.
	//
	// This used to return success when NONE were present and to verify whatever subset was, and
	// both are wrong at the point this runs: the preflight has already refused a partial control
	// plane, and this call happens after the migration that creates all three. So "none" and
	// "two" both mean a relation went missing between the two readings — a DROP by an owner
	// concurrent with this boot — and the answer to "can the application role write the control
	// plane?" is then unknown, not no. verifyAppendOnlyACL's own rule already says an incomplete
	// answer about a boundary is a refusal; this is the same rule applied one level up.
	if len(live) != len(tables) {
		missing := make([]string, 0, len(tables))
		for _, t := range tables {
			if !present[t] {
				missing = append(missing, t)
			}
		}
		return fmt.Errorf("sqlstore: %w: the guard control plane should hold %d relations in schema %q and %d are present (missing: %v); a relation that disappeared between the preflight and this check leaves the boundary unverifiable",
			store.ErrAppendOnlyACLUnverifiable, len(tables), dialect.EngineSchema, len(live), missing)
	}
	list, args := tableParams([]any{dialect.EngineSchema}, live)
	// #nosec G202 -- `list` is tableParams' output: ONLY "$2,$3,…" placeholders. The relation names travel as bound args, the schema as $1
	q := `SELECT c.relname,
       pg_catalog.has_table_privilege(c.oid, 'INSERT'),
       pg_catalog.has_table_privilege(c.oid, 'UPDATE'),
       pg_catalog.has_table_privilege(c.oid, 'DELETE'),
       pg_catalog.has_table_privilege(c.oid, 'TRUNCATE')
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('r','p') AND c.relname IN (` + list + `)`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("sqlstore: guard metadata ACL verification: %w", err)
	}
	defer rows.Close()
	var open []string
	seen := 0
	for rows.Next() {
		var table string
		var canInsert, canUpdate, canDelete, canTruncate bool
		if err := rows.Scan(&table, &canInsert, &canUpdate, &canDelete, &canTruncate); err != nil {
			return fmt.Errorf("sqlstore: guard metadata ACL verification: %w", err)
		}
		seen++
		var held []string
		for _, p := range []struct {
			name string
			ok   bool
		}{{"INSERT", canInsert}, {"UPDATE", canUpdate}, {"DELETE", canDelete}, {"TRUNCATE", canTruncate}} {
			if p.ok {
				held = append(held, p.name)
			}
		}
		if len(held) > 0 {
			open = append(open, fmt.Sprintf("%s: %s", table, strings.Join(held, ",")))
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlstore: guard metadata ACL verification: %w", err)
	}
	if seen != len(live) {
		// An incomplete answer about a boundary is a refusal, never a pass. The scope was
		// computed from this same schema moments ago, so a relation that cannot be re-resolved
		// now means the reading is partial.
		return fmt.Errorf("sqlstore: %w: resolved %d of %d guard control-plane relations in schema %q",
			store.ErrAppendOnlyACLUnverifiable, seen, len(live), dialect.EngineSchema)
	}
	if len(open) > 0 {
		sort.Strings(open)
		return fmt.Errorf(
			"sqlstore: refusing to start: %w — the application role can write the guard control plane: %v. "+
				"That history is what says which schema changes were authorized and applied, so a role that can append to it can fabricate authorisations. "+
				"The engine re-asserts this revoke on every boot, so a privilege that survives it was granted outside it: to this role directly, to a group role it belongs to, or to PUBLIC. "+
				"In the single-role topology the application role OWNS these tables and can re-grant itself anything, so the hardened posture is a separate owner role (--owner-dsn) with the application role holding at most SELECT",
			store.ErrAppendOnlyACLOpen, open)
	}
	return nil
}
