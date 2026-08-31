#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-kms-backends.sh — the public surfaces may not out-count the code on BYOK/CMEK.
#
# WHY THIS EXISTS. On 2026-08-04 the front page, its six translations, the launch blog post,
# the public trust matrix and two design documents all advertised envelope encryption across
# FOUR KMS backends. The code has THREE. The trust matrix was the worst of them because it
# named the missing one — "(AWS, GCP, Azure, local)" — and there is no local backend anywhere
# in the tree. Two design documents made the inverse error, counting the read-only KMIP
# inventory connector as an envelope backend and marking the total LIVE and verified.
#
# The claim was written once, translated into six languages, carried into the trust matrix
# that four public documents link, and FIVE documentation gates passed over it the whole time:
# lint:i18n, lint:i18n-anchors, lint:docs-parity, lint:public-counts and lint:docs-honesty.
# Not one of them reads the KMS providers from the source, because check-public-counts.sh
# derives modules, integrations, catalogues and enforcement points and nothing else. It was
# not an oversight by a reviewer; it was an absent check. PR #503 corrected the eleven files.
# This closes the class so the next writer cannot reopen it.
#
# WHAT IT DERIVES, so a number's provenance is code and not a memory:
#   providers = the Provider* string constants in core/secure/kmswrap/kmswrap.go
# and it cross-checks that the contract comment in core/secure/envelope.go lists the same
# spellings, because those two drifting apart is how "local" got invented in the first place.
#
# WHAT IT REFUSES, on every public surface that states the count:
#   1. A digit or spelled-out number that is not the derived one, in any of the seven
#      locales, next to a KMS noun — including CJK counters (三种/3 つの) and cases
#      (трём/четырём).
#   2. Naming a backend that does not exist. "local", "KMIP", "HSM" and "Vault" beside the
#      KMS count are refused BY NAME, because a wrong name survives a count correction: you
#      can fix "four" to "three" and still be lying about which three.
#   3. A hedge ("more than", "about", "至少"…). The count is exact or it is not a claim.
#
# WHAT IT DOES NOT DO, stated so nobody reads more into a green run. It does not verify that
# each backend works, and it does not police prose that mentions KMS without counting it —
# "BYOK/CMEK across your KMS" is not a claim about cardinality and is left alone.
#
# THE POLARITY IT ALSO PINS, because it was nearly published backwards. A rescued draft
# proposed saying that without a configured KEK "keys are local key files, not
# envelope-wrapped". That is false in the direction that matters: core/secure/envelope.go
# refuses — seal() with a nil KeyWrapper returns "no key wrapper configured" and encrypts
# nothing. It fails CLOSED. The local key file is a different mechanism (the Ed25519 audit
# signing key in core/secure/keys.go, which may be backed by an HSM/KMS instead). Conflating
# them would have told a reader that envelope encryption silently degrades when it in fact
# stops. So this gate refuses any public text that puts "local" and the KMS count together.
#
# THE SURFACE LIST IS SPLIT, and the split is measured. The first version of this gate held
# one list of twelve and announced "12 public surfaces agree". Three of those twelve are NOT
# public: the export curation script --manifest removes docs/launch/blog-launch-post.md and both
# design/ documents. So a gate whose whole purpose is that prose may not out-claim the code
# was itself out-claiming its own reach by three. Worse, a missing surface was skipped by a
# bare `continue`: in the published tree those three are absent, the check quietly narrowed to
# nine, and it still printed a clean line. A scope that shrinks in silence is the fail-open
# shape this repo refuses. Now:
#   PUBLISHED  — in the export manifest. Missing = exit 2, never a silent skip. If one is
#                renamed, this gate stops and says so instead of checking one fewer.
#   HUB-ONLY   — curated out by the export. Absent in the published tree BY DESIGN, so the
#                absence is announced by name. Each is declared and guarded below so
#                the export-closure gate can see the omission is deliberate.
#
# Exit 0 = every surface it could reach agrees with the derived provider set, and it says how
#          many of each kind it read.
# Exit 1 = a surface out-counts or misnames the code; the file, line and reason are printed.
# Exit 2 = the check could not look (source missing, unreadable, a PUBLISHED surface gone).
#          NOT the same as clean: "I could not measure" must never be reported as "it is
#          correct".

set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || { echo "check-kms-backends: cannot enter repo root" >&2; exit 2; }

WRAP="core/secure/kmswrap/kmswrap.go"
ENV_FILE="core/secure/envelope.go"

[ -r "$WRAP" ] || { echo "check-kms-backends: cannot read $WRAP — NOT a clean result" >&2; exit 2; }
[ -r "$ENV_FILE" ] || { echo "check-kms-backends: cannot read $ENV_FILE — NOT a clean result" >&2; exit 2; }

# The hub-only surfaces, each declared and guarded at its own call site. The guard is what
# lets the published tree name what it is not checking instead of dying on a missing file.
KMS_HUB_ONLY=()

# export-closure: hub-only docs/launch/blog-launch-post.md — it is an unpublished editorial draft with maintainer placeholders.
if [ -f "docs/launch/blog-launch-post.md" ]; then
	KMS_HUB_ONLY+=("docs/launch/blog-launch-post.md")
else
	echo "check-kms-backends: docs/launch/blog-launch-post.md is hub-only and absent here — not checked" >&2
fi

# export-closure: hub-only design/capability-map-verified-2026-06.md — it is an internal capability audit, not published product documentation.
if [ -f "design/capability-map-verified-2026-06.md" ]; then
	KMS_HUB_ONLY+=("design/capability-map-verified-2026-06.md")
else
	echo "check-kms-backends: design/capability-map-verified-2026-06.md is hub-only and absent here — not checked" >&2
fi

# export-closure: hub-only design/PRESENTATION-OVERHAUL-PLAN.md — it is an internal planning document, not published product documentation.
if [ -f "design/PRESENTATION-OVERHAUL-PLAN.md" ]; then
	KMS_HUB_ONLY+=("design/PRESENTATION-OVERHAUL-PLAN.md")
else
	echo "check-kms-backends: design/PRESENTATION-OVERHAUL-PLAN.md is hub-only and absent here — not checked" >&2
fi

export LC_ALL=C.UTF-8 2>/dev/null || export LC_ALL=C

python3 - "$WRAP" "$ENV_FILE" ${KMS_HUB_ONLY[@]+"${KMS_HUB_ONLY[@]}"} <<'PY'
import re, subprocess, sys, os

wrap, envf = sys.argv[1], sys.argv[2]
# Already existence-checked by the guarded call sites above, so anything here is readable.
HUB_ONLY = sys.argv[3:]

# ── derive: the Provider* constants are the only source of truth ────────────────────────
src = open(wrap, encoding="utf-8").read()
providers = re.findall(r'^\s*Provider(\w+)\s*=\s*"([^"]+)"', src, re.M)
if not providers:
    print(f"check-kms-backends: no Provider* constants found in {wrap} — NOT a clean result",
          file=sys.stderr)
    sys.exit(2)

names = [p[0] for p in providers]        # AWS, GCP, Azure
values = [p[1] for p in providers]       # aws-kms, gcp-kms, azure-kv
N = len(providers)

# ── cross-check: the contract comment in envelope.go must list the same spellings ───────
env = open(envf, encoding="utf-8").read()
missing = [v for v in values if v not in env]
if missing:
    print(f"FAIL {envf}: the Provider() contract does not mention {', '.join(missing)}.")
    print("     kmswrap and envelope.go disagree on the backend set. That drift is how a")
    print("     fourth backend gets invented in prose. Reconcile them before shipping.")
    sys.exit(1)

# ── the PUBLISHED surfaces that state the count ─────────────────────────────────────────
# Every one of these is in the export curation script --manifest. The hub-only ones arrive
# through argv, already guarded. Keep the two apart: merging them is what let this gate
# call three curated-out documents "public".
PUBLISHED = [
    "README.md", "README.de.md", "README.es.md", "README.fr.md",
    "README.ja.md", "README.ru.md", "README.zh.md",
    "docs/trust/feature-matrix.md",
    "docs/trust/one-pager.md",
]

# numerals 1..9 in every locale we publish, digit + word + CJK counter + Russian cases
WORDS = {
 1:[r"one",r"un[ao]?",r"eine?[nrs]?",r"один",r"одному",r"одн[ои]м",r"一"],
 2:[r"two",r"dos",r"deux",r"zwei",r"дв(?:а|ум|умя)",r"二|两"],
 3:[r"three",r"tres",r"trois",r"drei",r"тр(?:и|ём|ем|емя)",r"三"],
 4:[r"four",r"cuatro",r"quatre",r"vier",r"четыр(?:е|ём|ем|ьмя)",r"四"],
 5:[r"five",r"cinco",r"cinq",r"fünf",r"пят(?:ь|и|ью)",r"五"],
 6:[r"six",r"seis",r"sechs",r"шест(?:ь|и|ью)",r"六"],
 7:[r"seven",r"siete",r"sept",r"sieben",r"сем(?:ь|и|ью)",r"七"],
 8:[r"eight",r"ocho",r"huit",r"acht",r"восьм|восем",r"八"],
 9:[r"nine",r"nueve",r"neuf",r"neun",r"девят",r"九"],
}
# a KMS noun in any locale, with the CJK counters that sit between numeral and noun
# Both orders matter: English/German/CJK put KMS FIRST ("KMS backends", "KMS 后端"),
# while Spanish, French and Russian put the noun first ("backends KMS", "бэкендам KMS").
# Missing the second order is a real bug this gate shipped with until a Russian mutant
# passed green: "по четырём бэкендам KMS" went unseen because only the Latin noun-first
# form was covered. Cyrillic declines, so the stem is matched with a loose suffix.
NOUNWORD = r"(?:backends?|Backends?|后端|バックエンド|бэкенд\w*)"
KMSNOUN = r"(?:KMS[- ]?" + NOUNWORD + r"|" + NOUNWORD + r"\s+KMS)"
JOIN    = r"(?:[\s\-]|种|個|个|つの|項|件)*"
HEDGE   = r"(?:more than|at least|about|around|over|más de|al menos|plus de|mehr als|более|около|至少|以上|超过)"

def numeral_alternatives(n):
    alts = [str(n)] + WORDS.get(n, [])
    return "(?:" + "|".join(alts) + ")"

WRONG = [n for n in WORDS if n != N]
wrong_re = re.compile(
    r"(?<![0-9A-Za-z])(?:" + "|".join(numeral_alternatives(n) for n in WRONG) + r")"
    + JOIN + KMSNOUN, re.I)
hedge_re = re.compile(HEDGE + JOIN + r"[^.\n]{0,24}" + KMSNOUN, re.I)
# A backend named that does not exist, standing next to the count. The canonical provider
# display names are BLANKED OUT of the line first — otherwise "Azure Key Vault" trips the
# Vault pattern and the gate condemns the very names it exists to protect. Verified by
# mutation in both directions: that false positive is what the first run of this gate did.
CANON = [r"Azure\s+Key\s+Vault", r"AWS\s+KMS", r"Google\s+Cloud\s+KMS", r"GCP\s+KMS",
         r"Azure\s+Key[- ]?Vault", r"azure-kv", r"aws-kms", r"gcp-kms"]
canon_re = re.compile("|".join(CANON), re.I)
GHOST = r"(?:local|KMIP|HSM|Vault|on-?prem\w*)"
ghost_re = re.compile(
    r"(?:" + numeral_alternatives(N) + r")" + JOIN + KMSNOUN + r"[^.\n]{0,80}?" + GHOST, re.I)

fails = []


def scan(f):
    try:
        text = open(f, encoding="utf-8").read()
    except OSError as e:
        print(f"check-kms-backends: cannot read {f}: {e} — NOT a clean result", file=sys.stderr)
        sys.exit(2)
    for i, line in enumerate(text.splitlines(), 1):
        if m := wrong_re.search(line):
            fails.append((f, i, f"states {m.group(0)!r} but the code derives {N}", line.strip()[:110]))
        if m := hedge_re.search(line):
            fails.append((f, i, f"hedges the count ({m.group(0)!r}); it is exactly {N}", line.strip()[:110]))
        if m := ghost_re.search(canon_re.sub(" ", line)):
            fails.append((f, i, f"names a backend that does not exist near the count: {m.group(0)!r}",
                          line.strip()[:110]))


if not PUBLISHED:
    print("check-kms-backends: the published surface list is empty — NOT a clean result",
          file=sys.stderr)
    sys.exit(2)

# A PUBLISHED surface that is gone is not "one fewer to check": it is a question this gate
# can no longer answer. Renaming a README must break here, loudly, not shrink the check.
for f in PUBLISHED:
    if not os.path.exists(f):
        print(f"check-kms-backends: published surface {f} is missing — NOT a clean result",
              file=sys.stderr)
        print("     It is in the export manifest, so its absence is a rename or a deletion, not",
              file=sys.stderr)
        print("     curation. Update PUBLISHED here or restore the file; do not let the check",
              file=sys.stderr)
        print("     narrow itself in silence.", file=sys.stderr)
        sys.exit(2)
    scan(f)

for f in HUB_ONLY:
    scan(f)

if fails:
    print(f"KMS backend claims disagree with the code. Derived from {wrap}: "
          f"{N} ({', '.join(values)}).")
    for f, i, why, line in fails:
        print(f"  {f}:{i}: {why}")
        print(f"      {line}")
    print("")
    print("  Fix the surface, not this gate. If a backend was genuinely added, the Provider*")
    print("  constants change first and this check follows them automatically.")
    sys.exit(1)

print(f"kms-backends: OK — {len(PUBLISHED)} published and {len(HUB_ONLY)} hub-only surface(s) "
      f"agree with the {N} providers derived from {wrap} ({', '.join(values)}), and "
      f"{os.path.basename(envf)} lists the same set.")
PY
