#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-guard-bash.sh — el testigo de `.claude/hooks/guard-bash.sh`.
#
# ⛔ POR QUE EXISTE. Ese hook corta comandos que en este entorno miden mal, y hasta hoy **no lo
# probaba nada**: ningun guion del arbol lee `.claude/hooks/`. Una guarda sin testigo es una
# leccion escrita e indefensa — se puede romper entera sin que nada enrojezca, y el sintoma seria
# el peor posible: **deja de cortar y nadie se entera**, porque un hook que no bloquea es
# indistinguible de un hook que no hacia falta.
#
# QUE COMPRUEBA, y las dos direcciones importan por igual:
#   · CORTA lo que debe cortar (rc 2)         — si deja de hacerlo, el control desaparece en silencio
#   · DEJA PASAR lo que no lo es (rc 0)       — un falso positivo cuesta trabajo real a cada carril
#   · el escape `# guard:ok` funciona          — o el override documentado seria mentira
#   · FALLA ABIERTO sin `jq`                   — es su postura declarada: un guardian roto que
#                                                bloquea todo convierte un despiste en sesion muerta
#
# TAMBIEN CALIFICA, con el mismo criterio, la regla `heavy-job` de guard-bash.sh (compilacion, test
# o navegador fuera del envoltorio) y `.claude/hooks/guard-edit.sh` (migracion aplicada y salida
# generada). Un solo testigo para los guardianes de `.claude/hooks`: otro guion seria un segundo
# mecanismo para la misma pregunta.
set -uo pipefail

# Aislamiento de git: este guion empareja `mktemp -d` con `git` —aunque aqui `git` solo aparece
# como DATO de prueba, una cadena que se le pasa al hook— y el gate no distingue uso de dato, con
# razon: un GIT_DIR envenenado apuntaria al repo real y el coste de equivocarse es destruir algo.
# Fallar cerrado: no poder sanear es «no he podido aislar», nunca «no hacia falta aislar».
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}

cd "$(dirname "$0")/.."
# export-closure: hub-only .claude/hooks/guard-bash.sh — el hook es del ENTORNO DE TRABAJO, no
# del producto: corta comandos que miden mal en esta caja (zsh, /tmp noexec, clon compartido) y no
# tiene sentido en el arbol publicado, que ni tiene esas condiciones ni ejecuta hooks de sesion.
# Por eso el export lo quita y este testigo lo llama GUARDADO: si no esta, sale 2 —NO HE PODIDO
# MIRAR— en vez de fingir un pase.
if [ -f ".claude/hooks/guard-bash.sh" ]; then
	H=".claude/hooks/guard-bash.sh"
else
	# The hook is hub-only (`.claude/` is TOP_BLOCK). In a stamped public export that
	# absence is the contract, not a missing control: answer SCOPED. In any other tree
	# it remains COULD NOT LOOK (exit 2).
	_cls=""
	if [ -f scripts/hub-leg.sh ]; then
		_cls="$(bash scripts/hub-leg.sh --classify --root . 2>/dev/null || true)"
	fi
	if [ "$_cls" = "public" ]; then
		echo "test-guard-bash: SCOPED — public export; .claude/hooks/guard-bash.sh is hub-only"
		echo "  session tooling and is curated out of the published tree. This witness has no"
		echo "  subject here and never will. In the full source tree it still grades the hook."
		exit 0
	fi
	echo "test-guard-bash: .claude/hooks/guard-bash.sh does not exist (hub-only); cannot test it" >&2
	exit 2
fi
command -v jq >/dev/null 2>&1 || { echo "test-guard-bash: without jq cannot construct input" >&2; exit 2; }

pass=0; fail=0
_err="$(mktemp)"
# Un caso que espera rc 2 puede llevar una MARCA: un trozo del mensaje de la regla que debe cortar.
# Sin ella, un caso pasaria igual si lo cortara OTRA regla.
caso() { # caso <nombre> <rc-esperado> <comando> [marca-en-stderr]
	local nombre="$1" want="$2" cmd="$3" marca="${4:-}" rc=0
	# export-closure: hub-only .claude/hooks/guard-bash.sh — el hook es del entorno de trabajo y el
	# export lo quita; la llamada va GUARDADA aqui mismo porque una declaracion excusa una llamada
	# guardada, no un fichero: sin la guarda, en el arbol publicado esta linea moriria con exit 127.
	if [ ! -f "$H" ]; then
		printf 'FAIL  %-58s (hook disappeared during the run)\n' "$nombre"; fail=$((fail + 1)); return
	fi
	printf '%s' "$(jq -nc --arg c "$cmd" '{tool_input:{command:$c}}')" | bash "$H" >/dev/null 2>"$_err" || rc=$?
	if [ "$rc" -eq "$want" ] && { [ -z "$marca" ] || grep -qF -- "$marca" "$_err"; }; then
		printf 'ok    %-58s rc=%s\n' "$nombre" "$rc"; pass=$((pass + 1))
	else
		printf 'FAIL  %-58s rc=%s, expected %s%s\n' "$nombre" "$rc" "$want" "${marca:+ with \"$marca\" on stderr}"; fail=$((fail + 1))
	fi
}

# --- CORTA (rc 2) · cada uno con la medida que lo puso en el hook ---
caso "for over an unquoted variable (zsh does not split words)" 2 'for x in $VAR; do echo $x; done'
caso "/dev/tcp probe (zsh does not implement it: always reports closed)"  2 '(echo > /dev/tcp/localhost/5432) 2>/dev/null'
caso "kill by pattern on a shared host"                        2 'pkill -f algo'
caso "git add without a path in a shared clone"                        2 'git add -A'
caso "comm over lists sorted with sort -n"                       2 'comm -23 <(sort -n a) <(sort -n b)'

# --- DEJA PASAR (rc 0): un falso positivo cuesta trabajo real ---
caso "for over a QUOTED variable"                         0 'for x in "$VAR"; do echo "$x"; done'
caso "while read, the correct form in zsh"                          0 'while IFS= read -r x; do echo "$x"; done < f'
caso "git add WITH an explicit path"                                    0 'git add core/algo.go'
caso "comm over lexicographically sorted lists"                      0 'comm -23 a.lex b.lex'
caso "standalone sort -n, without comm"                                      0 'sort -n input.txt'
# ⛔ LIMITACION CONOCIDA, FIJADA A PROPOSITO CON rc=2. El hook casa la PALABRA, no el sujeto:
#    la palabra prohibida DENTRO de una cadena dispara igual. Medido dos veces el 2026-08-31, las
#    dos con la palabra en un `echo` de prosa explicativa.
#    NO se «arregla» sin parsear el comando —distinguir codigo de cadena en shell exige un lexer,
#    y este hook existe para ser barato y fallar abierto—. Se FIJA aqui para que quien lo cambie
#    vea que el cambio es deliberado: si algun dia deja de cortar, esta fila enrojece y obliga a
#    decidirlo, en vez de que el control se afloje sin que nadie lo note.
#    El escape documentado (`# guard:ok`) es la salida para el caso legitimo.
caso "LIMITATION: the word in prose ALSO triggers"               2 'echo "no uses pkill aqui"'
caso "kill by PID, the approved form"                      0 'kill -TERM 1234'

# --- el escape documentado ---
caso "guard:ok escape on a blocked command"              0 'git add -A # guard:ok'

# --- FALLA ABIERTO: es su postura, y se fija para que no se invierta sin querer ---
sin_jq=0
# ⚠ Se quita SOLO `jq`, no el PATH entero: un `PATH=/nonexistent` mata tambien `cat` y devuelve
#   127 («command not found»), que NO es el fallo-abierto del hook sino que no arranco nada.
#   Medido: la primera version de esta prueba daba 127 y acusaba al hook de algo que no hacia.
_sinjq="$(mktemp -d)"; trap 'rm -rf "$_sinjq"' EXIT
for _b in cat printf grep sed awk bash; do
	_p="$(command -v "$_b" 2>/dev/null)" && ln -sf "$_p" "$_sinjq/$_b"
done
# export-closure: hub-only .claude/hooks/guard-bash.sh — mismo motivo que arriba; se repite la
# declaracion porque este sitio de llamada esta lejos del primero y una exencion vale donde alguien
# la miro, no en todo el fichero.
if [ -f "$H" ]; then
	printf '%s' '{"tool_input":{"command":"git add -A"}}' \
		| PATH="$_sinjq" bash "$H" >/dev/null 2>&1 || sin_jq=$?
fi
if [ "$sin_jq" -eq 0 ]; then
	printf 'ok    %-58s rc=0\n' "without jq FAILS OPEN (declared behavior)"; pass=$((pass + 1))
else
	printf 'FAIL  %-58s rc=%s, expected 0\n' "without jq FAILS OPEN (declared behavior)" "$sin_jq"; fail=$((fail + 1))
fi

# --- GUARDA 6: `pgrep -f` cuyo resultado se MATA en el mismo comando -----------------------------
# El patron viaja en la linea de comandos que `pgrep` inspecciona, asi que se casa a SI MISMO. No es
# un descuido: es estructural, y `-f` lo garantiza. El caso que la puso aqui es del 2026-09-01 y es
# propio — un `pgrep -f` mio caso mi shell y el `kill` de al lado le mando SIGTERM (rc 144).
caso "pgrep -f result killed in the same command"  2 'pgrep -f foo | xargs kill'
caso "and the loop form that caused the failure"         2 'for p in $(pgrep -f foo); do kill $p; done'
caso "and --full, the equivalent long option"              2 'pgrep --full foo && kill 123'
# ⚠ LOS TRES NEGATIVOS IMPORTAN TANTO COMO LOS POSITIVOS: sin ellos la guarda podria estar
#    bloqueando TODO y estas filas no lo verian.
caso "inspection does not kill: pgrep -f alone, without kill, PASSES"         0 'pgrep -f foo'
caso "kill by PID without pgrep PASSES"                         0 'kill -TERM 1234'
caso "without -f it does not apply: pgrep -l does not inspect cmdline"  0 'pgrep -l foo | wc -l'

# --- REGLA heavy-job: compilacion, test o navegador FUERA del envoltorio ------------------------
# Medida que la puso en el hook: el 2026-10-08 cinco suites Go locales llevaron el servidor a carga 98.
# Calibrada en bash (el hook corre en bash; el shell interactivo usa otro `grep`). Las filas que cortan
# llevan la MARCA `heavy-job`: sin ella pasarian igual si las cortara otra regla.
HJ='bash /workspace/.claude/skills/olivares-heavy-job/scripts/heavy-job.sh'
# CORTA (rc 2): cada forma en que un trabajo pesado llega a la linea de comandos sin envoltorio.
caso "heavy: go test of a package"                          2 'go test ./core/api' heavy-job
caso "heavy: go test with flags and -run"                   2 'go test -p 2 ./core/api -run TestX -count=1' heavy-job
caso "heavy: go build"                                      2 'go build ./cmd/olivares' heavy-job
caso "heavy: after cd && (command position after &&)"       2 'cd core && go build ./...' heavy-job
caso "heavy: after a semicolon"                             2 'echo hi; go test ./x' heavy-job
caso "heavy: inside a subshell, opening paren"              2 '(go test ./x)' heavy-job
caso "heavy: inside a subshell, after cd &&"                2 '(cd web && pnpm test)' heavy-job
caso "heavy: inside a brace group"                          2 '{ go test ./x; }' heavy-job
caso "heavy: behind a pipe"                                 2 'echo x | go test ./y' heavy-job
caso "heavy: behind then (if ...; then go test)"            2 'if true; then go test ./x; fi' heavy-job
caso "heavy: env assignment prefix"                         2 'GOFLAGS=-p=8 go test ./core/api' heavy-job
caso "heavy: quoted env assignment prefix"                  2 'GOFLAGS="-p 8" go test ./core/api' heavy-job
caso "heavy: time wrapper"                                  2 'time go test ./core/api' heavy-job
caso "heavy: nice wrapper with its option"                  2 'nice -n 10 go test ./core/api' heavy-job
caso "heavy: env wrapper"                                   2 'env FOO=1 go test ./core/api' heavy-job
caso "heavy: sudo wrapper"                                  2 'sudo go test ./core/api' heavy-job
caso "heavy: nohup wrapper"                                 2 'nohup go test ./core/api &' heavy-job
caso "heavy: xargs wrapper"                                 2 'echo ./x | xargs go test' heavy-job
caso "heavy: timeout wrapper"                               2 'timeout 600 go test ./core/api' heavy-job
caso "heavy: timeout wrapper with -k"                       2 'timeout -k 5 600 go test ./core/api' heavy-job
caso "heavy: go -C <dir> test"                              2 'go -C core test ./api' heavy-job
caso "heavy: task build:bin"                                2 'task build:bin' heavy-job
caso "heavy: task test"                                     2 'task test' heavy-job
caso "heavy: task e2e"                                      2 'task e2e' heavy-job
caso "heavy: pnpm --dir web build"                          2 'pnpm --dir web build' heavy-job
caso "heavy: pnpm -C web test"                              2 'pnpm -C web test' heavy-job
caso "heavy: pnpm --filter web test"                        2 'pnpm --filter web test' heavy-job
caso "heavy: pnpm run build"                                2 'pnpm run build' heavy-job
caso "heavy: pnpm test"                                     2 'pnpm test' heavy-job
caso "heavy: pnpm e2e"                                      2 'pnpm e2e' heavy-job
caso "heavy: bare pnpm vitest"                              2 'pnpm vitest run' heavy-job
caso "heavy: pnpm exec vitest"                              2 'pnpm --dir web exec vitest run' heavy-job
caso "heavy: pnpm exec playwright"                          2 'pnpm exec playwright test' heavy-job
caso "heavy: pnpm dlx playwright"                           2 'pnpm dlx playwright test' heavy-job
caso "heavy: npx vitest"                                    2 'npx vitest run' heavy-job
caso "heavy: npx playwright"                                2 'npx playwright test' heavy-job
caso "heavy: bare playwright test"                          2 'playwright test --workers=1' heavy-job
caso "heavy: hidden in bash -c after &&"                    2 "bash -c 'cd web && pnpm test'" heavy-job
caso "heavy: bash -c, single quotes, first word"            2 "bash -c 'go test ./x'" heavy-job
caso "heavy: sh -c, double quotes, first word"              2 'sh -c "go test ./x"' heavy-job
# DEJA PASAR (rc 0): el envoltorio, lo que no pesa, y la palabra que no es una orden.
# Los envueltos que de verdad PRUEBAN la exencion son los que llevan la orden pesada en posicion de
# comando tras `-c '`: quitar la exencion los pondria en rc 2.
caso "heavy: wrapped sh -c go test passes"                  0 "$HJ W1-x test -- sh -c 'go test ./x'"
caso "heavy: wrapped bash -c pnpm vitest passes"            0 "$HJ --remote W1-x test -- bash -c 'cd web && pnpm install --frozen-lockfile && pnpm vitest run'"
caso "heavy: wrapped go test passes"                        0 "$HJ W1-x test -- go test ./core/api"
caso "heavy: wrapped --remote multi-package passes"         0 "$HJ --remote W1-x test -- go test -p 2 ./..."
caso "heavy: wrapped git push passes"                       0 "$HJ W1-push build -- git push origin task/1-x"
caso "heavy: wrapper status passes"                         0 "$HJ --status"
caso "heavy: go vet passes"                                 0 'go vet ./core/api'
caso "heavy: gofmt passes"                                  0 'gofmt -l core/api'
caso "heavy: go list and mod tidy pass"                     0 'go list ./... && go mod tidy'
caso "heavy: git status passes"                             0 'git status --short'
caso "heavy: git diff passes"                               0 'git diff origin/main...HEAD -- go.mod'
caso "heavy: git commit passes"                             0 'git commit -m "fix(api): tidy the handler"'
caso "heavy: pnpm install passes"                           0 'pnpm install --frozen-lockfile'
caso "heavy: task lint passes"                              0 'task lint:guard-bash'
caso "heavy: task --list passes"                            0 'task --list'
caso "heavy: task testdata is not task test"                0 'task testdata'
caso "heavy: word quoted in echo passes"                    0 'echo "run go test later"'
caso "heavy: word quoted in grep passes"                    0 'grep -rn "go test" docs'
# El escape documentado tambien vale para esta regla.
caso "heavy: guard:ok escape on go test"                    0 'go test ./core/api # guard:ok'
# ⛔ LIMITACIONES CONOCIDAS, FIJADAS A PROPOSITO (misma razon que la fila de `pkill` en prosa): la regla
#    casa la palabra, no el sujeto. Si alguien la afina, estas filas enrojecen y obligan a decidirlo.
caso "LIMITATION: go test after ; inside a commit message ALSO triggers" 2 'git commit -m "x; go test ./y"' heavy-job
caso "LIMITATION: a heredoc LINE starting with go test ALSO triggers"    2 $'cat > notes.md <<EOF\nRun:\ngo test ./core/api\nEOF' heavy-job
caso "LIMITATION: npm test is not covered"                  0 'npm test'
caso "LIMITATION: task -d <dir> test is not covered"        0 'task -d core test'

# --- guard-edit.sh: migracion aplicada y salida generada -----------------------------------------
# export-closure: hub-only .claude/hooks/guard-edit.sh — hermano de guard-bash.sh: hook del
# ENTORNO DE TRABAJO que el export quita con `.claude/`; las llamadas van guardadas por -f.
if [ -f ".claude/hooks/guard-edit.sh" ]; then
	E=".claude/hooks/guard-edit.sh"
else
	E="" # ausente: cada fila de guard-edit enrojece ("hook missing") en vez de pasar en silencio
fi
_fx="$(mktemp -d)"; _fxnogit="$(mktemp -d)"; _fxnoref="$(mktemp -d)"
trap 'rm -rf "$_sinjq" "$_fx" "$_fx.lnk" "$_fxnogit" "$_fxnoref"; rm -f "$_err"' EXIT
# Fixture: un repo cuyo origin/main YA tiene migraciones aplicadas (con .down, en raiz y anidada en un
# subdirectorio), un .sql y un README que NO son migraciones, y un HEAD ADELANTADO de origin/main con una
# migracion que solo existe en la rama.
mkdir -p "$_fx/modules/x/migrations/sqlite" "$_fx/migrations" "$_fx/docs" \
	"$_fxnogit/modules/x/migrations" "$_fxnoref/modules/x/migrations"
: > "$_fx/modules/x/migrations/001_a.up.sql"; : > "$_fx/modules/x/migrations/001_a.down.sql"
: > "$_fx/modules/x/migrations/sqlite/003_s.up.sql"
: > "$_fx/migrations/002_top.sql"; : > "$_fx/migrations/README.md"; : > "$_fx/docs/query.sql"
: > "$_fxnogit/modules/x/migrations/001_a.up.sql"; : > "$_fxnoref/modules/x/migrations/001_a.up.sql"
{ git -C "$_fx" init -q \
	&& git -C "$_fx" add modules migrations docs \
	&& git -C "$_fx" -c user.name=t -c user.email=t@t -c commit.gpgsign=false commit -qm fixture \
	&& git -C "$_fx" update-ref refs/remotes/origin/main HEAD \
	&& : > "$_fx/modules/x/migrations/004_ahead.up.sql" \
	&& git -C "$_fx" add modules \
	&& git -C "$_fx" -c user.name=t -c user.email=t@t -c commit.gpgsign=false commit -qm ahead \
	&& git -C "$_fxnoref" init -q \
	&& ln -sfn "$_fx" "$_fx.lnk" \
	&& ln -sfn core/api/x.gen.go "$_fx/link.go"; } || { echo "test-guard-bash: cannot build the guard-edit fixture" >&2; exit 2; }

caso_edit() { # caso_edit <nombre> <rc-esperado> <proyecto> <ruta-del-fichero> [marca-en-stderr]
	local nombre="$1" want="$2" proj="$3" ruta="$4" marca="${5:-}" rc=0
	if [ ! -f "$E" ]; then
		printf 'FAIL  %-58s (hook missing)\n' "$nombre"; fail=$((fail + 1)); return
	fi
	printf '%s' "$(jq -nc --arg p "$ruta" '{tool_input:{file_path:$p}}')" \
		| CLAUDE_PROJECT_DIR="$proj" bash "$E" >/dev/null 2>"$_err" || rc=$?
	if [ "$rc" -eq "$want" ] && { [ -z "$marca" ] || grep -qF -- "$marca" "$_err"; }; then
		printf 'ok    %-58s rc=%s\n' "$nombre" "$rc"; pass=$((pass + 1))
	else
		printf 'FAIL  %-58s rc=%s, expected %s%s\n' "$nombre" "$rc" "$want" "${marca:+ with \"$marca\" on stderr}"; fail=$((fail + 1))
	fi
}

# settings.json ejecuta los hooks DIRECTAMENTE, no con `bash`: sin el bit de ejecucion Claude Code recibe
# «permission denied», lo trata como un error que no bloquea, y el guardian queda inerte sin que nada se
# note. El resto del testigo los llama con `bash "$H"`, asi que ESTO es lo unico que lo ve.
# Dos bloques explicitos y no un `for`: `lint:export-closure` solo reconoce una prueba de presencia si ve
# la ruta declarada en la propia prueba, y una variable de bucle la esconde.
if [ -x "$H" ]; then
	printf 'ok    %-58s\n' "guard-bash.sh is executable"; pass=$((pass + 1))
else
	printf 'FAIL  %-58s\n' "guard-bash.sh is NOT executable (settings.json runs it directly)"; fail=$((fail + 1))
fi
if [ -x "$E" ]; then
	printf 'ok    %-58s\n' "guard-edit.sh is executable"; pass=$((pass + 1))
else
	printf 'FAIL  %-58s\n' "guard-edit.sh is NOT executable (settings.json runs it directly)"; fail=$((fail + 1))
fi

# CORTA (rc 2)
caso_edit "edit: generated Go (*.gen.go)"                    2 "$_fx" "$_fx/core/api/openapi_op_descriptions.gen.go" "salida generada"
caso_edit "edit: generated TypeScript (*.gen.ts)"            2 "$_fx" "$_fx/web/src/lib/api/openapi.gen.ts" "salida generada"
caso_edit "edit: embedded console output (webui/dist)"       2 "$_fx" "$_fx/core/internal/webui/dist/index.html" "salida generada"
caso_edit "edit: applied migration (.up.sql on origin/main)" 2 "$_fx" "$_fx/modules/x/migrations/001_a.up.sql" "aplicada"
caso_edit "edit: applied migration (.down.sql)"              2 "$_fx" "$_fx/modules/x/migrations/001_a.down.sql" "aplicada"
caso_edit "edit: applied migration in a nested subdirectory" 2 "$_fx" "$_fx/modules/x/migrations/sqlite/003_s.up.sql" "aplicada"
caso_edit "edit: applied migration in a top-level migrations/" 2 "$_fx" "$_fx/migrations/002_top.sql" "aplicada"
caso_edit "edit: .. segments cannot dodge the dist rule"     2 "$_fx" "$_fx/core/../core/internal/webui/dist/a.js" "salida generada"
caso_edit "edit: a symlinked project dir cannot dodge it"    2 "$_fx.lnk" "$_fx/core/api/x.gen.go" "salida generada"
caso_edit "edit: a symlink INSIDE the path cannot dodge it"  2 "$_fx" "$_fx/link.go" "salida generada"
# DEJA PASAR (rc 0)
caso_edit "edit: a NEW migration (not on origin/main) passes" 0 "$_fx" "$_fx/modules/x/migrations/002_b.up.sql"
caso_edit "edit: a migration only on the branch (HEAD ahead of origin/main) passes" 0 "$_fx" "$_fx/modules/x/migrations/004_ahead.up.sql"
caso_edit "edit: ordinary Go source passes"                  0 "$_fx" "$_fx/core/api/server.go"
caso_edit "edit: a tracked .sql outside migrations/ passes"  0 "$_fx" "$_fx/docs/query.sql"
caso_edit "edit: a tracked non-.sql inside migrations/ passes" 0 "$_fx" "$_fx/migrations/README.md"
caso_edit "edit: a path outside the project passes"          0 "$_fx" "/tmp/elsewhere/x.gen.go"
caso_edit "edit: a sibling dir sharing the project prefix passes" 0 "$_fx" "$_fx-other/x.gen.go"
caso_edit "edit: gen-like names that are not generated pass" 0 "$_fx" "$_fx/core/api/generator.go"
caso_edit "edit: *.generated.go is not *.gen.go"             0 "$_fx" "$_fx/core/api/x.generated.go"
caso_edit "edit: a docs file under dist-like name passes"    0 "$_fx" "$_fx/docs/dist/notes.md"
# ⛔ LIMITACION CONOCIDA: las migraciones en Go crecen en sitio y una regla por ruta no las guarda.
caso_edit "LIMITATION: Go-coded migrations (schema.go) pass"  0 "$_fx" "$_fx/core/internal/store/sqlstore/schema.go"
# FALLA ABIERTO: sin origin/main, fuera de un repo, sin ruta y sin jq la edicion se deja pasar.
caso_edit "edit: no origin/main ref FAILS OPEN"              0 "$_fxnoref" "$_fxnoref/modules/x/migrations/001_a.up.sql"
caso_edit "edit: not a git repo FAILS OPEN"                  0 "$_fxnogit" "$_fxnogit/modules/x/migrations/001_a.up.sql"
caso_edit "edit: empty file_path FAILS OPEN"                 0 "$_fx" ""
# Si falta el hook, el valor inicial 1 hace que la fila enrojezca en vez de pasar en silencio.
sin_jq_edit=1
if [ -f "$E" ]; then
	sin_jq_edit=0
	printf '%s' "{\"tool_input\":{\"file_path\":\"$_fx/core/api/x.gen.go\"}}" \
		| CLAUDE_PROJECT_DIR="$_fx" PATH="$_sinjq" bash "$E" >/dev/null 2>&1 || sin_jq_edit=$?
fi
if [ "$sin_jq_edit" -eq 0 ]; then
	printf 'ok    %-58s rc=0\n' "edit: without jq FAILS OPEN (declared behavior)"; pass=$((pass + 1))
else
	printf 'FAIL  %-58s rc=%s, expected 0\n' "edit: without jq FAILS OPEN (declared behavior)" "$sin_jq_edit"; fail=$((fail + 1))
fi

# Public export: the hook is curated out with `.claude/`. Exit 2 there is a CI red
# by construction. The witness must answer SCOPED when hub-leg classifies `public`.
# GUARD_BASH_SKIP_PUBLIC_FIXTURE stops the copy from recursing into this case.
if [ "${GUARD_BASH_SKIP_PUBLIC_FIXTURE:-}" != "1" ]; then
	_pub="$(mktemp -d "${TMPDIR:-/workspace/.olivares-tmptest}/guard-bash-public.XXXXXX")"
	mkdir -p "$_pub/scripts/lib"
	cp scripts/hub-leg.sh "$_pub/scripts/hub-leg.sh"
	cp scripts/lib/git-env.sh "$_pub/scripts/lib/git-env.sh"
	cp scripts/test-guard-bash.sh "$_pub/scripts/test-guard-bash.sh"
	printf '%s\n' \
		'This repository is the public, curated export of the Olivares AI control plane.' \
		> "$_pub/PUBLIC-EXPORT.md"
	_pub_out=0
	_pub_txt="$(
		cd "$_pub" && env GUARD_BASH_SKIP_PUBLIC_FIXTURE=1 bash scripts/test-guard-bash.sh
	)" || _pub_out=$?
	if [ "$_pub_out" -eq 0 ] && grep -qF 'SCOPED' <<<"$_pub_txt"; then
		printf 'ok    %-58s rc=0 SCOPED\n' "public export without the hook is SCOPED, not exit 2"
		pass=$((pass + 1))
	else
		printf 'FAIL  %-58s rc=%s (want 0+SCOPED)\n' "public export without the hook is SCOPED, not exit 2" "$_pub_out"
		printf '%s\n' "$_pub_txt" | sed 's/^/          /' | head -8
		fail=$((fail + 1))
	fi
	rm -rf "$_pub"
fi

printf '\ntest-guard-bash: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
