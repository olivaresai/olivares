#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Test the OTLP seed generator without an engine. The optional live branch verifies
# receiver idempotence and reports when skipped.
set -u -o pipefail

if ! command -v python3 >/dev/null 2>&1; then
	printf 'test-seed-adoption-otlp: CANNOT INSPECT: python3 is not in PATH\n' >&2
	exit 2
fi

RAIZ="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
GUION="$RAIZ/scripts/seed-adoption-otlp.py"
TRABAJO="$(mktemp -d)"
# Require a present anchor, changed output and valid Python syntax before evaluating a
# mutant.
mutate_file() { # source, destination, old text, new text; rc 0 only after construction
	python3 - "$1" "$2" "$3" "$4" <<'PYMUT' || return 1
import sys
fuente, destino, viejo, nuevo = sys.argv[1:5]
src = open(fuente).read()
if viejo not in src:
    print(f"    ⛔ the anchor is absent from {fuente}: {viejo[:70]!r}", file=sys.stderr)
    sys.exit(1)
mut = src.replace(viejo, nuevo, 1)
if mut == src:
    print("    ⛔ the replacement changed nothing", file=sys.stderr)
    sys.exit(1)
if fuente.endswith(".py"):
    compile(mut, destino, "exec")  # Reject an invalid Python mutant.
open(destino, "w").write(mut)
PYMUT
	[ -s "$2" ] || return 1
	! cmp -s "$1" "$2" || return 1
	return 0
}

trap 'rm -rf "$TRABAJO"' EXIT

ok=0
fail=0
paso() { printf 'ok   %s\n' "$1"; ok=$((ok + 1)); }
malo() { printf 'FAIL %s\n' "$1"; fail=$((fail + 1)); }

# Retain output so each expected failure must identify its cause.
OUTPUT="$TRABAJO/output.txt"
rc_de() {
	local g="$1"
	shift
	python3 "$g" "$@" >"$OUTPUT" 2>&1
	printf '%s' "$?"
}
casa() { command grep -qE "$1" "$OUTPUT"; }

# Require nonempty output without a Python traceback before counting an expected mutant
# failure.
muerte_valida() {
	if [ ! -s "$1" ]; then
		return 1
	fi
	if command grep -qE 'Traceback \(most recent call last\)' "$1"; then
		return 1
	fi
	return 0
}

# 1. Verify each flag changes its stated quantity, range or prefix.
if python3 - "$GUION" <<'PY'
import importlib.util, sys, time
s = importlib.util.spec_from_file_location("a", sys.argv[1])
m = importlib.util.module_from_spec(s)
s.loader.exec_module(m)
DIA = 86_400_000_000_000
ANCLA = m.ancla_de("2026-08-30")
fallos = []


def recursos(sobre):
    return sobre["resourceMetrics"]


def attrs(r):
    return {a["key"]: a["value"]["stringValue"] for a in r["resource"]["attributes"]}


# --equipos N produces exactly N distinct teams.
so = m.sobre_otlp(m.EQUIPOS[:2], 8, 30, "demo", ANCLA)
eq = {attrs(r)["team"] for r in recursos(so)}
if len(eq) != 2:
    fallos.append(f"--equipos 2 produced {len(eq)} teams")

# --por-equipo N produces exactly N sessions per team.
so = m.sobre_otlp(m.EQUIPOS[:3], 4, 30, "demo", ANCLA)
cuenta = {}
for r in recursos(so):
    cuenta[attrs(r)["team"]] = cuenta.get(attrs(r)["team"], 0) + 1
if set(cuenta.values()) != {4}:
    fallos.append(f"--por-equipo 4 produced {sorted(set(cuenta.values()))} per team")

# --dias N bounds timestamps to [0, N-1] days before the same generation anchor; future
# timestamps fail.
so = m.sobre_otlp(m.EQUIPOS[:6], 8, 3, "demo", ANCLA)
sellos = [int(x["sum"]["dataPoints"][0]["timeUnixNano"])
          for r in recursos(so) for x in r["scopeMetrics"][0]["metrics"]]
edades = [(ANCLA - t) // DIA for t in sellos]
if max(edades) > 2:
    fallos.append(f"--dias 3 produced a timestamp {max(edades)} days from the anchor")
if min(edades) < 0:
    fallos.append(f"--dias 3 produced a timestamp {-min(edades)} days IN THE FUTURE from the anchor")

# --prefijo P prefixes every session ID.
so = m.sobre_otlp(m.EQUIPOS[:2], 2, 30, "otro", ANCLA)
malos = [attrs(r)["session.id"] for r in recursos(so)
         if not attrs(r)["session.id"].startswith("otro-")]
if malos:
    fallos.append(f"--prefijo otro produced IDs without the prefix: {malos[:2]}")

if fallos:
    print(fallos)
    sys.exit(1)
sys.exit(0)
PY
then
	paso "all four flags do what they claim (teams, sessions per team, age, prefix)"
else
	malo "a flag does not fulfill its semantics: the inert --sembrar defect family"
fi

# 2. Put session.id and team on the resource and use delta temporality (1).
if python3 - "$GUION" <<'PY'
import importlib.util, sys
s = importlib.util.spec_from_file_location("a", sys.argv[1])
m = importlib.util.module_from_spec(s)
s.loader.exec_module(m)
ANCLA = m.ancla_de("2026-08-30")
rm = m.sobre_otlp(m.EQUIPOS[:2], 2, 7, "t", ANCLA)["resourceMetrics"][0]
claves = {a["key"] for a in rm["resource"]["attributes"]}
metricas = rm["scopeMetrics"][0]["metrics"]
fallos = []
if "session.id" not in claves:
    fallos.append("session.id is absent from RESOURCE attributes")
if "team" not in claves:
    fallos.append("missing team attribute (without it, the teams tab is unnamed)")
if any(x["sum"]["aggregationTemporality"] != 1 for x in metricas):
    fallos.append("aggregationTemporality != 1 (DELTA)")
if not any(x["name"] == "claude_code.session.count" for x in metricas):
    fallos.append("missing claude_code.session.count, which counts sessions")
if fallos:
    print(fallos)
    sys.exit(1)
sys.exit(0)
PY
then
	paso "the envelope carries session.id on the RESOURCE, team, and DELTA temporality"
else
	malo "the envelope shape differs from what the connector reads"
fi

# 3. Identical arguments must produce identical envelopes across separate constructions;
# prefix and anchor changes must alter the result.
if python3 - "$GUION" <<'PY'
import importlib.util, json, sys, time
s = importlib.util.spec_from_file_location("a", sys.argv[1])
m = importlib.util.module_from_spec(s)
s.loader.exec_module(m)
ancla = m.ancla_de("2026-08-30")
a = m.sobre_otlp(m.EQUIPOS[:3], 4, 30, "demo", ancla)
time.sleep(1.1)  # Expose current-clock timestamps.
b = m.sobre_otlp(m.EQUIPOS[:3], 4, 30, "demo", ancla)
c = m.sobre_otlp(m.EQUIPOS[:3], 4, 30, "otro", ancla)
fallos = []
if json.dumps(a, sort_keys=True) != json.dumps(b, sort_keys=True):
    fallos.append("constructions with the SAME arguments differ")
if json.dumps(a, sort_keys=True) == json.dumps(c, sort_keys=True):
    fallos.append("different prefixes produce the SAME envelope")
d = m.sobre_otlp(m.EQUIPOS[:3], 4, 30, "demo", m.ancla_de("2026-08-29"))
if json.dumps(a, sort_keys=True) == json.dumps(d, sort_keys=True):
    fallos.append("different anchors produce the same envelope: the anchor would be inert")
ids = [x["value"]["stringValue"] for r in a["resourceMetrics"]
       for x in r["resource"]["attributes"] if x["key"] == "session.id"]
if len(set(ids)) != len(ids):
    fallos.append("duplicate session.id values within the same envelope")
if fallos:
    print(fallos)
    sys.exit(1)
sys.exit(0)
PY
then
	paso "separate constructions produce the SAME envelope; a different prefix or anchor produces a different one"
else
	malo "the envelope is not reproducible: the idempotence promised in the header does not hold"
fi

# 3-bis. Reading the current clock must break deterministic envelope generation.
m0="$TRABAJO/m0.py"
if ! mutate_file "$GUION" "$m0" \
	'            ts = ancla_ns - r.randint(0, max(dias - 1, 0)) * dia' \
	'            ts = time.time_ns() - r.randint(0, max(dias - 1, 0)) * dia  # MUTANTE: reloj vivo'; then
	malo "could NOT construct mutant 0 (live clock): its anchor is absent from the subject"
elif python3 - "$m0" <<'PY2'
import importlib.util, json, sys, time
s = importlib.util.spec_from_file_location("a", sys.argv[1])
m = importlib.util.module_from_spec(s)
s.loader.exec_module(m)
ancla = m.ancla_de("2026-08-30")
a = m.sobre_otlp(m.EQUIPOS[:2], 2, 30, "demo", ancla)
time.sleep(1.1)
b = m.sobre_otlp(m.EQUIPOS[:2], 2, 30, "demo", ancla)
sys.exit(0 if json.dumps(a, sort_keys=True) == json.dumps(b, sort_keys=True) else 1)
PY2
then
	malo "the live-clock mutant SURVIVED: case 3 does not detect the defect that motivated it"
else
	paso "the mutant restoring the live clock to the generator DIES in case 3 (different envelopes)"
fi

# Require the clock mutant to fail with its reproducibility diagnostic.
muerto0="$(python3 - <<'PY2'
ports = set()
for f in ("/proc/net/tcp", "/proc/net/tcp6"):
    try:
        for ln in open(f).read().splitlines()[1:]:
            p = ln.split()
            if len(p) > 3 and p[3] == "0A":
                ports.add(int(p[1].split(":")[1], 16))
    except OSError:
        pass
print(next(p for p in range(29901, 30100) if p not in ports))
PY2
)"
r="$(rc_de "$m0" "http://127.0.0.1:$muerto0" tok ten --otlp "http://127.0.0.1:$muerto0/v1/metrics" --control-dedup)"
if [ "$r" = "1" ] && casa 'two builds of the SAME envelope differ'; then
	paso "m0 dies NAMING its cause: constructions of the same envelope differ"
elif [ "$r" = "1" ]; then
	malo "m0 died with rc 1 for another cause: does not establish the named check"
else
	malo "m0 returned rc $r against a dead port: did not reach its assertion"
fi

# The rc mutant is judged by its exit code; the missing-field mutant by the function
# exit. Each observable must identify the mutated behavior.

# 3-ter. Redact credentials and query data while retaining the diagnostic host and port.
if python3 - "$GUION" <<'PY'
import importlib.util, sys
s = importlib.util.spec_from_file_location("a", sys.argv[1])
m = importlib.util.module_from_spec(s)
s.loader.exec_module(m)
fallos = []
sucia = "https://usuario:sk-abcdefghijklmnopqrstuvwx@collector.example:4318/v1/metrics?token=zzz"
limpia = m.sanea(sucia)
for prohibido in ("sk-abcdefghijklmnopqrstuvwx", "usuario", "token=zzz"):
    if prohibido in limpia:
        fallos.append(f"the redacted URL still contains {prohibido!r}")
if "collector.example" not in limpia or "4318" not in limpia:
    fallos.append(f"the redacted URL lost the host or port and cannot help diagnose: {limpia!r}")
if fallos:
    print(fallos)
    sys.exit(1)
sys.exit(0)
PY
then
	paso "the receiver URL is printed redacted: no credential or query, with host and port"
else
	malo "the full URL appears in output: an embedded credential would reach stderr"
fi

# 3-quater. Check redaction at the output boundary for malformed URLs and reflected HTTP
# error bodies.
if python3 - "$GUION" <<'PY'
import importlib.util, io, contextlib, sys, threading, http.server, traceback
s = importlib.util.spec_from_file_location("a", sys.argv[1])
m = importlib.util.module_from_spec(s)
s.loader.exec_module(m)
SEC = "sk-abcdefghijklmnopqrstuvwx"
fugas = []
# Malformed URLs may raise within urllib.
for u in (f"://usuario:{SEC}@host/v1/metrics", SEC,
          f"https://usuario:{SEC}@/v1/metrics?token=zzz",
          f"https://usuario:{SEC}@no.invalid:4318/v1/metrics"):
    err, out, cap = io.StringIO(), io.StringIO(), ""
    try:
        with contextlib.redirect_stderr(err), contextlib.redirect_stdout(out):
            m.postear(u, {"resourceMetrics": []})
    except SystemExit:
        pass
    except Exception as e:
        cap = f"{type(e).__name__}: {e}\n" + traceback.format_exc()
    if SEC in err.getvalue() + out.getvalue() + cap:
        fugas.append(f"malformed URL {u[:28]}…")
# The HTTP 400 body reflects a credential-bearing URL.
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        self.send_response(400)
        self.end_headers()
        self.wfile.write(f"rejected endpoint https://usuario:{SEC}@localhost/v1/metrics".encode())
    def log_message(self, *a):
        pass
srv = http.server.HTTPServer(("127.0.0.1", 0), H)
threading.Thread(target=srv.serve_forever, daemon=True).start()
try:
    _, cuerpo = m.postear(f"http://127.0.0.1:{srv.server_port}/v1/metrics", {"resourceMetrics": []})
    if SEC in cuerpo:
        fugas.append("HTTPError body returned unredacted")
finally:
    srv.shutdown()
if fugas:
    print("LEAK:", fugas)
    sys.exit(1)
sys.exit(0)
PY
then
	paso "neither leak path exposes the credential (malformed URL or 400 response body)"
else
	malo "the credential appears in output: redaction does not cover every path"
fi

# 3-quinquies. Disable redaction in the shared library and require the credential tests
# to detect leakage.
mkdir -p "$TRABAJO/libnula"
if ! mutate_file "$RAIZ/scripts/lib/redaccion.py" "$TRABAJO/libnula/redaccion.py" \
	'        fuera = str(texto)' \
	'        return str(texto)  # MUTANTE: la frontera no redacta nada
        fuera = str(texto)'; then
	malo "could NOT construct the null-boundary mutant: its anchor is absent from redaccion.py"
elif ! OLIVARES_LIB_DIR="$TRABAJO/libnula" cred_arbitraria "$GUION" >/dev/null 2>&1; then
	paso "the mutant disabling the library boundary LEAKS: the credential cases detect it"
else
	malo "disabling the boundary causes no leak: the witnesses do not exercise their claims"
fi

# 3-sexies. Use an arbitrary credential that token-pattern matching cannot hide. Isolate
# the HTTP-error case from earlier calls that remember credentials.
SEC_ARB="Zq8plano-nada-especial-2026"

cred_arbitraria() { # subject script; rc 0 means no leak
	SUJETO="$1" SEC_ARB="$SEC_ARB" OLIVARES_LIB_DIR="${OLIVARES_LIB_DIR:-}" python3 - <<'PY2'
import contextlib, importlib.util, io, os, sys, traceback
SEC = os.environ["SEC_ARB"]
spec = importlib.util.spec_from_file_location("m", os.environ["SUJETO"])
m = importlib.util.module_from_spec(spec); sys.modules["m"] = m; spec.loader.exec_module(m)
fugas = []
for u in (f"://usuario:{SEC}@host/v1/metrics", SEC,
          f"https://usuario:{SEC}@/v1/metrics?token=zzz",
          f"https://usuario:{SEC}@no.invalid:4318/v1/metrics"):
    err, out, cap = io.StringIO(), io.StringIO(), ""
    try:
        with contextlib.redirect_stderr(err), contextlib.redirect_stdout(out):
            m.postear(u, {"resourceMetrics": []})
    except SystemExit:
        pass
    except Exception as e:
        cap = f"{type(e).__name__}: {e}\n" + traceback.format_exc()
    if SEC in err.getvalue() + out.getvalue() + cap:
        fugas.append("url-malformada")
print("FUGAS:" + ",".join(fugas) if fugas else "SIN-FUGAS")
sys.exit(1 if fugas else 0)
PY2
}

cuerpo_400_aislado() { # subject script; isolated process; rc 0 means redacted
	SUJETO="$1" SEC_ARB="$SEC_ARB" OLIVARES_LIB_DIR="${OLIVARES_LIB_DIR:-}" python3 - <<'PY2'
import http.server, importlib.util, os, sys, threading
SEC = os.environ["SEC_ARB"]
spec = importlib.util.spec_from_file_location("m", os.environ["SUJETO"])
m = importlib.util.module_from_spec(spec); sys.modules["m"] = m; spec.loader.exec_module(m)
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        self.send_response(400); self.end_headers()
        self.wfile.write(f"rejected endpoint https://usuario:{SEC}@localhost/v1/metrics".encode())
    def log_message(self, *a): pass
srv = http.server.HTTPServer(("127.0.0.1", 0), H)
threading.Thread(target=srv.serve_forever, daemon=True).start()
try:
    _, cuerpo = m.postear(f"http://127.0.0.1:{srv.server_port}/v1/metrics", {"resourceMetrics": []})
finally:
    srv.shutdown()
sys.exit(1 if SEC in cuerpo else 0)
PY2
}

if cred_arbitraria "$GUION" >/dev/null 2>&1; then
	paso "an ARBITRARY credential is not exposed through any malformed URL path"
else
	malo "a credential matching no regex LEAKS through a malformed URL"
fi
if cuerpo_400_aislado "$GUION" >/dev/null 2>&1; then
	paso "a 400 body with a NEVER-SEEN credential is redacted (isolated process)"
else
	malo "the 400 body leaks a credential the script had not seen"
fi

# 3-octies. Inspect exception context and cause for credentials as well as visible
# output.
contexto_limpio() { # subject script; rc 0 means no chained credential leak
	SUJETO="$1" SEC_ARB="$SEC_ARB" OLIVARES_LIB_DIR="${OLIVARES_LIB_DIR:-}" python3 - <<'PY2'
import contextlib, importlib.util, io, os, sys
SEC = os.environ["SEC_ARB"]
spec = importlib.util.spec_from_file_location("m", os.environ["SUJETO"])
m = importlib.util.module_from_spec(spec); sys.modules["m"] = m; spec.loader.exec_module(m)
malas = []
for u in (f"://usuario:{SEC}@host/v1/metrics", f"https://usuario:{SEC}@no.invalid:4318/v1/metrics"):
    try:
        with contextlib.redirect_stderr(io.StringIO()), contextlib.redirect_stdout(io.StringIO()):
            m.postear(u, {"resourceMetrics": []})
    except BaseException as e:
        # Inspect the chained exception objects.
        vistos, cur = 0, e
        while cur is not None and vistos < 12:
            if SEC in f"{cur!r}" + f"{cur}":
                malas.append(type(cur).__name__)
                break
            cur = cur.__context__ or cur.__cause__
            vistos += 1
sys.exit(1 if malas else 0)
PY2
}

if contexto_limpio "$GUION"; then
	paso "no CHAINED exception carries the secret (__context__/__cause__ are traversed, not discarded)"
else
	malo "the secret travels in a chained exception despite a clean message"
fi

# 3-nonies. Redact reflected credential-header values by header name, regardless of
# token shape.
cabecera_reflejada() { # subject script; isolated process; rc 0 means redacted
	# Forward OLIVARES_LIB_DIR explicitly to the Python subprocess so the requested library
	# is loaded.
	SUJETO="$1" SEC_ARB="$SEC_ARB" OLIVARES_LIB_DIR="${OLIVARES_LIB_DIR:-}" python3 - <<'PY2'
import contextlib, http.server, importlib.util, io, os, sys, threading
SEC = os.environ["SEC_ARB"]
spec = importlib.util.spec_from_file_location("m", os.environ["SUJETO"])
m = importlib.util.module_from_spec(spec); sys.modules["m"] = m; spec.loader.exec_module(m)


# Reflect an unknown credential so declared-secret redaction cannot satisfy this header
# test.
AJENA = "Up7-credencial-de-otro-salto-2026"


class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        self.send_response(400); self.end_headers()
        # Reflect the selected header so each removed alternation entry exercises its
        # own boundary.
        self.wfile.write((f"upstream rejected: {os.environ.get('CAB','X-Api-Key')}: " + AJENA).encode())

    def log_message(self, *a):
        pass


srv = http.server.HTTPServer(("127.0.0.1", 0), H)
threading.Thread(target=srv.serve_forever, daemon=True).start()
err, out = io.StringIO(), io.StringIO()
try:
    with contextlib.redirect_stderr(err), contextlib.redirect_stdout(out):
        try:
            # Exercise postear, which returns the HTTP error body carrying the reflected
            # credential.
            _, cuerpo = m.postear(f"http://127.0.0.1:{srv.server_port}/v1/metrics",
                                  {"resourceMetrics": []})
        except BaseException:
            cuerpo = ""
finally:
    srv.shutdown()
sys.exit(1 if AJENA in (cuerpo + err.getvalue() + out.getvalue()) else 0)
PY2
}

if cabecera_reflejada "$GUION"; then
	paso "a 400 reflecting a header with an UNKNOWN credential is redacted"
else
	malo "a reflected header exposes a credential the script does not know: that boundary is missing"
fi

mkdir -p "$TRABAJO/libmut"
if ! mutate_file "$RAIZ/scripts/lib/redaccion.py" "$TRABAJO/libmut/redaccion.py" \
	'        fuera = _RX_CABECERA.sub(' \
	'        fuera = fuera if True else _RX_CABECERA.sub('; then
	malo "could NOT construct the HEADER-boundary mutant: its anchor is absent from redaccion.py"
elif ! OLIVARES_LIB_DIR="$TRABAJO/libmut" cabecera_reflejada "$GUION"; then
	paso "the mutant removing the HEADER boundary LEAKS: case 3-nonies detects it"
else
	malo "removing the header boundary causes no leak: the case does not exercise its claim"
fi

mX="$TRABAJO/mX.py"
if ! mutate_file "$GUION" "$mX" \
	'        motivo = f"{type(e).__name__}: {e}"' \
	'        raise SystemExit(salir(RC_UNAVAILABLE, redacta(f"cannot reach the OTLP receiver at {sanea(url)}: {type(e).__name__}: {e}", url)))'; then
	malo "could NOT construct the chaining mutant: its anchor is absent from the subject"
elif ! contexto_limpio "$mX"; then
	paso "the mutant raising again INSIDE except chains the secret: case 3-octies detects it"
else
	malo "chaining again yields no secret-bearing context: the case does not exercise its claim"
fi

# 3-septies. Mutate the library paths that remember bare credentials and redact userinfo
# positions.
mkdir -p "$TRABAJO/lib-pelada" "$TRABAJO/lib-posicional"
if mutate_file "$RAIZ/scripts/lib/redaccion.py" "$TRABAJO/lib-pelada/redaccion.py" \
	'        if "://" not in url and "//" not in url and len(url) >= 8:' \
	'        if False:  # MUTANTE: la credencial pelada ya no se recuerda'; then
	if ! OLIVARES_LIB_DIR="$TRABAJO/lib-pelada" cred_arbitraria "$GUION" >/dev/null 2>&1; then
		paso "the mutant forgetting the BARE credential LEAKS: case 3-sexies detects it"
	else
		malo "forgetting the bare credential causes no leak: the case exercises nothing"
	fi
else
	malo "could NOT construct the bare-credential mutant: its anchor is absent from redaccion.py"
fi

if mutate_file "$RAIZ/scripts/lib/redaccion.py" "$TRABAJO/lib-posicional/redaccion.py" \
	'        fuera = _RX_USERINFO.sub("//<oculto>@", fuera)' \
	'        pass  # MUTANTE: el texto ajeno ya no se tapa por posicion'; then
	if ! OLIVARES_LIB_DIR="$TRABAJO/lib-posicional" cuerpo_400_aislado "$GUION" >/dev/null 2>&1; then
		paso "the mutant removing POSITIONAL redaction LEAKS through the 400 body"
	else
		malo "removing positional redaction causes no leak: the case exercises nothing"
	fi
else
	malo "could NOT construct the positional mutant: its anchor is absent from redaccion.py"
fi

# 3-decies. Removing each header-name entry must expose that header across its supported
# spellings.
for CAB_NOMBRE in authorization api-key auth-token; do
	D="$TRABAJO/lib-cab-$CAB_NOMBRE"
	mkdir -p "$D"
	# Anchor to the complete regex line so prose cannot match. Keep its literal backslash
	# and handle the final entry without a trailing alternation separator.
	LINEA_RX='    r"(?i)\b(authorization|api-key|auth-token)"'
	LINEA_MUT="$(printf '%s' "$LINEA_RX" | sed -E "s/\\|?${CAB_NOMBRE}\\|?/|/; s/\\(\\|/(/; s/\\|\\)/)/")"
	if mutate_file "$RAIZ/scripts/lib/redaccion.py" "$D/redaccion.py" \
		"$LINEA_RX" "$LINEA_MUT" ; then
		if ! CAB="$CAB_NOMBRE" OLIVARES_LIB_DIR="$D" cabecera_reflejada "$GUION" >/dev/null 2>&1; then
			paso "removing \`$CAB_NOMBRE\` from the alternation LEAKS its header: that entry is covered"
		else
			malo "removing \`$CAB_NOMBRE\` causes no leak: nobody covers that list entry"
		fi
	else
		malo "could NOT construct the \`$CAB_NOMBRE\` mutant: its name is absent from the regex"
	fi
done

# 4. An unreachable receiver must return rc 2. Read listening ports with Python rather
# than relying on awk strtonum support.
muerto="$(python3 - <<'PY'
ports = set()
for f in ("/proc/net/tcp", "/proc/net/tcp6"):
    try:
        for ln in open(f).read().splitlines()[1:]:
            p = ln.split()
            if len(p) > 3 and p[3] == "0A":
                ports.add(int(p[1].split(":")[1], 16))
    except OSError:
        pass
print(next(p for p in range(29501, 29800) if p not in ports))
PY
)"
r="$(rc_de "$GUION" "http://127.0.0.1:$muerto" tok ten --otlp "http://127.0.0.1:$muerto/v1/metrics")"
if [ "$r" = "2" ]; then
	paso "unreachable engine/receiver => rc 2 (cannot inspect), not 0 or 1"
else
	malo "unreachable receiver should exit 2, returned $r"
fi

# 5. Changing unavailable evidence from rc 2 to rc 0 must be observable.
m1="$TRABAJO/m1.py"
if ! mutate_file "$GUION" "$m1" \
	'RC_LIMPIO, RC_RECHAZADO, RC_UNAVAILABLE = 0, 1, 2' \
	'RC_LIMPIO, RC_RECHAZADO, RC_UNAVAILABLE = 0, 1, 0'; then
	malo "could NOT construct mutant 1 (rc 2 collapsed to 0): its anchor is absent from the subject"
fi
r="$(rc_de "$m1" "http://127.0.0.1:$muerto" tok ten --otlp "http://127.0.0.1:$muerto/v1/metrics")"
if [ "$r" = "0" ]; then
	paso "the mutant collapsing 'cannot inspect' to 'clean' is DETECTABLE by case 4"
else
	malo "mutant 1 did not produce the 0 detected by case 4 (returned $r)"
fi

# 6. Deduplication requires an initial increase and no second increase; dropping every
# delivery cannot satisfy both.
if python3 - "$GUION" <<'PY'
import sys, re
src = open(sys.argv[1]).read()
cuerpo = src[src.index("def control_dedup"):src.index("def main(")]
tiene_subida = "uno != por_equipo" in cuerpo
tiene_quietud = "dos != uno" in cuerpo
if not (tiene_subida and tiene_quietud):
    print("control_dedup lost one of its two halves:",
          "increases" if tiene_subida else "WITHOUT the increase assertion",
          "|", "unchanged" if tiene_quietud else "WITHOUT the unchanged assertion")
    sys.exit(1)
sys.exit(0)
PY
then
	paso 'the control retains BOTH halves (increases on the first run, unchanged on the second)'
else
	malo 'the control retains only one half: a receiver discarding everything would pass'
fi

# 8. Missing fields return rc 2; legitimate zero and empty values remain valid results.
# Suppress the expected fixture diagnostics.
if python3 - "$GUION" 2>/dev/null <<'PY'
import importlib.util, sys
s = importlib.util.spec_from_file_location("a", sys.argv[1])
m = importlib.util.module_from_spec(s)
s.loader.exec_module(m)
fallos = []


def rc_de_llamada(fn, arg):
    try:
        fn(arg)
    except SystemExit as e:
        return e.code
    return "did not exit"


# Missing fields return rc 2.
for etiqueta, d in [("without telemetry", {}),
                    ("without totals", {"telemetry": {}}),
                    ("totals without sessions", {"telemetry": {"totals": {"commits": 3}}})]:
    r = rc_de_llamada(m.sesiones_de, d)
    if r != 2:
        fallos.append(f"{etiqueta}: expected 2, got {r!r}")
r = rc_de_llamada(m.equipos_de, {})
if r != 2:
    fallos.append(f"missing teams: expected 2, got {r!r}")

# Legitimate zero values remain valid results.
try:
    v = m.sesiones_de({"telemetry": {"totals": {"sessions": 0}}})
    if v != 0:
        fallos.append(f"sessions=0 returned {v!r}")
except SystemExit as e:
    fallos.append(f"sessions=0 exited {e.code}: a legitimate zero is NOT unavailable evidence")
try:
    v = m.equipos_de({"teams": []})
    if v != []:
        fallos.append(f"teams=[] returned {v!r}")
except SystemExit as e:
    fallos.append(f"teams=[] exited {e.code}: a legitimate empty list is NOT unavailable evidence")

if fallos:
    print(fallos)
    sys.exit(1)
sys.exit(0)
PY
then
	paso "a MISSING field exits 2 and a legitimate zero remains a verdict"
else
	malo "does not distinguish missing from zero: a contract change would be read as «no data»"
fi

# 9. Require both mutant substitutions before evaluating a missing field treated as
# zero.
m2="$TRABAJO/m2.py"
if ! mutate_file "$GUION" "$TRABAJO/m2-paso1.py" \
	'    if "sessions" not in tot:' \
	'    if False:  # MUTANTE: ausente vuelve a ser cero'; then
	malo "could NOT construct mutant 2 (step 1): its anchor is absent from the subject"
elif ! mutate_file "$TRABAJO/m2-paso1.py" "$m2" \
	'    return tot["sessions"]' \
	'    return tot.get("sessions", 0)'; then
	malo "could NOT construct mutant 2 (step 2): the completing .get replacement was not applied"
else
	# Evaluate only after both substitutions produce the mutant file.
	salida_m2="$(python3 - "$m2" <<'PY' 2>&1
import importlib.util, sys
s = importlib.util.spec_from_file_location("a", sys.argv[1])
m = importlib.util.module_from_spec(s)
s.loader.exec_module(m)
try:
    m.sesiones_de({"telemetry": {"totals": {"commits": 3}}})
except SystemExit as e:
    sys.exit(0 if e.code == 2 else 1)
sys.exit(1)
PY
)"
	r=$?
	# A traceback is a crash, not evidence of the expected defect.
	if command grep -q 'Traceback' <<<"$salida_m2"; then
		malo "mutant 2 died with an EXCEPTION rather than the defect: does not establish case 8"
	elif [ "$r" = "1" ]; then
		paso 'the mutant restoring the defaulting get DIES: case 8 covers something'
	else
		malo "the defaulting-get mutant SURVIVED (rc $r)"
	fi
fi

# Diagnostic strings must not execute unescaped backticks. Use process substitution for
# the boolean grep so pipefail cannot turn an early match into a SIGPIPE failure.
if command grep -q '[^\\]`' <(command grep -nE "^[[:space:]]*(paso|malo) \"" "$0"); then
	command grep -nE "^[[:space:]]*(paso|malo) \"" "$0" | command grep '[^\\]`' >&2
	malo "messages contain UNESCAPED backticks inside double quotes: the shell executes them"
else
	paso "no test message contains an unescaped backtick inside double quotes"
fi

# 6-bis. The fake receiver exposes both routes and an unrelated first team with 99
# sessions. This verifies team scoping without an engine; only the live branch can prove
# real receiver deduplication.
cat > "$TRABAJO/doble.py" <<'PY2'
import http.server, json, os, subprocess, sys, threading

AJENO, AJENO_N = "equipo-ajeno-de-otro-carril", 99
vistas = {}          # equipo -> set de session.id


def cosecha(sobre):
    for rm in sobre.get("resourceMetrics", []) or []:
        crudo = json.dumps(rm)
        equipos, sesiones = set(), set()
        def anda(n):
            if isinstance(n, dict):
                k, v = n.get("key"), n.get("value")
                if isinstance(v, dict) and isinstance(v.get("stringValue"), str):
                    if k == "team":
                        equipos.add(v["stringValue"])
                    elif k == "session.id":
                        sesiones.add(v["stringValue"])
                for x in n.values():
                    anda(x)
            elif isinstance(n, list):
                for x in n:
                    anda(x)
        anda(rm)
        del crudo
        for e in equipos or {""}:
            vistas.setdefault(e, set()).update(sesiones)


class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        try:
            cosecha(json.loads(self.rfile.read(n).decode() or "{}"))
        except Exception:
            pass
        self.send_response(200); self.end_headers(); self.wfile.write(b"{}")

    def do_GET(self):
        # ⛔ EL AJENO VA EL PRIMERO A PROPOSITO: sin el filtro por equipo, el control se queda con
        #    esta fila y su guarda tiene que dispararse.
        eq = [{"team": AJENO, "totals": {"sessions": AJENO_N}}]
        eq += [{"team": k, "totals": {"sessions": len(v)}} for k, v in sorted(vistas.items()) if k]
        cuerpo = json.dumps({"teams": eq}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(cuerpo)))
        self.end_headers(); self.wfile.write(cuerpo)

    def log_message(self, *a):
        pass


srv = http.server.HTTPServer(("127.0.0.1", 0), H)
threading.Thread(target=srv.serve_forever, daemon=True).start()
base = f"http://127.0.0.1:{srv.server_port}"
# El SLA lo decide CADA CASO por el entorno: sin el, el control ya no puede declarar
# idempotencia (esperar mas no demuestra ausencia), y hay un caso que lo comprueba.
_sla = os.environ.get("SLA", "")
_arg = ["--sla-persistencia", _sla] if _sla else []
r = subprocess.run([sys.executable, sys.argv[1], base, "tok", "ten",
                    "--otlp", base + "/v1/metrics", "--control-dedup"] + _arg,
                   capture_output=True, text=True)
srv.shutdown()
sys.stdout.write(r.stdout); sys.stderr.write(r.stderr)
sys.exit(r.returncode)
PY2

r="$(SLA=8 python3 "$TRABAJO/doble.py" "$GUION" >"$OUTPUT" 2>&1; printf '%s' "$?")"
if [ "$r" = "0" ] && casa 'rows from THIS RUN for team .*: 0 -> 10 .* -> 10'; then
	paso "the deduplication control is HERMETIC: 0 -> 10 -> 10 in own rows, without an engine"
elif [ "$r" = "0" ]; then
	malo "exited 0 without the 0->10->10 trace: success does not show what was measured"
else
	malo "the hermetic control exited $r (see $OUTPUT): the double does not reproduce what the control uses"
fi

# 6-ter. Verify the producer still filters by team before constructing its
# filter-removal mutant.
if command grep -qF 'if (t.get("team") or "") == marca:' "$GUION"; then
	paso "the subject retains the team filter (mutant precondition checked beforehand)"
else
	malo "the script NO LONGER filters by team: the control would count the tenant aggregate again"
fi

mT="$TRABAJO/mT.py"
if ! mutate_file "$GUION" "$mT" \
	'            if (t.get("team") or "") == marca:' \
	'            if True:  # MUTANTE: se pierde el filtro por equipo'; then
	malo "could NOT construct the team-filter mutant: its anchor is absent from the subject"
fi
r="$(SLA=8 python3 "$TRABAJO/doble.py" "$mT" >"$OUTPUT" 2>&1; printf '%s' "$?")"
if [ "$r" != "0" ] && casa 'already had 99 sessions before starting'; then
	paso "the mutant removing the team filter DIES without an engine, naming its guard (rc $r)"
elif [ "$r" != "0" ]; then
	malo "the mutant died with rc $r without naming the guard: does not establish the filter"
else
	malo "the mutant removing the team filter SURVIVED without an engine: still no regression protection"
fi

# Live branch: verify idempotence only when all four engine, token, tenant and OTLP
# settings are supplied.
if [ -n "${OLIVARES_VERIFY_ENGINE:-}" ] && [ -n "${OLIVARES_VERIFY_TOKEN:-}" ] &&
	[ -n "${OLIVARES_VERIFY_TENANT:-}" ] && [ -n "${OLIVARES_VERIFY_OTLP:-}" ]; then
	# Count only the nonce-scoped team so concurrent seeding cannot affect the result.
	r="$(rc_de "$GUION" "$OLIVARES_VERIFY_ENGINE" "$OLIVARES_VERIFY_TOKEN" "$OLIVARES_VERIFY_TENANT" \
		--otlp "$OLIVARES_VERIFY_OTLP" --control-dedup)"
	if [ "$r" = "0" ] && casa 'rows from THIS RUN for team .*: 0 -> 10 .* -> 10'; then
		paso "against a live engine: 0 -> 10 -> 10 in OWN rows (idempotent and attributable)"
	elif [ "$r" = "0" ]; then
		malo "exited 0 without the own-row 0->10->10 trace: success does not show what was measured"
	else
		malo "the live-engine control exited $r"
	fi

	# The live receiver must report two executions with scoped counts 0 -> 10 -> 10. This
	# measures receiver persistence; local generator mutants do not prove it.
	if casa 'second run'; then
		paso "the live branch leaves a trace of both runs in the output"
	else
		malo "the live branch left no trace of the second run: success does not show what was measured"
	fi


else
	printf 'SKIPPED  the LIVE BRANCH was not run: export OLIVARES_VERIFY_ENGINE / _TOKEN /\n'
	printf '         _TENANT / _OTLP to exercise it. This is NOT a pass; it states EXACTLY what is\n'
	printf '         missing: idempotence against a REAL RECEIVER. The team filter no longer\n'
	printf '         depends on this branch — hermetic case 6-ter checks it on every run.\n'
fi

# 6-quater. Derive delayed deduplicating and delayed duplicating receivers. The test
# must allow healthy persistence and detect a late duplicate.
python3 - "$TRABAJO/doble.py" "$TRABAJO/doble-lento.py" "$TRABAJO/doble-dup.py" <<'PYD'
import ast, sys
src = open(sys.argv[1]).read()

VIEJO_PUB = '        for e in equipos or {""}:\n            vistas.setdefault(e, set()).update(sesiones)'
NUEVO_PUB = '        for e in equipos or {""}:\n            pendientes.append((time.monotonic() + RETRASO, e, set(sesiones)))'
VIEJO_EQ = '        eq = [{"team": AJENO, "totals": {"sessions": AJENO_N}}]'
NUEVO_EQ = ('        ahora = time.monotonic()\n'
            '        for reg in [x for x in pendientes if x[0] <= ahora]:\n'
            '            vistas.setdefault(reg[1], set()).update(reg[2]); pendientes.remove(reg)\n'
            + VIEJO_EQ)


def cambia(t, viejo, nuevo):
    # Require exactly one replacement anchor and report a mismatch.
    if t.count(viejo) != 1:
        sys.stderr.write("    ⛔ anchor %dx (expected 1): %r\n" % (t.count(viejo), viejo[:70]))
        sys.exit(1)
    return t.replace(viejo, nuevo, 1)


# Acknowledge immediately and persist later, retaining deduplication.
lento = cambia(src, 'vistas = {}          # equipo -> set de session.id',
               'vistas = {}          # equipo -> set de session.id\n'
               'import os, time\n'
               'RETRASO = float(os.environ.get("RETRASO", "3"))   # segundos hasta PERSISTIR\n'
               'pendientes = []      # (visible_en, equipo, sesiones)')
lento = cambia(lento, VIEJO_PUB, NUEVO_PUB)
lento = cambia(lento, VIEJO_EQ, NUEVO_EQ)
ast.parse(lento)
open(sys.argv[2], "w").write(lento)

# Use the same delay but count repeated sessions with multiplicity.
dup = cambia(lento, '            pendientes.append((time.monotonic() + RETRASO, e, set(sesiones)))',
             '            pendientes.append((time.monotonic() + RETRASO, e, list(sesiones)))')
dup = cambia(dup, '            vistas.setdefault(reg[1], set()).update(reg[2]); pendientes.remove(reg)',
             '            vistas[reg[1]] = vistas.get(reg[1], 0) + len(reg[2]); pendientes.remove(reg)')
dup = cambia(dup, '        eq += [{"team": k, "totals": {"sessions": len(v)}} for k, v in sorted(vistas.items()) if k]',
             '        eq += [{"team": k, "totals": {"sessions": v}} for k, v in sorted(vistas.items()) if k]')
ast.parse(dup)
open(sys.argv[3], "w").write(dup)
PYD
if [ ! -s "$TRABAJO/doble-lento.py" ] || [ ! -s "$TRABAJO/doble-dup.py" ]; then
	malo "CANNOT INSPECT: could not derive slow/duplicating doubles from the hermetic double"
else
	r="$(RETRASO=3 SLA=8 python3 "$TRABAJO/doble-lento.py" "$GUION" >"$OUTPUT" 2>&1; printf '%s' "$?")"
	if [ "$r" = "0" ] && casa 'rows from THIS RUN for team .*: 0 -> 10 .* -> 10'; then
		paso "with 3s persistence, the control reports 0 -> 10 -> 10 and rc 0: does not blame a healthy engine"
	else
		malo "the control blames a healthy but slow receiver (rc $r): the false-failure bias remains"
	fi

	r="$(RETRASO=3 SLA=8 python3 "$TRABAJO/doble-dup.py" "$GUION" >"$OUTPUT" 2>&1; printf '%s' "$?")"
	if [ "$r" = "1" ] && casa "changed this run's rows from 10 to 20"; then
		paso "against a receiver that does NOT deduplicate and takes 3s, the control DETECTS it: 10 -> 20 and rc 1"
	else
		malo "a receiver duplicating with a delay exited $r: the control accepts idempotence"
	fi

	# Exercise each waiting mutant against the receiver that exposes its defect.
	if ! mutate_file "$GUION" "$TRABAJO/mMov.py" \
		'        t = 0
        for _ in range(presupuesto):' \
		'        t = 0
        return mias(), 0  # MUTANTE: no espera a VER movimiento
        for _ in range(presupuesto):'; then
		malo "could NOT construct the movement-wait mutant: its anchor is absent from the subject"
	elif [ -s "$TRABAJO/mMov.py" ] && ! cmp -s "$GUION" "$TRABAJO/mMov.py"; then
		r="$(RETRASO=3 SLA=8 python3 "$TRABAJO/doble-lento.py" "$TRABAJO/mMov.py" >"$OUTPUT" 2>&1; printf '%s' "$?")"
		if [ "$r" = "1" ] && casa 'left 0 rows from this run'; then
			paso "without waiting for movement, the control BLAMES the healthy receiver (rc 1, 0 rows): false failure has a mutant"
		else
			malo "the mutant not waiting for movement SURVIVED (rc $r): the slow-receiver case establishes nothing"
		fi
	else
		malo "could NOT construct the movement-wait mutant: no artifact means no judgment"
	fi

	if mutate_file "$GUION" "$TRABAJO/mSuelo.py" \
		'            if v is not None and n == v and t >= minimo:' \
		'            if v is not None and n == v:  # MUTANTE: el suelo de espera ya no manda'; then
		r="$(RETRASO=3 SLA=8 python3 "$TRABAJO/doble-dup.py" "$TRABAJO/mSuelo.py" >"$OUTPUT" 2>&1; printf '%s' "$?")"
		if [ "$r" = "0" ] && casa 'second run does not'; then
			paso "without the minimum wait, the control accepts idempotence for a DUPLICATING store: false success has a mutant"
		else
			malo "the minimum-wait mutant SURVIVED (rc $r): the duplicator case does not establish the minimum wait"
		fi
	else
		malo "could NOT construct the minimum-wait mutant: no artifact means no judgment"
	fi
fi

# 6-sexies. Without a declared persistence deadline, unchanged counts cannot prove
# idempotence; require rc 2.
r="$(RETRASO=3 python3 "$TRABAJO/doble-lento.py" "$GUION" >"$OUTPUT" 2>&1; printf '%s' "$?")"
if [ "$r" = "2" ] && casa 'does NOT prove'; then
	paso "without --sla-persistencia, the control exits 2 and explains why: distinguishes «did not move» from «will not move»"
elif [ "$r" = "0" ]; then
	malo "without an SLA, the control DECLARES idempotence (rc 0): waiting longer is still treated as proof"
else
	malo "without an SLA, the control exited $r without naming the reason: failure does not explain what is missing"
fi

# A declared persistence deadline permits a bounded idempotence verdict.
r="$(RETRASO=3 SLA=8 python3 "$TRABAJO/doble-lento.py" "$GUION" >"$OUTPUT" 2>&1; printf '%s' "$?")"
if [ "$r" = "0" ] && casa 'declared persistence SLA'; then
	paso "with --sla-persistencia, the control DOES declare idempotence and names the deadline"
else
	malo "with a declared SLA, the control exited $r: the deadline is not changing the verdict"
fi

# Detect a duplicate delayed seven seconds within the declared ten-second deadline.
r="$(RETRASO=7 SLA=10 python3 "$TRABAJO/doble-dup.py" "$GUION" >"$OUTPUT" 2>&1; printf '%s' "$?")"
if [ "$r" = "1" ] && casa "changed this run's rows from 10 to 20"; then
	paso "a duplicate delayed 7s is detected when the declared SLA covers it (rc 1)"
else
	malo "the 7s duplicate exited $r: the scenario breaking the previous version remains"
fi

# Removing the deadline guard must permit the unsupported verdict.
if mutate_file "$GUION" "$TRABAJO/mSla.py" \
	'    if sla <= 0:' \
	'    if False:  # MUTANTE: vuelve a declarar idempotencia sin SLA declarado'; then
	r="$(RETRASO=3 python3 "$TRABAJO/doble-lento.py" "$TRABAJO/mSla.py" >"$OUTPUT" 2>&1; printf '%s' "$?")"
	if [ "$r" = "0" ]; then
		paso "without the SLA guard, the control returns 0 again: case 6-sexies establishes that guard"
	else
		malo "the SLA mutant SURVIVED (rc $r): case 6-sexies establishes nothing"
	fi
else
	malo "could NOT construct the SLA mutant: no artifact means no judgment"
fi

# 6-septies. A rejected second delivery must fail; unchanged counts after rejection do
# not prove idempotence.
python3 - "$TRABAJO/doble.py" "$TRABAJO/doble-rechaza2.py" <<'PYR'
import ast, sys
src = open(sys.argv[1]).read()
V = '''    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)'''
N = '''    _entregas = []

    def do_POST(self):
        # Acepta la PRIMERA y rechaza la SEGUNDA: el caso que se leia como idempotencia.
        H._entregas.append(1)
        if len(H._entregas) >= 2:
            self.send_response(400); self.end_headers(); self.wfile.write(b'{"error":"nope"}')
            return
        n = int(self.headers.get("Content-Length") or 0)'''
assert src.count(V) == 1, "do_POST anchor %dx" % src.count(V)
mut = src.replace(V, N, 1)
ast.parse(mut)
open(sys.argv[2], "w").write(mut)
PYR
if [ ! -s "$TRABAJO/doble-rechaza2.py" ]; then
	malo "could NOT derive the double rejecting the second delivery: no artifact means no judgment"
else
	r="$(SLA=8 python3 "$TRABAJO/doble-rechaza2.py" "$GUION" >"$OUTPUT" 2>&1; printf '%s' "$?")"
	if [ "$r" = "1" ] && casa 'rejected the second control delivery'; then
		paso "a receiver REJECTING the duplicate exits rc 1 and names it: rejected input cannot be certified"
	elif [ "$r" = "0" ]; then
		malo "a REJECTED duplicate is certified as idempotent (rc 0): rejection is treated as proof"
	else
		malo "the rejected duplicate exited $r without naming the cause: failure does not explain what happened"
	fi

	# Ignoring the second HTTP status must expose the false success.
	if mutate_file "$GUION" "$TRABAJO/mCod.py" \
		'    if not (200 <= cod2 < 300):' \
		'    if False:  # MUTANTE: el codigo de la 2.a entrega deja de mirarse'; then
		r="$(SLA=8 python3 "$TRABAJO/doble-rechaza2.py" "$TRABAJO/mCod.py" >"$OUTPUT" 2>&1; printf '%s' "$?")"
		if [ "$r" = "0" ]; then
			paso "without checking the second-delivery status, rejection is treated as idempotence again: the case detects it"
		else
			malo "the second-delivery status mutant SURVIVED (rc $r): the case does not establish that guard"
		fi
	else
		malo "could NOT construct the second-delivery status mutant: no artifact means no judgment"
	fi
fi

# 6-octies. Run main with a control-character token and require redacted output with rc
# 2. The library Bearer rule protects this case; remembering the token also covers
# values without that prefix.
TOKEN_CONTROL=$'tok-SECRETO-DE-BANCO-con\rcontrol'
MUERTO='http://127.0.0.1:1/'

corre_sujeto() { # source root containing scripts/; prints "<rc>|<leaks>|<bytes>"
	local output rc
	output="$(cd "$1" && timeout 60 python3 scripts/seed-adoption-otlp.py "$MUERTO" \
		"$TOKEN_CONTROL" t --otlp "${MUERTO}v1/metrics" --equipos 1 --por-equipo 1 --dias 1 2>&1)"
	rc=$?
	# Count output bytes so a diagnostic without a trailing newline is still observed.
	printf '%s|%s|%s' "$rc" "$(command grep -c 'SECRETO-DE-BANCO' <<<"$output")" \
		"$(printf '%s' "$output" | wc -c)"
}

IFS='|' read -r rc fugas bytes <<<"$(corre_sujeto "$RAIZ")"
# Reject rc 127 and empty output: neither establishes that the subject ran.
if [ "$rc" = "127" ] || [ "$bytes" -lt 1 ]; then
	malo "CANNOT INSPECT: the script did not execute (rc $rc, $bytes bytes): no run means no verdict"
elif [ "$rc" != "2" ]; then
	malo "running main with a control-character token expected rc 2, got $rc"
elif [ "$fugas" != "0" ]; then
	malo "the token appears LITERALLY when running main ($fugas times): the boundary does not cover the header ValueError"
else
	paso "running main as a program with a token containing \`\\r\`, the token is not exposed and rc is 2"
fi

# 6-octies-bis. Copy the producer and library, then remove the library Bearer rule and
# require literal token leakage.
ARBOL_MUT="$TRABAJO/arbol-bearer"
mkdir -p "$ARBOL_MUT/scripts/lib"
cp "$RAIZ/scripts/seed-adoption-otlp.py" "$ARBOL_MUT/scripts/" 2>/dev/null
cp "$RAIZ"/scripts/lib/*.py "$ARBOL_MUT/scripts/lib/" 2>/dev/null
if [ ! -s "$ARBOL_MUT/scripts/seed-adoption-otlp.py" ] || [ ! -s "$ARBOL_MUT/scripts/lib/redaccion.py" ]; then
	malo "CANNOT INSPECT: could not copy the tree for the library mutant"
elif ! mutate_file "$RAIZ/scripts/lib/redaccion.py" "$ARBOL_MUT/scripts/lib/redaccion.py" \
	'        fuera = _RX_BEARER.sub(lambda m: m.group(1) + " <oculto>", fuera)' \
	'        pass  # MUTANTE: el `Bearer` suelto deja de taparse'; then
	malo "CANNOT INSPECT: could not construct the bare \`Bearer\` mutant"
else
	IFS='|' read -r rcm fugasm bytesm <<<"$(corre_sujeto "$ARBOL_MUT")"
	if [ "$rcm" = "127" ] || [ "$bytesm" -lt 1 ]; then
		malo "CANNOT INSPECT: the mutant did not execute (rc $rcm): that does NOT mean «survived»"
	elif [ "$fugasm" -lt 1 ]; then
		malo "the mutant removing bare \`Bearer\` redaction does NOT leak the token: that is not what blocks it, and this case establishes something else"
	else
		paso "the mutant removing bare \`Bearer\` redaction DIES by leaking the literal token: this is what blocks it"
	fi
fi

# 6-nonies. An unrelated team with few sessions must not fail a complete seeded team
# set; report that team separately.
cat > "$TRABAJO/doble-ajeno.py" <<'PY9'
import http.server, json, os, subprocess, sys, threading

POR_EQUIPO = 8
MIOS = ["platform", "billing", "growth", "sre", "data", "mobile"]
AJENO = "equipo-de-otro-carril"


class H(http.server.BaseHTTPRequestHandler):
    def _j(self, o):
        c = json.dumps(o).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(c)))
        self.end_headers(); self.wfile.write(c)

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        self.rfile.read(n)
        self._j({})

    def do_GET(self):
        if "/adoption/summary" in self.path:
            self._j({"telemetry": {"totals": {"sessions": len(MIOS) * POR_EQUIPO}}})
        elif "/adoption/teams" in self.path:
            eq = [{"team": AJENO, "totals": {"sessions": 1}}]
            eq += [{"team": m, "totals": {"sessions": POR_EQUIPO}} for m in MIOS]
            self._j({"teams": eq})
        elif "/adoption/trend" in self.path:
            self._j({"days": [{"d": i} for i in range(30)]})
        else:
            self._j({})

    def log_message(self, *a):
        pass


srv = http.server.HTTPServer(("127.0.0.1", 0), H)
threading.Thread(target=srv.serve_forever, daemon=True).start()
base = "http://127.0.0.1:%d" % srv.server_port
r = subprocess.run([sys.executable, sys.argv[1], base, "tok", "ten",
                    "--otlp", base + "/v1/metrics", "--por-equipo", str(POR_EQUIPO)],
                   capture_output=True, text=True)
srv.shutdown()
sys.stdout.write(r.stdout); sys.stderr.write(r.stderr)
sys.exit(r.returncode)
PY9
if [ ! -s "$TRABAJO/doble-ajeno.py" ]; then
	malo "CANNOT INSPECT: could not write the unrelated-team double"
else
	r="$(python3 "$TRABAJO/doble-ajeno.py" "$GUION" >"$OUTPUT" 2>&1; printf '%s' "$?")"
	# 7. Check the warning emitted by the CLI, using this existing receiver run.
	LENS_WARNING='^  ⛔ FOR CAPTURES:.*`analytics`.*zero.*Select `telemetry`.*empty' # language-data: fixture
	if [ "$r" = "0" ] && casa "$LENS_WARNING"; then
		paso "the capture warning names the empty analytics lens and selecting telemetry as the remedy"
	else
		malo "the successful seed run must warn that analytics stays empty and recommend selecting telemetry"
	fi

	if [ "$r" = "0" ] && casa 'OTHER team\(s\) below'; then
		paso "an underfilled UNRELATED team does not fail the seeded-set verdict and is still NAMED in the report"
	elif [ "$r" = "1" ]; then
		malo "an underfilled unrelated row still fails the seeded set (rc 1): the verdict measures the wrong population"
	else
		malo "the unrelated-team case exited $r (see $OUTPUT): the double does not reproduce what main reads"
	fi

	# Removing the seeded-team filter must make the unrelated row fail the result.
	if mutate_file "$GUION" "$TRABAJO/mAjeno.py" \
		'              if t.get("team") in mios and sesiones_de_fila(t) < a.por_equipo]' \
		'              if sesiones_de_fila(t) < a.por_equipo]  # MUTANTE: sobre TODO el tenant'; then
		r="$(python3 "$TRABAJO/doble-ajeno.py" "$TRABAJO/mAjeno.py" >"$OUTPUT" 2>&1; printf '%s' "$?")"
		if [ "$r" = "1" ]; then
			paso "measuring the whole tenant lets the unrelated row fail the seeded set (rc 1): the case establishes it"
		else
			malo "the mutant measuring the whole tenant SURVIVED (rc $r): the case does not establish the scope"
		fi
	else
		malo "could NOT construct the population mutant: no artifact means no judgment"
	fi
fi

# Compare the seeded metric set with the engine contract.
output="$(python3 - "$GUION" "$RAIZ" <<'PY'
import importlib.util, sys
spec = importlib.util.spec_from_file_location("sd", sys.argv[1])
m = importlib.util.module_from_spec(spec); sys.modules["sd"] = m
try:
    spec.loader.exec_module(m)
except SystemExit:
    pass
motor, razon = m.contrato_del_motor(sys.argv[2])
if razon:
    print("NOPUDE", razon); raise SystemExit(0)
mias = {n for n, _ in m.METRICAS}
print("MISSING", " ".join(sorted(motor - mias)) or "-")
print("SOBRAN", " ".join(sorted(mias - motor)) or "-")
PY
)"
if command grep -q '^NOPUDE' <<<"$output"; then
	malo "could not read the engine contract: $output"
elif ! command grep -q '^MISSING -$' <<<"$output"; then
	malo "the seeded set does NOT cover the engine OTLP contract: $(command grep '^MISSING' <<<"$output")"
elif ! command grep -q '^SOBRAN -$' <<<"$output"; then
	malo "the seeded set emits metrics the engine does not recognize: $(command grep '^SOBRAN' <<<"$output")"
else
	paso "the seeded metrics are EXACTLY those in the engine OTLP contract (read, not recalled)"
fi

# Verify the required dimensions on each metric.
output="$(python3 - "$GUION" <<'PY'
import importlib.util, sys
spec = importlib.util.spec_from_file_location("sd", sys.argv[1])
m = importlib.util.module_from_spec(spec); sys.modules["sd"] = m
try:
    spec.loader.exec_module(m)
except SystemExit:
    pass
env = m.sobre_otlp(["platform"], 1, 3, "p", 1_700_000_000_000_000_000)
dims = {}
for x in env["resourceMetrics"][0]["scopeMetrics"][0]["metrics"]:
    for dp in x["sum"]["dataPoints"]:
        for a in dp.get("attributes", []):
            dims.setdefault(x["name"], set()).add(a["key"])
esperado = {
    "claude_code.lines_of_code.count": {"type"},
    "claude_code.token.usage": {"type", "model"},
    "claude_code.code_edit_tool.decision": {"tool_name", "decision"},
    "claude_code.active_time.total": {"type"},
}
for n, e in esperado.items():
    if dims.get(n) != e:
        print("MAL", n, sorted(dims.get(n, [])), "esperado", sorted(e)); raise SystemExit(0)
# Require both added and removed line categories.
caras = set()
for x in env["resourceMetrics"][0]["scopeMetrics"][0]["metrics"]:
    if x["name"] == "claude_code.lines_of_code.count":
        caras = {a["value"]["stringValue"] for dp in x["sum"]["dataPoints"]
                 for a in dp.get("attributes", []) if a["key"] == "type"}
print("OK" if caras == {"added", "removed"} else f"MAL lineas {sorted(caras)}")
PY
)"
if [ "$output" = "OK" ]; then
	paso "each metric with a breakdown carries its dimensions, and lines include added AND removed"
else
	malo "the envelope dimensions differ from what the receiver reads: $output"
fi

# Seed active time in seconds; the receiver converts it to stored milliseconds.
output="$(python3 - "$GUION" <<'PY'
import importlib.util, sys
spec = importlib.util.spec_from_file_location("sd", sys.argv[1])
m = importlib.util.module_from_spec(spec); sys.modules["sd"] = m
try:
    spec.loader.exec_module(m)
except SystemExit:
    pass
env = m.sobre_otlp(["platform"], 4, 7, "p", 1_700_000_000_000_000_000)
peor = 0
for rm in env["resourceMetrics"]:
    for x in rm["scopeMetrics"][0]["metrics"]:
        if x["name"] == "claude_code.active_time.total":
            for dp in x["sum"]["dataPoints"]:
                peor = max(peor, int(dp["asInt"]))
# Limit a session datapoint to one day: 86,400 seconds, or 86,400,000 stored
# milliseconds.
print("MAL" if peor > 86_400 else "OK", peor)
PY
)"
if [ "${output%% *}" = "OK" ]; then
	paso "active time is seeded in SECONDS (largest datapoint ${output##* }s < 1 day): receiver x1000 conversion keeps it in range"
else
	malo "active time is seeded out of scale (${output##* }): the receiver multiplies by 1000 and the capture would show years of activity"
fi

# A team row missing sessions must report unavailable evidence (rc 2), preserving the
# distinction from zero.
cat > "$TRABAJO/doble-sin-sessions.py" <<'PYS'
import http.server, json, subprocess, sys, threading

POR_EQUIPO = 8
MIOS = ["platform", "billing", "growth", "sre", "data", "mobile"]


class H(http.server.BaseHTTPRequestHandler):
    def _j(self, o):
        c = json.dumps(o).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(c)))
        self.end_headers(); self.wfile.write(c)

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        self.rfile.read(n)
        self._j({})

    def do_GET(self):
        if "/adoption/summary" in self.path:
            self._j({"telemetry": {"totals": {"sessions": len(MIOS) * POR_EQUIPO}}})
        elif "/adoption/teams" in self.path:
            # Todas completas MENOS una: `totals` existe y `sessions` NO. Eso no es cero: es que
            # la respuesta cambio de forma.
            eq = [{"team": m, "totals": {"sessions": POR_EQUIPO}} for m in MIOS[1:]]
            eq.insert(0, {"team": MIOS[0], "totals": {"lines_added": 10}})
            self._j({"teams": eq})
        elif "/adoption/trend" in self.path:
            self._j({"days": [{"d": i} for i in range(30)]})
        else:
            self._j({})

    def log_message(self, *a):
        pass


srv = http.server.HTTPServer(("127.0.0.1", 0), H)
threading.Thread(target=srv.serve_forever, daemon=True).start()
base = "http://127.0.0.1:%d" % srv.server_port
r = subprocess.run([sys.executable, sys.argv[1], base, "tok", "ten",
                    "--otlp", base + "/v1/metrics", "--por-equipo", str(POR_EQUIPO)],
                   capture_output=True, text=True)
srv.shutdown()
sys.stdout.write(r.stdout); sys.stderr.write(r.stderr)
sys.exit(r.returncode)
PYS
if [ ! -s "$TRABAJO/doble-sin-sessions.py" ]; then
	malo "CANNOT INSPECT: could not write the missing-sessions row double"
else
	r="$(python3 "$TRABAJO/doble-sin-sessions.py" "$GUION" >"$OUTPUT" 2>&1; printf '%s' "$?")"
	if [ "$r" = "2" ] && casa 'missing does not mean zero'; then
		paso "a row with \`totals\` and WITHOUT \`sessions\` exits rc 2 saying «missing is not zero», rather than rc 1 blaming the seeded set"
	elif [ "$r" = "1" ]; then
		malo "the row without \`sessions\` exits rc 1: an engine contract change is treated as insufficient seeding"
	else
		malo "the row without \`sessions\` returns rc $r without naming the cause: $(head -2 "$OUTPUT" | tr '\n' ' ')"
	fi

	# Anchor to the complete row-specific block so the mutant changes sesiones_de_fila
	# alone. Restore the missing-as-zero behavior without introducing a KeyError.
	python3 - "$GUION" "$TRABAJO/mFila.py" <<'PYM'
import sys
src = open(sys.argv[1]).read()
viejo = (
    '    if "sessions" not in tot:\n'
    '        raise SystemExit(salir(RC_UNAVAILABLE,\n'
    '                               f"{ruta}: team row {t.get(\'team\')!r} contains `totals` WITHOUT "\n'
    '                               "`sessions`: missing does not mean zero"))\n'
    '    return tot["sessions"]\n'
)
assert src.count(viejo) == 1, f"row anchor: {src.count(viejo)} matches"
nuevo = '    return tot.get("sessions") or 0  # MUTANTE: ausente vuelve a caer a cero\n'
open(sys.argv[2], "w").write(src.replace(viejo, nuevo, 1))
PYM
	if [ ! -s "$TRABAJO/mFila.py" ]; then
		malo "CANNOT INSPECT: could not construct the row's \`or 0\` mutant"
	else
		rm2="$(python3 "$TRABAJO/doble-sin-sessions.py" "$TRABAJO/mFila.py" >"$OUTPUT" 2>&1; printf '%s' "$?")"
		if [ "$rm2" = "127" ]; then
			malo "CANNOT INSPECT: the row mutant did not execute (rc 127): that does NOT mean «survived»"
		elif ! muerte_valida "$OUTPUT"; then
			malo "CANNOT INSPECT: the row mutant CRASHED (Traceback) instead of dying: a crashing mutant establishes nothing"
		elif casa 'missing does not mean zero'; then
			malo "the mutant restoring \`or 0\` STILL says «missing is not zero» (rc $rm2): establishes nothing"
		else
			paso "the mutant restoring \`or 0\` DIES: stops distinguishing missing from zero and this case detects it"
		fi
	fi
fi

# A guard-removal decoy must raise KeyError and be rejected by muerte_valida rather than
# counted as an expected mutant failure.
python3 - "$GUION" "$TRABAJO/mRevienta.py" <<'PYR'
import sys
src = open(sys.argv[1]).read()
viejo = (
    '    if "sessions" not in tot:\n'
    '        raise SystemExit(salir(RC_UNAVAILABLE,\n'
    '                               f"{ruta}: team row {t.get(\'team\')!r} contains `totals` WITHOUT "\n'
)
assert src.count(viejo) == 1, f"decoy anchor: {src.count(viejo)} matches"
nuevo = (
    '    if False:  # SENUELO: la guarda se va y la funcion cae en KeyError\n'
    '        raise SystemExit(salir(RC_UNAVAILABLE,\n'
    '                               f"{ruta}: team row {t.get(\'team\')!r} contains `totals` WITHOUT "\n'
)
open(sys.argv[2], "w").write(src.replace(viejo, nuevo, 1))
PYR
if [ ! -s "$TRABAJO/mRevienta.py" ]; then
	malo "CANNOT INSPECT: could not construct the crashing decoy"
else
	rs="$(python3 "$TRABAJO/doble-sin-sessions.py" "$TRABAJO/mRevienta.py" >"$OUTPUT" 2>&1; printf '%s' "$?")"
	if ! command grep -q 'KeyError' "$OUTPUT"; then
		malo "the decoy did not crash (rc $rs): without KeyError, proves nothing; inspect the decoy"
	elif muerte_valida "$OUTPUT"; then
		malo "muerte_valida ACCEPTS output containing Traceback: a crash would count as a valid mutant failure"
	else
		paso "a CRASHING mutant is rejected as a valid mutant failure: the KeyError decoy demonstrates it"
	fi
fi

printf '\ntest-seed-adoption-otlp: %d passed, %d failed\n' "$ok" "$fail"
[ "$fail" -eq 0 ] || exit 1
exit 0
