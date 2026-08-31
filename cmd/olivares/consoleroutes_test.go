// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

// THE CLASS: A HAND-WRITTEN CONSOLE CLIENT CALLING A ROUTE THE ENGINE NEVER MOUNTS.
//
// The console asks, the engine answers 404, and every test stays green. That is not a
// gap in diligence — it is a gap in METHOD, and three existing guards each miss it for a
// different reason:
//
//   - core/api/openapi_router_parity_test.go asserts document ⊆ router, and explicitly
//     DECLINES the other direction (":26-29"). It never looks at a client.
//   - cmd/olivares/openapi_beta_coverage_test.go asserts document == router, but only
//     for /v1/m/. It never looks at a client either.
//   - task test:web runs the console against MOCKS, and web:check only proves the
//     committed bundle matches its sources. A mock answers for a route that does not
//     exist, so both stay green while an operator gets a 404.
//
// The console's typed clients under web/src/features/*/api.ts are HAND-WRITTEN — they
// are not generated from the OpenAPI document (openapi.gen.ts is; these are not). So
// nothing whatsoever connected what they CALL to what the binary REGISTERS. This does.
//
// WHY THE ROUTER AND NOT THE DOCUMENT. Measured on 2026-08-07: of 34 client paths absent
// from both OpenAPI documents, nearly all answer 401/405 on a live engine — they ARE
// registered, merely unpublished (/v1/auth/webauthn/*, /v1/console/dr/*, /v1/scim/v2/*,
// /v1/agent-groups/*). A check against the document would have opened with ~34 false
// positives and been switched off within the week. The router is the truth.

// ⛔⛔ EL PUNTO CIEGO DE ESTE GUARDIÁN, MEDIDO EL 2026-08-18 Y DICHO AQUÍ PORQUE SU NOMBRE PROMETE
//
//	MÁS DE LO QUE MIDE. `TestEveryConsoleClientCallHitsARegisteredRoute` dice «every console
//	client call» y **sólo ve las llamadas por el cliente tipado**: el regex de abajo casa
//	`http.get|post|put|patch|delete` y **nada más**. La palabra `fetch` no aparece en este
//	fichero ni una vez.
//
//	La consola usa `fetch` crudo en **14 ficheros** —exportaciones que necesitan la respuesta sin
//	parsear (`fetchRawExport`, `audit/export.ts`, `model-ops/export.ts`, `posture-export`) y los
//	flujos SSE (`shared/sse.ts`)—, así que esa familia entera es **invisible** para el test que
//	existe justamente para cazar una llamada a una ruta que el motor no registra. Un `fetch` con
//	un typo en la ruta pasa este gate y da 404 al operador.
//
// ⚠ Y NO ES LO MISMO QUE `consoleUnresolvedSites`, que es la otra lista de este fichero: allí están
//
//	los sitios que el parser VE y no puede resolver, y por eso se declaran uno a uno. Un `fetch`
//	no está sin resolver: está **sin ver**. Lo primero se cuenta; lo segundo no aparece en ningún
//	número, que es lo que lo hace peor.
//
//	Cómo salió a la luz: el censo de cobertura por namespace daba `m/posture 0/1`, y el fichero
//	`web/src/features/posture-export/api.ts:87` llama a esa única ruta — con `fetch` crudo. Las
//	dos afirmaciones eran ciertas a la vez, y sólo mirando el parser se ve cuál contesta qué.
//
// consoleAPIRe matches a typed client call. It accepts all three spellings the console
// actually uses — a template literal, a SINGLE-QUOTED literal, or a path constant — and
// the WithMeta/Raw wrapper verbs (web/src/lib/api/client.ts), because a call the parser
// cannot see is a hole in exactly the class this test claims to close.
var consoleAPIRe = regexp.MustCompile(`\bhttp\.(get|post|put|patch|delete)(?:WithMeta|Raw)?\s*(?:<[^(]*?>)?\s*\(\s*(` + "`" + `[^` + "`" + `]*` + "`" + `|'[^']*'|[A-Za-z_$][\w$]*)`)

// consoleDirectAPIRe casa las llamadas DIRECTAS a apiFetch/apiFetchWithMeta cuya ruta es
// literal. Son el escape deliberado de `http.*` para cuerpos de bytes sin serializar; por tanto,
// siguen siendo cliente tipado y el gate debe atribuirles su verbo exacto. El objeto de opciones
// se acepta sólo si no tiene llaves anidadas: adivinar dónde termina un objeto TypeScript con un
// regex convertiría una ruta dudosa en cobertura afirmada. Sin `method`, apiFetch usa GET.
var consoleDirectAPIRe = regexp.MustCompile(`(?s)\bapiFetch(?:WithMeta)?\s*(?:<[^(]*?>)?\s*\(\s*(` + "`" + `[^` + "`" + `]*` + "`" + `|'[^']*')\s*(?:,\s*\{([^{}]*)\})?\s*\)`)
var consoleDirectAPIMethodRe = regexp.MustCompile(`(?i)\bmethod\s*:\s*['"](get|post|put|patch|delete)['"]`)

// directAPIFetchesAsHTTP reescribe únicamente la representación que lee este parser. Conserva
// el número de saltos para que fichero:línea siga nombrando el árbol real; el fuente no se toca.
func directAPIFetchesAsHTTP(text string) string {
	return consoleDirectAPIRe.ReplaceAllStringFunc(text, func(call string) string {
		m := consoleDirectAPIRe.FindStringSubmatch(call)
		verb := "get"
		if method := consoleDirectAPIMethodRe.FindStringSubmatch(m[2]); method != nil {
			verb = strings.ToLower(method[1])
		}
		return "http." + verb + "(" + m[1] + ")" + strings.Repeat("\n", strings.Count(call, "\n"))
	})
}

// consoleFetchRe casa las llamadas con `fetch` CRUDO, que el regex de arriba no ve: las
// exportaciones que necesitan la respuesta sin parsear y los flujos SSE. Captura sólo la RUTA.
//
// ⛔ A PROPÓSITO NO CAPTURA EL MÉTODO, y esa es la decisión de diseño de este trozo. En un `fetch`
//
//	el verbo vive en el objeto de init (`{ method: 'POST' }`), a veces varias líneas más abajo y a
//	veces en una variable. Suponer GET convertiría un `fetch(..., {method:'POST'})` legítimo en un
//	**falso rojo** —«la consola llama a GET /x, que no existe»—, y un guardián que grita por lo
//	correcto se desactiva en una semana. Así que estas llamadas se comprueban **agnósticas del
//	método**: ¿existe esta RUTA bajo algún verbo? Es menos de lo que se comprueba a las tipadas, y
//	es exactamente la pregunta que este test existe para responder — una ruta que el motor no
//	registra da 404 se llame como se llame.
//
// trailingInterpRe casa un `${…}` al final que va PEGADO a texto literal, no precedido de `/`.
//
// ⛔ ESA CONDICIÓN ES EL ARREGLO, y me costó un falso positivo encontrarla. La primera versión
//
//	quitaba cualquier `${…}` final, «porque en un fetch crudo eso es la cadena de consulta». A
//	veces sí —`${BASE}/export${qs ? …}`— y a veces es un PARÁMETRO DE RUTA con su propio segmento:
//	`${BASE}/templates/${encodeURIComponent(type)}`. Al quitarlo, la ruta quedaba
//	`/v1/m/reporting/templates` y el guardián acusaba al motor de no registrarla — cuando registra
//	`/templates/{type}` en GET, PUT y DELETE (`modules/reporting/enterprise.go:105-107`).
//
//	El `[^/]` de delante distingue las dos: pegado al segmento es sufijo; con `/` delante es un
//	parámetro y va a `{}` como cualquier otro.
var trailingInterpRe = regexp.MustCompile(`([^/])\$\{[^{}]*(?:\{[^{}]*\}[^{}]*)*\}$`)

// ⭐ AQUÍ DECÍA `fetch(?:RawExport)?`: un envoltorio cableado POR SU NOMBRE. Retirado el
// 2026-08-19 y sustituido por la detección GENÉRICA de envoltorios de transporte (transportFnRe),
// que lo reconoce por su FORMA —primer parámetro `path: string` que viaja tal cual a `fetch`— y
// por tanto reconocerá también al siguiente, que con un nombre cableado habría vuelto a costar un
// hallazgo. Medido en las dos direcciones antes de retirarlo: sin el nombre y CON lo genérico
// salen 15 cubiertas y 74 huecos, los mismos que con el nombre; sin ninguno de los dos, 8 y 81.
// Es la misma cuenta y una regla en vez de un caso — igual que `check-cli-registries` dejó de
// llamarse `command-groups` cuando apareció la tercera instancia.
// consoleStreamRe casa una suscripción a un flujo por el helper COMPARTIDO de SSE:
// `useLiveStream<T>({ path: '/v1/m/sessions/stream', … })` o `subscribeStream({ path: … })`.
//
// ⛔ ES UN TERCER TRANSPORTE, y hasta hoy invisible. `features/shared/sse.ts:11` explica por qué no
// usa `EventSource` nativo —el motor pide `Authorization` y `X-Olivares-Tenant`, y EventSource no
// puede poner cabeceras—, así que hace `fetch` sobre una URL COMPUESTA. Resultado: ONCE rutas de
// flujo y descarga contadas como «sin superficie» con 31 llamantes vivos.
//
// ⚠ Y NO LO ALCANZABA LA DETECCIÓN GENÉRICA DE ENVOLTORIOS, por dos razones que conviene decir en
// vez de ampliarla a lo bruto: el helper recibe un OBJETO de opciones, no una ruta como primer
// parámetro, y vive en OTRO FICHERO —esa detección es por fichero—. Se resuelve por la forma de la
// LLAMADA, que es donde la ruta sí es literal.
//
// Agnóstico del método, como el `fetch` crudo y por lo mismo: el verbo lo decide el helper.
var consoleStreamRe = regexp.MustCompile(`(?s)\b(?:useLiveStream|subscribeStream)\s*(?:<[^(]*?>)?\s*\(\s*\{[^}]*?path:\s*(` + "`" + `[^` + "`" + `]*` + "`" + `|'[^']*')`)

// consoleStreamOpaqueRe casa la MISMA suscripción cuando el `path:` NO es literal:
// `path: runAttachPath(runRef)`. Esos helpers viven en OTRO fichero y `pathFns` se construye por
// fichero, así que el parser no puede resolverlos sin un mapa cruzado — y un mapa cruzado hace
// colisionar nombres iguales de features distintas, que es cambiar una ceguera por un error.
//
// ⇒ Se DECLARAN como sitio irresoluble en vez de dejar sus rutas calladas en el cubo de «sin
// superficie». La ruta existe y alguien la llama; lo que falta es mi capacidad de leerla, y ésa es
// justo la distinción que este fichero entero existe para no perder.
var consoleStreamOpaqueRe = regexp.MustCompile(`(?s)\b(?:useLiveStream|subscribeStream)\s*(?:<[^(]*?>)?\s*\(\s*\{[^}]*?path:\s*([A-Za-z_$][\w$]*\([^)]*\))`)

var consoleFetchRe = regexp.MustCompile(`\bfetch\s*\(\s*(` + "`" + `[^` + "`" + `]*` + "`" + `|'[^']*')`)

// constRe matches a module-level absolute-path constant the calls interpolate.
var constRe = regexp.MustCompile(`(?m)^\s*const\s+([A-Za-z_$][\w$]*)\s*=\s*'(/[^']*)'`)

// pathMapRe casa un `const NOMBRE: Record<…> = { clave: <ruta>, … }` de nivel de fichero: el
// idioma con el que la consola deletrea VARIAS rutas hermanas que el router registra por separado
// (`ADMISSION_POLICY_PATH` en catalog/api.ts). Se resuelve entero o no se resuelve.
//
// ⛔ El cuerpo se acota con `\n\}` —la llave en COLUMNA CERO—, no con `[^}]*`. Medido el
// 2026-08-19: `[^}]*` cortaba en la primera `}` del fichero, que es la de `${BASE}` dentro del
// primer valor, y el mapa salía con el cuerpo truncado a `\n  mcp: ` + "`" + `${BASE` y CERO valores. Una
// interpolación es una llave que no cierra nada: pararse en ella no es parar en el constructo.
var pathMapRe = regexp.MustCompile(`(?ms)^[ \t]*const\s+([A-Za-z_$][\w$]*)\s*(?::[^=\n]*)?=\s*\{(.*?)\n\}`)

// pathMapValRe saca el valor de cada entrada del objeto, con comilla simple o con backtick.
var pathMapValRe = regexp.MustCompile(":\\s*(?:'([^']*)'|\\x60([^\\x60]*)\\x60)")

// mapaDeRutas resuelve esos objetos a su CONJUNTO de rutas. Devuelve nada para un mapa cuyo valor
// no se pueda resolver ENTERO —una interpolación que no sea una constante conocida, o una ruta que
// no empiece por `/`—, porque resolver la mitad de un mapa es exactamente lo que inventa rutas que
// nadie llama. El precio de negarse es un sitio declarado irresoluble, que es visible; el precio de
// resolver a medias es cobertura afirmada de más, que no lo es.
func mapaDeRutas(text string, consts map[string]string) map[string][]string {
	out := map[string][]string{}
	for _, m := range pathMapRe.FindAllStringSubmatch(text, -1) {
		vals := pathMapValRe.FindAllStringSubmatch(m[2], -1)
		if len(vals) == 0 {
			continue
		}
		rutas := make([]string, 0, len(vals))
		for _, v := range vals {
			crudo := v[1]
			if crudo == "" {
				crudo = v[2]
			}
			p := simpleInterpRe.ReplaceAllStringFunc(crudo, func(sub string) string {
				name := simpleInterpRe.FindStringSubmatch(sub)[1]
				if c, ok := consts[name]; ok {
					return c
				}
				return sub
			})
			if !strings.HasPrefix(p, "/") || strings.Contains(p, "${") {
				rutas = nil
				break
			}
			rutas = append(rutas, p)
		}
		if len(rutas) > 0 {
			out[m[1]] = rutas
		}
	}
	return out
}

// tmplConstRe matches a module-level constant BUILT FROM ANOTHER ONE:
//
//	const ARTIFACTS = `${BASE}/agent-artifacts`
//
// The single-quoted form above is the base case; this is how every feature that owns
// more than one collection actually writes the second and third. Without it the parser
// resolved BASE and lost everything derived from it, which is why /agent-artifacts,
// /work-items and /decisions read as "not a literal path constant" while being static.
var tmplConstRe = regexp.MustCompile("(?m)^\\s*(?:export\\s+)?const\\s+([A-Za-z_$][\\w$]*)\\s*=\\s*`([^`]*)`")

// pathFnRe matches a single-expression path HELPER: one template literal, returned by
// an arrow or by a one-line function body.
//
//	const workflowPath = (id: string) => `${BASE}/workflows/${encodeURIComponent(id)}`
//	function nhiPath(ref: string): string { return `${GOVERNANCE}/nhi/${...}` }
//
// Their bodies are as static as any constant — the only dynamic part is the parameter,
// which normalises to {} exactly like an inline ${id}. Seventeen call sites were
// unresolvable purely because the shared prefix had been factored into a function.
var pathFnRe = regexp.MustCompile("(?ms)^\\s*(?:export\\s+)?(?:const\\s+([A-Za-z_$][\\w$]*)\\s*=\\s*\\([^)]*\\)(?:\\s*:\\s*string)?\\s*=>|function\\s+([A-Za-z_$][\\w$]*)\\s*\\([^)]*\\)(?:\\s*:\\s*string)?\\s*\\{\\s*return)\\s*`([^`]*)`")

// transportFnRe casa un ENVOLTORIO DE TRANSPORTE local: una función que recibe la ruta como
// PRIMER parámetro y se la pasa tal cual a un `http.<verbo>`. `postDocument(path, document)` en
// `features/compliance/api.ts:214` es el caso que lo destapó.
//
// ⛔ POR QUÉ IMPORTA, medido el 2026-08-19: el parser sólo mira llamadas `http.X(...)`, así que de
// un envoltorio veía SU interior —`http.post(path, …)`, con `path` no literal— y lo declaraba
// irresoluble, mientras sus CINCO llamantes, que sí pasan rutas literales
// (`${BASE}/dora/register`, `/dora/incidents`, `/oscal/profiles`, `/depth/us-law`,
// `/depth/sector`), no los miraba NADIE. Resultado: cinco rutas contadas como «sin superficie de
// consola» que la consola llama, más un sitio contado como ceguera que no lo era. El mismo error
// que me hizo diseñar una pantalla de SSO que ya existía, un envoltorio más adentro.
//
// ⚠ EL VERBO SE LEE, NO SE SUPONE, y de ahí sale el límite de esta regla: exige UNA sola llamada
// `http.<verbo>(<ese parámetro>` en el cuerpo. Un despachador que ramifica el verbo —`send()` en
// `features/work/api.ts:632`, que elige PATCH/DELETE/POST según `intent.method`— NO casa y sigue
// declarado, que es lo correcto: resolverlo exigiría el producto cartesiano ruta×verbo e
// inventaría llamadas que la consola quizá no hace. Ensanchar la entrada de una guarda para
// mejorar un informe es como se debilita una guarda sin querer.
// ⚠ Y el parámetro se comprueba EN GO, no en el regex: RE2 no tiene retro-referencias, así que
// `\2` ni compila (panic en init, medido). El regex captura nombre, primer parámetro y cuerpo; la
// comprobación de que ESE parámetro es el que viaja como ruta la hace transporteVerbo.
// ⚠ `async function` Y SIN TOPE DE CUERPO, las dos cosas medidas: `fetchRawExport` es `async` y
// mide 1122 bytes, asi que ni el `function` pelado ni el tope de 800 la alcanzaban — y el
// terminador viejo (`\n\s*\}`) cortaba en la primera llave ANIDADA, no al final de la funcion.
// Tres formas distintas de no ver un envoltorio que estaba a la vista. RE2 no retrocede, asi que
// pedir la llave a COLUMNA CERO acota sola y el tope no protegia de nada.
var transportFnRe = regexp.MustCompile(`(?ms)^\s*(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_$][\w$]*)\s*(?:<[^(]*?>)?\s*\(\s*([A-Za-z_$][\w$]*)\s*:\s*string\s*,(.*?)\n\}`)

// transporteVerbo devuelve el verbo con el que un envoltorio manda `param` como ruta, y `false` si
// no lo hace o si lo hace con MÁS DE UNO. Ese segundo caso es el despachador que ramifica, y
// devolver `false` es la respuesta correcta: elegir uno inventaría llamadas.
func transporteVerbo(cuerpo, param string) (string, bool) {
	re := regexp.MustCompile(`\bhttp\.(get|post|put|patch|delete)(?:WithMeta|Raw)?\s*(?:<[^(]*?>)?\s*\(\s*` +
		regexp.QuoteMeta(param) + `\s*[,)]`)
	vistos := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(cuerpo, -1) {
		vistos[m[1]] = true
	}
	if len(vistos) == 0 {
		// ⛔ ENVOLTORIO DE `fetch` CRUDO, y es una variante entera de la misma familia:
		// `fetchRawExport(path, filename)` en `features/compliance/api.ts:82` baja OCHO
		// exportaciones que el informe daba como «sin superficie» —dora, nis2, aims, las tres
		// de depth y evidence—. Se resuelve AGNÓSTICO DEL MÉTODO, que es la decisión que este
		// fichero ya documenta para el fetch crudo: el verbo vive en el objeto de init, a veces
		// varias líneas más abajo, y suponer GET convertiría un POST legítimo en un falso rojo.
		if regexp.MustCompile(`\bfetch\s*\(\s*` + regexp.QuoteMeta(param) + `\s*[,)]`).MatchString(cuerpo) {
			return "fetch", true
		}
		return "", false
	}
	if len(vistos) != 1 {
		return "", false
	}
	for v := range vistos {
		return v, true
	}
	return "", false
}

// pathFnMultiRe casa un helper de ruta cuyo cuerpo NO es un solo `return `template“: tiene
// consts y ternarios, y por tanto devuelve un CONJUNTO de rutas posibles. `ssoConfigPath(scope,
// alias)` en `features/console/api.ts:137` es el caso que lo obligó.
//
// ⛔ POR QUÉ VALE LA PENA, medido el 2026-08-19: DIECIOCHO de las 25 rutas que el informe daba
// como «sin superficie» en el namespace `console` son `/v1/console/sso/*`, y la consola las llama
// TODAS a través de ese helper. Ya me costó una vez ponerme a diseñar una pantalla de SSO que
// existía. Descarté resolverlo por «sobre-ingeniería para un solo helper» y esa cuenta estaba mal
// hecha: no es un helper, son dieciocho huecos falsos que invitan a construir lo ya construido.
var pathFnMultiRe = regexp.MustCompile("(?ms)^\\s*(?:export\\s+)?(?:async\\s+)?function\\s+([A-Za-z_$][\\w$]*)\\s*\\(([^)]*)\\)\\s*:\\s*string\\s*\\{(.{0,700}?)\\n\\}")

// partirTernario devuelve (rama-verdadera, rama-falsa, true) de una expresión ternaria, ignorando
// los `?` y `:` que viven DENTRO de una cadena o de una interpolación. Un `split` ingenuo por `?`
// acierta `alias !== 'default' ? …` de casualidad y falla `${a ? b : c}` siempre; esto no
// distingue por suerte.
func partirTernario(expr string) (string, string, bool) {
	prof, enCadena, comilla, inter := 0, false, byte(0), 0
	q, c := -1, -1
	for i := 0; i < len(expr); i++ {
		ch := expr[i]
		switch {
		case enCadena && ch == '$' && i+1 < len(expr) && expr[i+1] == '{':
			inter++
			i++
		case enCadena && inter > 0 && ch == '}':
			inter--
		case enCadena && inter == 0 && ch == comilla:
			enCadena = false
		case enCadena:
		case ch == '`' || ch == '\'' || ch == '"':
			enCadena, comilla = true, ch
		case ch == '(':
			prof++
		case ch == ')':
			prof--
		case ch == '?' && prof == 0 && q < 0:
			q = i
		case ch == ':' && prof == 0 && q >= 0 && c < 0:
			c = i
		}
	}
	if q < 0 || c < 0 || c < q {
		return "", "", false
	}
	return expr[q+1 : c], expr[c+1:], true
}

// evalRuta evalúa una expresión de ruta a su CONJUNTO de valores posibles: un ternario da sus dos
// ramas, una cadena da su contenido y un identificador da lo que valga ese `const`. Devuelve nil
// ante cualquier otra forma — no se resuelve a medias, porque media resolución inventa rutas.
func evalRuta(expr string, consts map[string][]string, prof int) []string {
	expr = strings.TrimSpace(expr)
	if prof > 4 || expr == "" {
		return nil
	}
	if a, b, ok := partirTernario(expr); ok {
		va, vb := evalRuta(a, consts, prof+1), evalRuta(b, consts, prof+1)
		if va == nil || vb == nil {
			return nil
		}
		return append(va, vb...)
	}
	if len(expr) >= 2 && (expr[0] == '`' || expr[0] == '\'') && expr[len(expr)-1] == expr[0] {
		return []string{expr[1 : len(expr)-1]}
	}
	if v, ok := consts[expr]; ok {
		return v
	}
	return nil
}

// pathSetFor evalúa el cuerpo de un helper a su CONJUNTO de rutas: resuelve cada `const` (que puede
// ocupar VARIAS líneas) y sustituye su valor dentro de la expresión del `return`.
//
// ⚠ ACOTADO EN 8 A PROPÓSITO. Un cuerpo con más alternativas devuelve nil y el sitio queda
// DECLARADO irresoluble, que es la respuesta honesta: enumerar 2^n inventaría llamadas que la
// consola no hace, y sobre-declarar cobertura esconde huecos reales — el error opuesto al que esto
// arregla, y peor, porque no se ve. La red que lo respalda es el test hermano: cada ruta emitida se
// comprueba contra el registro del router, así que una rama inventada sale roja, no callada.
func pathSetFor(cuerpo string) []string {
	// Los enunciados se parten por `const `/`return `, no por línea: el `const` de ssoConfigPath
	// ocupa TRES y una captura por línea se quedaba con `scope`, que no es una ruta.
	corte := regexp.MustCompile(`(?m)^\s*(const\s+[A-Za-z_$][\w$]*\s*=|return\b)`)
	idx := corte.FindAllStringSubmatchIndex(cuerpo, -1)
	if len(idx) == 0 {
		return nil
	}
	consts := map[string][]string{}
	var salidas []string
	for n, loc := range idx {
		fin := len(cuerpo)
		if n+1 < len(idx) {
			fin = idx[n+1][0]
		}
		cabeza := strings.TrimSpace(cuerpo[loc[2]:loc[3]])
		cuerpoExpr := cuerpo[loc[3]:fin]
		if cabeza == "return" {
			salidas = evalRuta(cuerpoExpr, consts, 0)
			continue
		}
		nombre := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(cabeza, "const"), "="))
		if v := evalRuta(cuerpoExpr, consts, 0); v != nil {
			consts[nombre] = v
		}
	}
	// Y las salidas que INTERPOLAN un const (`${base}/idps/…`) se expanden con cada valor suyo.
	// Sin este paso la rama que interpola no empieza por `/v1/` y el conjunto entero se descarta —
	// que es exactamente lo que hizo la primera versión de este evaluador, en silencio.
	for nombre, valores := range consts {
		var siguiente []string
		for _, sal := range salidas {
			if !strings.Contains(sal, "${"+nombre+"}") {
				siguiente = append(siguiente, sal)
				continue
			}
			for _, v := range valores {
				siguiente = append(siguiente, strings.ReplaceAll(sal, "${"+nombre+"}", v))
			}
		}
		salidas = siguiente
		if len(salidas) > 8 {
			return nil
		}
	}
	if len(salidas) == 0 || len(salidas) > 8 {
		return nil
	}
	for _, s := range salidas {
		if !strings.HasPrefix(s, "/v1/") {
			return nil
		}
	}
	return salidas
}

// Una FÁBRICA DE INTENTS: `let path: string` + `let method = 'POST'` y un `switch` que asigna la
// ruta y, cuando difiere, el verbo. `buildIntent` en `features/work/api.ts:550` es el caso.
//
// ⛔ POR QUÉ SE RESUELVE Y `send()` NO, que es la misma familia vista de cerca: en el DESPACHADOR
// el verbo es `intent.method`, una variable, y emparejarlo con las rutas exigiría el producto
// cartesiano ruta×verbo — inventar llamadas. En la FÁBRICA cada caso fija ruta Y verbo en el mismo
// `case`: son PARES determinados, y enumerarlos no inventa nada. Las rutas viven donde el intent se
// CONSTRUYE, no donde se manda, que es lo que ya decía la nota de `intent.path`.
//
// Medido el 2026-08-19: las 21 rutas de `m/sessions` que el informe daba como «sin superficie»
// —transitions, decisions, decisions/{}/revoke y las seis de lease/*— salen todas de este switch.
// interpConLlamadaRe casa una interpolación que INVOCA algo (`${id(x)}`, `${encodeURIComponent(y)}`)
// y por tanto es un parámetro de ruta, no una constante.
var interpConLlamadaRe = regexp.MustCompile(`\$\{[^{}]*\([^{}]*\)[^{}]*\}`)

var intentPathRe = regexp.MustCompile(`(?m)^[ \t]*path = (.+)$`)
var intentMethodRe = regexp.MustCompile(`(?m)^[ \t]*method = '([A-Z]+)'`)
var intentDefaultRe = regexp.MustCompile(`(?m)^[ \t]*let method[^=]*= '([A-Z]+)'`)

// enComentario dice si el desplazamiento cae en una línea de COMENTARIO.
//
// ⛔ POR QUÉ, medido el 2026-08-19: `features/compliance/api.ts:198` y `:650` son JSDoc que
// EXPLICAN la API —«`http.post(path, <string>)` JSON-encodes its body argument»— y el parser los
// contaba como sitios de llamada irresolubles. Dos de los siete del censo eran prosa.
//
// ⚠ Y LA DIRECCIÓN PELIGROSA ES LA OTRA, aunque hoy no ocurra: un comentario que citara
// `http.get('/v1/m/foo')` marcaría esa ruta como CUBIERTA sin que nadie la llame. Medido hoy: 18
// líneas de comentario mencionan `http.X` y NINGUNA cita una ruta `/v1`, con control positivo (el
// mismo barrido encuentra las dos de compliance). Cero hoy no es cero mañana, y es el mismo defecto
// que el integrador encontró en el gate de márgenes: derivar un dato de PROSA.
//
// Cubre `//`, `*` de JSDoc y `/*`. Un bloque cuyas líneas interiores no empiecen por `*` se le
// escapa; prettier no las produce en este árbol, y decirlo aquí es más honesto que fingir un
// analizador de comentarios dentro de un regex.
func enComentario(text string, off int) bool {
	ini := strings.LastIndexByte(text[:off], '\n') + 1
	linea := strings.TrimSpace(text[ini:off])
	return strings.HasPrefix(linea, "//") || strings.HasPrefix(linea, "*") || strings.HasPrefix(linea, "/*")
}

// callFnRe matches an interpolation that INVOKES such a helper: ${nhiPath(ref)}.
var callFnRe = regexp.MustCompile(`\$\{([A-Za-z_$][\w$]*)\(([^}]*)\)\}`)

// resolveConsts expands ${OTHER} inside collected constants until they stop changing,
// so a constant defined in terms of a constant lands on a literal path. Bounded by the
// number of constants: each pass must expand at least one name or we stop.
func resolveConsts(consts map[string]string) {
	for pass := 0; pass <= len(consts); pass++ {
		changed := false
		for name, val := range consts {
			expanded := simpleInterpRe.ReplaceAllStringFunc(val, func(s string) string {
				ref := simpleInterpRe.FindStringSubmatch(s)[1]
				if ref == name {
					return s // self-reference: leave it, never spin
				}
				if v, ok := consts[ref]; ok {
					return v
				}
				return s
			})
			if expanded != val {
				consts[name] = expanded
				changed = true
			}
		}
		if !changed {
			return
		}
	}
}

// interpRe matches a ${...} interpolation.
var interpRe = regexp.MustCompile(`\$\{[^}]*\}`)

// simpleInterpRe matches a ${IDENT} interpolation (resolvable against a const).
var simpleInterpRe = regexp.MustCompile(`\$\{([A-Za-z_$][\w$]*)\}`)

// conditionalInterpRe matches a ${... ? ... : ...} interpolation.
var conditionalInterpRe = regexp.MustCompile(`\$\{[^}]*\?[^}]*\}`)

// esCondicionalInterp distingue un TERNARIO de una COALESCENCIA NULA dentro de `${…}`.
//
// ⛔ `conditionalInterpRe` casa cualquier `?`, y `${id(args.itemId ?? ”)}` lleva DOS: el operador
// `??`, que no es una condición sino un valor por defecto. RE2 no tiene lookahead, así que se
// quitan los `??` y se vuelve a preguntar — si queda un `?`, entonces sí es un ternario.
//
// Defecto PRE-EXISTENTE que sólo se vio al resolver la fábrica de intents: hasta entonces esas
// rutas ni llegaban aquí, porque quedaban detrás de `intent.path`. Catorce sitios se declararon
// «conditional interpolation» siendo rutas perfectamente concretas.
func esCondicionalInterp(p string) bool {
	return conditionalInterpRe.MatchString(strings.ReplaceAll(p, "??", ""))
}

// literalTernaryRe matches a conditional interpolation whose TWO branches are string literals:
//
//	${active ? 'enable' : 'disable'}      ${ack ? '?acknowledge=true' : ''}
//
// ⛔ ÉSTAS SÍ SE PUEDEN ENUMERAR, y no hacerlo tenía un coste medible: sus rutas salían como «sin
//
//	superficie de consola» en la pata de cobertura —`/v1/users/{}/enable` y `/v1/console/license`—
//	cuando la consola las llama. Una ceguera declarada sigue siendo ceguera, y aquí es evitable:
//	dos ramas literales son dos rutas, no una incógnita.
//
// Lo que NO se enumera y sigue reportándose: una rama que no sea literal (una llamada, otra
// variable) o más de DOS condicionales en la misma plantilla — 2^n crece rápido y una explosión
// combinatoria inventaría rutas que nadie escribió.
var literalTernaryRe = regexp.MustCompile(`\$\{[^}?']*\?\s*'([^']*)'\s*:\s*'([^']*)'\s*\}`)

// expandLiteralTernaries devuelve TODAS las rutas que una plantilla puede producir, o nil si
// queda algún condicional que no sabe enumerar. El tope de dos evita la explosión.
func expandLiteralTernaries(p string) []string {
	out := []string{p}
	for range 2 {
		next := []string{}
		expandio := false
		for _, cand := range out {
			m := literalTernaryRe.FindStringSubmatchIndex(cand)
			if m == nil {
				next = append(next, cand)
				continue
			}
			expandio = true
			a := cand[:m[0]] + cand[m[2]:m[3]] + cand[m[1]:]
			b := cand[:m[0]] + cand[m[4]:m[5]] + cand[m[1]:]
			next = append(next, a, b)
		}
		out = next
		if !expandio {
			break
		}
	}
	for _, cand := range out {
		if esCondicionalInterp(cand) {
			return nil // queda uno que no es de dos literales, o pasó del tope
		}
	}
	return out
}

// gluedParamSegment reports whether any path segment mixes a {} parameter with literal
// text (e.g. "{}-admissions"), which the router spells out literally instead.
func gluedParamSegment(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if strings.Contains(seg, "{}") && seg != "{}" {
			return true
		}
	}
	return false
}

// consoleSeam is one DECLARED-but-unmounted route, with the reason it is not built. A
// silent allow-list would let the next 404 in unnoticed; a reason makes it a decision
// somebody made and a reviewer can argue with.
type consoleSeam struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Owner  string `json:"owner"`
}

type clientCall struct {
	method, path, file string
	line               int
}

func TestEveryConsoleClientCallHitsARegisteredRoute(t *testing.T) {
	routed := walkEveryRoute(t)
	calls, unresolved, rawFetches := parseConsoleClientCalls(t, filepath.Join("..", "..", "web", "src"))

	if len(calls) == 0 {
		t.Fatal("parsed ZERO client calls — the parser stopped discriminating and this test would pass vacuously")
	}
	seams := loadConsoleSeams(t)

	// NO SILENT CAPS: a call site the parser could not resolve is REPORTED, never
	// dropped. If this grows, the parser — not the coverage — is what regressed.
	for _, u := range unresolved {
		t.Logf("unresolved call site (not checked): %s:%d %s", u.file, u.line, u.path)
	}
	assertUnresolvedAreDeclared(t, unresolved)

	declared := map[string]consoleSeam{}
	for _, s := range seams {
		declared[s.Method+" "+s.Path] = s
	}
	used := map[string]bool{}

	var missing []string
	for _, c := range calls {
		key := c.method + " " + c.path
		if routed[key] {
			continue
		}
		if s, ok := declared[key]; ok {
			used[key] = true
			t.Logf("declared seam (not mounted, by decision): %s — %s [%s]", key, s.Reason, s.Owner)
			continue
		}
		missing = append(missing, key+"   called at "+c.file+":"+strconv.Itoa(c.line))
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("the console calls a route the engine does not register: %s\n"+
			"      an operator gets a 404 here. Mount it, or declare it in %s with a reason.", m, consoleSeamsPath)
	}

	// A seam that is now MOUNTED must be removed from the register, or the register
	// rots into a list of things that used to be broken.
	for key, s := range declared {
		if routed[key] {
			t.Errorf("%s is declared as an unmounted seam in %s but IS registered now — "+
				"delete the entry (reason on file: %q)", key, consoleSeamsPath, s.Reason)
			continue
		}
		if !used[key] {
			t.Errorf("%s is declared as a seam in %s but NO console client calls it — "+
				"delete the entry; a register of imaginary seams hides the real ones", key, consoleSeamsPath)
		}
	}
	t.Logf("%d client call(s) checked against %d registered route(s); %d declared seam(s)",
		len(calls), len(routed), len(seams))

	// Las llamadas con `fetch` crudo, comprobadas AGNÓSTICAS DEL MÉTODO (ver consoleFetchRe).
	rutasRegistradas := map[string]bool{}
	for k := range routed {
		if i := strings.Index(k, " "); i >= 0 {
			rutasRegistradas[k[i+1:]] = true
		}
	}
	declaradas := map[string]bool{}
	for _, sm := range seams {
		declaradas[sm.Path] = true
	}
	var huerfanas []string
	for _, f := range rawFetches {
		if rutasRegistradas[f.path] || declaradas[f.path] {
			continue
		}
		huerfanas = append(huerfanas, f.path+"   llamada con fetch en "+f.file+":"+strconv.Itoa(f.line))
	}
	sort.Strings(huerfanas)
	for _, h := range huerfanas {
		t.Errorf("la consola llama con `fetch` a una ruta que el motor no registra: %s\n"+
			"      el operador ve un 404. Móntala, o decláralaen %s con su motivo.", h, consoleSeamsPath)
	}
	t.Logf("%d llamada(s) con `fetch` crudo comprobadas (agnósticas del método)", len(rawFetches))

	logConsoleCoverageByNamespace(t, routed, calls)
}

// logConsoleCoverageByNamespace imprime, POR NAMESPACE, cuántas rutas registra el motor y a
// cuántas llega la consola.
//
// ⛔ POR QUÉ EXISTE. El backlog lleva estos porcentajes a mano —«sessions 23, evals 16, finops 13,
//
//	… m/posture (0 %)»— y se pudren en silencio, porque nada los re-deriva. Medido el 2026-08-18:
//	`m/posture` figuraba al 0 % y tiene UNA ruta (`/export`) con UN llamante
//	(`web/src/features/posture-export/api.ts:87`) y su vista montada. Es decir: 100 %.
//
//	No fue el único. Ese mismo día cinco afirmaciones del backlog salieron falsas al medirlas, y
//	una de ellas venía marcada «verificado». Un número copiado a mano en un documento no envejece
//	ruidosamente: envejece pareciendo vigente, y su coste es que alguien construya lo ya hecho o
//	dé por bloqueado lo que funciona.
//
// ⚠ NO FALLA, INFORMA. Un namespace al 0 % puede ser correcto —una superficie de servidor a
//
//	servidor no tiene por qué tener consola— y este test no sabe cuál es cuál. Lo que hace es que
//	la cifra salga del CÓDIGO en cada corrida, para que nadie tenga que copiarla.
func logConsoleCoverageByNamespace(t *testing.T, routed map[string]bool, calls []clientCall) {
	t.Helper()
	ns := func(key string) string {
		// key es "MÉTODO /v1/m/<ns>/…" o "MÉTODO /v1/<otro>/…"
		i := strings.Index(key, " /")
		if i < 0 {
			return "?"
		}
		partes := strings.Split(strings.TrimPrefix(key[i+1:], "/"), "/")
		if len(partes) >= 3 && partes[0] == "v1" && partes[1] == "m" {
			return "m/" + partes[2]
		}
		if len(partes) >= 2 && partes[0] == "v1" {
			return partes[1]
		}
		return "?"
	}
	total := map[string]int{}
	for k := range routed {
		total[ns(k)]++
	}
	llamadas := map[string]map[string]bool{}
	for _, c := range calls {
		k := c.method + " " + c.path
		if !routed[k] {
			continue
		}
		n := ns(k)
		if llamadas[n] == nil {
			llamadas[n] = map[string]bool{}
		}
		llamadas[n][k] = true
	}
	nombres := make([]string, 0, len(total))
	for n := range total {
		nombres = append(nombres, n)
	}
	// Ascendente por cobertura: lo menos cubierto primero, que es lo que hay que mirar.
	sort.Slice(nombres, func(i, j int) bool {
		ci := float64(len(llamadas[nombres[i]])) / float64(total[nombres[i]])
		cj := float64(len(llamadas[nombres[j]])) / float64(total[nombres[j]])
		if ci != cj {
			return ci < cj
		}
		return nombres[i] < nombres[j]
	})
	// ⛔ EL PORCENTAJE ES UN SUELO, NO UNA MEDIDA, y decirlo aquí evita que la campaña C07-04 se
	//    trabaje sobre una premisa falsa. Medido el 2026-08-19 abriendo dos namespaces:
	//
	//      · `m/sessions` daba 38/59 (64 %) y sus 21 «sin llamar» son el ciclo de vida de work-items
	//        —lease acquire/renew/release/takeover/revoke, transiciones, asignaciones, dependencias—.
	//        La consola las llama TODAS: `web/src/features/work/api.ts:568` construye
	//        `${WORK_ITEMS}/${id(...)}/lease/acquire` y despacha por `intent.path`.
	//      · `console` daba 31/57 y parte de sus 26 se alcanzan por `ssoConfigPath(scope, alias)`.
	//
	//    Los DOS ficheros están DECLARADOS en `consoleUnresolvedSites`, así que el instrumento ya
	//    nombra su punto ciego — pero el porcentaje no lo reflejaba, y «m/sessions 64 %» se lee como
	//    «faltan 21 pantallas» cuando no falta ninguna de ésas.
	//
	// ⇒ Se imprime la lista de sitios declarados junto al censo. Quien lea un namespace bajo debe
	//   comprobar primero si su feature está aquí: si lo está, el número NO es cobertura.
	declarados := make([]string, 0, len(consoleUnresolvedSites))
	for f := range consoleUnresolvedSites {
		declarados = append(declarados, f)
	}
	sort.Strings(declarados)
	t.Logf("⚠ %d fichero(s) con sitios de llamada DECLARADOS irresolubles: las rutas que alcanzan NO",
		len(declarados))
	t.Logf("  pueden aparecer como cubiertas, así que los porcentajes de abajo son un SUELO:")
	for _, f := range declarados {
		t.Logf("      %s", f)
	}
	t.Logf("cobertura de consola por namespace (derivada, no copiada):")
	for _, n := range nombres {
		usadas := len(llamadas[n])
		t.Logf("  %-24s %3d/%3d  %3.0f%%", n, usadas, total[n],
			100*float64(usadas)/float64(total[n]))
	}

	// ⛔ Y LAS RUTAS QUE FALTAN, POR NOMBRE. El porcentaje dice el TAMAÑO del hueco y no dice CUÁL
	//    es, y C07-04 —«resto por namespaces»— no se puede trabajar con un tamaño: alguien tiene que
	//    abrir la ruta concreta y decidir si merece superficie o si es de CLI por diseño. Con sólo
	//    `console 31/57` la única forma de avanzar era re-derivar la lista a mano en cada sesión.
	//
	//    Es la misma conversión que el hub ratificó como convención del backlog: «una cuenta describe
	//    el tamaño de un problema; sólo una lista lo identifica».
	//
	// ⚠ Y SIGUE SIENDO UNA LISTA DE CANDIDATOS, NO DE DEFECTOS. Que la consola no llame a una ruta
	//   NO la convierte en un hueco: hay rutas de CLI, de operador y de integración que no deben
	//   tener pantalla. La lista sirve para ABRIRLAS una a una, que es el trabajo; el instrumento no
	//   adjudica y este comentario existe para que nadie lea el volcado como una lista de defectos.
	for _, n := range nombres {
		if len(llamadas[n]) == len(total) && total[n] == len(llamadas[n]) {
			continue
		}
		faltan := make([]string, 0, total[n])
		for k := range routed {
			if ns(k) != n || llamadas[n][k] {
				continue
			}
			faltan = append(faltan, k)
		}
		if len(faltan) == 0 {
			continue
		}
		sort.Strings(faltan)
		t.Logf("  %s — %d ruta(s) que la consola NO llama (candidatas, no defectos):", n, len(faltan))
		for _, k := range faltan {
			t.Logf("      %s", k)
		}
	}
}

const consoleSeamsPath = "../../scripts/console-route-seams.json"

// consoleUnresolvedSites DECLARA, por fichero y por FORMA, cada sitio de llamada que el
// parser no puede resolver. Sustituye a `consoleUnresolvedBudget`, que era un número.
//
// ⛔ POR QUÉ EL NÚMERO NO SERVÍA, y no es una opinión mía: lo decía su propio comentario.
// «A count cannot tell "three more of a known shape" from "one instance of a shape we have
// never seen", and the second is the one that matters.» Un contador tampoco distingue
// ARREGLADO de EXCLUIDO — si alguien deja de resolver una forma y a la vez resuelve otra, el
// total no se mueve y el instrumento calla. Y un presupuesto es, además, algo que se empuja:
// pasó de 11 a 14 el 2026-08-12, con motivo legítimo, pero el gesto de subirlo es el mismo
// que el de taparlo.
//
// QUÉ FALLA AHORA, que son tres cosas distintas y cada una con su mensaje:
//  1. una forma NUEVA, que nadie ha declarado — el caso que de verdad importa;
//  2. MÁS sitios de una forma ya conocida — legítimo a veces, pero se declara, no se asume;
//  3. una entrada declarada que YA NO aparece — para que la lista no se pudra afirmando
//     límites que dejaron de existir. Es la dirección que un presupuesto nunca comprueba.
//
// Las formas de aquí abajo son irresolubles POR CLASE, no por omisión del parser: un
// parámetro pegado dentro de un segmento, interpolaciones condicionales cuyas ramas no se
// pueden enumerar, ayudantes que reciben la ruta como ARGUMENTO y rutas que son un valor de
// tiempo de ejecución. Resolverlas pide enumeración de ramas, que es otra capacidad — no un
// número más pequeño.
// consoleMachineFacing declara, POR PREFIJO Y CON SU RAZÓN, las rutas cuyo cliente no es un
// operador. Medidas una a una el 2026-08-19, no supuestas por namespace.
var consoleMachineFacing = map[string]string{
	"/.well-known/": "documentos de descubrimiento: los lee un cliente OAuth/AuthZEN, no una persona",
	"/healthz":      "sonda de kubernetes",
	"/livez":        "sonda de kubernetes",
	"/readyz":       "sonda de kubernetes",
	"/pod-readyz":   "sonda de kubernetes",
	"/metrics":      "scrape de Prometheus",
	"/openapi":      "la especificación de la API, servida a herramientas",
	"/v1/ssf/":      "Shared Signals ENTRANTE: lo empuja el IdP",

	// ⛔ LAS PRIMITIVAS DEL ROSTER, y la consola hace BIEN en no llamarlas. Medido el 2026-08-19
	// siguiendo la cadena entera, porque parecían dos huecos y no lo son:
	//
	//   `PutConnector`    sella los secretos del operador y LUEGO llama a `PutSource`
	//                     (`connectoronboard.go:370-374`)
	//   `DeleteConnector` llama a `DeleteSource` y DESPUÉS limpia las credenciales que esa
	//                     fuente poseía (`connectoronboard.go:440-455`)
	//
	// ⇒ `PUT`/`DELETE /v1/console/sources` son las primitivas que esas dos ENVUELVEN. Llamarlas
	// desde la consola saltaría el sellado —`SourceRosterInput.Config` espera REFERENCIAS, nunca
	// un secreto literal— y dejaría credenciales huérfanas en el almacén. La pestaña de conectores
	// ya ofrece editar y borrar sobre estas mismas entradas (`connectors-tab.tsx`: `editing` y
	// `del` son `SourceRosterEntry`), sólo que por la ruta que SÍ sella y SÍ limpia.
	//
	// El `GET` de esa colección NO está aquí a propósito: la consola lo llama y debe llamarlo.
	// Por eso las claves van cualificadas por método.
	"PUT /v1/console/sources":    "primitiva del roster: la consola usa PUT /v1/console/connectors, que sella los secretos antes de llamarla",
	"DELETE /v1/console/sources": "primitiva del roster: la consola usa DELETE /v1/console/connectors, que además limpia las credenciales que la fuente poseía",

	// ⛔ AQUÍ HABÍA `/v1/scim/`, `/access/v1/` y `/v1/access-edges`, Y LA GUARDA LOS TUMBÓ EN EL
	// PRIMER INTENTO. La consola SÍ llama a `GET /v1/scim/v2/Users`, `PATCH .../Users/{}`,
	// `GET .../ServiceProviderConfig`, `POST /access/v1/search/{subject,resource}` y
	// `POST /access/v1/access-review/export`: son superficies de consola, no de máquina. Clasificar
	// un namespace ENTERO por su protocolo era la suposición cómoda, y era falsa — la misma forma
	// que emparejar consola y motor por NOMBRE en vez de por RUTA. Lo que queda declarado está
	// medido ruta a ruta, y las de SCIM que la consola no llama vuelven al hueco: que algunas se
	// llamen prueba que ese namespace SÍ tiene superficie, así que las demás son trabajo abierto,
	// no infraestructura.
	// ── SCIM, ruta a ruta y CON EL MÉTODO, porque declarar el namespace entero ya me lo tumbó la
	// guarda: la consola llama `GET /Users`, `PATCH /Users/{}` y `GET /ServiceProviderConfig`.
	// Su modelo de pertenencia es invitar → censo → activar (`/v1/invites`, `/v1/members` y ese
	// PATCH). El APROVISIONAMIENTO —crear, reemplazar y borrar usuarios y grupos— es del IdP por
	// RFC 7644, y los documentos de descubrimiento los lee un cliente SCIM.
	//
	// ⚠ Y SE DEJAN FUERA LAS LECTURAS a propósito: `GET /Users/{}`, `GET /Groups` y `GET /Groups/{}`
	// siguen contando como hueco, porque una vista de detalle o un censo de grupos SÍ son pantalla
	// plausible. Declararlas también sería cómodo y convertiría este mapa en una supresión.
	"GET /v1/scim/v2/Schemas":       "documento de descubrimiento SCIM (RFC 7644 §4): lo lee un cliente SCIM",
	"GET /v1/scim/v2/ResourceTypes": "documento de descubrimiento SCIM (RFC 7644 §4): lo lee un cliente SCIM",
	"POST /v1/scim/v2/Users":        "aprovisionamiento: lo escribe el IdP; la consola invita por /v1/invites",
	"PUT /v1/scim/v2/Users":         "aprovisionamiento: lo escribe el IdP",
	"DELETE /v1/scim/v2/Users":      "desaprovisionamiento: lo escribe el IdP; la consola desactiva por PATCH",
	"POST /v1/scim/v2/Groups":       "aprovisionamiento de grupos: lo escribe el IdP",
	"PUT /v1/scim/v2/Groups":        "aprovisionamiento de grupos: lo escribe el IdP",
	"PATCH /v1/scim/v2/Groups":      "aprovisionamiento de grupos: lo escribe el IdP",
	"DELETE /v1/scim/v2/Groups":     "aprovisionamiento de grupos: lo escribe el IdP",
	"POST /v1/scim/v2/Events":       "señales SCIM ENTRANTES: las empuja el IdP",

	// El CANJE de una aprobación lo hace la máquina, no el operador: la consola APRUEBA
	// (`${BASE}/approvals` list/create/get/sweep en `features/governance/api.ts:102-122`) y el
	// PUENTE canjea el token — `cmd/olivares/approvalbridge.go:514` y `:554` son sus dos únicos
	// llamantes, con `file:line`, no por parecido de nombre.
	// ⚠ La clave es la ruta COMPLETA del canje, no `…/approvals`: escrita corta, el prefijo se
	// tragaba `cancel`, `decisions` y `sweep`, que la consola SÍ llama — y la guarda lo dijo
	// nombrando las cuatro. Tercera vez hoy que me caza una declaración demasiado ancha.
	"POST /v1/m/governance/approvals/{}/consume": "el canje lo hace el puente (approvalbridge.go:554), no un operador",
	"POST /v1/m/governance/breakglass/consume":   "lo canjea el puente (approvalbridge.go:514); la consola concede, no consume",

	"/v1/auth/federation/": "arranque y retorno de federación: son NAVEGACIONES del navegador, no XHR",
	"/v1/auth/token":       "endpoints de token OAuth: máquina a máquina",
}

var consoleUnresolvedSites = map[string]map[string]int{
	"web/src/features/catalog/api.ts": {
		"/v1/m/catalog/{}-admissions (parameter glued to a literal within one segment)": 1,
	},
	"web/src/features/compliance/api.ts": {
		// postDocument<T>(path: string, …) — el ayudante recibe la ruta como argumento.
		// Eran 3 y son 1: las otras dos (`:198` y `:650`) eran JSDoc que EXPLICA la API,
		// no código que la llama. La que queda es real — el interior de `postDocument`,
		// cuyo `path` es un parámetro; sus llamantes SÍ se resuelven desde 2026-08-19.
		"path (identifier is not a literal path constant)": 1,
	},
	"web/src/features/console/api.ts": {
		// ⛔ AQUÍ HABÍA DOS ENTRADAS Y EL PARSER YA LAS RESUELVE. Eran ternarios cuyas DOS ramas
		//    son literales, y eso son rutas concretas y no una incógnita: se enumeran en dos. Sus
		//    rutas salían como «sin superficie de consola» cuando la consola las llama — ceguera
		//    evitable que además contagiaba el número que todo el mundo lee.
		//    Lo que NO se enumera y sigue declarándose: una rama que no sea literal, o más de dos
		//    condicionales en la misma plantilla — 2^n inventaría rutas que nadie escribió.
		// ⭐ LOS CINCO SITIOS DE `ssoConfigPath` YA NO ESTÁN, y el censo lo exigió: al resolverlos
		// dio DECLARACIÓN PODRIDA, que es su tercer modo de fallo —una entrada declarada que ya
		// no aparece— y el que un simple contador nunca habría notado. Eran DIECIOCHO rutas
		// `/v1/console/sso/*` contadas como «sin superficie» que la consola llama todas.
	},
	// El helper COMPARTIDO de flujos con una ruta que sale de OTRO fichero. No es una ceguera
	// nueva del parser: es la misma de siempre —`pathFns` se construye por fichero— dicha en voz
	// alta en vez de dejar sus rutas calladas en el cubo de «sin superficie».
	"web/src/features/agentops/attach.ts": {
		"runAttachPath(runRef) (stream helper: path from a CROSS-FILE helper, not resolvable here)": 1,
	},
	"web/src/features/sandbox/stream.ts": {
		"runStreamPath(runId) (stream helper: path from a CROSS-FILE helper, not resolvable here)": 1,
	},
	"web/src/features/work/api.ts": {
		// intent.path es un valor de tiempo de ejecución: la intención decide la ruta.
		"intent (identifier is not a literal path constant)": 3,
	},
}

// assertUnresolvedAreDeclared compara lo observado con lo declarado en las DOS direcciones.
// Sin la segunda, la lista envejecería afirmando límites que ya no existen — que es
// exactamente lo que un presupuesto hace, y por lo que se cambia.
func assertUnresolvedAreDeclared(t *testing.T, unresolved []clientCall) {
	t.Helper()

	observado := map[string]map[string]int{}
	for _, u := range unresolved {
		f := filepath.ToSlash(u.file)
		if i := strings.Index(f, "web/src/"); i >= 0 {
			f = f[i:]
		}
		if observado[f] == nil {
			observado[f] = map[string]int{}
		}
		observado[f][u.path]++
	}

	for f, formas := range observado {
		for forma, n := range formas {
			esperado, ok := consoleUnresolvedSites[f][forma]
			if !ok {
				t.Errorf("FORMA NUEVA sin declarar en %s: %q (%d sitio(s)).\n"+
					"Es el caso que este control existe para ver: una clase de ruta que el parser "+
					"no sabe plegar y que nadie ha mirado. Decláralo en consoleUnresolvedSites con "+
					"su motivo, o enseña al parser a resolverlo — pero no lo dejes contarse solo.",
					f, forma, n)
				continue
			}
			if n != esperado {
				t.Errorf("%s: la forma %q aparece %d vez/veces, declaradas %d.\n"+
					"No es una forma nueva, es la MISMA creciendo (o encogiendo). Si crece por un "+
					"motivo, decláralo aquí; si encoge, baja el número: una declaración que sobra "+
					"afirma un límite que ya no existe.",
					f, forma, n, esperado)
			}
		}
	}

	for f, formas := range consoleUnresolvedSites {
		for forma, esperado := range formas {
			if observado[f][forma] == 0 {
				t.Errorf("DECLARACIÓN PODRIDA en %s: %q se declara con %d sitio(s) y ya no aparece "+
					"ninguno. Quítala. Una lista que sólo se puede ampliar es un presupuesto con "+
					"otro nombre.", f, forma, esperado)
			}
		}
	}
}

// walkEveryRoute mounts the production module set on a real server and walks chi for
// EVERY route — core and module alike, which is what a console client can reach.
func walkEveryRoute(t *testing.T) map[string]bool {
	t.Helper()
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	set, err := buildModules(signer, nil, nil, nil, nil, sourcesConfig{}, log)
	if err != nil {
		t.Fatalf("build modules: %v", err)
	}
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"},
		func(store.ExtensionRegistry) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, e := sys.EnsureSystemTenant(ctx)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token")),
		Logger: log, Version: "test", Modules: set.all,
	})
	if err != nil {
		t.Fatal(err)
	}
	router, ok := srv.Handler().(chi.Routes)
	if !ok {
		t.Fatal("handler is not a chi router")
	}
	routed := map[string]bool{}
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routed[method+" "+normalisePath(route)] = true
		return nil
	}); err != nil {
		t.Fatalf("walking the router: %v", err)
	}
	if len(routed) == 0 {
		t.Fatal("the router walk found no routes at all; this test would pass vacuously")
	}
	return routed
}

// normalisePath reduces a path to its SHAPE: every parameter — chi's {id}, OpenAPI's
// {tenant}, or a client's ${encodeURIComponent(ref)} — becomes {}, and a trailing slash
// is dropped. What a client's URL actually depends on is the shape, not the spelling.
func normalisePath(p string) string {
	var b strings.Builder
	depth := 0
	for _, c := range p {
		switch {
		case c == '{':
			if depth == 0 {
				b.WriteString("{}")
			}
			depth++
		case c == '}':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			b.WriteRune(c)
		}
	}
	out := b.String()
	if len(out) > 1 {
		out = strings.TrimSuffix(out, "/")
	}
	return out
}

// parseConsoleClientCalls reads the hand-written typed clients and returns the concrete
// (method, path-shape) pairs they call, plus the sites it could not resolve.
func parseConsoleClientCalls(t *testing.T, root string) (calls []clientCall, unresolved []clientCall, rawFetches []clientCall) {
	// ⛔ LISTA, NO CONTADOR — la misma lección que `consoleUnresolvedSites` en este mismo fichero.
	//    Un número dice cuántas llamadas no se comprueban y no dice CUÁLES, así que nadie puede ir
	//    a mirarlas ni notar que cambian de sitio.
	var fetchNoComprobadas []string
	enComentarios := 0
	defer func() {
		if len(fetchNoComprobadas) == 0 {
			return
		}
		if enComentarios > 0 {
			// Se DICE, no se calla: un parser que descarta en silencio es la misma clase de
			// ceguera que contar prosa como llamada, sólo que del otro lado.
			t.Logf("%d coincidencia(s) con forma de llamada IGNORADAS por estar en un comentario "+
				"(JSDoc que explica la API, no código que la llama)", enComentarios)
		}
		sort.Strings(fetchNoComprobadas)
		t.Logf("%d llamada(s) con `fetch` NO comprobadas, con nombre:", len(fetchNoComprobadas))
		for _, x := range fetchNoComprobadas {
			t.Logf("  %s", x)
		}
	}()
	t.Helper()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		name := info.Name()
		if !strings.HasSuffix(name, ".ts") && !strings.HasSuffix(name, ".tsx") {
			return nil
		}
		// Tests describe calls they mock; generated clients are covered by the
		// document-vs-router guards. Neither is a hand-written call site.
		if strings.Contains(name, ".test.") || strings.Contains(name, ".gen.") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := directAPIFetchesAsHTTP(string(src))
		consts := map[string]string{}
		for _, m := range constRe.FindAllStringSubmatch(text, -1) {
			consts[m[1]] = m[2]
		}
		// Constants built from other constants, then flattened. Registered after the
		// single-quoted ones so a plain literal always wins a name clash.
		for _, m := range tmplConstRe.FindAllStringSubmatch(text, -1) {
			if _, taken := consts[m[1]]; !taken {
				consts[m[1]] = m[2]
			}
		}
		// Mapas de rutas hermanas (`const M: Record<K, string> = {…}`), resueltos DESPUÉS de las
		// constantes porque sus valores interpolan alguna (`${BASE}/…`).
		pathMaps := mapaDeRutas(text, consts)
		// Envoltorios de transporte: se reescribe su INVOCACIÓN como el `http.<verbo>` que de
		// verdad hace, para que el resto de la maquinaria —plantillas, constantes, pathFns,
		// ternarios literales y la cola de normalización— resuelva sus llamantes sin duplicar
		// nada. La DEFINICIÓN se protege antes: reescribirla la convertiría en
		// `function http.post(` y volvería a producir el irresoluble que esto viene a quitar.
		for _, m := range transportFnRe.FindAllStringSubmatch(text, -1) {
			nombre, param, cuerpo := m[1], m[2], m[3]
			verbo, ok := transporteVerbo(cuerpo, param)
			if !ok {
				continue
			}
			// ⛔ LA MARCA NO PUEDE CONTENER EL NOMBRE, y la primera versión sí lo llevaba: el
			// regex de llamada casaba DENTRO de la marca y reescribía la definición igual,
			// añadiendo un irresoluble nuevo en vez de quitarlo (compliance pasó de 3 a 4).
			marca := "\x00TRANSPORTE\x00"
			text = strings.ReplaceAll(text, "function "+nombre, marca)
			llamada := regexp.MustCompile(`\b` + regexp.QuoteMeta(nombre) + `\s*(?:<[^(]*?>)?\s*\(`)
			destino := "http." + verbo + "("
			if verbo == "fetch" {
				destino = "fetch("
			}
			text = llamada.ReplaceAllString(text, destino)
			text = strings.ReplaceAll(text, marca, "function "+nombre)
		}
		// Helpers cuyo cuerpo da un CONJUNTO de rutas (consts + ternarios). Se registran aparte
		// de `pathFns` para que un helper de una sola ruta siga resolviéndose como siempre.
		type helperMulti struct {
			params []string
			set    []string
		}
		pathFnsMulti := map[string]helperMulti{}
		for _, m := range pathFnMultiRe.FindAllStringSubmatch(text, -1) {
			set := pathSetFor(m[3])
			if len(set) == 0 {
				continue
			}
			var params []string
			for _, p := range strings.Split(m[2], ",") {
				p = strings.TrimSpace(p)
				if i := strings.IndexAny(p, "?:"); i >= 0 {
					p = strings.TrimSpace(p[:i])
				}
				if p != "" {
					params = append(params, p)
				}
			}
			pathFnsMulti[m[1]] = helperMulti{params: params, set: set}
		}
		// ⛔ LOS ARGUMENTOS ACOTAN LAS RAMAS, y omitirlo produjo rutas INVENTADAS: con
		// `${ssoConfigPath(scope)}/idps` emití las cuatro salidas del helper, incluidas las que
		// dependen de `alias`, y salió `/v1/console/sso/idps/{}/idps`. Lo cazó el test hermano
		// —«la consola llama a una ruta que el motor no registra»— en la primera corrida, que es
		// justo por lo que esa red existe: sobre-emitir no puede pasar callado.
		ramasDe := func(h helperMulti, args string) []string {
			n := 0
			if strings.TrimSpace(args) != "" {
				n = len(strings.Split(args, ","))
			}
			var out []string
			for _, r := range h.set {
				posible := true
				for i := n; i < len(h.params); i++ {
					if strings.Contains(r, h.params[i]) {
						posible = false // depende de un parámetro que esta llamada no pasa
						break
					}
				}
				if posible {
					out = append(out, r)
				}
			}
			return out
		}
		// Fábrica de intents: cada `path = …` se reescribe como la llamada `http.<verbo>(…)` que
		// ese caso produce, con el verbo del propio caso (o el `let method` por defecto). De atrás
		// hacia delante, para que los desplazamientos sigan valiendo mientras se sustituye.
		if d := intentDefaultRe.FindStringSubmatchIndex(text); d != nil {
			porDefecto := text[d[2]:d[3]]
			asignaciones := intentPathRe.FindAllStringSubmatchIndex(text, -1)
			verbos := intentMethodRe.FindAllStringSubmatchIndex(text, -1)
			for i := len(asignaciones) - 1; i >= 0; i-- {
				verbo := porDefecto
				sigue := len(text)
				if i+1 < len(asignaciones) {
					sigue = asignaciones[i+1][0]
				}
				for _, v := range verbos {
					if v[0] > asignaciones[i][1] && v[0] < sigue {
						verbo = text[v[2]:v[3]]
					}
				}
				// ⛔ LA INTERPOLACIÓN CON LLAMADA SE NORMALIZA AQUÍ, y no después: contiene
				// comillas (`${id(args.itemId ?? '')}`) y la captura del argumento del regex de
				// llamada se corta en ellas — la ruta salía TRUNCADA como
				// `/v1/m/sessions/work-items/${}`, perdiendo `/lease/acquire`, y se declaraba
				// «parámetro pegado». Un `${CONST}` a secas se deja intacto para que la
				// resolución de constantes siga funcionando.
				expr := interpConLlamadaRe.ReplaceAllString(text[asignaciones[i][2]:asignaciones[i][3]], "{}")
				text = text[:asignaciones[i][0]] + "http." + strings.ToLower(verbo) + "(" + expr + ")" + text[asignaciones[i][1]:]
			}
		}
		pathFns := map[string]string{}
		for _, m := range pathFnRe.FindAllStringSubmatch(text, -1) {
			name := m[1]
			if name == "" {
				name = m[2]
			}
			pathFns[name] = m[3]
		}
		resolveConsts(consts)
		for name, body := range pathFns {
			pathFns[name] = simpleInterpRe.ReplaceAllStringFunc(body, func(s string) string {
				if v, ok := consts[simpleInterpRe.FindStringSubmatch(s)[1]]; ok {
					return v
				}
				return s
			})
		}
		// Substitute a helper INVOCATION wherever it appears: ${nhiPath(ref)}/events
		// becomes the helper body followed by /events.
		expandFns := func(s string) string {
			return callFnRe.ReplaceAllStringFunc(s, func(m string) string {
				if body, ok := pathFns[callFnRe.FindStringSubmatch(m)[1]]; ok {
					return body
				}
				return m
			})
		}
		rel, _ := filepath.Rel(filepath.Join("..", ".."), path)
		// Las llamadas con `fetch` crudo, con la MISMA resolución de constantes y ayudantes que las
		// tipadas — lo único que cambia es que no se les atribuye verbo.
		for _, loc := range consoleStreamRe.FindAllStringSubmatchIndex(text, -1) {
			if enComentario(text, loc[0]) {
				enComentarios++
				continue
			}
			raw := text[loc[2]:loc[3]]
			p := raw[1 : len(raw)-1]
			if strings.HasPrefix(raw, "`") {
				p = expandFns(p)
			}
			// ⛔ LAS INTERPOLACIONES SE COLAPSAN ANTES DE CORTAR POR `?`, Y NO ES ORDEN ARBITRARIO.
			// Cortando primero, un `?` que vive DENTRO de una interpolacion —un ternario, o el `??`
			// de `${encodeURIComponent(ref ?? '')}`— trunca la ruta por la mitad y deja un `${` sin
			// cerrar. `interpRe` ya no casa (le falta el `}`) y `normalisePath` escribe el `$`
			// literal y se traga todo lo que sigue, INCLUIDO EL SEGMENTO FINAL.
			//
			// Medido el 2026-08-22 sobre `web/src/features/voice/session-surface.tsx:139`, que
			// llama a `/v1/m/voice/sessions/${…}/stream`: salia como
			// `/v1/m/voice/sessions/${}` —sin `/stream`— y el test acusaba al motor de no
			// registrar una ruta que registra en `modules/voice/api.go:27`. Un rojo cronico en
			// `main` que decia «el operador ve un 404» cuando no lo ve.
			//
			// Este fichero YA documenta el mismo defecto para la rama de `fetch` crudo (la nota de
			// `trailingInterpRe`, mas abajo) y lo arreglo alli. La rama de `stream` se quedo
			// cortando en crudo.
			p = interpRe.ReplaceAllString(p, "{}")
			p = strings.SplitN(p, "?", 2)[0]
			if !strings.HasPrefix(p, "/v1/") {
				continue
			}
			rawFetches = append(rawFetches, clientCall{
				method: "",
				path:   normalisePath(p),
				file:   rel,
				line:   1 + strings.Count(text[:loc[0]], "\n"),
			})
		}
		for _, loc := range consoleStreamOpaqueRe.FindAllStringSubmatchIndex(text, -1) {
			if enComentario(text, loc[0]) {
				enComentarios++
				continue
			}
			unresolved = append(unresolved, clientCall{
				method: "",
				path:   text[loc[2]:loc[3]] + " (stream helper: path from a CROSS-FILE helper, not resolvable here)",
				file:   rel,
				line:   1 + strings.Count(text[:loc[0]], "\n"),
			})
		}
		for _, loc := range consoleFetchRe.FindAllStringSubmatchIndex(text, -1) {
			if enComentario(text, loc[0]) {
				enComentarios++
				continue
			}
			raw := text[loc[2]:loc[3]]
			var p string
			if strings.HasPrefix(raw, "`") {
				p = expandFns(raw[1 : len(raw)-1])
				p = simpleInterpRe.ReplaceAllStringFunc(p, func(s string) string {
					name := simpleInterpRe.FindStringSubmatch(s)[1]
					if v, ok := consts[name]; ok {
						return v
					}
					return s
				})
			} else {
				p = raw[1 : len(raw)-1]
			}
			p = strings.SplitN(p, "?", 2)[0]
			if !strings.HasPrefix(p, "/v1/") {
				// Rutas relativas, URLs completas a otro host y plantillas que quedan sin
				// resolver no son afirmaciones sobre NUESTRO registro de rutas.
				//
				// ⛔ PERO SE CUENTAN, y esto es lo que separa este `continue` de un punto ciego.
				//    Medido por mutación: `fetch(`${BASE}/export${qs ? `?${qs}` : ''}`)` lleva
				//    backticks ANIDADOS y el regex de arriba corta en el primero, así que captura
				//    un fragmento desbalanceado que nunca empieza por `/v1/`. Con un typo plantado
				//    ahí a propósito el test seguía VERDE. La familia sigue sin comprobarse —un
				//    parser de plantillas anidadas dentro de un regex es peor remedio— pero deja de
				//    ser invisible: sale en el informe con su número.
				if strings.Contains(raw, "${") {
					fetchNoComprobadas = append(fetchNoComprobadas,
						"(no empieza por /v1/) "+rel+":"+strconv.Itoa(1+strings.Count(text[:loc[0]], "\n")))
				}
				continue
			}
			// MISMA normalización que las tipadas, y no una propia: `${encodeURIComponent(id)}`
			// tiene que acabar en `{}` como lo deja el recorrido del router. Sin el paso de
			// `interpRe` quedaba `${}` —con el dólar— y ONCE rutas correctas salían como
			// inexistentes. Un guardián nuevo que grita por lo correcto dura una semana.
			//
			// ⛔ Y ANTES DE ESO SE QUITA UN `${…}` FINAL, que es la forma de la CADENA DE CONSULTA
			//    en un `fetch` crudo: `fetch(`${BASE}/export${qs ? `?${qs}` : ''}`)`. El `?` vive
			//    DENTRO de la interpolación, así que el `SplitN(p,"?")` de arriba no lo ve y el
			//    sufijo acaba pegado al último segmento (`export{}`).
			//
			//    Medido por mutación, y por eso está escrito: la primera versión trataba eso como
			//    «parámetro pegado» y hacía `continue` — con un typo plantado a propósito
			//    (`/export` → `/exportt`) el test **SEGUÍA VERDE**. Un guardián nuevo que descarta
			//    en silencio justo la familia que venía a cubrir no cubre nada.
			p = trailingInterpRe.ReplaceAllString(p, "$1")
			shaped := normalisePath(interpRe.ReplaceAllString(p, "{}"))
			// Un parámetro PEGADO a texto literal dentro de un segmento es un enum que el router
			// escribe entero: `{}` no casaría. NO se descarta en silencio — se cuenta, para que el
			// número de comprobadas no incluya lo que no se pudo comprobar.
			if gluedParamSegment(shaped) {
				// ⚠ AQUÍ CAEN TAMBIÉN LAS PLANTILLAS CON BACKTICKS ANIDADOS, y por eso esta rama
				//    NO es una tecnicidad. `fetch(`${BASE}/export${qs ? `?${qs}` : ''}`)` se lee a
				//    medias —el regex corta en el backtick interno— y el fragmento queda pegado al
				//    último segmento. Medido por mutación: un typo plantado en esa ruta
				//    (`/export` → `/exportt`) NO se caza. La familia de exportaciones con cadena
				//    de consulta anidada sigue sin cubrir, y sale nombrada abajo en vez de
				//    desaparecer en un contador.
				fetchNoComprobadas = append(fetchNoComprobadas,
					shaped+"   "+rel+":"+strconv.Itoa(1+strings.Count(text[:loc[0]], "\n")))
				// ⛔ PERO LA RUTA SE CUENTA COMO ALCANZADA, y esto es doble contabilidad del mismo
				// punto ciego si no se hace: `/v1/m/knowledge/memory/export${}` no se puede
				// VERIFICAR —el sufijo es una cadena de consulta dentro de la interpolación— pero
				// la consola SÍ llama a esa ruta, y dejarla en el cubo de «sin superficie» dice
				// que falta una pantalla que existe. Es el mismo defecto con el que empecé el día,
				// reaparecido en la otra lista: una pata declara «no pude leer esto» y la otra
				// convierte esa ceguera en «falta producto».
				//
				// Se recorta SÓLO el `${}` FINAL, que es la forma medida; cualquier otra queda
				// como estaba. Y sigue saliendo nombrada arriba: alcanzada NO es verificada.
				if base := strings.TrimSuffix(shaped, "${}"); base != shaped && strings.HasPrefix(base, "/v1/") {
					rawFetches = append(rawFetches, clientCall{
						method: "", path: normalisePath(base), file: rel,
						line: 1 + strings.Count(text[:loc[0]], "\n"),
					})
				}
				continue
			}
			rawFetches = append(rawFetches, clientCall{
				method: "",
				path:   shaped,
				file:   rel,
				line:   1 + strings.Count(text[:loc[0]], "\n"),
			})
		}
		for _, loc := range consoleAPIRe.FindAllStringSubmatchIndex(text, -1) {
			if enComentario(text, loc[0]) {
				enComentarios++
				continue
			}
			method := strings.ToUpper(text[loc[2]:loc[3]])
			raw := text[loc[4]:loc[5]]
			line := 1 + strings.Count(text[:loc[0]], "\n")
			c := clientCall{method: method, file: rel, line: line}
			// ⛔ LA COLA DE NORMALIZACIÓN VA EN UN CIERRE Y SE APLICA IGUAL A TODAS LAS RAMAS.
			//    La primera versión enumeraba el ternario y hacía `continue`, saltándose esta
			//    cola entera: las ramas salían con la query pegada (`?acknowledge=true`) y con
			//    `${encodeURIComponent(id)}` sin normalizar a `{}`. El propio test lo cazó al
			//    instante, acusando al motor de no registrar rutas que sí registra. Duplicar la
			//    cola habría sido peor: dos copias del mismo predicado divergen.
			emitir := func(p string, c clientCall) {
				// A QUERY STRING is not part of the route.
				if i := strings.IndexByte(p, '?'); i >= 0 {
					p = p[:i]
				}
				// A remaining ${...} is a path PARAMETER; normalise it to the same {}
				// shape the router walk uses so a dynamic route still gets checked.
				shaped := normalisePath(interpRe.ReplaceAllString(p, "{}"))
				// A parameter GLUED to literal text in one segment (`${kind}-admissions`)
				// is an enum the router spells out in full (mcp-admissions,
				// connector-admissions). {} would never match those, so report instead of
				// crying wolf.
				if gluedParamSegment(shaped) {
					c.path = shaped + " (parameter glued to a literal within one segment)"
					unresolved = append(unresolved, c)
					return
				}
				c.path = shaped
				calls = append(calls, c)
			}
			var p string
			switch {
			case strings.HasPrefix(raw, "`"):
				p = expandFns(raw[1 : len(raw)-1])
				p = simpleInterpRe.ReplaceAllStringFunc(p, func(s string) string {
					name := simpleInterpRe.FindStringSubmatch(s)[1]
					if v, ok := consts[name]; ok {
						return v
					}
					return s
				})
			case strings.HasPrefix(raw, "'"):
				p = raw[1 : len(raw)-1]
			default:
				// http.get(WORK_ITEMS) or http.get(nhiPath(ref)) — the call regex captures
				// the bare name either way, so both maps are consulted.
				v, ok := consts[raw]
				if !ok {
					v, ok = pathFns[raw]
				}
				if !ok {
					// Un helper de CONJUNTO: cada ruta posible es una llamada, y todas pasan por
					// la misma cola de normalización. El test hermano las comprueba una a una
					// contra el registro del router, así que sobre-emitir no puede pasar callado.
					// El regex de llamada no captura los argumentos de esta forma, así que se
					// asume que los pasa TODOS: es lo más permisivo, y si alguna llamada pasa
					// menos, la ruta de más la caza el test hermano en voz alta.
					if h, hay := pathFnsMulti[raw]; hay {
						for _, r := range h.set {
							emitir(r, c)
						}
						continue
					}
				}
				if !ok {
					// `M[kind]` — un índice sobre un mapa de rutas hermanas. La clave es un valor
					// de tiempo de ejecución, así que se emiten TODAS: es lo más permisivo, y el
					// test hermano comprueba cada una contra el registro, de modo que una ruta de
					// más sale roja en vez de callada.
					base := raw
					if i := strings.IndexByte(base, '['); i > 0 {
						base = base[:i]
					}
					if set, hay := pathMaps[base]; hay {
						for _, r := range set {
							emitir(r, c)
						}
						continue
					}
				}
				if !ok {
					c.path = raw + " (identifier is not a literal path constant)"
					unresolved = append(unresolved, c)
					continue
				}
				p = v
			}
			// `${helper(args)}/idps`: se sustituye la invocación por CADA ruta del conjunto y se
			// emite una llamada por rama, con el resto de la plantilla intacto.
			if m := callFnRe.FindStringSubmatch(p); m != nil {
				if h, hay := pathFnsMulti[m[1]]; hay {
					for _, r := range ramasDe(h, m[2]) {
						emitir(strings.Replace(p, m[0], r, 1), c)
					}
					continue
				}
			}
			// ⛔ VA ANTES de la guarda de ruta absoluta, y ahí estaba el fallo de la primera
			// versión: `${ssoConfigPath(scope)}/idps` no empieza por `/`, así que la guarda lo
			// declaraba irresoluble y mi enganche, puesto después, no llegaba a ejecutarse nunca.
			if !strings.HasPrefix(p, "/") {
				c.path = p + " (did not resolve to an absolute path)"
				unresolved = append(unresolved, c)
				continue
			}
			// A CONDITIONAL inside the template picks between literal alternatives
			// (`${active ? 'enable' : 'disable'}`, `${ack ? '?acknowledge=true' : ''}`).
			// The router registers those literally, so a {} placeholder would not match
			// and would look like a missing route. We cannot enumerate the branches, so
			// we report instead of crying wolf. This is checked BEFORE the query strip
			// below, because the `?` of a ternary is not the `?` of a query string and
			// stripping at it truncates the path mid-expression.
			if esCondicionalInterp(p) {
				// Si TODAS las ramas son literales, son rutas concretas y se enumeran: la
				// consola las llama, y contarlas como no descubiertas es ceguera evitable.
				if ramas := expandLiteralTernaries(p); ramas != nil {
					for _, r := range ramas {
						emitir(r, c)
					}
					continue
				}
				c.path = p + " (conditional interpolation: cannot enumerate the branches)"
				unresolved = append(unresolved, c)
				continue
			}
			emitir(p, c)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return calls, unresolved, rawFetches
}

func loadConsoleSeams(t *testing.T) []consoleSeam {
	t.Helper()
	b, err := os.ReadFile(consoleSeamsPath)
	if err != nil {
		t.Fatalf("reading the declared-seam register %s: %v", consoleSeamsPath, err)
	}
	var doc struct {
		Seams []consoleSeam `json:"seams"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parsing %s: %v", consoleSeamsPath, err)
	}
	for i, s := range doc.Seams {
		if strings.TrimSpace(s.Reason) == "" || strings.TrimSpace(s.Owner) == "" {
			t.Errorf("seam %d (%s %s) has no reason/owner: an undeclared reason is an allow-list, "+
				"and an allow-list is how the next 404 gets in", i, s.Method, s.Path)
		}
	}
	return doc.Seams
}

// TestEveryEngineRouteHasAConsoleSurface es la dirección INVERSA, y es la que faltaba.
//
// ⛔ POR QUÉ NO BASTA CON LA DIRECTA NI CON EL VOLCADO. `TestEveryConsoleClientCallHitsARegisteredRoute`
//
//	pregunta «¿toda llamada de la consola llega a una ruta?» — una consola que no llamara a NADA la
//	aprueba perfectamente. Y `logConsoleCoverageByNamespace` publica el reparto, pero es un LOGGER
//	PURO: barrido de su cuerpo entero buscando `t.Errorf|t.Error|t.Fatal` ⇒ CERO. No puede fallar
//	nunca, así que una ruta nueva que aterrice sin superficie de consola no rompe nada y nadie se
//	entera.
//
//	Rescatado de `feature-el-contador-que-pasa-a-ser-lista` el 2026-08-19. Esa rama estaba a
//	905 commits de `main` y un triaje la dio por SUPERADA; su refutador demostró que la supersesión
//	era PARCIAL —main publicó la lista y no la gateó— y que retirarla perdía el único gate del
//	árbol en esta dirección. No se mergeó la rama, que habría regresado `main`: se porta el delta
//	vivo sobre el `main` de hoy, con la firma actual de `parseConsoleClientCalls`, que ahora
//	devuelve TRES valores y no dos.
//
// Es un TRINQUETE, no un umbral: el conjunto sin cubrir sólo puede ENCOGER. Exigir cero hoy sería un
// gate que nadie puede satisfacer, y un gate que nadie puede satisfacer es un gate que alguien
// apaga — que es como este repositorio perdió cobertura antes.
func TestEveryEngineRouteHasAConsoleSurface(t *testing.T) {
	routed := walkEveryRoute(t)
	// ⛔ `rawFetches` SE DESCARTABA CON UN `_`, y ese carácter escondía una clase entera: una
	// llamada con `fetch` crudo NO entraba en `calls`, así que toda ruta que la consola sólo
	// alcanza por ahí —las exportaciones y los flujos— se contaba como «sin superficie». El test
	// hermano SÍ las comprobaba contra el router; este las ignoraba. La misma medida, dos
	// respuestas, por una variable tirada.
	calls, unresolved, rawFetches := parseConsoleClientCalls(t, filepath.Join("..", "..", "web", "src"))

	// ⛔⛔ LAS DOS POBLACIONES SE SEPARAN, Y ESTE FICHERO LAS CONFUNDÍA. Medido el 2026-08-19, el
	//     mismo día que escribí este test: de las 160 «sin superficie», DIECISIETE eran las rutas
	//     `/v1/console/sso/*` — y la consola SÍ las llama, desde `sso-tab.tsx`, con
	//     `${ssoConfigPath(scope)}/idps`. La otra pata de este mismo fichero ya lo declara —
	//     «unresolved call site (not checked): console/api.ts:1156 … (did not resolve)»— y ésta
	//     las volvía a contar como descubiertas.
	//
	//     Es DOBLE CONTABILIDAD DEL MISMO PUNTO CIEGO, y el coste no es cosmético: leyendo «160»
	//     me puse a diseñar una pantalla de SSO **que ya existe**. La regla del canon §0-COBERTURA
	//     lo dice antes que yo: un gate dice lo que su DESCUBRIMIENTO alcanza, no lo que comprueba.
	//
	// ⚠ El trinquete sigue contando el TOTAL a propósito. Si contara sólo las «sin llamante», se
	//   podría bajar moviendo rutas al cubo de «ilegible» — que es empeorar el parser para aprobar
	//   el gate. Lo que cambia es el INFORME, no el umbral.
	// ⛔⛔ AQUÍ HUBO UN REPARTO POR NOMBRE DE FEATURE Y ERA FALSO. Mapeaba `features/<nombre>` al
	//     namespace de la ruta, y **no coinciden**: las 21 rutas de `m/sessions` las llama
	//     `web/src/features/work/api.ts` —feature «work», namespace «sessions»—, así que la
	//     heurística no ató nada y las contó como «sin llamante» cuando están cubiertas. Lo
	//     verifiqué a mano: ese fichero construye `${WORK_ITEMS}/${id(itemId)}/acceptance` y
	//     compañía, y el parser reporta CUATRO sitios irresolubles ahí.
	//
	//     ⇒ **La atribución no se puede hacer**, y fingirla es peor que no darla: un reparto que
	//     parece preciso invita a actuar sobre él. Ya me pasó dos veces en una hora — primero
	//     conté ceguera como ausencia, y luego «arreglé» eso con una heurística que erraba en el
	//     namespace MÁS GRANDE. Lo que queda es la verdad sin adornar: cuántas rutas no tienen
	//     llamada RESUELTA, cuántos sitios de llamada no se pudieron leer, y que cualquiera de
	//     las primeras puede estar cubierta por uno de los segundos.
	sitiosIlegibles := len(unresolved)

	// ⛔ CONTROL POSITIVO, y sin él este test es peor que no tenerlo: con CERO llamadas parseadas
	//    reportaría TODAS las rutas como descubiertas y se le creería.
	if len(calls) == 0 {
		t.Fatal("se parsearon CERO llamadas de cliente: este test daría todas las rutas por descubiertas y se le creería")
	}

	called := map[string]bool{}
	for _, c := range calls {
		called[c.method+" "+c.path] = true
	}

	byNamespace := map[string][]string{}
	// ⛔ HAY RUTAS QUE NO DEBEN TENER PANTALLA NUNCA, y contarlas como hueco no es un número
	// inflado: es una invitación a construirlas. Medido el 2026-08-19 sobre las 150 «sin llamada
	// resuelta», TREINTA Y CUATRO eran de máquina — SCIM (RFC 7644: el cliente es el IdP, no un
	// operador), las sondas de k8s, `/metrics`, los documentos OpenAPI, los `.well-known`, los
	// endpoints AuthZEN que llama un PEP, los de token OAuth y el SSF entrante. Nadie va a abrir
	// una pantalla para `/metrics`, y un informe que lo pide gasta sesiones en no-trabajo.
	//
	// ⚠ SE DECLARA CON RAZÓN Y ES FALSABLE POR LOS DOS LADOS, que es lo que la separa de una
	// supresión: un prefijo que ya no case NINGUNA ruta registrada cae (declaración podrida), y
	// una ruta declarada de máquina que SÍ tenga llamada de consola resuelta cae también —
	// entonces o la clasificación está mal o la consola llama a algo que no debería.
	maquinaTocada := map[string]bool{}
	// Una clave con ESPACIO es método+ruta (`POST /v1/scim/v2/Users`); sin él es sólo prefijo de
	// ruta. Hace falta la distinción porque en SCIM el MÉTODO decide de quién es el endpoint: el
	// `GET /Users` lo llama la consola y el `POST /Users` lo escribe el IdP.
	esDeMaquina := func(key string) (string, bool) {
		ruta := key
		if i := strings.IndexByte(key, ' '); i >= 0 {
			ruta = key[i+1:]
		}
		for pref, razon := range consoleMachineFacing {
			sujeto := ruta
			if strings.ContainsRune(pref, ' ') {
				sujeto = key
			}
			if strings.HasPrefix(sujeto, pref) {
				maquinaTocada[pref] = true
				return razon, true
			}
		}
		return "", false
	}
	for key := range routed {
		if _, ok := esDeMaquina(key); ok && called[key] {
			t.Errorf("%s está declarada de MÁQUINA y la consola SÍ la llama: o la clasificación "+
				"está mal, o la consola llama a algo que no le toca", key)
		}
	}
	for pref := range consoleMachineFacing {
		if !maquinaTocada[pref] {
			t.Errorf("el prefijo de máquina %q ya no casa ninguna ruta registrada: una declaración "+
				"podrida esconde huecos reales", pref)
		}
	}

	// Cobertura por `fetch` crudo, AGNÓSTICA DEL MÉTODO por la misma razón que la comprobación:
	// en un `fetch` el verbo vive en el objeto de init y suponerlo convierte un POST legítimo en
	// un falso rojo. Para la pregunta que este test hace —¿hay superficie de consola para esta
	// ruta?— una descarga por `fetch` a esa ruta ES la superficie.
	porFetch := map[string]bool{}
	for _, f := range rawFetches {
		porFetch[f.path] = true
	}
	rutaDe := func(key string) string {
		if i := strings.IndexByte(key, ' '); i >= 0 {
			return key[i+1:]
		}
		return key
	}

	uncovered, deMaquina, cubiertasPorFetch := 0, 0, 0
	for key := range routed {
		if called[key] {
			continue
		}
		if _, ok := esDeMaquina(key); ok {
			deMaquina++
			continue
		}
		if porFetch[rutaDe(key)] {
			cubiertasPorFetch++
			continue
		}
		uncovered++
		ns := "core"
		if parts := strings.Split(key, "/"); len(parts) > 3 && strings.HasPrefix(parts[1], "v1") {
			if parts[2] == "m" && len(parts) > 3 {
				ns = parts[3]
			} else {
				ns = parts[2]
			}
		}
		byNamespace[ns] = append(byNamespace[ns], key)
	}

	names := make([]string, 0, len(byNamespace))
	for ns := range byNamespace {
		names = append(names, ns)
	}
	sort.Slice(names, func(i, j int) bool {
		if len(byNamespace[names[i]]) != len(byNamespace[names[j]]) {
			return len(byNamespace[names[i]]) > len(byNamespace[names[j]])
		}
		return names[i] < names[j]
	})
	for _, ns := range names {
		sort.Strings(byNamespace[ns])
		t.Logf("sin llamada resuelta — %s (%d): %s", ns, len(byNamespace[ns]),
			strings.Join(byNamespace[ns], ", "))
	}
	if cubiertasPorFetch > 0 {
		t.Logf("%d ruta(s) cubiertas por `fetch` CRUDO (agnóstico del método): la consola las alcanza, "+
			"pero por descarga o flujo, no por el cliente tipado", cubiertasPorFetch)
	}
	if deMaquina > 0 {
		t.Logf("%d ruta(s) declaradas DE MÁQUINA y descontadas: su cliente no es un operador", deMaquina)
	}
	t.Logf("%d de %d ruta(s) registradas sin llamada de consola RESUELTA. El parser dejó %d sitio(s)"+
		" de llamada SIN RESOLVER: cualquiera de esas rutas puede estar cubierta por uno de ellos, y"+
		" esta prueba NO puede atribuirlos — la lista de arriba es un TECHO del hueco, no un censo",
		uncovered, len(routed), sitiosIlegibles)

	// ⛔ El control positivo que impide que esta advertencia se vuelva decorativa: si el parser no
	//    reportara NINGÚN sitio irresoluble, la frase de arriba sería falsa y el número se leería
	//    como un censo. Que los haya es una propiedad medida de este árbol — el test hermano
	//    mantiene un CENSO DECLARADO de sitios irresolubles, con su razón cada uno, justo porque
	//    los hay POR CLASE. (Era un número y lo sustituyó un mapa: un contador no dice CUÁLES.)
	if sitiosIlegibles == 0 {
		t.Fatal("cero sitios de llamada irresolubles: o el parser mejoró y esta advertencia sobra, " +
			"o dejó de discriminar; en ambos casos hay que mirarlo antes de creerse el número")
	}

	if uncovered > consoleUncoveredBudget {
		t.Errorf("%d rutas no tienen superficie de consola, trinquete %d: la superficie ENCOGIÓ, "+
			"o aterrizó una ruta nueva sin ella. La lista está arriba — este número no se sube "+
			"nunca para que pase", uncovered, consoleUncoveredBudget)
	}
}

// consoleUncoveredBudget es el trinquete: cuántas rutas registradas pueden no tener llamada de
// consola. Sólo BAJA. Subirlo para que pase un rojo es apagar el gate, y queda en el historial de
// quien lo suba.
//
// RE-MEDIDO el 2026-08-20 sobre `main@f862d9494` con #1124 y #1066 dentro y el bundle regenerado:
// **52** rutas registradas no tenian llamada de consola, y el trinquete se fijo en ESE numero, no
// en uno de los dos que traian los PR.
//
// Y VOLVIO A BAJAR ESE MISMO DIA, unas horas despues: `fc8a275ae` cubrio una ruta mas —la vista
// previa que el motor construia PARA la consola y que la consola nunca llamaba— y midio el censo
// en **51**. Esa, y no 52, es la cifra que documenta la constante de abajo.
//
// Los dos PR llegaron con un valor propio y NINGUNO era el medido: #1124 traia 56 y #1066 traia
// 53, y al fusionarlos el conflicto era literalmente esta linea. Elegir uno de los dos habria
// dejado el trinquete flojo por 4 o por 1 — y un trinquete flojo no avisa de la primera ruta que
// aterriza sin superficie, que es justo para lo que existe.
//
// Y la cifra que habia AQUI estaba obsoleta y se contradecia con la constante que documentaba:
// decia «160 de 848» sobre `main@66056c562` mientras la constante era 53. Si 160 fuese cierto hoy,
// este test estaria rojo. Una justificacion que no casa con el numero que justifica es peor que no
// tenerla: invita a subir el trinquete «para alinearlo con el comentario».
//
// ⛔ Y VOLVIO A PASAR EXACTAMENTE ESO, UN COMMIT DESPUES de escribirse el parrafo de arriba:
// `fc8a275ae` bajo la constante a 51 y dejo este bloque diciendo 52. Lo corrijo aqui, y dejo dicho
// lo que el episodio ensena, porque no es «hay que acordarse»: la cifra vive en DOS sitios —el
// texto y la constante— y sincronizarlos a mano falla incluso a quien acaba de escribir la
// advertencia. Si alguien vuelve a encontrar este bloque desalineado, la salida NUNCA es subir la
// constante para que case con el texto: es corregir el texto, que es lo unico de los dos que no
// gatea nada. El numero manda desde la constante; el comentario solo la explica.
//
// Verificado por mutacion el 2026-08-20 sobre el par 51/50: a 51 pasa; a 50 falla nombrando las
// rutas. Esa mutacion describe la GUARDA (`uncovered > consoleUncoveredBudget`), que no ha
// cambiado. El VALOR, en cambio, es una medida nueva: 38, re-medido el 2026-08-24 por
// `scripts/rebase-web-branch.sh` al rebasar sobre `main` — corre el censo y fija lo que mide, no
// hereda el numero de la rama. El par 38/37 NO se ha vuelto a mutar; lo mutado es el mecanismo.
//
// RE-MEDIDO el 2026-08-29 sobre main@6c202d028: el parser no veía las dos llamadas directas a
// `apiFetch` que mandan cuerpos crudos (`memory/import` y `restore/upload`). Al reconocerlas, el
// techo real baja de 39 a 37. Mutación testigo: retirar la llamada de importación lo devuelve a
// 38 y este gate falla contra 37; por eso 37, y no el presupuesto anterior, es el trinquete.
const consoleUncoveredBudget = 37

func TestDirectAPIFetchSurfaceMutation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "api.ts")
	const source = "const BASE = '/v1/m/knowledge'\n" +
		"export const importMemory = (raw: string) =>\n" +
		"  apiFetch<unknown>(`${BASE}/memory/import`, {\n" +
		"    method: 'POST',\n" +
		"    rawBody: raw,\n" +
		"  })\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	hasSurface := func() bool {
		calls, _, _ := parseConsoleClientCalls(t, root)
		for _, c := range calls {
			if c.method == "POST" && c.path == "/v1/m/knowledge/memory/import" {
				return true
			}
		}
		return false
	}
	if !hasSurface() {
		t.Fatal("la llamada directa a apiFetch debe contar como superficie POST")
	}

	// Mutante pedido: al quitar el transporte, la ruta deja de estar cubierta. Así se prueba la
	// negativa; un parser que inventara la misma superficie desde BASE pasaría el caso positivo.
	mutant := strings.Replace(source, "apiFetch<unknown>", "withoutConsoleTransport<unknown>", 1)
	if err := os.WriteFile(path, []byte(mutant), 0o600); err != nil {
		t.Fatal(err)
	}
	if hasSurface() {
		t.Fatal("el mutante sin transporte todavía aparece cubierto: la guarda inventa superficie")
	}
}

// transporteVerbo decide si un envoltorio local puede resolverse, y su rama importante —«manda la
// ruta con MÁS DE UN verbo, así que NO»— es hoy INALCANZABLE desde el árbol: ningún envoltorio
// ramifica el verbo pasando su propio parámetro (`send()` en features/work usa `intent.path`, un
// campo, así que ni siquiera casa la forma). Una rama que nadie puede disparar no está probada
// porque el test pase: se fabrica el caso. Verificado por mutación — relajar `!= 1` a `< 1` NO
// rompía nada antes de que existiera esta prueba.
func TestTransportWrapperRefusesABranchingVerb(t *testing.T) {
	unSoloVerbo := `
	  const opts = { headers }
	  return http.post<T>(ruta, undefined, opts)
	`
	if v, ok := transporteVerbo(unSoloVerbo, "ruta"); !ok || v != "post" {
		t.Fatalf("un envoltorio con UN verbo debe resolverse a post, dio (%q, %v)", v, ok)
	}

	// El caso que el árbol no tiene: el mismo parámetro sale por dos verbos distintos.
	dosVerbos := `
	  if (modo === 'parcial') {
	    return http.patch<T>(ruta, body, opts)
	  }
	  return http.post<T>(ruta, body, opts)
	`
	if v, ok := transporteVerbo(dosVerbos, "ruta"); ok {
		t.Fatalf("con DOS verbos hay que negarse: elegir uno inventaría llamadas que la consola "+
			"quizá no hace, y eso debilita la guarda que este fichero existe para sostener. Dio %q", v)
	}

	// Y el que no manda SU parámetro como ruta tampoco es un transporte.
	otraRuta := `return http.get<T>(OTRA_CONSTANTE, opts)`
	if _, ok := transporteVerbo(otraRuta, "ruta"); ok {
		t.Fatal("sólo es transporte si viaja SU propio parámetro como ruta")
	}
}

// pathSetFor tiene dos negativas que el árbol de hoy NO puede disparar —más de ocho ramas, y una
// rama que no es ruta absoluta—, y una negativa que nadie puede provocar no está probada porque el
// test pase: quitar el tope de 8 NO rompía nada. Se fabrica el caso.
func TestPathSetForRefusesWhatItCannotResolveHonestly(t *testing.T) {
	// El caso bueno, para que las negativas de abajo no las pase un evaluador que diga nil siempre.
	bueno := "\n\tconst base = scope\n\t\t? `/v1/console/sso/tenants/${id(scope)}`\n\t\t: '/v1/console/sso'\n" +
		"\treturn alias\n\t\t? `${base}/idps/${id(alias)}`\n\t\t: base\n"
	if set := pathSetFor(bueno); len(set) != 4 {
		t.Fatalf("dos ternarios sobre un const dan CUATRO rutas, dio %d: %v", len(set), set)
	}

	// Más de ocho: cuatro consts binarios interpolados en el return son dieciséis.
	var b strings.Builder
	b.WriteString("\n")
	for _, n := range []string{"a", "b", "c", "d"} {
		fmt.Fprintf(&b, "\tconst %s = x ? `/v1/%s1` : `/v1/%s2`\n", n, n, n)
	}
	b.WriteString("\treturn y ? `${a}${b}${c}${d}` : `${a}${b}${c}${d}/otra`\n")
	if set := pathSetFor(b.String()); set != nil {
		t.Fatalf("con %d ramas hay que negarse: enumerar 2^n inventa rutas que nadie escribió", len(set))
	}

	// Una rama que no es ruta absoluta invalida el conjunto ENTERO: media resolución emite
	// fragmentos que el router jamás registra, y eso sale rojo en el test hermano por lo equivocado.
	if set := pathSetFor("\n\treturn c ? `/v1/console/sso` : `relativa/no`\n"); set != nil {
		t.Fatalf("una rama relativa invalida el conjunto, dio %v", set)
	}
}

// El `??` de TypeScript no es un condicional, y confundirlo declara irresoluble una ruta concreta.
// Hoy la distinción no la dispara el árbol —la fábrica de intents normaliza esas interpolaciones
// antes—, así que va con su caso fabricado: una rama que nadie puede provocar no está probada
// porque el test pase, y `??` es demasiado común en TypeScript para dejarla a la suerte.
// mapaDeRutas resuelve un mapa de rutas hermanas, o no lo resuelve: media resolución inventa
// rutas que nadie llama, y una ruta inventada se lee como cobertura.
func mismasRutas(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestMapaDeRutasResuelveEnteroONada(t *testing.T) {
	consts := map[string]string{"BASE": "/v1/m/catalog"}

	// ⭐ EL CASO QUE FALLÓ AL ESCRIBIRLO: la primera versión acotaba el cuerpo con `[^}]*` y
	// cortaba en la `}` de `${BASE}`, devolviendo CERO valores con el mapa entero delante.
	t.Run("una interpolación no termina el objeto", func(t *testing.T) {
		got := mapaDeRutas("const M: Record<K, string> = {\n"+
			"  mcp: `${BASE}/mcp-admission/policy`,\n"+
			"  connector: `${BASE}/connector-admission/policy`,\n"+
			"}\n", consts)
		want := []string{"/v1/m/catalog/mcp-admission/policy", "/v1/m/catalog/connector-admission/policy"}
		if !mismasRutas(got["M"], want) {
			t.Fatalf("M = %v, quería %v", got["M"], want)
		}
	})

	t.Run("comillas simples también", func(t *testing.T) {
		got := mapaDeRutas("const M = {\n  a: '/v1/m/x',\n}\n", consts)
		if want := []string{"/v1/m/x"}; !mismasRutas(got["M"], want) {
			t.Fatalf("M = %v, quería %v", got["M"], want)
		}
	})

	// Las dos negativas: si UNA entrada no se resuelve, el mapa entero se rechaza. Sin esto el
	// resolutor emitiría las que sí puede y la llamada quedaría contada como cubierta a medias.
	t.Run("rechaza el mapa entero si una interpolación es desconocida", func(t *testing.T) {
		got := mapaDeRutas("const M = {\n  a: `${BASE}/x`,\n  b: `${DESCONOCIDA}/y`,\n}\n", consts)
		if _, hay := got["M"]; hay {
			t.Fatalf("resolvió a medias: %v", got["M"])
		}
	})

	t.Run("rechaza el mapa entero si un valor no es una ruta absoluta", func(t *testing.T) {
		got := mapaDeRutas("const M = {\n  a: `${BASE}/x`,\n  b: `relativa/y`,\n}\n", consts)
		if _, hay := got["M"]; hay {
			t.Fatalf("aceptó una ruta relativa: %v", got["M"])
		}
	})

	// Control positivo del propio control: sin esto, un `mapaDeRutas` que devolviera SIEMPRE nada
	// pasaría las dos negativas de arriba y las dos positivas serían lo único que lo delata.
	t.Run("un objeto sin valores de cadena no es un mapa de rutas", func(t *testing.T) {
		if got := mapaDeRutas("const M = {\n  a: 1,\n}\n", consts); len(got) != 0 {
			t.Fatalf("inventó un mapa: %v", got)
		}
	})
}

func TestNullishCoalescingIsNotATernary(t *testing.T) {
	casos := []struct {
		nombre string
		ruta   string
		quiero bool
	}{
		{"coalescencia sola", "/v1/m/sessions/work-items/${id(args.itemId ?? '')}/lease/renew", false},
		{"ternario de verdad", "/v1/m/x/${flag ? 'a' : 'b'}", true},
		{"las dos cosas", "/v1/m/x/${id(a ?? '')}/${flag ? 'a' : 'b'}", true},
		{"sin interpolación", "/v1/m/x/y", false},
	}
	for _, c := range casos {
		if got := esCondicionalInterp(c.ruta); got != c.quiero {
			t.Errorf("%s: esCondicionalInterp(%q) = %v, quiero %v", c.nombre, c.ruta, got, c.quiero)
		}
	}
}
