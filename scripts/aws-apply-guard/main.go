// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// aws-apply-guard parses the two AWS delivery workflows with a real YAML parser and
// verifies the invariants that decide whether an apply can authenticate at all.
//
// ⛔ POR QUÉ NO ES UN `grep`, Y ES EL MISMO ARGUMENTO QUE HIZO NACER `hcl-module-guard`.
// `check-aws-estate.sh` certificaba la forma del workflow con `grep` sobre el fichero
// entero. Un `grep` encuentra su patrón igual en un paso EJECUTABLE que en un COMENTARIO,
// y este workflow llevaba —hasta el 2026-08-27— exactamente eso: un comentario de cuatro
// líneas diciendo «the OIDC pin is the integrator's» donde tenía que ir el paso. Una
// invariante escrita como «el fichero menciona configure-aws-credentials» la habría
// satisfecho ESE COMENTARIO. La misma clase que los «log-bucket names in comments» que
// el guard de HCL existe para rechazar.
//
// ⛔ Y POR QUÉ EN GO Y NO EN PYTHON. `import yaml` no está disponible: ningún contenedor
// de este proyecto tiene PyYAML y ninguno tiene `pip` ni `ensurepip`
// (`scripts/check-ci-env-reach.sh:17-23`, medido el 2026-08-19 después de que ese mismo
// defecto rechazara TODOS los push de TODOS los carriles). Go sí está en todas partes
// donde corre este gate, y `check-aws-estate.sh` ya construye un helper Go pinchado.
//
// QUÉ ALCANZA SU MECANISMO DE DESCUBRIMIENTO, dicho aquí porque un gate dice lo que su
// descubrimiento alcanza y no lo que uno querría que comprobara (canon §0-COBERTURA):
//
//   - Lee el ÁRBOL YAML. Ve pasos, no texto: un `uses:` dentro de un comentario de YAML
//     no existe para este guard, que es justo lo que se quiere. Y dentro de un `run:`
//     descarta las líneas de comentario de shell antes de juzgar, por el mismo motivo.
//   - Comprueba la FORMA del pin (40 hex). NO puede comprobar que ese digest sea el
//     commit que la etiqueta `# vN.N.N` de al lado nombra: eso exige red y este gate no
//     la tiene. Esa correspondencia se verifica a mano al bumpear, y el comentario existe
//     para que se pueda.
//   - NO prueba que el apply se autentique de verdad contra AWS. Prueba que el canje
//     ESTÁ CABLEADO, es incondicional y está ordenado antes de quien lo necesita. Lo
//     otro sólo lo dice un dispatch real, que la orden 12 prohíbe hoy.
//   - NO modela `run:` como lenguaje. Si alguien obtiene credenciales dentro de un script
//     —o a través de una acción COMPUESTA que las canjee por dentro— este guard no lo ve;
//     lo que hace entonces es lo contrario de aprobar: no encuentra el paso y **falla**.
//     Sus falsos son de la dirección segura.
//
// Contrato: 0 limpio · 1 hallazgo · 2 no he podido mirar.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const prefix = "aws-apply-guard"

const (
	rcOK      = 0
	rcFinding = 1
	rcBlind   = 2
)

// El digest tiene que ser un OID de commit completo. Una etiqueta (`@v6`) es un puntero
// móvil: quien controle el repositorio de la acción cambia lo que corre en un job que
// asume un rol de administrador. 40 hex, en minúsculas, y nada más.
// ⛔ ANCLADA AL COMANDO ENTERO, no al principio de una línea suya. Un
// `echo "bash scripts/aws-iam-phase2.sh check"` contiene la subcadena y no comprueba nada
// —ésa es la lección que ya costó dos falsos verdes en las anclas de cosign—, pero el
// contraste enseñó la siguiente: con el ancla por LÍNEA, un `run` que corre `tofu apply` y
// DESPUÉS el check seguía contando como «el paso de la fase», y con el mismo índice el
// orden salía verde. Un paso que hace dos cosas no es el paso dedicado que esto exige.
// `tofu output` en su forma humana redacta lo marcado `sensitive`; `-json` no. Por eso el
// paso que alimenta el resumen usa la primera, y esta ancla no acepta la segunda.
// El instalador oficial, por su forma: `<algo>/aws/install` con destinos explícitos. Un
// `apt install awscli` o un `pip install` no casan a propósito — no van pinchados.
var awsCliInstall = regexp.MustCompile(`(?m)^[[:space:]]*"?\$[A-Z_{}]*RUNNER_TEMP[^"\n]*/aws/install"?[[:space:]]`)

var sudoRe = regexp.MustCompile(`(^|[^\w.-])sudo[[:space:]]`)

var tofuOutputRe = regexp.MustCompile(`(?m)^[[:space:]]*(?:[\w.\-/]*/)?(?:tofu|terraform)[[:space:]]+output[[:space:]]*$`)

var iamPhaseCheck = regexp.MustCompile(`\Abash[[:space:]]+scripts/aws-iam-phase2\.sh[[:space:]]+check[^\n]*\z`)

var sha40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

// El nombre de la acción se compara EN MINÚSCULAS: GitHub resuelve `owner/repo` sin
// distinguir mayúsculas, así que `AWS-Actions/Configure-AWS-Credentials@…` es la misma
// acción y tiene que reconocerse como tal. Sin esto el guard fallaría contra una grafía
// legítima — un falso de la dirección segura, pero un falso.
const credentialAction = "aws-actions/configure-aws-credentials@"

// Los subcomandos de tofu/terraform que NECESITAN credenciales, y sólo ésos.
//
// ⛔ LAS DOS VERSIONES ANTERIORES ESTABAN MAL EN DIRECCIONES OPUESTAS, y las dos se
// midieron contra este workflow antes de quedarse:
//
//	· `tofu\s+(init|apply|plan|destroy)` — perdía `tofu -chdir=deploy/aws apply` entera,
//	  que es una forma legítima y documentada. Un falso NEGATIVO.
//	· `\b(tofu|terraform)\b` a secas — contaba el paso «install OpenTofu (pinned)», cuyo
//	  `run` termina en `tofu version`, como un paso que necesita credenciales, y acusaba
//	  al orden de estar mal estándolo bien. Un falso POSITIVO, y ruidoso.
//
// La forma que queda pide el binario Y un subcomando que hable con el backend o con el
// proveedor, en la misma línea lógica. `version`, `fmt` y `validate` quedan fuera a
// propósito: ninguno lee el backend.
//
//	· ⛔ Y LA TERCERA, medida por el contraste `sol max` (H-04): `[^\w./-]` excluía la
//	  BARRA, así que `/usr/local/bin/tofu apply` no casaba con nada. El detector veía un
//	  paso sin tofu y el orden salía verde con el apply delante del canje. Un binario
//	  invocado por su ruta absoluta no es un caso raro: es la forma que deja `install`.
//	  Ahora el prefijo de ruta se consume explícitamente, sin volver a admitir
//	  `algo-tofu` ni `tofu.md`.
var tofuRe = regexp.MustCompile(
	`(^|[^\w.-])(?:[\w.\-/]*/)?(tofu|terraform)\b[^\n]*\b(init|apply|plan|destroy|refresh|import|state|providers|output)\b`)

// Sub-caso: el paso que inicializa el backend. Se distingue del resto para poder exigirle
// el bloqueo de estado sin pedírselo a un `validate` que corre con `-backend=false`.
var tofuInitRe = regexp.MustCompile(`(^|[^\w.-])(?:[\w.\-/]*/)?(tofu|terraform)\b[^\n]*\binit\b`)

// continuationRe une las continuaciones de línea del shell ANTES de juzgar. Sin esto,
// `tofu \` + salto + `  apply -auto-approve` son dos líneas y ninguna casa: el binario en
// una y el subcomando en la otra. Es una forma normal de escribir un comando largo y este
// mismo workflow la usa en el `tofu init` de abajo.
var continuationRe = regexp.MustCompile(`\\\n\s*`)

// Las condiciones EXACTAS que pueden abrir un job privilegiado. Se comparan normalizadas
// (espacios colapsados), no por subcadena.
//
// ⛔ POR QUÉ EXACTAS Y NO «contiene el token». El contraste `sol max` del 2026-08-27 (C-01)
// borró la línea `if:` entera del job `apply` y este guard siguió diciendo `apply-wiring-ok`:
// no miraba `applyJob.If` en ningún sitio. Con una comprobación por subcadena, además, basta
// negar el predicado (`!=` en vez de `==`) conservando las mismas palabras. Una puerta se
// verifica por su forma completa o no se verifica.
//
// ⛔ Y ES LA LISTA DE QUIEN PUEDE ASUMIR UN ROL, no sólo la de las condiciones. Antes había
// DOS fuentes: este mapa y el nombre suelto que se le pasaba a `checkNoCredentialsOutside`.
// Con dos entornos —piloto y producción— esa duplicidad se convierte en un agujero: añadir
// un job privilegiado y olvidar una de las dos listas deja un job que canjea OIDC sin
// puerta exacta, o una puerta exacta sobre un job al que se le prohíbe autenticarse. Aquí
// hay UNA lista: estar en ella es a la vez el permiso de canjear y la obligación de traer
// la condición exacta.
// Qué estate aplica cada job. Es lo que ata un job a su rol, a sus piezas de policy y —lo
// que aquí importa— al `sub` exacto al que se estrecha su confianza, que vive en
// `scripts/aws-iam-phase2.sh`. Un job privilegiado sin entrada aquí no se puede emparejar
// con ninguna confianza, y eso es hallazgo: no se adivina el estate por el nombre.
// `push` (aws-images.yml) NO aplica un estate: empuja imagenes a ECR. Pero asume el MISMO
// rol que el apply del piloto (`secrets.AWS_ROLE_ARN`, `aws-images.yml:134`), asi que su
// `sub` tiene que casar con la MISMA confianza — y por eso figura como `sandbox` y no
// exento. El dia que las imagenes se empujen con un rol propio, esta linea es donde se ve.
var jobEstate = map[string]string{
	"apply":            "sandbox",
	"apply-production": "production",
	"push":             "sandbox",
}

// La tabla del guion de IAM, leída y no supuesta:
//
//	sandbox)    ROLE="olivares-apply-sandbox";    SUB_TARGET="repo:$REPO:ref:refs/heads/main" ;;
var estateSubRe = regexp.MustCompile(
	`(?m)^([a-z][a-z0-9-]*)\)\s+ROLE="[^"]+";\s+SUB_TARGET="repo:\$REPO:([^"]+)"`)

// Los jobs de `aws-terraform.yml` que aplican, ADEMÁS del `apply` que este guard exige que
// exista. `apply` va aparte porque su ausencia es «no he podido mirar»: es el sujeto. Éstos
// son opcionales, y su presencia obliga a las mismas invariantes.
var privilegedTerraformJobs = []string{"apply-production"}

var allowedApplyIf = map[string]string{
	"apply":            "github.event_name == 'workflow_dispatch' && github.event.inputs.confirm == 'apply-sandbox-estate'",
	"apply-production": "github.event_name == 'workflow_dispatch' && github.event.inputs.confirm == 'apply-production-estate'",
	"push":             "github.event_name == 'workflow_dispatch' && github.event.inputs.confirm == 'push-images-to-ecr'",
}

// Variables de entorno que SUSTITUYEN la cadena de credenciales que el canje acaba de
// poner. Un `env:` con cualquiera de éstas en un job privilegiado hace que `tofu` o
// `docker` hablen con otra cuenta, y el canje sigue estando y sigue estando ordenado.
// `AWS_ROLE_ARN` NO está en la lista: es la nuestra, y es la que el canje consume.
var credentialEnvOverrides = map[string]bool{
	"AWS_ACCESS_KEY_ID":                      true,
	"AWS_SECRET_ACCESS_KEY":                  true,
	"AWS_SESSION_TOKEN":                      true,
	"AWS_PROFILE":                            true,
	"AWS_SHARED_CREDENTIALS_FILE":            true,
	"AWS_CONFIG_FILE":                        true,
	"AWS_WEB_IDENTITY_TOKEN_FILE":            true,
	"AWS_CONTAINER_CREDENTIALS_FULL_URI":     true,
	"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": true,
	"AWS_EC2_METADATA_SERVICE_ENDPOINT":      true,
	"AWS_ENDPOINT_URL":                       true,
	"AWS_ENDPOINT_URL_S3":                    true,
	"AWS_ENDPOINT_URL_STS":                   true,
	// cosign: no cambian credenciales de AWS, cambian DÓNDE acaba la firma o si sale.
	"COSIGN_REPOSITORY": true,
	"COSIGN_UPLOAD":     true,
}

var spaces = regexp.MustCompile(`\s+`)

func normalise(s string) string { return strings.TrimSpace(spaces.ReplaceAllString(s, " ")) }

// isFalse acepta la ausencia del campo y el literal `false`, y nada más. Un `${{ … }}` en
// `continue-on-error` es un valor que este guard no puede evaluar: se rechaza.
func isFalse(n yaml.Node) bool {
	return n.IsZero() || n.Value == "false"
}

var findings []string

func finding(format string, args ...any) {
	findings = append(findings, fmt.Sprintf(format, args...))
}

func cannot(format string, args ...any) {
	fmt.Fprintf(os.Stderr, prefix+": COULD NOT LOOK — "+format+"\n", args...)
	os.Exit(rcBlind)
}

// stripShellComments quita las líneas de comentario de un bloque `run:`.
//
// ⛔ NO ES COSMÉTICO Y ES LA MISMA LECCIÓN QUE EL COMENTARIO DE YAML. El paso que aplica
// lleva doce líneas de comentario explicando por qué el bloqueo es `use_lockfile` y no
// DynamoDB — y esas líneas MENCIONAN «tofu init», «use_lockfile=true» y «dynamodb_table».
// Sin este filtro, un paso cuyo comentario nombra el bloqueo satisface la invariante del
// bloqueo, y un paso cuyo comentario nombra tofu cuenta como paso de tofu para el orden.
//
// LÍMITE DECLARADO: reconoce una línea de comentario por su primer carácter no blanco.
// Un `#` dentro de una cadena, en mitad de una línea, NO se quita — y eso es deliberado:
// quitarlo exigiría un parser de shell, y la dirección del error importa. Al conservar
// esas líneas, el guard ve MÁS de lo que hay, nunca menos.
func stripShellComments(run string) string {
	var kept []string
	for _, line := range strings.Split(run, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		kept = append(kept, line)
	}
	return continuationRe.ReplaceAllString(strings.Join(kept, "\n"), " ")
}

// step es un paso de un job, ya resuelto por el parser: `uses`, su `with`, y el `run`.
type step struct {
	Name            string            `yaml:"name"`
	Uses            string            `yaml:"uses"`
	With            map[string]string `yaml:"with"`
	Run             string            `yaml:"run"`
	If              string            `yaml:"if"`
	ContinueOnError yaml.Node         `yaml:"continue-on-error"`
	Env             map[string]string `yaml:"env"`
	Shell           string            `yaml:"shell"`
}

// command devuelve el `run` sin sus comentarios de shell.
func (s step) command() string { return stripShellComments(s.Run) }

type job struct {
	Needs           yaml.Node         `yaml:"needs"`
	If              string            `yaml:"if"`
	Permissions     map[string]string `yaml:"permissions"`
	Env             map[string]string `yaml:"env"`
	Steps           []step            `yaml:"steps"`
	Strategy        yaml.Node         `yaml:"strategy"`
	ContinueOnError yaml.Node         `yaml:"continue-on-error"`
	Environment     yaml.Node         `yaml:"environment"`
	// `uses:` A NIVEL DE JOB es un workflow reutilizable: código que este guard no
	// puede leer, ejecutándose con los permisos de este workflow.
	Uses string `yaml:"uses"`
}

type workflow struct {
	Name string            `yaml:"name"`
	Env  map[string]string `yaml:"env"`
	// ⚠ `on:` SE DECODIFICA COMO CADENA AQUÍ, y no se puede dar por supuesto: en YAML 1.1
	// `on` es el booleano verdadero, y PyYAML lo devuelve como la clave `True` (medido el
	// 2026-08-27 sobre este mismo workflow). `gopkg.in/yaml.v3` sigue el core schema de
	// YAML 1.2, donde `on` es `!!str` — comprobado con una sonda antes de escribir esta
	// línea, no supuesto. Si algún día se porta este guard a otro parser, ESTA es la
	// línea que se rompe en silencio: el mapa saldría vacío y el gate diría CLEAN.
	On   map[string]yaml.Node `yaml:"on"`
	Jobs map[string]job       `yaml:"jobs"`
}

func parse(path string) workflow {
	raw, err := os.ReadFile(path)
	if err != nil {
		cannot("cannot read %s: %v", path, err)
	}
	var wf workflow
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		cannot("%s is not parseable YAML: %v", path, err)
	}
	if len(wf.Jobs) == 0 {
		cannot("%s declares no jobs — nothing to judge", path)
	}
	return wf
}

func isCredentialStep(s step) bool {
	return strings.HasPrefix(strings.ToLower(s.Uses), credentialAction)
}

// credentialSteps devuelve los índices de TODOS los pasos que canjean el token, no sólo
// el primero: dos pasos de credenciales son legales y el segundo decide qué rol queda
// puesto. Juzgar sólo el primero dejaría el segundo sin mirar.
func credentialSteps(steps []step) []int {
	var idx []int
	for i, s := range steps {
		if isCredentialStep(s) {
			idx = append(idx, i)
		}
	}
	return idx
}

// firstTofuStep devuelve el índice del primer paso cuyo comando invoca tofu/terraform.
func firstTofuStep(steps []step) int {
	for i, s := range steps {
		if c := s.command(); c != "" && tofuRe.MatchString(c) {
			return i
		}
	}
	return -1
}

// needsNames devuelve los jobs de los que depende éste. `needs:` admite escalar
// (`needs: validate`) y secuencia (`needs: [a, b]`), y las dos formas son legales.
func needsNames(n yaml.Node) []string {
	if n.IsZero() {
		return nil
	}
	var one string
	if err := n.Decode(&one); err == nil && one != "" {
		return []string{one}
	}
	var many []string
	if err := n.Decode(&many); err == nil {
		return many
	}
	return nil
}

// requireGate exige que el job privilegiado dependa del que verifica el estado.
//
// ⛔ ES LA PUERTA, NO EL ORDEN, y el propio `aws-terraform.yml` lo tiene escrito con su
// coste: «sin `needs`, el `if` de confirmación deja correr `apply` EN PARALELO con
// `validate`, así que un dispatch confirmado podía APLICAR SOBRE AWS con el gate del
// estate en rojo — la corrida acababa en rojo, pero DEMASIADO TARDE: el efecto externo ya
// se había producido». Ese razonamiento estaba en un comentario y **no lo comprobaba
// nadie**: quitar la línea `needs:` dejaba el gate en verde. Un diagnóstico en un
// comentario no es un control.
func requireGate(path, jobName string, j job, gate string, wf workflow) {
	declared := false
	for _, n := range needsNames(j.Needs) {
		if n == gate {
			declared = true
		}
	}
	if !declared {
		finding("%s job %q does not declare `needs: %s`: an `if` decides WHETHER a job runs, "+
			"only `needs` decides WHEN — without it the confirmed dispatch races the gate and the "+
			"external effect happens before the red arrives", path, jobName, gate)
		return
	}
	// ⛔ Y QUE EL JOB DE PUERTA CORRA EL GATE, no que se llame así. Contraste `sol max`
	// 2026-08-27 (C-01): `requireGate` comprobaba sólo el NOMBRE, así que un `validate`
	// vacío —o uno al que se le quitaran los dos pasos del gate— seguía siendo una puerta
	// válida para este guard y no verificaba nada.
	g, ok := wf.Jobs[gate]
	if !ok {
		finding("%s names %q in `needs` but declares no such job", path, gate)
		return
	}
	ran := false
	for _, s := range g.Steps {
		for _, line := range strings.Split(s.command(), "\n") {
			if estateGateCmd.MatchString(line) {
				ran = true
			}
		}
	}
	if !ran {
		finding("%s job %q is named as the gate of %q but runs no `check-aws-estate.sh` command: "+
			"an empty gate is a door that is always open", path, gate, jobName)
	}
}

// ⛔ ANCLADOS AL PRINCIPIO DE LÍNEA, Y ÉSTA ES LA SEGUNDA VERSIÓN. La primera decía «una
// línea sin `|`, `&`, `;` ni `#`» y **aceptaba `echo "bash scripts/cosign-verified.sh sign
// …"`**, que es exactamente el mutante del contraste que venía a cerrar: una mención
// dentro de una cadena no tiene ninguno de esos caracteres. Lo cacé con el caso de la
// batería antes de publicar, y la lección es la de siempre — un arreglo se prueba con el
// defecto que YA ocurrió, no con la idea que uno tiene del defecto.
//
// La forma que queda exige que la línea EMPIECE por el intérprete y el guion; un `echo`
// empieza por `echo`.
//
// LÍMITE DECLARADO: no es un parser de shell. Una invocación tras `&&`, dentro de `$( )`,
// de una función o de un bucle NO se reconoce — y esa dirección es la segura: lo que no se
// ve no aprueba, hace fallar la invariante que lo busca.
var (
	signCommand   = regexp.MustCompile(`^[[:space:]]*(bash|sh)[[:space:]]+scripts/cosign-verified\.sh[[:space:]]+sign\b`)
	verifyCommand = regexp.MustCompile(`^[[:space:]]*(bash|sh)[[:space:]]+scripts/cosign-verified\.sh[[:space:]]+verify\b`)
	estateGateCmd = regexp.MustCompile(`^[[:space:]]*(bash|sh)[[:space:]]+scripts/check-aws-estate\.sh[[:space:]]*$`)
	// ⛔ SOBRE LA LINEA YA UNIDA, no sobre `--push` a solas. `step.command()` termina en
	// `continuationRe.ReplaceAllString(...)`, que JUNTA las continuaciones: el `docker buildx
	// build \` y sus quince flags llegan aqui como UNA linea, asi que un patron anclado a un
	// `--push` suelto con `$` no puede casar nunca — y no fallaba: contaba CERO, que este
	// guard lee como «no se cuantas imagenes publica». Se ancla a la invocacion entera, que
	// ademas ata el flag a su `buildx build` en vez de a cualquier linea que lo mencione.
	// La comprobacion de forma del digest, anclada a su `case` sobre el valor y no a la
	// cadena `sha256` suelta — que aparece tambien en comentarios y en el pin de OpenTofu.
	digestShapeCheck = regexp.MustCompile(`\*@sha256:\*\)`)
	buildxPush       = regexp.MustCompile(`^[[:space:]]*docker buildx build\b.*[[:space:]]--push([[:space:]]|$)`)
)

// lastArg devuelve el ultimo campo de una linea de comando ya unida, que en las invocaciones
// de firma y verificacion es la referencia `${X_REF}@${X_DIGEST}`. No interpreta el shell: lo
// que se compara son las expresiones tal como estan escritas, que es exactamente lo que hace
// falta para saber si dos lineas hablan de la MISMA imagen o de dos distintas.
func lastArg(line string) string {
	// ⛔ SIN LAS REDIRECCIONES. Las tres `verify` terminan en `>/dev/null`, asi que el ultimo
	// campo era `/dev/null` para las tres: el conjunto de verificados tenia UN elemento y el
	// guard acusaba a un arbol correcto. Lo caza su propio control positivo — que es para lo
	// que existe, y por eso este comentario esta aqui y no en el mensaje de commit.
	f := strings.Fields(strings.TrimSpace(line))
	for len(f) > 0 {
		last := f[len(f)-1]
		if strings.HasPrefix(last, ">") || strings.HasPrefix(last, "2>") ||
			strings.HasPrefix(last, "&>") || strings.HasPrefix(last, "|") {
			f = f[:len(f)-1]
			continue
		}
		break
	}
	if len(f) == 0 {
		return ""
	}
	return strings.Trim(f[len(f)-1], `"`)
}

func stepLabel(s step) string {
	if s.Name != "" {
		return s.Name
	}
	if s.Uses != "" {
		return s.Uses
	}
	return "<unnamed run step>"
}

// checkApplyJob es el corazón: el job que aplica tiene que poder autenticarse.
func checkApplyJob(path string, j job, jobName string, wf workflow) {
	where := fmt.Sprintf("%s job %q", path, jobName)

	// ⛔ LA PUERTA, EXACTA. Sin esto el guard nunca miraba `if:` y un mutante que la
	// BORRABA ENTERA seguía dando `apply-wiring-ok` (contraste `sol max`, C-01). Y una
	// comprobación por subcadena tampoco vale: `!=` conserva todas las palabras.
	if want, known := allowedApplyIf[jobName]; known && normalise(j.If) != want {
		finding("%s guards itself with `if: %s`; the only condition that may open it is "+
			"`%s` — a substring check would accept the same words negated", where, normalise(j.If), want)
	}

	// ⛔ UNA MATRIZ MULTIPLICA EL JOB Y ESTE GUARD LEE UNA SOLA FORMA. El contraste lo
	// demostró con `matrix.AWS_ROLE_ARN` de dos valores: `role-to-assume` seguía
	// «conteniendo AWS_ROLE_ARN» y el rol real lo elegía la matriz. En un job que asume un
	// rol de la cuenta, una matriz no se audita: se prohíbe.
	if !j.Strategy.IsZero() {
		finding("%s declares a `strategy`: a matrix multiplies a privileged job into variants "+
			"this guard reads as one, and the assumed role can come from the matrix", where)
	}

	// ⛔ `continue-on-error` DE JOB no continúa pasos: hace que el WORKFLOW pase con el job
	// privilegiado en rojo — después de un apply parcial o de una firma fallida.
	if !isFalse(j.ContinueOnError) {
		finding("%s sets job-level continue-on-error: the workflow would go green with a "+
			"partial apply or an unsigned push behind it", where)
	}

	// ⛔ `environment:` CAMBIA EL `sub` DEL TOKEN, Y LA TRUST DEL ROL LO FIJA. Con la
	// trust estrechada a `repo:<owner>/<repo>:ref:refs/heads/main` (fase 2 de IAM),
	// GitHub emite `repo:<owner>/<repo>:environment:<nombre>` en su lugar en cuanto un
	// job declara environment: deja de casar y el rol se vuelve INASUMIBLE. Eso no se ve
	// leyendo el diff del workflow — se ve en un AssumeRoleWithWebIdentity denegado, con
	// el dispatch ya lanzado. Es un invariante ENTRE dos ficheros que nadie lee a la vez,
	// y por eso lo tiene que sostener un gate y no un comentario.
	//
	// Si algún día se quiere el environment protegido (es la puerta MEJOR: la guarda
	// GitHub con sus reglas y no una condición dentro del propio fichero que se quiere
	// proteger), se cambia la trust a `environment:<nombre>` EN EL MISMO commit y se
	// retira esta comprobación con esa razón escrita. Lo que no vale es añadir uno y
	// descubrir el otro cuarenta minutos después.
	checkEnvironmentMatchesTrust(path, j, jobName, where)
	checkConsumedImagesAreQualified(path, j, jobName, where)

	if strings.TrimSpace(j.Uses) != "" {
		finding("%s is a reusable workflow (`uses: %s`): its steps live in code this guard "+
			"does not read, running with this workflow's permissions", where, j.Uses)
	}

	// ⛔ EL CANJE PONE UNA CADENA DE CREDENCIALES Y UN `env:` LA SUSTITUYE. El paso existe,
	// está pinchado y está ordenado — y `tofu` habla con otra cuenta. Se miran los tres
	// niveles: workflow, job y paso.
	for scope, env := range map[string]map[string]string{"workflow": wf.Env, "job": j.Env} {
		for k := range env {
			if credentialEnvOverrides[strings.ToUpper(strings.TrimSpace(k))] {
				finding("%s inherits %s-level env %s, which overrides the credential chain the "+
					"OIDC exchange just installed", where, scope, k)
			}
		}
	}
	for _, s := range j.Steps {
		for k := range s.Env {
			if credentialEnvOverrides[strings.ToUpper(strings.TrimSpace(k))] {
				finding("%s step %q sets env %s, which overrides the credential chain the OIDC "+
					"exchange just installed", where, stepLabel(s), k)
			}
		}
	}

	// El permiso sin el canje es un permiso que nadie ejerce; el canje sin el permiso
	// es un canje que no puede pedir token. Se exigen los dos.
	if j.Permissions["id-token"] != "write" {
		finding("%s does not request id-token: write — the OIDC token cannot be minted", where)
	}

	idx := credentialSteps(j.Steps)
	if len(idx) == 0 {
		finding("%s has no aws-actions/configure-aws-credentials step: the apply would run "+
			"on the runner's default credential chain, which on a self-hosted box is no chain at all", where)
		return
	}

	for _, i := range idx {
		s := j.Steps[i]
		ref := s.Uses[strings.Index(s.Uses, "@")+1:]
		if !sha40.MatchString(ref) {
			finding("%s pins configure-aws-credentials to %q, which is not a 40-hex commit OID "+
				"(a tag is a movable pointer into a job that assumes an administrative role)", where, ref)
		}

		role := strings.TrimSpace(s.With["role-to-assume"])
		switch {
		case role == "":
			finding("%s configure-aws-credentials has no role-to-assume", where)
		case !strings.Contains(role, "AWS_ROLE_ARN"):
			finding("%s assumes %q, which does not come from AWS_ROLE_ARN — the refusal step above "+
				"guards a secret this step would then ignore", where, role)
		}

		if strings.TrimSpace(s.With["aws-region"]) == "" {
			finding("%s configure-aws-credentials has no aws-region", where)
		}

		// ⛔ UN CANJE CONDICIONAL O TOLERANTE A FALLO ES UN CANJE QUE PUEDE NO OCURRIR, y
		// el paso siguiente correría igual. `if:` lo salta; `continue-on-error` se traga
		// su fallo. Las dos formas dejan al `tofu init` sin credenciales con el gate en
		// verde, que es exactamente el defecto que este guard existe para cerrar.
		if strings.TrimSpace(s.If) != "" {
			finding("%s guards the credential exchange with `if: %s` — a skipped exchange leaves "+
				"tofu on the runner's default chain", where, strings.TrimSpace(s.If))
		}
		if !s.ContinueOnError.IsZero() && s.ContinueOnError.Value != "false" {
			finding("%s sets continue-on-error on the credential exchange: a failed exchange would "+
				"not stop the apply", where)
		}
	}

	t := firstTofuStep(j.Steps)

	// ⛔ LA FASE DE IAM DECLARADA, Y EL PASO QUE LA COMPRUEBA ANTES DE TOCAR NADA.
	// `IAM_PHASE` es la única forma que tiene el repositorio de decir «este rol ya no
	// lleva AdministratorAccess»; sin ella el paso de verificación no tiene contra qué
	// juzgar y pasa siempre. Y el paso tiene que ir ANTES del primer paso de tofu por la
	// misma razón que el canje: un control colocado detrás del apply describe un estado
	// que ya se usó.
	switch strings.TrimSpace(j.Env["IAM_PHASE"]) {
	case "1", "2":
	case "":
		finding("%s declares no IAM_PHASE: the phase check has nothing to judge against and "+
			"would pass whatever the role's policies are", where)
	default:
		finding("%s declares IAM_PHASE=%q, which is neither 1 nor 2", where,
			strings.TrimSpace(j.Env["IAM_PHASE"]))
	}

	// ⛔ EL PASO ES UNA ESTRUCTURA CERRADA, NO UNA SUBCADENA — H-04 del contraste, con
	// SEIS falsos verdes medidos sobre la versión anterior de esta comprobación:
	// `if: false`, `continue-on-error: true`, la llamada dentro de una función que nadie
	// invoca, `tofu apply` y el check en el MISMO `run`, un `env.IAM_PHASE` de PASO que
	// pisa el del job, y `/usr/local/bin/tofu` esquivando el detector léxico. Los seis
	// daban `apply-wiring-ok`.
	//
	// La causa común es una sola: se inferían EJECUCIÓN y ORDEN de un texto. Un `run` es
	// un programa, y este guard no ejecuta programas — así que deja de intentar leerlos.
	// El paso tiene que ser exactamente eso: un `run` con UNA línea, la canónica, sin
	// `if`, sin tolerancia, sin `shell`, sin `env` propio, y **inmediatamente después**
	// del último canje de credenciales. Lo que no case con esa forma no es el paso.
	phase := -1
	for i, s := range j.Steps {
		if iamPhaseCheck.MatchString(strings.TrimSpace(s.command())) {
			phase = i
			break
		}
	}
	if phase < 0 {
		finding("%s has no dedicated IAM phase-check step: the only accepted form is a `run:` "+
			"whose whole command is `bash scripts/aws-iam-phase2.sh check …` — a mention "+
			"inside a larger script proves nothing about it running", where)
	} else {
		ps := j.Steps[phase]
		if strings.TrimSpace(ps.If) != "" {
			finding("%s guards the IAM phase check with `if: %s`: a skipped check is not a "+
				"passed check, and the apply behind it would run anyway",
				where, strings.TrimSpace(ps.If))
		}
		if !ps.ContinueOnError.IsZero() && ps.ContinueOnError.Value != "false" {
			finding("%s sets continue-on-error on the IAM phase check: its whole job is to "+
				"stop the apply, and this makes its failure decorative", where)
		}
		// ⛔ `env` DE PASO PISA `env` DE JOB. Un `IAM_PHASE` aquí decide contra qué se
		// juzga, y el guard lo estaba leyendo del job: se comprobaba un valor y corría
		// otro. Medido: `step.env.IAM_PHASE: "3"` sobre `job.env: "1"` daba verde.
		if len(ps.Env) > 0 {
			finding("%s gives the IAM phase check its own env (%d key(s)): a step-level "+
				"IAM_PHASE overrides the job value this guard reads, so the phase checked "+
				"and the phase declared stop being the same one", where, len(ps.Env))
		}
		if strings.TrimSpace(ps.Shell) != "" {
			finding("%s sets `shell` on the IAM phase check: the command is pinned to `bash` "+
				"in the run line and a different interpreter is a different program", where)
		}
		// El ORDEN, sin inferir nada de un `run`: el paso va JUSTO detrás del último
		// canje. Comparar índices contra «el primer tofu» dejaba pasar un tofu escrito
		// en el mismo paso (mismo índice) o invocado por ruta absoluta.
		last := idx[len(idx)-1]
		if phase != last+1 {
			finding("%s puts the IAM phase check at step %d; it must be step %d, immediately "+
				"after the last credential exchange — anything in between runs with the "+
				"assumed role before anything has qualified it", where, phase, last+1)
		}
	}

	// ⛔ LA HERRAMIENTA QUE EL CONTROL NECESITA SE INSTALA, NO SE SUPONE. El run
	// 33240917638 murió en la comprobación de fase con «no hay AWS CLI en esta caja»
	// (rc=2): el runner no lo traía. Falló CERRADO, que es lo que ese control promete —
	// pero un control que no puede correr no protege nada, y descubrirlo cuesta un
	// despacho entero.
	//
	// Es la misma clase que el `sudo`: en un pool heterogéneo, **una herramienta que no
	// instalas tú es una moneda al aire**. Así que si el job corre la comprobación de
	// fase, tiene que instalar el CLI ANTES — y «antes» se comprueba por índice, no por
	// presencia, porque un paso de instalación colocado después existe y no sirve.
	if phase >= 0 {
		install := -1
		for i, s := range j.Steps {
			if awsCliInstall.MatchString(s.command()) {
				install = i
				break
			}
		}
		switch {
		case install < 0:
			finding("%s runs the IAM phase check but never installs the AWS CLI: on a runner "+
				"without it the check answers «could not look» and stops the job — a control "+
				"that cannot run protects nothing", where)
		case install > phase:
			finding("%s installs the AWS CLI at step %d, AFTER the phase check at step %d: an "+
				"install placed later exists and does not help", where, install, phase)
		}
	}

	// ⛔ Y LOS OUTPUTS TIENEN QUE SALIR DEL RUNNER. `deploy/aws/outputs.tf` publica los
	// tres CNAME para que nadie los deduzca, pero el apply corre aquí y el estado vive en
	// S3: sin un paso que los imprima, el único camino para leerlos es tener credenciales
	// y correr `tofu output` a mano — o sea, el diseño exige valores literales y el
	// pipeline no los enseña. Sin esto la partición en dos fases es INEJECUTABLE, y el
	// fallo no se ve en ningún fichero por separado: se ve al ir a despachar.
	if applyStep := firstTofuStep(j.Steps); applyStep >= 0 {
		found := false
		for _, s := range j.Steps {
			if tofuOutputRe.MatchString(s.command()) && strings.Contains(s.command(), "GITHUB_STEP_SUMMARY") {
				found = true
				break
			}
		}
		if !found {
			finding("%s never publishes `tofu output` into GITHUB_STEP_SUMMARY: the ACM "+
				"validation record and the two service CNAMEs stay inside the S3 state, and "+
				"the two-phase apply this repository documents cannot be carried out", where)
		}
	}

	// El ORDEN es la mitad de la invariante. Un canje colocado DESPUÉS del `tofu init`
	// existe, casa con todos los greps y no sirve de nada.
	first := idx[0]
	if t >= 0 && t < first {
		finding("%s runs %q (step %d) BEFORE the credential exchange (step %d): "+
			"the S3 backend is read without credentials", where, stepLabel(j.Steps[t]), t, first)
	}
}

// checkNoCredentialsOutside es la dirección de NO DISPARO, y es una invariante por derecho
// propio: ningún job que no sea el privilegiado toma credenciales de AWS. Sin esto,
// «cablear OIDC» se podría satisfacer poniéndolo en el job equivocado — el que dispara
// cualquier rama — y el gate diría CLEAN. Se comprueba sobre TODOS los demás jobs y no
// sólo sobre `validate`: un tercer job añadido mañana caería en el mismo hueco.
// checkNoSudo recorre TODOS los jobs, y esa palabra es el arreglo entero.
//
// ⛔ EL DEFECTO YA OCURRIÓ: el run 33212068653 murió en «install OpenTofu (pinned)» con
// «sudo: a terminal is required to read the password». El zip se había descargado y
// verificado; lo único que falló fue pedir root para escribir en /usr/local/bin.
//
// Lo que lo convierte en clase y no en anécdota: el MISMO paso, con el MISMO código, PASÓ
// en `validate` y MURIÓ en `apply`. El pool de runners no es homogéneo —unos corren como
// root y otros tienen `sudo` con contraseña—, así que un paso que necesita root es una
// moneda al aire, y reintentar PARECE un arreglo sin serlo.
//
// ⚠ Y esta función vive fuera de `checkApplyJob` por una razón medida, no por gusto: la
// escribí primero DENTRO, y su propio mutante la desmintió — cayó sobre la copia de
// `validate`, que el job privilegiado no mira, y el gate dio verde. La copia de fuera es
// exactamente la misma lotería. Un gate que sólo cubre el job privilegiado tiene un
// agujero del tamaño de los demás.
//
// Se mira el comando SIN comentarios: el propio paso explica en prosa por qué no usa sudo,
// y una comprobación por subcadena se acusaría a sí misma.
func checkNoSudo(path string, wf workflow) {
	for name, j := range wf.Jobs {
		for _, s := range j.Steps {
			if sudoRe.MatchString(s.command()) {
				finding("%s job %q step %q invokes sudo: the runner pool is not homogeneous "+
					"—the same step passed on one runner and died on another with «a password "+
					"is required»— so a step that needs root is a coin flip, and retrying is "+
					"not a fix", path, name, stepLabel(s))
			}
		}
	}
}

// ⛔ `environment:` CAMBIA EL `sub` DEL TOKEN, Y LA TRUST DEL ROL LO FIJA. GitHub emite
// `repo:<owner>/<repo>:ref:refs/heads/main` para un job SIN environment y
// `repo:<owner>/<repo>:environment:<nombre>` EN SU LUGAR para uno que lo declara — no los
// dos. Con la trust estrechada (fase 2 de IAM) a la forma equivocada, el rol se vuelve
// INASUMIBLE, y eso no se ve leyendo el diff del workflow: se ve en un
// AssumeRoleWithWebIdentity denegado, con el dispatch ya lanzado.
//
// ⛔ ANTES ESTO ERA UNA NEGATIVA EN SECO —«ningún job privilegiado declara environment»—
// porque sólo había un estate y su trust se estrechaba a un ref. Esa forma tenía la
// invariante correcta y el sujeto equivocado: prohibía la puerta MEJOR de las dos. Un
// environment protegido lo guarda GitHub del lado del SERVIDOR, y no una condición dentro del
// propio fichero que se quiere proteger.
//
// ⚠ Y LO QUE ESTE GUARD NO VE, dicho aquí porque un control que no declara su punto ciego
// miente: la mitad que acota la RAMA es la política de rama de despliegue del environment,
// que es configuración del servidor y no vive en el repositorio. El `sub` por sí solo no
// acota la rama —cualquier rama que declare el environment emite el mismo—, así que este
// control sostiene el PAR environment↔trust y no la restricción de rama. Los revisores
// obligatorios, que serían la otra forma de acotarla, NO están disponibles en este plan
// (422 «billing plan», medido el 2026-09-02). Los pasos están en
// `an internal design note (not shipped)` §4.
//
// Ahora se comprueba el PAR, y en las dos direcciones: quien declara environment tiene que
// estrecharse a ese environment, y quien no lo declara no puede estrecharse a ninguno. La
// tabla se LEE de `scripts/aws-iam-phase2.sh`; no se copia aquí, que es como los dos
// extremos de un invariante entre ficheros acaban discrepando.
func checkEnvironmentMatchesTrust(path string, j job, jobName, where string) {
	estate, known := jobEstate[jobName]
	if !known {
		finding("%s has no estate in this guard's table, so its OIDC `sub` cannot be paired "+
			"with any role trust — a privileged job whose estate nobody declares is a job "+
			"whose trust nobody can check", where)
		return
	}
	subs := estateSubTargets(path)
	if subs == nil {
		return // su ausencia ya se ha reportado como hallazgo; no se acusa dos veces
	}
	sub, ok := subs[estate]
	if !ok {
		finding("%s applies estate %q and scripts/aws-iam-phase2.sh declares no SUB_TARGET "+
			"for it: the trust this job needs is not written anywhere", where, estate)
		return
	}
	env := strings.TrimSpace(j.Environment.Value)
	if j.Environment.IsZero() {
		env = ""
	}
	wantEnv := strings.TrimPrefix(sub, "environment:")
	switch {
	case env == "" && strings.HasPrefix(sub, "environment:"):
		finding("%s declares no `environment` but the trust of estate %q pins %q: GitHub "+
			"switches the OIDC `sub` claim only when a job declares one, so it would emit a "+
			"`ref:` sub and the role would stop being assumable", where, estate, sub)
	case env != "" && !strings.HasPrefix(sub, "environment:"):
		finding("%s declares `environment: %s` but the trust of estate %q pins %q: declaring "+
			"an environment switches the OIDC `sub` claim, so the role stops being "+
			"assumable — and it fails at STS, not here", where, env, estate, sub)
	case env != "" && env != wantEnv:
		finding("%s declares `environment: %s` while the trust of estate %q pins "+
			"`environment:%s`: the two names have to be the same string", where, env, estate, wantEnv)
	}
}

// estateSubTargets lee la tabla estate → sub de scripts/aws-iam-phase2.sh. La raíz se
// deriva de la ruta del workflow (.github/workflows/<f>), así que el guard no necesita
// saber dónde está montado el árbol.
func estateSubTargets(wfPath string) map[string]string {
	abs, err := filepath.Abs(wfPath)
	if err != nil {
		cannot("cannot resolve %s: %v", wfPath, err)
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(abs)))
	iam := filepath.Join(root, "scripts", "aws-iam-phase2.sh")
	raw, err := os.ReadFile(iam)
	if err != nil {
		if os.IsNotExist(err) {
			// ⛔ AUSENTE ES HALLAZGO, NO «no he podido mirar». El fichero que falta es el que
			// HACE la transicion de IAM, y `check-aws-estate.sh` ya lo reporta como hallazgo
			// (rc 1). Contestar 2 desde aqui corre PRIMERO y degrada ese 1 a un 2 — es decir,
			// convierte un defecto medido en «no pude mirar», que es exactamente la confusion
			// que la regla de las tres respuestas existe para cortar.
			finding("%s is missing, so no job's environment can be paired with its role trust: "+
				"the script that performs the IAM phase is not in the tree", iam)
			return nil
		}
		cannot("cannot read %s, which holds the estate→sub table this check needs: %v", iam, err)
	}
	out := make(map[string]string)
	for _, m := range estateSubRe.FindAllStringSubmatch(string(raw), -1) {
		out[m[1]] = m[2]
	}
	if len(out) == 0 {
		cannot("scripts/aws-iam-phase2.sh declares no estate→SUB_TARGET line this guard can " +
			"read: the pairing between a job's environment and its role trust cannot be checked")
	}
	return out
}

// ⛔ DOS ENTORNOS OBLIGAN A DOS JOBS, Y DOS JOBS DERIVAN. Actions no tiene herencia de job,
// así que `apply` y `apply-production` son copias: mismo OpenTofu pinchado por sha256, mismo
// CLI de AWS pinchado, mismo canje OIDC pinchado por digest, mismo `tofu init` con bloqueo
// de estado. Nada obliga a que sigan siéndolo — y el modo de fallo es exactamente el que esta
// casa lleva midiendo: se sube un pin en el job que alguien está tocando y el otro se queda
// atrás, en silencio, hasta que un apply corre con una versión que nadie revisó.
//
// Se comparan las LÍNEAS EJECUTABLES, no los bytes: los comentarios difieren A PROPÓSITO
// (la razón de cada paso se escribe UNA vez, en `apply`, para que no derive), y compararlos
// obligaría a duplicar la prosa, que es el defecto contrario. Y se comparan tras sustituir
// el nombre del entorno, que es lo único que puede diferir legítimamente.
func checkApplyJobsParity(path string, wf workflow) {
	a, okA := wf.Jobs["apply"]
	b, okB := wf.Jobs["apply-production"]
	if !okA || !okB {
		return // un árbol con un solo entorno no tiene nada que comparar
	}
	if len(a.Steps) != len(b.Steps) {
		finding("%s job \"apply\" has %d step(s) and \"apply-production\" has %d: the two "+
			"environments run the same procedure, so a step that exists on one side only is "+
			"drift — and the missing one is usually a check", path, len(a.Steps), len(b.Steps))
		return
	}
	for i := range a.Steps {
		sa, sb := a.Steps[i], b.Steps[i]
		// `EqualFold` y no `!=`: el nombre de una accion es insensible a mayusculas en
		// GitHub y lo que se fija es el DIGEST. Compararlo sensible convertia una variante
		// de capitalizacion —punto ciego DECLARADO de la comprobacion de firma— en una
		// divergencia inventada.
		if !strings.EqualFold(strings.TrimSpace(sa.Uses), strings.TrimSpace(sb.Uses)) {
			finding("%s step %d differs between the two apply jobs: `uses: %s` and `uses: %s` "+
				"— a pin bumped on one environment and not on the other means one apply runs "+
				"code nobody reviewed", path, i+1, sa.Uses, sb.Uses)
			continue
		}
		if ea, eb := executableLines(sa.command()), executableLines(sb.command()); ea != eb {
			finding("%s step %d (%q / %q) runs different commands in the two apply jobs, after "+
				"the estate name is normalised away: %s", path, i+1,
				stepLabel(sa), stepLabel(sb), firstDifference(ea, eb))
		}
	}
}

// executableLines deja el cuerpo de un `run:` en sus líneas con código: sin comentarios de
// shell, sin líneas en blanco, y con el nombre del entorno normalizado. `production` →
// `sandbox` en ese orden, porque `apply-production-estate` contiene `production`.
func executableLines(body string) string {
	body = strings.ReplaceAll(body, "cloud/production/", "cloud/sandbox/")
	body = strings.ReplaceAll(body, "apply-production-estate", "apply-sandbox-estate")
	body = strings.ReplaceAll(body, "production", "sandbox")
	out := make([]string, 0, 16)
	for _, l := range strings.Split(body, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, t)
	}
	return strings.Join(out, "\n")
}

func firstDifference(a, b string) string {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(la) || i < len(lb); i++ {
		x, y := "", ""
		if i < len(la) {
			x = la[i]
		}
		if i < len(lb) {
			y = lb[i]
		}
		if x != y {
			return fmt.Sprintf("line %d is %q in \"apply\" and %q in \"apply-production\"", i+1, x, y)
		}
	}
	return "the bodies differ but no line does — report this, it is a bug in the comparison"
}

// ⛔ DOS ESTATES NO PUEDEN PEDIR EL MISMO NOMBRE. ACM valida por DNS, así que dos estates
// que declaren el mismo hostname piden el mismo certificado y **el mismo CNAME de
// validación**: el segundo pisa al primero y el síntoma es un certificado que se queda
// PENDING_VALIDATION para siempre, o peor, un CNAME de servicio apuntando al balanceador
// equivocado. No lo ve `tofu validate`, porque cada estate es correcto por separado.
//
// Y el modo de fallo REAL no es que alguien escriba el mismo nombre dos veces: es el
// OLVIDO. Los defaults de la raíz son los nombres de PRODUCCIÓN —decisión de del
// 2026-09-02, y correcta: son los definitivos—, así que un job de apply que deje su
// hostname vacío hereda el de producción sin que nada lo diga. Mientras hubo un solo
// estate eso era el comportamiento deseado; con dos es una colisión silenciosa.
//
// ⇒ se exige que CADA job de apply resuelva a un hostname propio y no vacío. La expresión
// no se evalúa —eso es de GitHub—, se compara como texto: dos jobs con la MISMA expresión
// resuelven al mismo sitio, y un job sin ella resuelve al default de la raíz, que es el de
// producción. Las dos son hallazgo.
func checkApplyHostnames(path string, wf workflow, privileged map[string]bool) {
	names := make([]string, 0, len(privileged))
	for name := range privileged {
		if _, applies := jobEstate[name]; applies && name != "push" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	seen := map[string]string{} // hostname -> primer job que lo declara
	for _, name := range names {
		j := wf.Jobs[name]
		for _, key := range []string{"TF_VAR_hostname", "TF_VAR_ingest_hostname"} {
			v := strings.TrimSpace(j.Env[key])
			where := fmt.Sprintf("%s job %q", path, name)
			if v == "" {
				finding("%s does not set %s: it would fall back to the ROOT default, which is "+
					"the PRODUCTION name, and two estates would request the same ACM "+
					"certificate and the same validation CNAME", where, key)
				continue
			}
			// ⛔ Y LO QUE VALE ES A QUÉ RESUELVE CUANDO NADIE TECLEA NADA, que es el camino
			// del OLVIDO y el único que importa aquí. `${{ inputs.hostname }}` a secas NO
			// está vacío como texto —parece puesto— y sin embargo resuelve a "" con una
			// entrada en blanco, así que el estate cae al default de la raíz, que es el
			// nombre de producción. La primera versión de este control comparaba el texto
			// y por eso NO mataba ese mutante: lo cazó su propio banco antes de aterrizar.
			resolved, ok := resolvedWhenInputEmpty(v)
			if !ok {
				finding("%s sets %s to %q, an expression with no non-empty fallback: an empty "+
					"dispatch input resolves it to \"\" and the estate falls back to the ROOT "+
					"default, which is the PRODUCTION name", where, key, v)
				continue
			}
			if first, dup := seen[resolved]; dup {
				finding("%s resolves %s to the same name as job %q (%s): ACM validates by DNS, "+
					"so two estates on one name fight over the same certificate and the same "+
					"validation CNAME", where, key, first, resolved)
				continue
			}
			seen[resolved] = name
		}
	}
}

// resolvedWhenInputEmpty devuelve el nombre al que un valor de `env:` resuelve cuando la
// entrada del dispatch llega vacía. Un literal resuelve a sí mismo. Una expresión sólo
// resuelve a algo si trae un respaldo literal no vacío tras un `||`; si no lo trae, resuelve
// a "" y el segundo booleano lo dice.
func resolvedWhenInputEmpty(v string) (string, bool) {
	if !strings.Contains(v, "${{") {
		return v, true
	}
	i := strings.LastIndex(v, "||")
	if i < 0 {
		return "", false
	}
	tail := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v[i+2:]), "}}"))
	tail = strings.TrimSpace(tail)
	if len(tail) >= 2 && (tail[0] == '\'' || tail[0] == '"') && tail[len(tail)-1] == tail[0] {
		tail = tail[1 : len(tail)-1]
	}
	if tail == "" || strings.Contains(tail, "${{") || strings.Contains(tail, "inputs.") {
		return "", false
	}
	return tail, true
}

// ⛔ LO QUE UN APPLY CONSUME TIENE QUE ESTAR FIJADO Y FIRMADO. `aws-images.yml` firma lo que
// publica y lee su firma de vuelta, pero esa promesa acababa en el REGISTRO: nada ataba el
// digest que alguien teclea en el dispatch con el que se firmo alli, asi que un digest de otro
// sitio —o de una corrida que nadie reviso— se desplegaba igual. Y una referencia por ETIQUETA
// aplica sin protestar, dejando de fijar lo que se despliega. Lo levanto el contraste `sol max`
// (F-09).
//
// Dos pasos, y los dos por LINEA DE COMANDO y no por subcadena: una mencion dentro de un `echo`
// no es una invocacion, que es la leccion que este arbol lleva pagada varias veces.
//
// ⚠ Y su limite, declarado: esto comprueba que los pasos ESTAN y que invocan lo que dicen. Que
// la firma sea buena lo dice cosign en la corrida; que el digest sea el de la imagen que se
// quiere, lo dice quien lo teclea. Un gate sin red no puede saber ninguna de las dos.
// tfvarsRefusal: `TF_VAR_*` es la PRECEDENCIA MAS BAJA de OpenTofu por encima de los defaults.
// Un `terraform.tfvars` o cualquier `*.auto.tfvars` en el directorio del estate GANA, asi que el
// valor que este job verifica y el que OpenTofu aplica pueden ser distintos — el contraste
// `sol max` (B-02) lo midio con un tag SIN FIRMA que quedo CLEAN. La unica forma de que la
// verificacion signifique algo es que no exista ese canal, y el job tiene que comprobarlo.
// ⛔ ANCLADO AL BUCLE QUE LOS ENUMERA, no a la palabra. La primera version casaba cualquier
// linea con `tfvars`, y su propio mutante la desmintio: el `echo` que REPORTA la comprobacion
// —«…y ningun fichero tfvars puede sobreescribirlas»— la satisfacia, asi que se podia borrar el
// rechazo entero y el guard seguia en verde. Es la quinta vez en esta jornada que una
// comprobacion mia se cumple con prosa, y aqui la prosa era el mensaje de exito del propio
// paso: el sitio mas facil de olvidar.
var tfvarsRefusal = regexp.MustCompile(`(?m)^[[:space:]]*for [A-Za-z_][A-Za-z0-9_]* in [^\n]*tfvars`)

func checkConsumedImagesAreQualified(path string, j job, jobName, where string) {
	if jobName == "push" {
		return // el de imagenes publica, no consume
	}
	pinned, verified, noTfvars := false, false, false
	for _, st := range j.Steps {
		for _, line := range strings.Split(st.command(), "\n") {
			if digestShapeCheck.MatchString(line) {
				pinned = true
			}
			if verifyCommand.MatchString(line) {
				verified = true
			}
			if tfvarsRefusal.MatchString(line) {
				noTfvars = true
			}
		}
	}
	if !noTfvars {
		finding("%s verifies TF_VAR_* values that OpenTofu may not be the ones it applies: a "+
			"terraform.tfvars or any *.auto.tfvars in the estate directory takes precedence "+
			"over TF_VAR_*, so the digest checked and the digest deployed can differ. The job "+
			"must refuse that channel before applying", where)
	}
	// ⛔⛔ Y NINGUNO DE ESOS PASOS PUEDE TOLERAR SU PROPIO FALLO. Comprobar que un paso EXISTE
	// no dice nada si se le permite fallar: `continue-on-error: true` sobre la verificacion de
	// firma deja aplicar despues de que cosign diga que no, y los dos controles de arriba
	// siguen viendo el paso y siguen en verde. Lo midio el contraste `sol max` (B-03) con un
	// mutante de UNA linea.
	//
	// Se prohibe en TODO paso del job y no solo en los dos: un `continue-on-error` en el canje
	// de credenciales, en la comprobacion de fase de IAM o en el propio `tofu apply` tiene
	// exactamente la misma forma y el mismo final. Y un `if:` de paso es la otra mitad de la
	// misma puerta —un paso que no corre no protege— asi que se prohibe igual sobre los pasos
	// que cualifican.
	for i, st := range j.Steps {
		if !isFalse(st.ContinueOnError) {
			finding("%s step %d (%q) sets continue-on-error: a step that may fail does not "+
				"qualify anything — the apply would proceed after cosign said no, with every "+
				"presence check still green", where, i+1, stepLabel(st))
		}
	}

	if !pinned {
		finding("%s never checks that the image references it applies are pinned by digest: a "+
			"tag applies without complaining and stops pinning what gets deployed, and a typo "+
			"surfaces as a task definition that cannot pull, with the estate already applied",
			where)
	}
	if !verified {
		finding("%s applies image references without reading their signature back with "+
			"cosign-verified.sh verify: aws-images.yml signs what it publishes, but nothing "+
			"tied that promise to the digest THIS job consumes", where)
	}
}

// privilegedJobs son los jobs de ESTE workflow que `allowedApplyIf` autoriza a canjear
// OIDC. Se deriva del mapa a propósito: un job nuevo que asuma un rol sin entrada allí sale
// como hallazgo, y una entrada allí sin job en el fichero no inventa nada.
func privilegedJobs(wf workflow) map[string]bool {
	set := make(map[string]bool)
	for name := range wf.Jobs {
		if _, ok := allowedApplyIf[name]; ok {
			set[name] = true
		}
	}
	return set
}

func checkNoCredentialsOutside(path string, wf workflow, privileged map[string]bool) {
	names := make([]string, 0, len(wf.Jobs))
	for name := range wf.Jobs {
		names = append(names, name)
	}
	sort.Strings(names)
	allowed := make([]string, 0, len(privileged))
	for name := range privileged {
		allowed = append(allowed, name)
	}
	sort.Strings(allowed)
	for _, name := range names {
		if privileged[name] {
			continue
		}
		j := wf.Jobs[name]
		where := fmt.Sprintf("%s job %q", path, name)
		if len(credentialSteps(j.Steps)) > 0 {
			finding("%s assumes an AWS role, but only %v may: this job is reachable without the "+
				"dispatch confirmation", where, allowed)
		}
		if j.Permissions["id-token"] == "write" {
			finding("%s requests id-token: write, but only %v may", where, allowed)
		}
	}
}

// ⛔ AQUÍ VIVÍA `checkBackendLock`, Y SE RETIRA PORQUE ERA FALSIFICABLE. Comprobaba que el
// `run:` del paso de apply contuviera `use_lockfile=true`, y el contraste `sol max` del
// 2026-08-27 (C-01) lo rompió con un mutante de una línea: poner el flag real en `false` y
// añadir `echo use_lockfile=true`. También pasaba en vacío si alguien borraba todos los
// `tofu init`. La invariante no se ha perdido: se ha MOVIDO a donde no se puede fingir —
// `deploy/aws/versions.tf` la declara en el bloque `backend "s3"` y la verifica
// `scripts/hcl-module-guard`, que lee el árbol HCL. Un comentario no es un atributo.

// ── El workflow de imágenes ──────────────────────────────────────────────────
//
// Su invariante es la GEMELA de la del apply, y por la misma razón: empuja a un registro
// de NUESTRA cuenta, así que también canjea OIDC y también tiene que estar ordenado. Y una
// propia: sólo un dispatch confirmado puede llegar a ECR.
func checkImagesWorkflow(path string) {
	wf := parse(path)

	// ⛔ NADA AUTOMÁTICO TOCA AWS. Este workflow empuja a un registro de NUESTRA cuenta,
	// así que su único disparador legítimo es un dispatch confirmado. Un `push:` de rama
	// o de etiqueta convertiría cualquier `git push --tags` en un efecto externo sobre la
	// cuenta sin que nadie lo confirme, y la orden 12 (el apply de AWS/cloud va AL FINAL)
	// no distingue entre «crear un recurso» y «publicar un artefacto en él».
	//
	// Se comprueba por AUSENCIA y no por presencia: exigir «tiene workflow_dispatch»
	// dejaría pasar un fichero que ADEMÁS tuviera `push:` o `workflow_call:`. El conjunto
	// tiene que ser exactamente uno.
	if len(wf.On) != 1 {
		names := make([]string, 0, len(wf.On))
		for k := range wf.On {
			names = append(names, k)
		}
		sort.Strings(names)
		finding("%s declares triggers %v; the only trigger that may reach ECR is workflow_dispatch",
			path, names)
	} else if _, ok := wf.On["workflow_dispatch"]; !ok {
		for k := range wf.On {
			finding("%s is triggered by %q, not by workflow_dispatch", path, k)
		}
	}

	pushJob, ok := wf.Jobs["push"]
	if !ok {
		finding("%s has no job named \"push\" — this guard cannot name what it did not find", path)
		return
	}

	if !strings.Contains(pushJob.If, "workflow_dispatch") {
		finding("%s job \"push\" is not limited to workflow_dispatch", path)
	}
	if !strings.Contains(pushJob.If, "push-images-to-ecr") {
		finding("%s job \"push\" does not require the confirmation token push-images-to-ecr", path)
	}

	requireGate(path, "push", pushJob, "validate", wf)
	checkApplyJob(path, pushJob, "push", wf)
	checkNoCredentialsOutside(path, wf, privilegedJobs(wf))
	checkNoSudo(path, wf)

	// Firma: la instaladora aprobada y el lanzador verificado. Que el pin concreto sea el
	// aprobado lo dice `check-cosign-pins.sh`, que ya barre todos los workflows; aquí lo
	// que se exige es que la firma EXISTA en el camino que publica, y que no sea una
	// mención en un comentario.
	// ⛔ EL NUMERO DE IMAGENES SE DERIVA, NO SE ESCRIBE. Este bloque decia `uploaded < 2` y
	// `verified` era un BOOLEANO, y las dos cosas eran correctas por accidente mientras el
	// workflow construyera exactamente dos imagenes. Al anadir la tercera (`cloud-roles`),
	// `3 < 2` es falso y `verified` sigue siendo true con UNA sola verificacion: una imagen
	// podria irse a ECR sin firma leida de vuelta y este guard diria que todo bien. Es la
	// misma clase que los tres defectos que destapo el segundo job de apply.
	//
	// Se cuentan los `docker buildx build … --push` del propio job y se exige que firmas y
	// verificaciones igualen esa cuenta. Una imagen nueva sin su firma ya no pasa.
	built := 0
	for _, s := range pushJob.Steps {
		for _, line := range strings.Split(s.command(), "\n") {
			if buildxPush.MatchString(line) {
				built++
			}
		}
	}
	if built == 0 {
		finding("%s job \"push\" has no `docker buildx build … --push` line this guard can "+
			"read: the number of images it publishes is UNKNOWN, so no signature count can be "+
			"required of it", path)
	}
	// ⛔ CONJUNTOS DE DESTINOS, NO CUENTAS. Contar firmas y compararlas con `<` deja pasar la
	// compensacion: firmar DOS veces la misma imagen y ninguna vez otra da el mismo total y el
	// guard callaba (contraste `sol max`, F-04). Lo que tiene que valer es que el conjunto de
	// destinos FIRMADOS y el de VERIFICADOS sean, cada uno, tan grande como el numero de
	// imagenes construidas — y el mismo conjunto.
	signTargets := map[string]bool{}
	verifyTargets := map[string]bool{}
	installer, signed, verifies, uploaded := false, false, 0, 0
	for _, s := range pushJob.Steps {
		if strings.HasPrefix(strings.ToLower(s.Uses), "sigstore/cosign-installer@") {
			installer = true
		}
		// ⛔ POR LÍNEA DE COMANDO, NO POR SUBCADENA. El contraste sustituyó las dos llamadas
		// por `echo "cosign-verified.sh sign (not executed)"` y este guard dijo que la
		// imagen se firmaba (C-01). Una mención dentro de un `echo` no es una invocación.
		for _, line := range strings.Split(s.command(), "\n") {
			if signCommand.MatchString(line) {
				signed = true
				// ⛔ `--upload` VALE true POR DEFECTO **salvo que el entorno diga otra cosa**:
				// cosign v2 vincula `COSIGN_UPLOAD` al flag cuando el flag no es explícito, así
				// que en un runner persistente `COSIGN_UPLOAD=false` deja la firma calculada y
				// SIN PUBLICAR, con el paso en verde (E-01). Un flag explícito gana al entorno.
				if strings.Contains(line, "--upload=true") {
					uploaded++
					if t := lastArg(line); t != "" {
						signTargets[t] = true
					}
				}
			}
			if verifyCommand.MatchString(line) {
				verifies++
				if t := lastArg(line); t != "" {
					verifyTargets[t] = true
				}
			}
		}
	}
	if !installer {
		finding("%s job \"push\" publishes without installing cosign", path)
	}
	if !signed {
		finding("%s job \"push\" publishes without a cosign-verified.sh sign COMMAND: "+
			"an unsigned image in ECR is an artifact nobody can attribute", path)
	}
	if signed && built > 0 && uploaded < built {
		finding("%s job \"push\" builds %d image(s) and signs with an explicit --upload=true "+
			"only %d time(s): an inherited COSIGN_UPLOAD=false leaves the signature unpublished "+
			"and the step green, so every image needs its own explicit flag", path, built, uploaded)
	}
	if built > 0 && len(signTargets) < built {
		finding("%s job \"push\" builds %d image(s) but signs only %d DISTINCT target(s): "+
			"signing one image twice and another not at all keeps the count and leaves an "+
			"unsigned image in ECR", path, built, len(signTargets))
	}
	if built > 0 && len(verifyTargets) < built {
		finding("%s job \"push\" builds %d image(s) but reads back only %d DISTINCT "+
			"signature(s): a repeated verify does not cover the one nobody checked",
			path, built, len(verifyTargets))
	}
	for t := range signTargets {
		if !verifyTargets[t] {
			finding("%s job \"push\" signs %s and never reads that signature back: signing "+
				"without verifying the same target is a claim, not evidence", path, t)
		}
	}
	if verifies == 0 {
		finding("%s job \"push\" never reads the signature back with cosign-verified.sh verify: "+
			"signing that is not verified against the pushed digest is a claim, not evidence", path)
	} else if built > 0 && verifies < built {
		finding("%s job \"push\" builds %d image(s) and reads back only %d signature(s): the "+
			"one nobody verifies is the one that can reach ECR unsigned", path, built, verifies)
	}
}

func main() {
	if len(os.Args) != 3 {
		cannot("usage: aws-apply-guard <aws-terraform.yml> <aws-images.yml>")
	}
	tfPath, imgPath := os.Args[1], os.Args[2]

	wf := parse(tfPath)

	applyJob, ok := wf.Jobs["apply"]
	if !ok {
		cannot("%s has no job named \"apply\" — the subject of this guard is absent", tfPath)
	}
	if _, ok := wf.Jobs["validate"]; !ok {
		cannot("%s has no job named \"validate\"", tfPath)
	}

	requireGate(tfPath, "apply", applyJob, "validate", wf)
	checkApplyJob(tfPath, applyJob, "apply", wf)

	// ⛔ CADA ENTORNO ES UN JOB, Y CADA JOB PAGA LAS MISMAS INVARIANTES. `apply-production`
	// es opcional en el fichero —el árbol puede no tenerlo todavía— pero si está, no entra
	// por una puerta más barata: misma dependencia del gate, misma condición exacta, mismo
	// canje ordenado. Un segundo entorno cuyo job se auditara menos que el primero sería
	// justo al revés de lo que hace falta.
	for _, name := range privilegedTerraformJobs {
		j, ok := wf.Jobs[name]
		if !ok {
			continue
		}
		requireGate(tfPath, name, j, "validate", wf)
		checkApplyJob(tfPath, j, name, wf)
	}
	checkApplyJobsParity(tfPath, wf)
	checkApplyHostnames(tfPath, wf, privilegedJobs(wf))
	checkNoCredentialsOutside(tfPath, wf, privilegedJobs(wf))
	checkNoSudo(tfPath, wf)
	checkImagesWorkflow(imgPath)

	if len(findings) > 0 {
		for _, f := range findings {
			fmt.Fprintf(os.Stderr, prefix+": FAIL — %s\n", f)
		}
		os.Exit(rcFinding)
	}
	fmt.Printf("%s: apply-wiring-ok — privileged jobs gated by an exact `if` and by `needs` on a "+
		"gate that actually runs, no matrix, no job-level continue-on-error, no credential env "+
		"override, OIDC exchange pinned by digest and ordered before tofu, images signed AND "+
		"verified on the way to ECR, each job's environment paired with its role trust, and "+
		"the two apply jobs running the same procedure on hostnames of their own\n", prefix)
	os.Exit(rcOK)
}
