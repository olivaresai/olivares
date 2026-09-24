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
blind() { printf 'test-install-docs: NO HE PODIDO MIRAR — %s\n' "$*" >&2; exit 2; }
for tool in bash jq find cp sed grep; do command -v "$tool" >/dev/null 2>&1 || blind "missing $tool"; done
[ -x "$CHECK" ] || blind "checker missing or not executable: $CHECK"

# ⛔ EL NOMBRE DEL TESTIGO SE DERIVA DEL CANON, COMO EN EL SUJETO. Estaba tecleado en cinco
# sitios con la version de la PRIMERA release: al re-derivar RELEASE-VERSION, el banco copiaba
# el testigo viejo y el sujeto pedia el nuevo, asi que los 19 casos salian 2 — «no pude mirar» —
# acusando al banco de lo que era una ruta caducada. Un hecho, una fuente.
VERSION="$(awk '!/^[[:space:]]*(#|$)/ { print; exit }' "$ROOT/RELEASE-VERSION")" \
	|| blind "no pude leer RELEASE-VERSION"
case "$VERSION" in
v[0-9]*.[0-9]*.[0-9]*) ;;
*) blind "RELEASE-VERSION no tiene un valor vYY.M.PATCH legible" ;;
esac
WITNESS="docs/releases/$VERSION-install-surfaces.json"
[ -r "$ROOT/$WITNESS" ] || blind "falta el testigo del canon: $WITNESS"

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
	.github/workflows/release-chart.yml scripts/install.sh
	"$WITNESS" deploy/helm/README.md deploy/helm/olivares/Chart.yaml
	deploy/helm/olivares/templates/NOTES.txt docs/UPGRADE-AND-ROLLBACK.md
	deploy/gitops/README.md scripts/check-docs-honesty.sh modules/governance/risktier.go
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
[ -n "$PODADO" ] || blind "no pude derivar el directorio podado del find de check-install-docs.sh"
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

# ⛔ EL NOMBRE DEL PAQUETE SE COMPRUEBA EN LOS SIETE IDIOMAS, no solo en el ingles. Medido en
# el corte de v26.9.1: veintiun nombres `olivares_<anterior>_linux_amd64.{deb,rpm,apk}` seguian
# en pie tras barrer la version, porque NINGUNA de las dos expresiones de check-release-version.sh
# los ve — las dos terminan en `\b` y el caracter siguiente es `_`, que es de palabra. Tres los
# cazaba este sujeto por nombrarlos literalmente en ingles; los otros DIECIOCHO no los cazaba
# nadie. El mutante muta una traduccion a proposito: en ingles ya estaba cubierto, y el nombre
# que inyecta NO lleva version: un fixture con una version real caduca y ademas es una afirmacion.
cp "$TREE/docs-site/src/content/docs/ru/how-to/install-from-packages.md" "$W/ru-pkg.good"
sed -i "s/olivares_${VERSION#v}_linux_amd64[.]deb/olivares_ANTERIOR_linux_amd64.deb/" \
	"$TREE/docs-site/src/content/docs/ru/how-to/install-from-packages.md"
expect "mutant: one locale keeps a stale package name" 1 'localized package command'
cp "$W/ru-pkg.good" "$TREE/docs-site/src/content/docs/ru/how-to/install-from-packages.md"

printf '\n[VERIFICAR: mutant stale package name]\n' \
	>>"$TREE/docs-site/src/content/docs/how-to/install-from-packages.md"
expect "mutant: pre-release package marker returns" 1 'pre-release state'
sed -i '$d' "$TREE/docs-site/src/content/docs/how-to/install-from-packages.md"

# ── THE HELM PUBLICATION STATE IS ARMED BY EACH CASE, NEVER INHERITED (added 2026-09-24) ──
# Three results, and each one needs its own kind of evidence. A refused or unreadable read is an
# INABILITY (publication-unverified), never an absence. Not-published needs an authoritative
# absence with its scope stated. Published needs a digest-bound record: identity, digest and the
# chart version. The cases below build each state in the fixture (witness and chart README), so
# they do not depend on what today's witness happens to say.
cp "$TREE/$WITNESS" "$W/helm.orig"
cp "$TREE/deploy/helm/README.md" "$W/helm-readme.orig"
cp -a "$TREE/docs-site" "$W/docs-site.orig"
CHART_VERSION="$(awk '/^version:/ {print $2; exit}' "$TREE/deploy/helm/olivares/Chart.yaml" | tr -d '"')"
UNVERIFIED='{"inabilities":[{"observed_at":"2026-09-23T20:27:44Z","request":"anonymous pull token for the chart repository","answer":"HTTP 403 DENIED"},{"observed_at":"2026-09-23T23:44:52Z","request":"authenticated read of the organization package inventory","answer":"HTTP 403 Resource not accessible"}]}'
ABSENT='{"absence":{"observed_at":"2026-09-23T00:00:00Z","source":"the registry package inventory, read with authority over the organization","scope":"every container package of the organization, charts/olivares included"}}'
digest_bound() { jq -cn --arg v "$1" '{publication:{identity:"ghcr.io/olivaresai/charts/olivares",digest:("sha256:"+("0"*64)),version:$v}}'; }
helm_arm() { # helm_arm <status> <extra keys as a JSON object>
	jq --arg s "$1" --argjson x "$2" \
		'(.surfaces[] | select(.id == "helm-oci")) |= ((del(.inabilities, .absence, .publication) | .status = $s) + $x)' \
		"$W/helm.orig" >"$TREE/$WITNESS"
}
helm_readme() { # the chart README states exactly one status: not-published | unverified | none
	cp "$W/helm-readme.orig" "$TREE/deploy/helm/README.md"
	sed -i -e 's/not published to the public OCI registry/STATUS-NEUTRAL/g' \
		-e 's/its publication to the public OCI registry is \*\*unverified\*\*/STATUS-NEUTRAL/g' \
		"$TREE/deploy/helm/README.md"
	case "$1" in
	not-published) printf '\nThe chart is not published to the public OCI registry.\n' >>"$TREE/deploy/helm/README.md" ;;
	unverified) printf '\nThe chart source ships here; its publication to the public OCI registry is **unverified**.\n' \
		>>"$TREE/deploy/helm/README.md" ;;
	esac
}
helm_remote_docs() { # every current self-hosting and Kubernetes page offers the remote coordinate
	while IFS= read -r f; do printf '\nhelm install olivares %s\n' 'oci://ghcr.io/olivaresai/charts/olivares' >>"$f"
	done < <(find "$TREE/docs-site/src/content/docs" -type f ! -path '*/2026-06/*' \
		\( -name self-hosting.md -o -path '*/tutorials/getting-started/kubernetes.mdx' \))
}
helm_reset() {
	cp "$W/helm.orig" "$TREE/$WITNESS"; cp "$W/helm-readme.orig" "$TREE/deploy/helm/README.md"
	rm -rf "$TREE/docs-site"; cp -a "$W/docs-site.orig" "$TREE/docs-site"
}

# (a) a refused or unreadable read: INABILITY. The docs gate still binds the prose and says so;
#     the publication qualification answers 2; every remote or absence claim is a finding.
helm_arm publication-unverified "$UNVERIFIED"; helm_readme unverified
expect "control (a): denial/unreadable inventory -> unverified; docs gate binds, says INABILITY" 0 'INABILITY'
expect "control (a): its publication qualification is INABILITY, exit 2, never qualified" 2 'publication NOT QUALIFIED' --qualify
printf '\nhelm install mutant %s\n' 'oci://ghcr.io/olivaresai/charts/olivares' \
	>>"$TREE/docs-site/src/content/docs/ja/how-to/docker-deployment.md"
expect "control (a): an unverified remote installation claim is a finding" 1 'publication is unverified, but current docs offer its remote coordinate'
helm_reset; helm_arm publication-unverified "$UNVERIFIED"; helm_readme not-published
expect "control (a): an absence claim while unverified is a finding" 1 'states an absence the witness cannot verify'
helm_readme unverified; sed -i 's#helm install olivares deploy/helm/olivares#helm install olivares deploy/helm/missing#' \
	"$TREE/deploy/helm/README.md"
expect "control (a): the source install stays documented while unverified" 1 'local Helm command anchor is absent'
helm_reset; helm_arm not-published '{}'; helm_readme not-published
expect "control (a): not-published resting on a denial alone is refused" 1 'a refused read is an inability, not absence'
helm_reset
jq '(.surfaces[] | select(.id == "github-release")) |= (.status = "publication-unverified") + '"$UNVERIFIED" \
	"$W/helm.orig" >"$TREE/$WITNESS"
expect "control (a): an unverified release surface makes the docs gate unable, exit 2" 2 'github-release publication is unverified'
helm_reset

# (b) a definitive absence from an authoritative source with its scope: not-published.
helm_arm not-published "$ABSENT"; helm_readme not-published
expect "control (b): authoritative absence with scope -> not-published" 0 'check-install-docs: OK'
expect "control (b): its publication qualification is 0" 0 'publication qualified' --qualify
helm_reset

# (c) a digest-bound publication: published, and only then may the docs offer the remote chart.
helm_arm published "$(digest_bound "$CHART_VERSION")"; helm_readme none; helm_remote_docs
expect "control (c): digest-bound publication -> published" 0 'check-install-docs: OK'
expect "control (c): its publication qualification is 0" 0 'publication qualified' --qualify
helm_reset; helm_arm published '{}'; helm_readme none; helm_remote_docs
expect "control (c): published without identity, digest and version is refused" 1 'digest-bound'
helm_reset; helm_arm published "$(digest_bound 0.0.0-not-this-chart)"; helm_readme none; helm_remote_docs
expect "control (c): a digest bound to another chart version is refused" 1 'digest-bound'
helm_reset; helm_arm published "$(digest_bound "$CHART_VERSION")"; helm_readme none
expect "mutant: Helm state flips without commands" 1 'published Helm command anchor is absent'
helm_reset

# The chart README must state the armed status; saying nothing is a finding in every branch.
helm_readme none
expect "mutant: Helm README states no publication status" 1 'Helm status anchor is absent'
helm_reset

# ── EVERY LIVE HELM SURFACE, NOT ONE FILE (added 2026-09-24) ──
# A remote chart install or upgrade is a claim that the chart can be pulled; while the witness
# cannot verify its publication the upgrade guide and the chart's own NOTES may not make it.
# A chart described as published is refused in both arms that are not `published`. And
# --qualify answers with its verdict only: no docs "OK" line inside a refused qualification.
helm_arm publication-unverified "$UNVERIFIED"
printf '\nhelm upgrade olivares %s --version 1.0.0\n' 'oci://ghcr.io/olivaresai/charts/olivares' \
	>>"$TREE/docs/UPGRADE-AND-ROLLBACK.md"
expect "unverified: a remote chart upgrade in the upgrade guide is a finding" 1 'current docs offer its remote coordinate'
helm_reset; cp "$ROOT/docs/UPGRADE-AND-ROLLBACK.md" "$TREE/docs/UPGRADE-AND-ROLLBACK.md"
helm_arm publication-unverified "$UNVERIFIED"
printf '\n   cosign verify %s:1.0.0\n' 'ghcr.io/olivaresai/charts/olivares' \
	>>"$TREE/deploy/helm/olivares/templates/NOTES.txt"
expect "unverified: a remote chart in the chart NOTES is a finding" 1 'current docs offer its remote coordinate'
cp "$ROOT/deploy/helm/olivares/templates/NOTES.txt" "$TREE/deploy/helm/olivares/templates/NOTES.txt"
helm_reset; helm_arm not-published "$ABSENT"; helm_readme not-published
printf '\nhelm upgrade olivares %s --version 1.0.0\n' 'oci://ghcr.io/olivaresai/charts/olivares' \
	>>"$TREE/docs/UPGRADE-AND-ROLLBACK.md"
expect "not-published: a remote chart upgrade in the upgrade guide is a finding" 1 'current docs offer its remote coordinate'
helm_reset; cp "$ROOT/docs/UPGRADE-AND-ROLLBACK.md" "$TREE/docs/UPGRADE-AND-ROLLBACK.md"
helm_arm publication-unverified "$UNVERIFIED"
sed -i 's/not published to the public OCI registry/published to the public OCI registry/' "$TREE/deploy/helm/README.md"
expect "mutant: Helm README overclaims publication (unverified arm)" 1 'claims the chart is published'
helm_reset; helm_arm not-published "$ABSENT"; helm_readme not-published
sed -i 's/not published to the public OCI registry/published to the public OCI registry/' "$TREE/deploy/helm/README.md"
expect "mutant: Helm README overclaims publication (not-published arm)" 1 'claims the chart is published'
helm_reset; helm_arm publication-unverified "$UNVERIFIED"
expect_without "--qualify answers with its verdict only while refusing" 2 'publication NOT QUALIFIED' 'check-install-docs: OK' --qualify
helm_reset

# ── A REMOTE CHART COMMAND ON ANY PUBLISHED DOC, NOT ONLY THE NAMED ONES (added 2026-09-24) ──
# The command, not the coordinate: INSTALL.md names the coordinate to say it is unverified, and
# the positive cases keep that green. A remote install, upgrade, pull, show or template command in
# a README, in INSTALL.md or in a published doc under docs/ is refused by both gates, also when it
# is wrapped over `\` continuations inside a code block. A curated-out doc is not published, so the
# same command there is not a claim: that control stays 0 before and after the guard.
REM='helm install olivares oci://ghcr.io/olivaresai/charts/olivares --version 1.0.0'
helm_reset; helm_arm publication-unverified "$UNVERIFIED"
cp "$TREE/README.md" "$W/readme.cmd"; cp "$TREE/INSTALL.md" "$W/install.cmd"
printf '\n%s\n' "$REM" >>"$TREE/README.md"
expect "remote command: README.md install is a finding" 1 'offers a remote chart command'
expect_honesty "remote command: README.md install (honesty)" 1 'remote chart command'
cp "$W/readme.cmd" "$TREE/README.md"
printf '\n%s\n' "$REM" >>"$TREE/INSTALL.md"
expect "remote command: INSTALL.md install is a finding" 1 'offers a remote chart command'
expect_honesty "remote command: INSTALL.md install (honesty)" 1 'remote chart command'
cp "$W/install.cmd" "$TREE/INSTALL.md"
mkdir -p "$TREE/docs/guides"
printf '```sh\nhelm upgrade olivares \\\n  oci://ghcr.io/olivaresai/charts/olivares \\\n  --version 1.0.0\n```\n' \
	>"$TREE/docs/guides/wrapped.md"
expect "remote command: wrapped in a docs/ code block" 1 'docs/guides/wrapped.md'
expect_honesty "remote command: wrapped in docs/ (honesty)" 1 'docs/guides/wrapped.md'
rm -rf "$TREE/docs/guides"
printf '\n%s\n' "$REM" >>"$TREE/docs/launch/post.md"
if [ "$EXPORTER_PRESENT" = 1 ]; then
	expect "remote command: a curated-out doc is no claim" 0 'check-install-docs: OK'
	expect_honesty "remote command: curated-out doc (honesty)" 0 'docs-honesty: OK'
else
	expect "remote command: no curation, every doc counts" 1 'offers a remote chart command'
	expect_honesty "remote command: no curation (honesty)" 1 'remote chart command'
fi
sed -i '$d' "$TREE/docs/launch/post.md"; sed -i '$d' "$TREE/docs/launch/post.md"
helm_reset

# ── THE NOTES OFFER `helm verify` ONLY WITH ITS CONDITION (added 2026-09-24) ──
# `helm verify` checks a Helm-native GPG .prov. The chart publisher signs with cosign and writes
# none, and a source install has no .tgz, so the NOTES may offer it only for the chart .tgz of an
# air-gap bundle packaged with --gpg-key, named on the line above it. The fixture restores the
# unconditioned wording, where `helm verify` reads as a plain alternative next to "no chart
# signature"; the refusal is owed in every publication state.
cp "$TREE/deploy/helm/olivares/templates/NOTES.txt" "$W/notes.good"
sed -i 's|^   # .*--gpg-key.*$|   # OR the Helm-native GPG provenance (.prov), air-gap friendly:|' \
	"$TREE/deploy/helm/olivares/templates/NOTES.txt"
expect "NOTES: helm verify without its air-gap condition" 1 'helm verify without'
cp "$W/notes.good" "$TREE/deploy/helm/olivares/templates/NOTES.txt"

# ── CHART.YAML, THE NESTED READMES, INLINE CURATION AND SYMLINKED DOCS (added 2026-09-24) ──
# 1 · Chart.yaml's full-line comments are evidence about the chart. The comment that called it
#     published, restored verbatim, is refused by both gates; a YAML value naming the OCI
#     coordinate is product data, not a claim, and stays 0.
helm_reset; helm_arm publication-unverified "$UNVERIFIED"
cp "$TREE/deploy/helm/olivares/Chart.yaml" "$W/chart.good"
printf '%s\n' '#        Docker Hub anonymous-pull rate limits. The CHART itself is still published' \
	'#        only to oci://ghcr.io/olivaresai/charts (release-chart.yml) — unchanged.' \
	>>"$TREE/deploy/helm/olivares/Chart.yaml"
expect "Chart.yaml: a comment calling the chart published" 1 'deploy/helm/olivares/Chart.yaml:'
expect_honesty "Chart.yaml: the same comment (honesty)" 1 'Chart.yaml comments'
cp "$W/chart.good" "$TREE/deploy/helm/olivares/Chart.yaml"
printf 'x-chart-source: %s\n' 'oci://ghcr.io/olivaresai/charts/olivares' >>"$TREE/deploy/helm/olivares/Chart.yaml"
expect "Chart.yaml: a coordinate as a YAML value is no claim" 0 'check-install-docs: OK'
expect_honesty "Chart.yaml: a coordinate value (honesty)" 0 'docs-honesty: OK'
cp "$W/chart.good" "$TREE/deploy/helm/olivares/Chart.yaml"

# 2 · the chart README and the gitops README are read for the remote command like the root ones.
cp "$TREE/deploy/gitops/README.md" "$W/gitops.cmd"
printf '\n%s\n' "$REM" >>"$TREE/deploy/helm/README.md"
expect "remote command: the chart README" 1 'deploy/helm/README.md:'
expect_honesty "remote command: the chart README (honesty)" 1 'deploy/helm/README.md:'
helm_reset; helm_arm publication-unverified "$UNVERIFIED"
printf '\n%s\n' "$REM" >>"$TREE/deploy/gitops/README.md"
expect "remote command: the gitops README" 1 'deploy/gitops/README.md:'
expect_honesty "remote command: the gitops README (honesty)" 1 'deploy/gitops/README.md:'
cp "$W/gitops.cmd" "$TREE/deploy/gitops/README.md"

# 3 · a curation list written inline is valid shell the export honours, but it is outside the
#     grammar the readers follow, so it is COULD NOT LOOK, never "the kept doc is curated out".
#     The exporter here is a minimal fixture, so the case holds with and without the real one.
[ "$EXPORTER_PRESENT" = 0 ] || cp -p "$TREE/scripts/export-public.sh" "$W/exporter.keep"
printf '%s\n' 'TOP_BLOCK=(' '  design' ')' 'DOCS_BLOCK=(' '  docs/launch' ')' \
	'DOCS_KEEP=(docs/launch/kept.md)' 'SCRIPTS_BLOCK=()' 'GITHUB_BLOCK=()' 'COMMERCIAL_BLOCK=()' \
	'MISC_BLOCK=()' >"$TREE/scripts/export-public.sh"
printf '%s\n' "$REM" >"$TREE/docs/launch/kept.md"
expect "curation: an inline list is COULD NOT LOOK" 2 'curation'
expect_honesty "curation: an inline list (honesty)" 2 'curation'
rm -f "$TREE/docs/launch/kept.md"
if [ "$EXPORTER_PRESENT" = 1 ]; then cp -p "$W/exporter.keep" "$TREE/scripts/export-public.sh"
else rm -f "$TREE/scripts/export-public.sh"; fi

# 4 · a symlinked doc is a doc. Dangling: COULD NOT LOOK. Resolving to a command: read and
#     refused. Resolving out of the tree: not read, COULD NOT LOOK. A linked directory, which the
#     census does not follow: COULD NOT LOOK. A link to a clean doc passes.
ln -s nowhere.md "$TREE/docs/zz-dangling.md"
expect "link: a dangling doc link" 2 'dangling'
expect_honesty "link: a dangling doc link (honesty)" 2 'dangling'
rm -f "$TREE/docs/zz-dangling.md"
printf '%s\n' "$REM" >"$TREE/zz-outside.txt"; ln -s ../zz-outside.txt "$TREE/docs/zz-link.md"
expect "link: to a doc carrying a command" 1 'docs/zz-link.md:'
expect_honesty "link: to a command (honesty)" 1 'docs/zz-link.md:'
rm -f "$TREE/docs/zz-link.md" "$TREE/zz-outside.txt"
printf '%s\n' "$REM" >"$W/outside.md"; ln -s "$W/outside.md" "$TREE/docs/zz-escape.md"
expect "link: out of the tree is not read" 2 'outside the tree'
expect_honesty "link: out of the tree (honesty)" 2 'outside the tree'
rm -f "$TREE/docs/zz-escape.md" "$W/outside.md"
mkdir -p "$TREE/zz-dir"; printf '%s\n' "$REM" >"$TREE/zz-dir/x.md"; ln -s ../zz-dir "$TREE/docs/zz-dirlink"
expect "link: a linked directory" 2 'linked directory'
expect_honesty "link: a linked directory (honesty)" 2 'linked directory'
rm -rf "$TREE/docs/zz-dirlink" "$TREE/zz-dir"
ln -s ../README.md "$TREE/docs/zz-ok.md"
expect "link: to a clean doc passes" 0 'check-install-docs: OK'
expect_honesty "link: to a clean doc (honesty)" 0 'docs-honesty: OK'
rm -f "$TREE/docs/zz-ok.md"
# The curation decision comes before any read: a curated-out link is not looked at, and a
# published link whose target is curated out is not read either (its content is private, and in
# the export the link dangles). Both need the curation lists.
if [ "$EXPORTER_PRESENT" = 1 ]; then
	ln -s nowhere.md "$TREE/docs/launch/zz-dangling.md"
	expect "link: a curated-out dangling link is not looked at" 0 'check-install-docs: OK'
	expect_honesty "link: curated-out dangling link (honesty)" 0 'docs-honesty: OK'
	rm -f "$TREE/docs/launch/zz-dangling.md"
	mkdir -p "$TREE/design"; printf 'private notes\n' >"$TREE/design/zz-private.md"
	ln -s ../design/zz-private.md "$TREE/docs/zz-private.md"
	expect "link: to curated-out content is not read" 2 'curated-out'
	expect_honesty "link: to curated-out content (honesty)" 2 'curated-out'
	rm -rf "$TREE/docs/zz-private.md" "$TREE/design"
fi
helm_reset

# ── check-docs-honesty.sh: the Helm surfaces say the publication is unverified (2026-09-24) ──
expect_honesty "honesty positive: the tree's Helm wording passes" 0 'docs-honesty: OK'
cp "$TREE/README.md" "$W/readme.good"; cp "$TREE/README.ja.md" "$W/readme-ja.good"
cp "$TREE/deploy/gitops/README.md" "$W/gitops.good"; cp "$TREE/deploy/helm/README.md" "$W/helm-honesty.good"
printf '\nthe chart is not published to an OCI registry yet.\n' >>"$TREE/README.md"
expect_honesty "honesty: a README line on the chart's OCI state without the witness result" 1 'must name the witness result'
cp "$W/readme.good" "$TREE/README.md"
printf '\nチャートはまだ OCI レジストリに公開されていません。\n' >>"$TREE/README.ja.md"
expect_honesty "honesty: the same in a translated README" 1 'must name the witness result'
cp "$W/readme-ja.good" "$TREE/README.ja.md"
printf '\nUntil a chart tag is cut the registry path is **empty**.\n' >>"$TREE/deploy/gitops/README.md"
expect_honesty "honesty: the gitops README states an empty registry (an absence)" 1 'states an absence nobody observed'
cp "$W/gitops.good" "$TREE/deploy/gitops/README.md"
sed -i 's/Publication is \*\*unverified\*\*/Publication is STATUS-NEUTRAL/' "$TREE/deploy/gitops/README.md"
expect_honesty "honesty: the gitops README must say the publication is unverified" 1 'must state that the chart'
cp "$W/gitops.good" "$TREE/deploy/gitops/README.md"
printf '\nThe chart is not published to the public OCI registry.\n' >>"$TREE/deploy/helm/README.md"
expect_honesty "honesty: an unscoped absence in the chart README" 1 'beyond its demonstrated scope'
cp "$W/helm-honesty.good" "$TREE/deploy/helm/README.md"

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

printf 'test-install-docs: %d ok, %d failed\n' "$pass" "$failed"
[ "$failed" -eq 0 ] || exit 1
