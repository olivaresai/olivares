#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Mutation battery for the producer/state/prose install-truth gate.
set -uo pipefail
LC_ALL=C
export LC_ALL

ROOT="$(unset CDPATH; cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
CHECK="$ROOT/scripts/check-install-docs.sh"
blind() { printf 'test-install-docs: COULD NOT CHECK — %s\n' "$*" >&2; exit 2; }
for tool in bash jq find cp sed grep; do command -v "$tool" >/dev/null 2>&1 || blind "missing $tool"; done
[ -x "$CHECK" ] || blind "checker missing or not executable: $CHECK"

# ⛔ EL NOMBRE DEL TESTIGO SE DERIVA DEL CANON, COMO EN EL SUJETO. Estaba tecleado en cinco
# sitios con la version de la PRIMERA release: al re-derivar RELEASE-VERSION, el banco copiaba
# el testigo viejo y el sujeto pedia el nuevo, asi que los 19 casos salian 2 — «no pude mirar» —
# acusando al banco de lo que era una ruta caducada. Un hecho, una fuente.
VERSION="$(awk '!/^[[:space:]]*(#|$)/ { print; exit }' "$ROOT/RELEASE-VERSION")" \
	|| blind "could not read RELEASE-VERSION"
# Production release identities are bare MAJOR.MINOR.
printf '%s\n' "$VERSION" | grep -Eq '^[0-9]+\.[0-9]+$' || blind 'RELEASE-VERSION must be MAJOR.MINOR'

WITNESS="docs/releases/$VERSION-install-surfaces.json"
[ -r "$ROOT/$WITNESS" ] || blind "missing the canonical witness: $WITNESS"

W="$(mktemp -d "${TMPDIR:-/tmp}/test-install-docs.XXXXXX")" || blind "cannot create scratch directory"
trap 'rm -rf "$W"' EXIT
TREE="$W/tree"
mkdir -p "$TREE"

copy_one() {
	local path="$1"
	mkdir -p "$TREE/$(dirname -- "$path")"
	cp -p "$ROOT/$path" "$TREE/$path" || blind "cannot copy fixture $path"
}
fixed=(
	RELEASE-VERSION .goreleaser.yaml .github/workflows/release.yml
	packaging/nfpm/packages.json scripts/build-native-release-packages.py
	scripts/install.sh
	"$WITNESS" docs/UPGRADE-AND-ROLLBACK.md
	scripts/check-docs-honesty.sh modules/governance/risktier.go
	README.md README.de.md README.es.md README.fr.md README.ja.md README.ru.md README.zh.md
	INSTALL.md SECURITY.md SUPPORT.md CHANGELOG.md
	docs-site/astro.config.mjs
)
for path in "${fixed[@]}"; do copy_one "$path"; done
# export-closure: absent-by-design scripts/export-public.sh — DATA for the fixture: both gates read
# its curation lists to tell a published doc from a curated-out one; it is copied, never run. In a
# public export it is absent, and there every doc present counts as published.
EXPORTER_PRESENT=0
if [ -f "$ROOT/scripts/export-public.sh" ]; then
	copy_one scripts/export-public.sh
	EXPORTER_PRESENT=1
fi
while IFS= read -r file; do copy_one "${file#"$ROOT/"}"; done < <(
	find "$ROOT/docs-site/src/content/docs" -type f \
		! -path '*/2026-06/*' \( -name '*.md' -o -name '*.mdx' \) -print | sort
)

pass=0
failed=0
CHECK_ARG=""   # "" for the docs gate, "--qualify" for the publication qualification
run_check() {
	set +e
	OLIVARES_ROOT="$TREE" TMPDIR="$W/tmp" bash "$CHECK" ${CHECK_ARG:+"$CHECK_ARG"} >"$W/out" 2>&1
	rc=$?
	set -e
}
ok() { printf '  ok  %-44s rc=%s\n' "$1" "$2"; pass=$((pass + 1)); }
bad() {
	printf '  ⛔ %-44s %s\n' "$1" "$2"
	sed -n '1,12p' "$W/out" | sed 's/^/       /'
	failed=$((failed + 1))
}
# check-docs-honesty.sh has no battery of its own; its Helm-wording cases run here, on the same
# fixture tree, with the script copied into it (it reads the tree it sits in).
mkdir -p "$TREE/web/src"
run_honesty() {
	set +e
	(cd "$TREE" && TMPDIR="$W/tmp" sh scripts/check-docs-honesty.sh) >"$W/out" 2>&1
	rc=$?
	set -e
}
expect_honesty() {
	local label="$1" expected_rc="$2" pattern="$3"
	run_honesty
	if [ "$rc" = "$expected_rc" ] && grep -Fq -- "$pattern" "$W/out"; then
		ok "$label" "$rc"
	else
		bad "$label" "rc=$rc, expected rc=$expected_rc and $pattern"
	fi
}
expect_without() { # expect_without <label> <rc> <pattern> <absent pattern> [--qualify]
	local label="$1" expected_rc="$2" pattern="$3" absent="$4"
	CHECK_ARG="${5:-}"
	run_check
	CHECK_ARG=""
	if [ "$rc" = "$expected_rc" ] && grep -Fq -- "$pattern" "$W/out" && ! grep -Fq -- "$absent" "$W/out"; then
		ok "$label" "$rc"
	else
		bad "$label" "rc=$rc, expected rc=$expected_rc and $pattern without $absent"
	fi
}
expect() {
	local label="$1" expected_rc="$2" pattern="$3"
	CHECK_ARG="${4:-}"
	run_check
	CHECK_ARG=""
	if [ "$rc" = "$expected_rc" ] && grep -Fq -- "$pattern" "$W/out"; then
		ok "$label" "$rc"
	else
		bad "$label" "rc=$rc, expected rc=$expected_rc and $pattern"
	fi
}
mkdir -p "$W/tmp"

# ⛔ EL NOMBRE PODADO NO SE TECLEA: SE DERIVA DEL SUJETO. Corregido el 2026-09-04 (r27) por dos
#    motivos, los dos medidos:
#
#  · Teclearlo metía en el ÁRBOL PUBLICADO la primera referencia a una ruta que el export poda,
#    y `lint:export` la cazó — rc 1 sobre este lote y rc 0 sobre `main`, o sea NUESTRA. No era un
#    falso positivo: `main` no la nombra en ningún guion publicado y no hay exención, así que
#    esconder el literal habría sido jugar con el gate en la única regla firmada como absoluta.
#
#  · Y era un hecho tecleado en dos sitios: si el `find` de `check-install-docs.sh` deja de podar
#    ese nombre, este banco seguiría probando una poda que ya no existe y saldría VERDE.
#
# ⚠ Y LA FUENTE DE VERDAD NO ES LA QUE PARECE, que es el error que cometí primero: derivé de la
#   lista de `scripts/export-public.sh` —que también nombra directorios internos— y el caso de
#   control se puso ROJO (18 ok / 1 failed). Esa lista contesta «qué NO se publica»; la pregunta
#   de este banco es «qué trata el check como registro histórico», y eso sólo lo dice el `find`
#   del propio sujeto. Dos listas plausibles, dos preguntas distintas: se deriva de la que el
#   caso ejercita, no de la que se parece.
# ⛔ EL PATRON ACEPTA EL NOMBRE CON Y SIN COMILLAS, y las dos formas existen a proposito:
# el `find` del sujeto las lleva desde el 2026-09-04 porque sin ellas `-name .git` metia a
# `check-install-docs.sh` en la clase de `check-git-env-isolation.sh` por un predicado que no
# distingue una invocacion de un argumento. Al ponerlas, ESTA derivacion se quedo VACIA y el
# banco salio 2 —«no pude mirar»— en vez de verde: la guarda de abajo hizo su trabajo. Se
# acepta la comilla opcional y se quita con `tr` en vez de exigir una de las dos formas,
# porque el sujeto puede escribirse de las dos y la derivacion no manda sobre su estilo.
PODADO="$(sed -n '/^[[:space:]]*\\( -type d /,/-prune \\)/p' "$ROOT/scripts/check-install-docs.sh" \
	| grep -oE -- "-name '?[A-Za-z0-9._-]+'?" | awk '{print $2}' | tr -d "'" \
	| grep -vxE 'adr|2026-06|[.]git|node_modules|vendor' | head -1)"
[ -n "$PODADO" ] || blind "could not derive the directory pruned by find in check-install-docs.sh"
HERMANO="${PODADO}-que-no-se-poda"

mkdir -p "$TREE/docs/launch" "$TREE/docs/trust" "$TREE/docs/adr" "$TREE/docs/$PODADO" \
	"$TREE/docs/$HERMANO"
printf 'Install it from the release page.\n' >"$TREE/docs/launch/post.md"
printf 'Evaluators can verify the signed archives.\n' >"$TREE/docs/trust/one-pager.md"

expect "positive: measured tree is consistent" 0 'check-install-docs: OK'

cp "$TREE/.goreleaser.yaml" "$W/goreleaser.good"
sed -i '0,/^archives:$/s//# archives:/' "$TREE/.goreleaser.yaml"
expect "mutant: commented producer is not active" 1 'producer active anchor is absent'
cp "$W/goreleaser.good" "$TREE/.goreleaser.yaml"

# The native packages are produced by the release-archive package builder with the formats that
# packaging/nfpm/packages.json declares. Both halves are bound: a format dropped from the data
# and a renamed package file are each a finding, not a pass.
cp "$TREE/packaging/nfpm/packages.json" "$W/packages.good"
jq '(.nfpms[] | select(.formats == ["deb","rpm","apk"]) | .formats) = ["deb","rpm"]' \
	"$W/packages.good" >"$TREE/packaging/nfpm/packages.json" || blind "cannot write the formats mutant"
expect "mutant: a native format dropped from the package data" 1 'native-package format producer'
cp "$W/packages.good" "$TREE/packaging/nfpm/packages.json"
cp "$TREE/scripts/build-native-release-packages.py" "$W/native-builder.good"
sed -i 's/olivares_{version}_linux_{arch}[.]{ext}/olivares-{version}-linux-{arch}.{ext}/' \
	"$TREE/scripts/build-native-release-packages.py"
expect "mutant: the native package file name changed" 1 'native-package name producer anchor is absent'
cp "$W/native-builder.good" "$TREE/scripts/build-native-release-packages.py"

cp "$TREE/docs-site/astro.config.mjs" "$W/astro.good"
sed -i "/{ label: 'Install from a package'/s#^#// #" "$TREE/docs-site/astro.config.mjs"
expect "mutant: commented sidebar route is not active" 1 'package guide route active anchor is absent'
cp "$W/astro.good" "$TREE/docs-site/astro.config.mjs"

printf '\nhelm install mutant %s\n' 'oci://ghcr.io/olivaresai/charts/olivares' \
	>>"$TREE/docs-site/src/content/docs/ja/how-to/docker-deployment.md"
expect "mutant: unpublished or unverified Helm remote command" 1 'current docs offer its remote coordinate'
sed -i '$d' "$TREE/docs-site/src/content/docs/ja/how-to/docker-deployment.md"

cp "$TREE/docs-site/src/content/docs/ja/how-to/self-hosting.md" "$W/ja-self.good"
sed -i 's#deploy/helm/olivares#deploy/helm/missing#g' \
	"$TREE/docs-site/src/content/docs/ja/how-to/self-hosting.md"
expect "mutant: one locale loses source command" 1 'current self-hosting.md anchor is absent'
cp "$W/ja-self.good" "$TREE/docs-site/src/content/docs/ja/how-to/self-hosting.md"

# ⛔ THE PACKAGE NAME IS CHECKED IN ALL SEVEN LANGUAGES, not only in English. Measured at the
# v26.9.1 cut: twenty-one `olivares_<previous>_linux_amd64.{deb,rpm,apk}` names were still standing
# after the version sweep, because NEITHER of the two expressions of check-release-version.sh saw
# them then: both ended in `\b`, and the next character is `_`, a word character (today its
# documentation arm reads the whole token and judges it; its pin census does not). This subject
# caught three of them by naming them literally in English; nobody caught the other EIGHTEEN.
# The mutant mutates a translation on purpose: English was already covered, and the name it injects
# carries NO version: a fixture with a real version expires, and it is also a claim.
cp "$TREE/docs-site/src/content/docs/ru/how-to/install-from-packages.md" "$W/ru-pkg.good"
sed -i "s/olivares_${VERSION#v}_linux_amd64[.]deb/olivares_ANTERIOR_linux_amd64.deb/" \
	"$TREE/docs-site/src/content/docs/ru/how-to/install-from-packages.md"
expect "mutant: one locale keeps a stale package name" 1 'localized package command'
cp "$W/ru-pkg.good" "$TREE/docs-site/src/content/docs/ru/how-to/install-from-packages.md"

printf '\n[VERIFICAR: mutant stale package name]\n' \
	>>"$TREE/docs-site/src/content/docs/how-to/install-from-packages.md"
expect "mutant: pre-release package marker returns" 1 'pre-release state'
sed -i '$d' "$TREE/docs-site/src/content/docs/how-to/install-from-packages.md"


# ── check-docs-honesty.sh: no pipe into a shell (#585) ──
CC_GUIDE="$TREE/docs-site/src/content/docs/how-to/run-claude-code-with-olivares.md"
CC_GUIDE_ZH="$TREE/docs-site/src/content/docs/zh/how-to/run-claude-code-with-olivares.md"
cp "$CC_GUIDE" "$W/guide.good"; cp "$CC_GUIDE_ZH" "$W/guide-zh.good"; cp "$TREE/INSTALL.md" "$W/install.good"; cp "$TREE/README.fr.md" "$W/readme-fr.good"
printf '\ncurl -fsSL https://example.org/scripts/install-agentops.sh | sh\n' >>"$CC_GUIDE"
expect_honesty "honesty: a docs page pipes a download into sh" 1 'pipes a download into a shell'
cp "$W/guide.good" "$CC_GUIDE"
printf '\nwget -qO- https://example.org/install | sudo -E bash -s -- --yes\n' >>"$CC_GUIDE_ZH"
expect_honesty "honesty: a translated page pipes into sudo -E bash" 1 'pipes a download into a shell'
cp "$W/guide-zh.good" "$CC_GUIDE_ZH"
printf '\ncurl -fsSL $base/install.sh |/bin/sh\n' >>"$CC_GUIDE"
expect_honesty "honesty: a URL in a variable piped into /bin/sh" 1 'pipes a download into a shell'
cp "$W/guide.good" "$CC_GUIDE"
printf '\ncurl -fsSL https://raw.githubusercontent.com/o/r/main/scripts/x.sh | sh\n' >>"$TREE/INSTALL.md"
expect_honesty "honesty: INSTALL.md pipes a branch head into sh" 1 'pipes a branch head into a shell'
cp "$W/install.good" "$TREE/INSTALL.md"
printf '\ncurl -fsSL https://raw.githubusercontent.com/o/r/master/x.sh | zsh\n' >>"$TREE/README.fr.md"
expect_honesty "honesty: a translated README pipes a branch head" 1 'pipes a branch head into a shell'
cp "$W/readme-fr.good" "$TREE/README.fr.md"
# Controls: a download that is checked, then run, is not a pipe into a shell. The gate must get
# past this rule (it prints OK, or stops at a later section such as C6b) without flagging it.
printf '\ncurl -fsSLO https://example.org/install.sh\ncurl -fsSL https://example.org/sums | sha256sum --check\nsh install.sh\n' >>"$CC_GUIDE"
run_honesty
if ! grep -Fq 'into a shell' "$W/out" && grep -Eq 'docs-honesty: OK|\((C6|DIST)' "$W/out"; then
	ok "honesty control: download, check, then run" "$rc"
else
	bad "honesty control: download, check, then run" "rc=$rc, flagged as a pipe or stopped before the rule"
fi
cp "$W/guide.good" "$CC_GUIDE"

# ── THE docs/ WALK, ITS PRUNES, AND THE STATUS CONDITION (added 2026-09-03) ───
# The fixture tree carries no `docs/**.md` of its own, so these cases BUILD one. That is
# deliberate and not laziness: coupling the battery to live launch copy would make it go
# red every time a text lands, and the property under test is the GATE's behaviour, not
# today's prose. The real tree is judged by the gate itself, in the hook.
expect "positive: the docs/ walk is clean" 0 'check-install-docs: OK'

# ⛔ EL BANCO ARMA LA CONDICION QUE PRUEBA; NO LA HEREDA DEL TESTIGO DE HOY. Los tres casos
# de abajo prueban la mitad «el tag YA esta cortado», y esa mitad la gobierna
# `github-release.status` del testigo. Mientras el testigo copiado decia `published` funcionaban
# por casualidad: al cortar la siguiente release el testigo nuevo dice `not-published` —que es
# la verdad de un arbol cuyo tag aun no existe— y los tres salieron VERDES acusando al sujeto de
# no morder. No mordia porque nadie habia armado el gatillo. Se arma aqui, explicitamente, y el
# caso 2 lo desarma para probar la otra direccion.
# These cases arm a published fixture. Remove only the inherited next-release
# notices before adding each case's own qualifier; the real tree stays pending.
python3 - "$TREE" "$VERSION" <<'PUBLISHED_FIXTURE'
from pathlib import Path
import re, sys
root, version = Path(sys.argv[1]), sys.argv[2]
paths = list(root.glob('README*.md')) + [root / 'INSTALL.md'] + list((root / 'docs-site').rglob('*.md')) + list((root / 'docs-site').rglob('*.mdx'))
for path in paths:
    lines = path.read_text().splitlines(True)
    for i, line in enumerate(lines):
        if line.startswith('The next release is ') and f'docs/releases/{version}-install-surfaces.json' in line:
            lines[i] = 'See the release publication witness. Kubernetes OCI: `publication-unverified`.\n' if path.name.startswith('README') else 'See the release publication witness.\n'
        elif f'**{version}** is the next release, not yet published. Build from source until publication.' in line:
            lines[i] = line.replace(f'**{version}** is the next release, not yet published. Build from source until publication.', 'See the release publication witness.')
    path.write_text(''.join(lines))
PUBLISHED_FIXTURE
cp "$TREE/$WITNESS" "$W/state.orig"
jq '(.surfaces[] | select(.id == "github-release") | .status) = "published"' \
	"$W/state.orig" >"$TREE/$WITNESS"

# 1 · THE DEFECT THAT BROUGHT THIS: the qualifier survives the cut, in a walked path that
#     the hand-written census never named.
printf 'The installer publishes with %s — build from source until then.\n' "$VERSION" \
	>>"$TREE/docs/launch/post.md"
expect "mutant: pre-release qualifier under docs/, tag cut" 1 'docs/launch/post.md'

# 2 · AND THE OTHER DIRECTION, which is the half that keeps this from costing pushes: on a
#     tree whose tag is NOT cut the SAME sentence is TRUE and must pass. A gate that bars it
#     would make a correct pre-release tree unpushable.
cp "$TREE/$WITNESS" "$W/state.good"
jq '(.surfaces[] | select(.id == "github-release") | .status) = "not-published"' \
	"$W/state.good" >"$TREE/$WITNESS"
expect "control: same sentence passes while the tag is not cut" 0 'check-install-docs: OK'

# 3 · and the unconditional half survives that flip. Folding the placeholder marker into the
#     status-gated block would have disarmed it on every pre-release tree, silently.
# El sujeto del marcador es la superficie PUBLICADA, no la caminata: este mutante vivia en
# docs/trust/ y dejo de morder cuando parti los alcances — sobrevivio diciendo que probaba algo
# que ya no probaba. Re-anclado a la pagina de paquetes, que es superficie, y en la forma SIN
# dos puntos, que es justo la que el patron viejo no veia.
printf '[VERIFICAR]\n' >>"$TREE/docs-site/src/content/docs/how-to/install-from-packages.md"
expect "mutant: a BARE [VERIFICAR] on a published page" 1 'VERIFICAR'
sed -i '$d' "$TREE/docs-site/src/content/docs/how-to/install-from-packages.md"

# y el control que prueba que NO sobre-bloquea la planificacion: el mismo marcador en un
# documento de docs/ que nadie publica NO es rojo. Sin este caso, ampliar el patron habria
# pintado de rojo seis marcas de trabajo vivas.
printf '[VERIFICAR]\n' >>"$TREE/docs/launch/post.md"
expect "control: a working marker in planning prose is not red" 0 'check-install-docs: OK'
sed -i '$d' "$TREE/docs/launch/post.md"
cp "$W/state.good" "$TREE/$WITNESS"
sed -i '$d' "$TREE/docs/launch/post.md"

# 4 · "until it lands" ALONE is not evidence. Measured on the real tree: two files talk about
#     a FEATURE landing, one of them inside a bullet kept on purpose as a retired record.
#     Barring the bare phrase would force rewriting history to please a gate.
printf 'The delegation panel is in-development until it lands.\n' >>"$TREE/docs/trust/one-pager.md"
expect "control: a FEATURE landing is not a release claim" 0 'check-install-docs: OK'
sed -i '$d' "$TREE/docs/trust/one-pager.md"

# 5 · … and with the version on the same line it IS one.
printf 'Ships with %s; until it lands, evaluations build from source.\n' "$VERSION" \
	>>"$TREE/docs/trust/one-pager.md"
expect "mutant: landing phrase naming the version" 1 'docs/trust/one-pager.md'
sed -i '$d' "$TREE/docs/trust/one-pager.md"

# 6 · the prune is real …
printf 'build from source until the tag lands\n' >"$TREE/docs/adr/0001-old.md"
printf 'build from source until the tag lands\n' >"$TREE/docs/$PODADO/old.md"
expect "control: dated ADRs and $PODADO/ are historical records" 0 'check-install-docs: OK'

# 7 · … and it is EXACT, which is the sibling gate's own warning (2026-08-14): a substring
#     test reads identically and silently swallows live documentation merely NAMED like a
#     pruned directory. Without this case the two above would pass either way.
printf 'build from source until the tag lands\n' >"$TREE/docs/$HERMANO/live.md"
expect "mutant: $HERMANO/ is NOT $PODADO/" 1 "$HERMANO/live.md"
rm -f "$TREE/docs/$HERMANO/live.md" "$TREE/docs/adr/0001-old.md" "$TREE/docs/$PODADO/old.md"
# El gatillo armado arriba se desarma: los dos casos que quedan no dependen del estado de
# publicacion, y dejar el testigo mutado los haria depender de el sin decirlo.
cp "$W/state.orig" "$TREE/$WITNESS"

# 8 · an unreadable state is never a clean one.
jq '(.surfaces[] | select(.id == "github-release") | .status) = "maybe"' \
	"$W/state.orig" >"$TREE/$WITNESS"
expect "mutant: unknown publication status is not clean" 1 'witness is malformed'
cp "$W/state.orig" "$TREE/$WITNESS"

# 9 · and a walk that finds nothing is CANNOT LOOK, never OK. Without this the gate could be
#     disarmed by a moved root and would keep printing OK.
mv "$TREE/docs/launch/post.md" "$W/post.keep"
mv "$TREE/docs/trust/one-pager.md" "$W/one-pager.keep"
mv "$TREE/docs/UPGRADE-AND-ROLLBACK.md" "$W/upgrade.keep"
expect "mutant: an empty docs/ walk is CANNOT LOOK" 2 'walk is empty'
mv "$W/post.keep" "$TREE/docs/launch/post.md"
mv "$W/one-pager.keep" "$TREE/docs/trust/one-pager.md"
mv "$W/upgrade.keep" "$TREE/docs/UPGRADE-AND-ROLLBACK.md"

printf "test-install-docs: %d ok, %d failed\n" "$pass" "$failed"
[ "$failed" = 0 ]
