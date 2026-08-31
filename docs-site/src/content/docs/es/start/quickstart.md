---
title: Inicio rápido
description: >-
  De cero a un grafo de acceso de lectura/escritura poblado con un resultado real
  de desviación Permitido-frente-a-Observado en unos cinco minutos — primero sobre
  el estate de demostración incluido, y luego sobre un conector pgAudit real para
  demostrar que no es una demo.
---

Esta es la vía rápida para ver para *qué* sirve Olivares AI: un **mapa de acceso de
lectura/escritura** de tu estate y la **desviación Permitido-frente-a-Observado** sobre
él — la brecha entre el acceso que se *concede* a un agente y el acceso que se *observa*
que usa.

Llegarás a ese resultado dos veces, en unos cinco minutos en total:

1. **En un minuto, sobre el estate de demostración incluido** — la vía de entrada
   instantánea para ver "qué pinta tiene esto" (observaciones sintéticas, fluyendo a
   través del motor real).
2. **Luego sobre un conector real** — el mismo grafo y la misma desviación, esta vez
   parseados literalmente desde un log **pgAudit** de PostgreSQL, para demostrar que el
   resultado estrella corre sobre datos genuinos, no una demo.

Todos los comandos de abajo los ejecuta, exactamente tal como están escritos,
`scripts/quickstart-smoke.sh` ([reproducibilidad](#5-reprodúcelo-tú-mismo)) — así que esta
página no puede desviarse silenciosamente del binario.

Es una vía de aprendizaje, no un despliegue de producción. Para la instalación real (sin
credenciales por defecto, un token de configuración de un solo uso, TLS), ve a
[autoalojamiento](/es/how-to/self-hosting/). Para un recorrido guiado por la UI, consulta el
[tutorial de cero a grafo](/es/tutorials/zero-to-graph/).

:::caution[El modo demo es solo para aprender]
`--seed-demo` aprovisiona un administrador de demostración con una **contraseña pública,
incluida en el árbol de fuentes** y datos sintéticos, y **se niega a arrancar en una
dirección que no sea loopback**. Nunca lo uses para una instalación real — la vía genuina
de primer arranque es el paso 3 de abajo y la de
[autoalojamiento](/es/how-to/self-hosting/).
:::

## 1. Compila el binario único

Desde un clon del repositorio (necesita Go 1.26+, [Task](https://taskfile.dev) y
pnpm — `task build` empaqueta la UI web antes de compilar; el almacén es SQLite puro en Go,
así que no hace falta toolchain de C):

```bash
task build                      # compiles ./bin/olivares with the web UI embedded
./bin/olivares version
```

`task build` produce un único artefacto autocontenido en `./bin/olivares` — el
motor, la UI web embebida y los plugins de conectores de primera parte. Las **instalaciones
en contenedor y Kubernetes envuelven este mismo binario**: una imagen publicada más un
fichero Compose ([autoalojamiento](/es/how-to/self-hosting/)), o un manifiesto plano que
aplicas con `kubectl apply -f deploy/manifests/install.yaml` (sin necesidad de Helm). El
resultado estrella que ves abajo es idéntico en las tres — solo difiere la semilla de
demostración (solo loopback, nunca en una instalación real).

## 2. Arranca el estate de demostración (solo loopback)

```bash
DATA="$(mktemp -d)"
./bin/olivares serve --insecure --seed-demo \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 \
  --data-dir "$DATA"
```

`--insecure` sirve HTTP en texto plano sobre loopback (correcto para una demo local;
**TLS está activado por defecto** en cualquier otro caso). Verás líneas `WARN` honestas
para los seams que vienen deny-closed de fábrica (sin juez, sin embedder, sin gate de
aprobación, sin fuentes reales), y luego un banner de **DEMO MODE** con las credenciales:

```text
demo@olivares.local / olivares-demo-estate
```

El estate sintético fluye a través del bus de eventos **real** exactamente como lo haría
un colector pgAudit u OpenTelemetry en vivo — solo las observaciones están sembradas.

## 3. Llega al grafo de acceso y a su desviación (el resultado estrella)

Deja el servidor corriendo; en una segunda terminal, inicia sesión, resuelve el tenant de
demostración y obtén el grafo y su desviación:

```bash
BASE=http://127.0.0.1:8901
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"email":"demo@olivares.local","password":"olivares-demo-estate"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

TENANT="$(curl -sf "$BASE/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  | python3 -c 'import sys,json;[print(o["tenant_id"]) for o in json.load(sys.stdin)["items"] if o["slug"]=="demo"]')"

# The read/write access map — module III:
curl -sf "$BASE/v1/m/accessmap/graph?limit=200" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool

# The Permitted-vs-Observed drift:
curl -sf "$BASE/v1/m/accessmap/drift" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
```

El estate de demostración devuelve exactamente **20 nodos y 13 aristas**, y la desviación
saca a la superficie **8 accesos inesperados** y **2 concesiones sin usar**. Cada arista
lleva los ejes de honestidad del producto, para que puedas leer cada hallazgo sin adivinar:

- **`mode`** — `read` / `write` / `readwrite` / `unknown`: la clasificación de L/E, tomada
  literalmente de la señal, nunca inferida.
- **`attribution_tier`** — `firm` / `approximate` / `unknown`: con qué firmeza el acceso
  está ligado a una identidad de agente o de carga de trabajo *específica*. En la demo,
  **6 aristas son firmes y 7 aproximadas** — p. ej. un agente leyendo un recurso que nunca
  le fue concedido (`appdb.public.secrets`, *firme*) frente a una identidad de pool
  compartido escribiendo logs (`appdb.public.logs`, honestamente *aproximada*).
- **`coverage_tier`** — `clean` / `lossy` / `opaque` / `mixed`: la fidelidad de la señal
  *del recurso*, ortogonal a la atribución.

:::tip[Una capacidad diferenciada clave]
El **diff entre Permitido y Observado** es la *desviación de mínimo privilegio* — eso que
quieres encontrar antes de que lo haga un auditor o un atacante. La semilla demuestra que
es real, no "todo es desviación": las 3 aristas concedidas **y** observadas se reconcilian
y desaparecen del resultado de desviación; solo quedan las brechas genuinas (8 accesos
inesperados + 2 concesiones que están declaradas pero nunca se ejercen). Y el producto
nunca fabrica una etiqueta que no puede demostrar — una atribución que es meramente
`approximate` lo dice, en lugar de inventar un agente `firm`.
:::

El mismo grafo se renderiza en la UI web embebida en `http://127.0.0.1:8901` (inicia
sesión con las credenciales de demostración y cambia a la organización **Demo Estate**).

Detén el servidor de demostración (`Ctrl-C`) antes del siguiente paso.

## 4. Demuéstralo sobre un conector real (no una demo)

El resultado estrella no es magia sembrada: corre sobre lo que sea que tus fuentes
observen. Aquí cableas el **conector pgAudit real** — la misma vía de código que usa una
instalación de producción — contra un log de auditoría de PostgreSQL, con **ninguna semilla
de demostración**.

Primero, un pequeño csvlog de `pgAudit` (tres líneas de auditoría reales: dos lecturas y una
escritura por una aplicación). En producción pgAudit escribe estas en el log de Postgres;
aquí un fichero hace las veces de ese tail:

```bash
WORK="$(mktemp -d)"
python3 - "$WORK/postgresql.csv" <<'PY'
import csv, sys
def row(ts, user, db, msg, app):
    r = [''] * 26
    r[0], r[1], r[2] = ts, user, db
    r[11] = 'LOG'; r[13] = msg; r[22] = app; r[23] = 'client backend'
    return r
rows = [
    row("2026-06-09 09:00:01.001 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,1,1,READ,SELECT,TABLE,public.customers", "billing-agent"),
    row("2026-06-09 09:00:02.002 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,2,1,WRITE,INSERT,TABLE,public.orders", "billing-agent"),
    row("2026-06-09 09:00:03.003 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,3,1,READ,SELECT,TABLE,public.secrets", "billing-agent"),
]
with open(sys.argv[1], 'w', newline='') as f:
    csv.writer(f).writerows(rows)
PY
```

Ahora haz un **primer arranque real**: arranca una vez sin credenciales por defecto,
reclama el token de configuración de un solo uso y crea un tenant al que adjuntar el
conector.

```bash
BASE=http://127.0.0.1:8901
./bin/olivares serve --insecure \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 \
  --data-dir "$WORK/data" > "$WORK/server.log" 2>&1 &
SERVER=$!
sleep 2

# The one-time setup token is printed to stdout on first boot (look for `olst_…` on the
# server's console, or read it from the redirected log):
SETUP="$(grep -oE 'olst_[A-Z0-9]+' "$WORK/server.log" | head -1)"

curl -sf -X POST "$BASE/v1/setup" -H 'Content-Type: application/json' \
  -d "{\"token\":\"$SETUP\",\"email\":\"admin@local\",\"password\":\"correct-horse-battery-staple\"}"

TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

TENANT="$(curl -sf -X POST "$BASE/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"Production","slug":"prod"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["tenant_id"])')"
echo "tenant: $TENANT"

kill "$SERVER"                  # stop the first-run server; we restart it with pgAudit wired
```

Los conectores se cablean desde un único fichero de configuración del operador, por valor,
nunca persistido por el motor. Apunta pgAudit al log de tu tenant y **reinicia** con la
configuración:

```bash
cat > "$WORK/sources.json" <<JSON
{"sources":[{"name":"salesdb-pgaudit","kind":"pgaudit","tenant":"$TENANT",
  "config":{"log_path":"$WORK/postgresql.csv","format":"csvlog"}}]}
JSON

OLIVARES_SOURCES_CONFIG="$WORK/sources.json" ./bin/olivares serve --insecure \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$WORK/data"
```

El log de arranque imprime `ingest: wired source … kind=pgaudit`. En una segunda terminal,
inicia sesión de nuevo y lee el grafo — esta vez las aristas están **genuinamente
parseadas**, no sembradas:

```bash
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

curl -sf "$BASE/v1/m/accessmap/graph?limit=200" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
curl -sf "$BASE/v1/m/accessmap/drift" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
```

Obtienes **3 aristas** — `salesdb.public.customers` (lectura), `…orders` (escritura),
`…secrets` (lectura) — cada una con `signal_source: pg_audit` y `coverage_tier: clean`
(pgAudit reporta L/E literalmente), y la desviación marca las **3 como accesos inesperados**
(aún no hay ninguna concesión cableada, así que todo acceso observado es desviación).

:::note[Honesto por defecto: aproximado hasta que cablees la identidad]
Estas aristas reales aterrizan como `attribution_tier: approximate`, no `firm` — la señal
de pgAudit nombra un rol de base de datos / aplicación, no un *agente gobernado*. Ese es el
valor por defecto honesto: el producto no afirmará que atribuyó firmemente un acceso a un
agente que no puede demostrar. Ganas `firm` cableando una fuente de identidad
(LDAP/IdP/SPIFFE) que liga la credencial a una identidad de agente o de carga de trabajo —
consulta [conectar una fuente](/es/how-to/connect-a-source/). El estate de demostración muestra
aristas `firm` precisamente porque pre-liga sus agentes.
:::

:::note[La forma del endpoint]
El resultado Permitido-frente-a-Observado se sirve en `/v1/m/accessmap/drift` (no hay
`/diff`). Las rutas `/v1/m/accessmap/*` no están en el contrato estable del núcleo de
53 rutas; se publican en un documento **beta** separado — la
[referencia de rutas de módulos](/reference/api-beta/). La
[referencia de la API](/reference/api/) documenta la superficie estable del núcleo.
:::

## 5. Reprodúcelo tú mismo

Todo lo de arriba está aseverado, de extremo a extremo, contra el binario real:

```bash
task smoke:quickstart          # or: scripts/quickstart-smoke.sh
```

Arranca el estate de demostración **y** la vía pgAudit real, ejecuta los comandos exactos de
esta página y comprueba los números (20 nodos / 13 aristas, 8 inesperados + 2 sin usar, 3
aristas pgAudit reales). Si la vía de instalación→valor o el resultado de desviación dejara
alguna vez de ser cierto, el smoke falla — ese es el contrato que mantiene honesta esta
página. Se completa en unos pocos segundos de reloj; la vía recorrida por un humano de
arriba son los **cinco minutos** documentados.

## Próximos pasos

- **Ejecútalo de verdad:** los tutoriales de primeros pasos recorren cada escenario de
  instalación de extremo a extremo —
  [nodo único (systemd)](/es/tutorials/getting-started/single-node/),
  [Docker Compose](/es/tutorials/getting-started/docker-compose/),
  [Kubernetes/Helm](/es/tutorials/getting-started/kubernetes/) y
  [air-gapped](/es/tutorials/getting-started/air-gapped/);
  [autoalojamiento](/es/how-to/self-hosting/) es la página de decisión que los abarca.
- **Aliméntalo con señales reales:** [conectar una fuente](/es/how-to/connect-a-source/) y el
  [catálogo de conectores](/es/reference/connectors/) — qué observa cada fuente, su nivel de
  cobertura honesto, y cómo cablear la identidad para que la atribución pase a ser `firm`.
- **Endurécelo:** [endurecimiento de seguridad](/es/how-to/security-hardening/) — valores por
  defecto seguros, aprobaciones human-in-the-loop, y verificar una release antes de
  ejecutarla.
- **Conoce los límites:** [Honestidad y límites](/es/start/honesty-and-limits/) — qué corre
  hoy, qué está en fase de diseño, y qué hace el producto deliberadamente que no.
