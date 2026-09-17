#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""The one overlay fact evaluator, shared by the live-main and the exact-candidate adapters.

OGL2 release-boundary decision 1 ("Candidate facts and main observation"): the live-main reader
keeps its sealed main semantics, the exact-candidate evaluator reads the six private blobs and
the paired Community slug map from immutable objects, and BOTH adapters share one fact
evaluator that distinguishes a finding from an inability to evaluate. This file is that
evaluator. It was extracted from the inline program of `scripts/check-overlay-live-facts.sh`;
its predicates, the `overlay-ast/v1` protocol, the closed-shape digests and the exit meaning are
the ones that program had, and the main adapter's messages are unchanged byte for byte.

    0  the facts hold      1  a demonstrated finding      2  an input could not be observed

Two subjects, never merged:

  main       <overlay-repo> <community-repo> <acta> <ast-bin> <captured-main>
             The LT1 Module captured <captured-main> from the sealed clone. Every Community
             input — the slug map and, for the current construction, the Community support
             files — is read at the gitlink THAT commit declares, read before evaluation. No
             other SHA is accepted here.

  candidate  --overlay-repo --overlay-commit --community-repo --community-commit
             --acta --ast-bin --identity-out
             Both commits are explicit 40-hex COMMIT object ids. Their commit, tree and the
             overlay's public gitlink are recorded as distinct identities before any fact is
             judged, so a wrong or missing object is 2 whatever the facts would have been. The
             identity document is written on every exit that got far enough to open it.

Two whole durable constructions (r116 durable-facts successor). The AST reader names the one
it found; the acta holds exactly one complete reviewed profile per construction; the report
must match exactly one profile, and that one must be the construction the reader named:

  legacy-direct-v1         ten present nodes, `durableLicensedAt` explicitly absent, seven
                           inputs (six private sources + the Community slug map).
  observed-crl-keyring-v1  eleven present nodes, eleven inputs: those seven plus four support
                           files bound by exact bytes (one private, three Community).

No per-field alternative, fallback or mixing: a digest, import list or support binding of one
profile never completes another. A source fact result is not release authority.

The evaluator reads git objects with `--no-replace-objects` and runs exactly one program over
them: the trusted `overlay-ast` binary the adapter built from its OWN tree. It never checks out,
never reads a working tree and never executes anything the candidate objects contain.
"""

import argparse, hashlib, io, json, os, re, shutil, subprocess, sys, tempfile

HEX40 = re.compile(r"^[0-9a-f]{40}$")
HEX64 = re.compile(r"^[0-9a-f]{64}$")

P_LEGALHOLD = "enterprise/rtbf/legalhold.go"
P_DURABLE = "cmd-overlay/olivares/durablebus_enterprise.go"
P_PACKS = "cmd-overlay/olivares/addonpacks_enterprise.go"
P_CATALOG = "enterprise/activation/catalog.go"
P_CUT = "cmd-overlay/olivares/wire_enterprise_addon_cp.go"
P_STUB = "cmd-overlay/olivares/wire_enterprise_noaddon_cp.go"
P_PUBLIC_MAP = "commercial/module-slug-package.json"
BLOB_PATHS = (P_LEGALHOLD, P_DURABLE, P_PACKS, P_CATALOG, P_CUT, P_STUB)

# Named predicates the AST reader must emit. Missing one is a weaker contract, not CLEAN.
REQUIRED_CHECKS = (
    "durable.present",
    "durable.denies_are_false",
    "durable.selection_is_composition",
    "durable.pack",
    "durable.term_guard_denies",
    "durable.resolve_and_blob_denies",
    "durable.invalid_key_denies",
    "durable.claims_are_read",
    "durable.statement_sequence_is_reviewed",
    "combined.present",
    "combined.v3_branch_returns_fromGrants",
    "combined.fallback_is_fromClaims",
    "grants.present",
    "grants.nil_provider_denies",
    "grants.unreadable_denies",
    "grants.empty_list_denies",
    "grants.unknown_pack_denies",
    "grants.product_match_grants",
    "claims.present",
    "claims.vocabulary_answers_attestation",
    "claims.legacy_switch_present",
    "claims.legacy_never_grants_by_default",
    "claims.legacy_grants_exactly_self_serve",
    "legalhold.present",
    "legalhold.statement_sequence_is_reviewed",
    "legalhold.hold_id_required",
    "legalhold.disabled_refuses",
    "legalhold.justification_refuses",
    "legalhold.approver_floor_refuses",
    "legalhold.success_allows",
    "legalhold.calls_are_reviewed",
    "aims.composition_is_the_only_return",
    "aims.stub_returns_nil",
    "catalog.present",
    "setpacks.present",
    "setpacks.ids_is_identity_scale",
    "setpacks.mapping_is_reviewed",
    "productid.present",
    "productid.identity_scale_is_reviewed",
    "productid.mapping_is_reviewed",
)

# Frozen reviewed-node set. Source and acta omitting the same name cannot shrink it.
REQUIRED_NODES = (
    P_LEGALHOLD + "#LegalHoldOverride.EvaluateOverride",
    P_DURABLE + "#durableLicensed",
    P_DURABLE + "#durableLicensedAt",
    P_PACKS + "#combinedPurchaseView",
    P_PACKS + "#purchaseViewFromGrants",
    P_PACKS + "#purchaseViewFromClaims",
    P_PACKS + "#attestedPacks",
    P_PACKS + "#packProductID",
    P_PACKS + "#setCodePacks",
    P_CUT + "#newAIMSPackager",
    P_STUB + "#newAIMSPackager",
)
READER_SCHEMA = "overlay-ast/v2"
DIGEST_SCHEMA = "overlay-ast/v1"
ACTA_SCHEMA = "overlay-live-facts/v2"
RESULT_SCHEMA = "overlay-facts-result/v1"

CONSTRUCTION_LEGACY = "legacy-direct-v1"
CONSTRUCTION_CURRENT = "observed-crl-keyring-v1"
# The support closure of the current construction, in its fixed order. Data only: hashed and
# compared, never parsed or executed. Private files come from E, Community files from the
# ACTUAL paired C — never from the working directory or the acta baseline's C.
SUPPORT_FILES = (
    ("private", "enterprise/addongate/addongate.go"),
    ("community", "cmd/olivares/license_holder.go"),
    ("community", "cmd/olivares/license_trust.go"),
    ("community", "cmd/olivares/license_crl.go"),
)
CONSTRUCTIONS = {
    CONSTRUCTION_LEGACY: {
        "absent": (P_DURABLE + "#durableLicensedAt",),
        "support": (),
        "observed_revocation": "not_read_by_legacy_durable_selection",
    },
    CONSTRUCTION_CURRENT: {
        "absent": (),
        "support": SUPPORT_FILES,
        "observed_revocation": "canonical_observation_grace_at_boot",
    },
}
CONSTRUCTION_FIELDS = {"reviewed_node_digests", "required_absent_nodes", "reviewed_imports",
                       "support_blobs", "observed_revocation"}
SUPPORT_FIELDS = {"role", "path", "blob", "sha256", "bytes", "review_source"}


def fail(msg):
    print(msg, file=sys.stderr)
    raise SystemExit(1)


def look(msg):
    print(msg, file=sys.stderr)
    raise SystemExit(2)


def gitb(repo, *args):
    p = subprocess.run(
        ["git", "--no-replace-objects", "-C", repo, *args],
        capture_output=True,
    )
    return p.returncode, p.stdout, p.stderr


def gits(repo, *args):
    rc, out, err = gitb(repo, *args)
    return rc, out.decode("utf-8", "replace"), err.decode("utf-8", "replace")


# ── 1 · the acta ──────────────────────────────────────────────────────────────────────────
def load_acta(acta_path):
    try:
        acta = json.load(io.open(acta_path, encoding="utf-8"))
    except (OSError, ValueError) as exc:
        look("acta %s is not readable JSON (%s)" % (acta_path, exc))

    if acta.get("schema") != ACTA_SCHEMA:
        fail("unknown schema %r" % acta.get("schema"))
    for key in ("overlay_main_sha", "overlay_main_public_pin"):
        val = acta.get(key)
        if not isinstance(val, str) or not HEX40.match(val):
            fail("%s must be a 40-hex object id" % key)

    if acta.get("panel_executed") is not False:
        fail("panel_executed must stay false: this contract measures facts, it does not close the panel")
    for k in ("u_f", "u_d"):
        if acta.get(k) != "UNKNOWN":
            fail("%s must stay UNKNOWN" % k)
    if acta.get("iso42001_certification") != "none":
        fail("iso42001_certification must stay \"none\": the pack builds drafts for an auditor")
    if acta.get("iso42001_in_every_enterprise_artifact") is not False:
        fail("KindWired is not 'in every artifact': the bytes follow the build-tag cut")
    validate_constructions(acta)
    return acta


def is_import_list(value):
    return isinstance(value, list) and all(
        isinstance(b, dict) and set(b) == {"name", "path"}
        and isinstance(b["name"], str) and isinstance(b["path"], str) for b in value)


def validate_constructions(acta):
    """The two whole reviewed profiles, closed: every key, count, absence and support slot."""
    if acta.get("reviewed_digest_schema") != DIGEST_SCHEMA:
        fail("acta reviewed_digest_schema must be %s: the declaration digest preimage is versioned "
             "apart from the report" % DIGEST_SCHEMA)
    reviewed = acta.get("reviewed_constructions")
    if not isinstance(reviewed, dict) or set(reviewed) != set(CONSTRUCTIONS):
        got = sorted(reviewed) if isinstance(reviewed, dict) else reviewed
        fail("acta reviewed_constructions must be exactly %s, got %r" % (sorted(CONSTRUCTIONS), got))
    for cid in sorted(CONSTRUCTIONS):
        spec, entry = CONSTRUCTIONS[cid], reviewed[cid]
        where = "acta reviewed_constructions[%s]" % cid
        if not isinstance(entry, dict) or set(entry) != CONSTRUCTION_FIELDS:
            got = sorted(entry) if isinstance(entry, dict) else entry
            fail("%s must have exactly the fields %s, got %r" % (where, sorted(CONSTRUCTION_FIELDS), got))
        absent = entry["required_absent_nodes"]
        if not isinstance(absent, list) or sorted(absent) != sorted(spec["absent"]):
            fail("%s.required_absent_nodes must be exactly %s" % (where, list(spec["absent"])))
        present = set(REQUIRED_NODES) - set(spec["absent"])
        digests = entry["reviewed_node_digests"]
        if not isinstance(digests, dict) or set(digests) != present:
            got = sorted(digests) if isinstance(digests, dict) else []
            fail("%s.reviewed_node_digests must be exactly the %d present nodes %s, got %s "
                 "(a missing target plus a missing acta key cannot drop a reviewed decision)"
                 % (where, len(present), sorted(present), got))
        for key in sorted(digests):
            if not isinstance(digests[key], str) or not HEX64.match(digests[key]):
                fail("%s.reviewed_node_digests[%s] is not a lowercase sha256 hex" % (where, key))
        imports = entry["reviewed_imports"]
        if not isinstance(imports, dict) or set(imports) != set(BLOB_PATHS) \
                or not all(is_import_list(imports[p]) for p in BLOB_PATHS):
            fail("%s.reviewed_imports must name exactly the six sealed sources, each with its bindings" % where)
        if entry["observed_revocation"] != spec["observed_revocation"]:
            fail("%s.observed_revocation must be %r" % (where, spec["observed_revocation"]))
        blobs = entry["support_blobs"]
        if not isinstance(blobs, list) or len(blobs) != len(spec["support"]):
            fail("%s.support_blobs must bind exactly %d support files" % (where, len(spec["support"])))
        for (role, path), d in zip(spec["support"], blobs):
            if not isinstance(d, dict) or set(d) != SUPPORT_FIELDS or d["role"] != role or d["path"] != path:
                fail("%s.support_blobs must be exactly %s in that order, each with %s"
                     % (where, [p for _, p in spec["support"]], sorted(SUPPORT_FIELDS)))
            if not (isinstance(d["blob"], str) and HEX40.match(d["blob"])
                    and isinstance(d["sha256"], str) and HEX64.match(d["sha256"])
                    and isinstance(d["bytes"], int) and not isinstance(d["bytes"], bool) and d["bytes"] >= 0
                    and isinstance(d["review_source"], str) and HEX40.match(d["review_source"])):
                fail("%s.support_blobs[%s] needs a 40-hex blob, a sha256, a byte size and a 40-hex review source"
                     % (where, path))
    sources = acta.get("reviewed_construction_sources")
    if not isinstance(sources, dict) or set(sources) != set(CONSTRUCTIONS) or not all(
            isinstance(sources[c], dict) and set(sources[c]) == {"overlay", "community"}
            and all(isinstance(v, str) and HEX40.match(v) for v in sources[c].values()) for c in CONSTRUCTIONS):
        fail("acta reviewed_construction_sources must name the exact reviewed overlay and Community "
             "commit of each construction")


def input_identity(repo, commit, path, role, raw):
    """One immutable input as the semantic result records it."""
    rc, out, err = gits(repo, "rev-parse", "--verify", "%s:%s" % (commit, path))
    blob = out.strip()
    if rc != 0 or not HEX40.match(blob):
        look("%s input %s:%s has no readable object id: %s"
             % (role, commit[:9], path, (err or "").strip()[:200] or "missing object"))
    return {"role": role, "path": path, "commit": commit, "blob": blob,
            "bytes": len(raw), "sha256": hashlib.sha256(raw).hexdigest()}


def read_support(ent, pin, community_repo, community_commit):
    """The four support files of observed-crl-keyring-v1, from the ACTUAL paired E and C.

    A missing, non-file or unreadable path is 2: a required input of this construction is
    not observable. A changed binding is judged later, as 1.
    """
    out = []
    for role, path in SUPPORT_FILES:
        repo, commit = (ent, pin) if role == "private" else (community_repo, community_commit)
        rc, oid, err = gits(repo, "rev-parse", "--verify", "%s:%s" % (commit, path))
        oid = oid.strip()
        if rc != 0 or not HEX40.match(oid):
            look("%s support %s:%s is unreadable (--no-replace-objects): %s"
                 % (role, commit[:9], path, (err or "").strip()[:200] or "missing path"))
        rc, kind, _ = gits(repo, "cat-file", "-t", oid)
        if rc != 0 or kind.strip() != "blob":
            look("%s support %s:%s is a %s object, not a file" % (role, commit[:9], path, kind.strip() or "unreadable"))
        rc, raw, errb = gitb(repo, "cat-file", "blob", oid)
        if rc != 0:
            look("%s support %s:%s is unreadable: %s"
                 % (role, commit[:9], path, errb.decode("utf-8", "replace").strip()[:200]))
        out.append({"role": role, "path": path, "commit": commit, "blob": oid,
                    "bytes": len(raw), "sha256": hashlib.sha256(raw).hexdigest()})
    return out


def construction_mismatch(report, cid, entry, where):
    """The first difference between the report and ONE whole reviewed construction, or None."""
    nodes = report.get("nodes") or {}
    absent = set(entry["required_absent_nodes"])
    for key in REQUIRED_NODES:
        fn = nodes.get(key) or {}
        if key in absent:
            if fn.get("found") or fn.get("digest"):
                return ("estructura no verificada: %s is present, and construction %s requires it absent"
                        % (key, cid))
            continue
        if not fn.get("found"):
            return "%s is absent from the %s overlay (construction %s)" % (key, where, cid)
        got = fn.get("digest") or ""
        if not isinstance(got, str) or not HEX64.match(got):
            return "estructura no verificada: %s is readable but has no closed-form digest" % key
        if got != entry["reviewed_node_digests"][key]:
            return ("estructura no verificada: %s is readable but is not the reviewed closed form of "
                    "construction %s (needs an explicit contract update and a behaviour test)" % (key, cid))
    got_files = report.get("files") or {}
    for path in BLOB_PATHS:
        if entry["reviewed_imports"][path] != (got_files.get(path) or {}).get("imports"):
            return ("estructura no verificada: import bindings of %s are not the reviewed bindings of "
                    "construction %s (the same function AST with an altered package import is a "
                    "different decision)" % (path, cid))
    return None


# ── 3 · extract immutable blobs and parse them with the real Go AST ───────────────────────
def evaluate_overlay(ent, pin, community_repo, community_commit, acta, ast_bin, where):
    """Sections 3-5 over the six blobs at `pin` and, for the current construction, the support
    files at `pin` and at the paired Community commit. `where` names the subject in messages."""
    inputs = []
    blobdir = tempfile.mkdtemp(prefix="overlayfacts-blobs.")
    try:
        for path in BLOB_PATHS:
            rc, raw, errb = gitb(ent, "show", "%s:%s" % (pin, path))
            if rc != 0:
                look("overlay %s:%s is unreadable (--no-replace-objects): %s"
                     % (pin[:9], path, (errb.decode("utf-8", "replace") or "").strip()[:200] or "missing path"))
            inputs.append(input_identity(ent, pin, path, "private", raw))
            dest = os.path.join(blobdir, path)
            os.makedirs(os.path.dirname(dest), exist_ok=True)
            with open(dest, "wb") as fh:
                fh.write(raw)
        p = subprocess.run([ast_bin, blobdir], capture_output=True, text=True)
        if p.returncode in (126, 127):
            look("overlay-ast could not be executed (exit %s); TMPDIR=%s may be noexec"
                 % (p.returncode, os.environ.get("TMPDIR", "/tmp")))
        if p.returncode == 2:
            look((p.stderr or p.stdout or "overlay-ast could not parse a %s blob" % where).strip()[:400])
        if p.returncode != 0:
            look("overlay-ast exited %s: %s" % (p.returncode, (p.stderr or "")[:400]))
        try:
            report = json.loads(p.stdout)
        except ValueError as exc:
            look("overlay-ast did not emit a report (%s)" % exc)
    finally:
        shutil.rmtree(blobdir, ignore_errors=True)

    if report.get("schema") != READER_SCHEMA:
        fail("the AST reader declared schema %r, not %s" % (report.get("schema"), READER_SCHEMA))
    if acta.get("reviewed_surface_schema") != READER_SCHEMA:
        fail("acta reviewed_surface_schema must be %s: the closed node manifest is versioned" % READER_SCHEMA)
    if set(report.get("surface") or []) != set(REQUIRED_NODES):
        fail("the AST reader surface is not the frozen reviewed-node set")
    if report.get("digestSchema") != DIGEST_SCHEMA:
        fail("the AST reader framed its digests as %r, not %s" % (report.get("digestSchema"), DIGEST_SCHEMA))
    construction = report.get("construction")
    if construction not in CONSTRUCTIONS:
        fail("the AST reader judged an unknown durable construction %r" % construction)

    # The support closure is an INPUT of the current construction: read before any fact is
    # judged, so an unobservable required file is 2 whatever the facts would have said.
    support = []
    if CONSTRUCTIONS[construction]["support"]:
        support = read_support(ent, pin, community_repo, community_commit)

    checks = report.get("checks") or {}
    for name in REQUIRED_CHECKS:
        c = checks.get(name)
        if not isinstance(c, dict):
            fail("the AST reader did not report %s: the live contract cannot drop a reviewed predicate" % name)
        if not c.get("ok"):
            detail = (c.get("detail") or "").strip() or "predicate failed"
            fail("%s: %s" % (name, detail))
    if set(checks) != set(REQUIRED_CHECKS):
        fail("the AST reader reported predicates outside the %d reviewed IDs: %s"
             % (len(REQUIRED_CHECKS), sorted(set(checks) - set(REQUIRED_CHECKS))))

    nodes = report.get("nodes") or {}
    if set(nodes) != set(REQUIRED_NODES):
        missing = sorted(set(REQUIRED_NODES) - set(nodes))
        extra = sorted(set(nodes) - set(REQUIRED_NODES))
        fail("the AST reader node set drifted (missing %s extra %s); the reviewed set cannot shrink"
             % (missing, extra))

    got_files = report.get("files") or {}
    if set(got_files) != set(BLOB_PATHS):
        fail("the AST reader did not report exactly the six sealed sources")

    # ONE whole profile. The construction the reader named must match completely, and no
    # other may: a valid acta cannot be satisfied twice, and a profile is never completed
    # with another profile's digest, imports or absence.
    profiles = acta["reviewed_constructions"]
    why = construction_mismatch(report, construction, profiles[construction], where)
    if why is not None:
        fail(why)
    matched = [cid for cid in sorted(profiles) if construction_mismatch(report, cid, profiles[cid], where) is None]
    if matched != [construction]:
        fail("acta reviewed_constructions is invalid: this report matches %s; exactly one whole "
             "construction may" % matched)

    want_sets = acta.get("reviewed_set_code_packs")
    got_sets = report.get("setCodePacks") or {}
    if not isinstance(want_sets, dict) or want_sets != got_sets:
        fail("estructura no verificada: setCodePacks is %s and the acta records %s"
             % (got_sets, want_sets))
    want_pids = acta.get("reviewed_pack_product_ids")
    got_pids = report.get("packProductIDs") or {}
    if not isinstance(want_pids, dict) or want_pids != got_pids:
        fail("estructura no verificada: packProductID is %s and the acta records %s"
             % (got_pids, want_pids))

    if acta.get("legal_hold_evaluation_consults_purchase") is not False:
        fail("legal_hold_evaluation_consults_purchase must stay false")
    if acta.get("durable_selection_reads_term") is not True:
        fail("durable_selection_reads_term must stay true")
    if acta.get("durable_selection_reads_purchase_composition") is not True:
        fail("durable_selection_reads_purchase_composition must stay true")
    if acta.get("purchase_composition_order") != ["v3-grants", "set-vocabulary", "legacy-no-vocabulary"]:
        fail("purchase_composition_order must record the policy the code implements")
    if acta.get("legacy_no_vocabulary_entitles_enterprise") is not False:
        fail("legacy_no_vocabulary_entitles_enterprise must stay false")

    # ── 4 · catalog as DATA, packs as declared constants ──────────────────────────────────
    rows = report.get("catalog") or []
    iso = [r for r in rows if r.get("key") == "iso42001"]
    if len(iso) == 0:
        fail("the activation catalog has NO iso42001 row (%d rows read)" % len(rows))
    if len(iso) > 1:
        fail("the activation catalog has %d iso42001 rows: a duplicated key is not a catalog" % len(iso))
    row = iso[0]
    if acta.get("iso42001_in_catalog") is not True:
        fail("iso42001_in_catalog must stay true")
    pack_consts = report.get("packConsts") or {}
    pack_value = pack_consts.get(row.get("pack") or "")
    if not pack_value:
        fail("the iso42001 row's Pack is %r and no Pack constant declares its value: a pack that "
             "names nothing sells nothing" % row.get("pack"))
    if pack_value != acta.get("iso42001_pack"):
        fail("iso42001 is sold by pack %r and the acta records %r" % (pack_value, acta.get("iso42001_pack")))
    if row.get("kind") != acta.get("iso42001_kind"):
        fail("iso42001 Kind is %r and the acta records %r" % (row.get("kind"), acta.get("iso42001_kind")))
    if row.get("disp") != acta.get("iso42001_disposition"):
        fail("iso42001 Disp is %r and the acta records %r" % (row.get("disp"), acta.get("iso42001_disposition")))

    pack_const = (checks.get("durable.pack") or {}).get("detail") or ""
    durable_pack_value = pack_consts.get(pack_const)
    if not durable_pack_value:
        fail("durableLicensed selects on %r and the catalog declares no such Pack constant" % pack_const)
    if durable_pack_value != acta.get("durable_purchase_pack"):
        fail("durableLicensed selects pack %r and the acta records %r"
             % (durable_pack_value, acta.get("durable_purchase_pack")))

    # ── 5 · the cut: KindWired is not "in every artifact" ─────────────────────────────────
    files = report.get("files") or {}
    cut_build = (files.get(P_CUT) or {}).get("build")
    stub_build = (files.get(P_STUB) or {}).get("build")
    if cut_build != acta.get("iso42001_cut_build_tag"):
        fail("%s is built under %r and the acta records %r"
             % (P_CUT, cut_build, acta.get("iso42001_cut_build_tag")))
    if stub_build != acta.get("iso42001_stub_build_tag"):
        fail("%s is built under %r and the acta records %r"
             % (P_STUB, stub_build, acta.get("iso42001_stub_build_tag")))

    # ── 5b · the support closure, last: success needs every binding ───────────────────────
    # The moved durable.invalid_key_denies and observed-revocation facts live in these files.
    # Any byte change is a readable, unreviewed decision; the local AST call shape alone is
    # never that evidence.
    for want, got in zip(profiles[construction]["support_blobs"], support):
        for field in ("blob", "bytes", "sha256"):
            if want[field] != got[field]:
                fail("estructura no verificada: %s support %s at %s changed (%s %s, reviewed %s from %s): "
                     "the durable trust and observed-revocation facts of %s need an explicit review"
                     % (got["role"], got["path"], got["commit"][:9], field, got[field], want[field],
                        want["review_source"][:9], construction))

    return {
        "catalog_rows": len(rows),
        "iso42001_pack": pack_value,
        "iso42001_kind": row.get("kind"),
        "iso42001_disposition": row.get("disp"),
        "durable_purchase_pack": durable_pack_value,
        "construction": construction,
        "observed_revocation": CONSTRUCTIONS[construction]["observed_revocation"],
        "nodes": {key: {"present": bool((nodes.get(key) or {}).get("found")),
                        "digest": (nodes.get(key) or {}).get("digest") or None} for key in REQUIRED_NODES},
        "private_inputs": inputs,
        "support": support,
    }


def semantic_result(facts, public_map, overlay, community):
    """The retained semantic result, built ONLY after complete success and shared by both
    adapters. `community.source` says whether C was the captured main's gitlink or an
    independent candidate commit; neither is a qualification or release result."""
    inputs = facts["private_inputs"] + [public_map["input"]] + facts["support"]
    want = 7 + len(CONSTRUCTIONS[facts["construction"]]["support"])
    if len(inputs) != want:
        look("internal: construction %s recorded %d inputs, not %d" % (facts["construction"], len(inputs), want))
    result = {
        "schema": RESULT_SCHEMA,
        "construction": facts["construction"],
        "observed_revocation": facts["observed_revocation"],
        "report_schema": READER_SCHEMA,
        "digest_schema": DIGEST_SCHEMA,
        "checks": {name: True for name in REQUIRED_CHECKS},
        "nodes": facts["nodes"],
        "inputs": inputs,
        "support": facts["support"],
        "overlay": overlay,
        "community": community,
        "iso42001_in_public_slug_map": public_map["present"],
    }
    for key in ("catalog_rows", "iso42001_pack", "iso42001_kind", "iso42001_disposition", "durable_purchase_pack"):
        result[key] = facts[key]
    return result


def read_public_gitlink(ent, pin):
    rc, out, err = gits(ent, "ls-tree", pin, "public")
    if rc != 0 or "commit" not in out:
        look("cannot read the overlay's public gitlink at %s: %s" % (pin[:9], (err or out).strip()[:200]))
    return out.split()[2]


# ── 6 · the public slug map, at one Community commit ─────────────────────────────────────
def evaluate_public_map(root, community, acta, label):
    """`label` says which Community commit this is: declared by main, or explicit candidate."""
    rc, _, err = gits(root, "rev-parse", "--verify", "%s^{commit}" % community)
    if rc != 0:
        look("%s %s is not in this object store: %s"
             % (label, community[:9], (err or "").strip()[:200] or "missing object"))
    rc, raw, errb = gitb(root, "show", "%s:%s" % (community, P_PUBLIC_MAP))
    err = errb.decode("utf-8", "replace")
    if rc != 0:
        look("Community %s:%s is unreadable: %s"
             % (community[:9], P_PUBLIC_MAP, (err or "").strip()[:200] or "missing path"))
    try:
        sold = json.loads(raw)
        entries = {e["slug"]: e.get("package") for e in sold["entries"]}
    except (ValueError, KeyError, TypeError) as exc:
        look("the public slug map at %s is not readable as the sold map (%s)" % (community[:9], exc))
    present = "iso42001" in entries
    if present is not acta.get("iso42001_in_public_slug_map"):
        fail("the public slug map at %s %s iso42001 and the acta records %r"
             % (community[:9], "names" if present else "does not name",
                acta.get("iso42001_in_public_slug_map")))
    if present and entries["iso42001"] != acta.get("iso42001_public_package"):
        fail("the public slug map sells iso42001 as %r and the acta records %r"
             % (entries["iso42001"], acta.get("iso42001_public_package")))
    return {"present": present, "input": input_identity(root, community, P_PUBLIC_MAP, "community", raw)}


def run_main(ent, root, acta_path, ast_bin, pin):
    acta = load_acta(acta_path)

    # ── 2 · the CAPTURED main, and ONLY the captured main ─────────────────────────────────
    # The Module captured this 40-hex from the sealed clone, established that origin/main still
    # resolved to it, validated the acta's own baseline against its own objects, and will check
    # at completion that the ref did not move. Nothing below resolves a moving name.
    if not HEX40.match(pin):
        look("the Module did not supply a 40-hex captured main: %r" % pin[:80])
    rc, _, err = gits(ent, "rev-parse", "--verify", "%s^{commit}" % pin)
    if rc != 0:
        look("captured overlay main %s is unreadable as a commit: %s" % (pin, (err or "").strip()[:200]))

    # ⛔ EL PIN SE LEE DEL MAIN CAPTURADO, NO DEL ACTA, y esa es la mitad de LT1 que mas facil
    # seria hacer mal: «sustituir el pin del JSON sin leer el codigo al que apunta» esta prohibido,
    # y leer el blob del pin VIEJO mientras el overlay declara otro seria exactamente eso. El
    # Modulo ya validó que el pin del ACTA es el gitlink de SU PROPIA linea base, y que el gitlink
    # ACTUAL existe en este almacen de Community — si no existe, sale 2 con su dependencia
    # nombrada, y NUNCA cae al blob viejo.
    # It is read BEFORE evaluation: the Community support files of the current construction
    # are read at this same immutable gitlink, never at the working directory's HEAD.
    gitlink = read_public_gitlink(ent, pin)
    facts = evaluate_overlay(ent, pin, root, gitlink, acta, ast_bin, "sealed")
    public_map = evaluate_public_map(root, gitlink, acta, "declared Community commit")
    result = semantic_result(facts, public_map,
                             {"commit": pin, "public_gitlink": gitlink},
                             {"commit": gitlink, "source": "captured-main-gitlink"})

    print("%s: overlay main %s (captured) · Community %s · catalog rows %d · iso42001 pack %s "
          "%s/%s · durable pack %s · construction %s"
          % ("check-overlay-live-facts", pin[:9], gitlink[:9], facts["catalog_rows"],
             facts["iso42001_pack"], facts["iso42001_kind"], facts["iso42001_disposition"],
             facts["durable_purchase_pack"], facts["construction"]))
    print("%s %s" % (RESULT_SCHEMA, json.dumps(result, sort_keys=True, separators=(",", ":"))))


def exact_commit(repo, oid, label, ident):
    """An explicit candidate input is a COMMIT object id, not a tag, tree, blob or name."""
    if not HEX40.match(oid or ""):
        look("%s must be a 40-hex commit object id, got %r" % (label, (oid or "")[:80]))
    rc, out, err = gits(repo, "cat-file", "-t", oid)
    if rc != 0:
        look("%s %s is not in the object store %s: %s"
             % (label, oid[:9], repo, (err or "").strip()[:200] or "missing object"))
    if out.strip() != "commit":
        look("%s %s is a %s object, not a commit" % (label, oid[:9], out.strip() or "unknown"))
    rc, out, err = gits(repo, "rev-parse", "--verify", "%s^{tree}" % oid)
    tree = out.strip()
    if rc != 0 or not HEX40.match(tree):
        look("%s %s has no readable tree: %s" % (label, oid[:9], (err or "").strip()[:200]))
    ident["commit"] = oid
    ident["tree"] = tree


def run_candidate(args):
    ident = {
        "enterprise": {"repository": args.overlay_repo, "commit": None, "tree": None, "public_gitlink": None},
        "community": {"repository": args.community_repo, "commit": None, "tree": None},
        "facts": None,
    }
    code = 2
    try:
        for repo, label in ((args.overlay_repo, "overlay"), (args.community_repo, "Community")):
            rc, _, err = gits(repo, "rev-parse", "--git-dir")
            if rc != 0:
                look("the %s repository %s is not readable as a git repository: %s"
                     % (label, repo, (err or "").strip()[:200]))
        exact_commit(args.overlay_repo, args.overlay_commit, "candidate overlay commit", ident["enterprise"])
        ident["enterprise"]["public_gitlink"] = read_public_gitlink(args.overlay_repo, args.overlay_commit)
        exact_commit(args.community_repo, args.community_commit, "candidate Community commit", ident["community"])

        acta = load_acta(args.acta)
        facts = evaluate_overlay(args.overlay_repo, args.overlay_commit, args.community_repo,
                                 args.community_commit, acta, args.ast_bin, "candidate")
        public_map = evaluate_public_map(args.community_repo, args.community_commit, acta,
                                         "candidate Community commit")
        ident["facts"] = semantic_result(
            facts, public_map,
            {"commit": args.overlay_commit, "public_gitlink": ident["enterprise"]["public_gitlink"]},
            {"commit": args.community_commit, "source": "explicit-candidate-commit"})
        code = 0
    except SystemExit as exc:
        code = exc.code if exc.code in (1, 2) else 2
    except Exception as exc:  # an evaluator that crashed did not evaluate: 2, never a finding
        print("the evaluator could not complete (%s: %s)" % (type(exc).__name__, exc), file=sys.stderr)
        code = 2
    finally:
        ident["exit"] = code
        tmp = args.identity_out + ".tmp"
        with io.open(tmp, "w", encoding="utf-8") as fh:
            json.dump(ident, fh, indent=2, sort_keys=True)
        os.replace(tmp, args.identity_out)
    raise SystemExit(code)


def main(argv):
    ap = argparse.ArgumentParser(prog="overlay-facts.py")
    sub = ap.add_subparsers(dest="subject", required=True)
    m = sub.add_parser("main")
    for name in ("overlay_repo", "community_repo", "acta", "ast_bin", "captured_main"):
        m.add_argument(name)
    c = sub.add_parser("candidate")
    for name in ("overlay-repo", "overlay-commit", "community-repo", "community-commit",
                 "acta", "ast-bin", "identity-out"):
        c.add_argument("--" + name, required=True)
    args = ap.parse_args(argv)
    if args.subject == "main":
        run_main(args.overlay_repo, args.community_repo, args.acta, args.ast_bin, args.captured_main)
    else:
        run_candidate(args)


if __name__ == "__main__":
    main(sys.argv[1:])
