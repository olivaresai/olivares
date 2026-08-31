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
var allowedApplyIf = map[string]string{
	"apply": "github.event_name == 'workflow_dispatch' && github.event.inputs.confirm == 'apply-sandbox-estate'",
	"push":  "github.event_name == 'workflow_dispatch' && github.event.inputs.confirm == 'push-images-to-ecr'",
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
)

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
	if !j.Environment.IsZero() {
		finding("%s declares an `environment`: that switches the OIDC `sub` claim to "+
			"`environment:<name>` while the role's trust pins `ref:refs/heads/main`, so the "+
			"role stops being assumable — and it fails at STS, not here", where)
	}

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

func checkNoCredentialsOutside(path string, wf workflow, privileged string) {
	names := make([]string, 0, len(wf.Jobs))
	for name := range wf.Jobs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name == privileged {
			continue
		}
		j := wf.Jobs[name]
		where := fmt.Sprintf("%s job %q", path, name)
		if len(credentialSteps(j.Steps)) > 0 {
			finding("%s assumes an AWS role, but only %q may: this job is reachable without the "+
				"dispatch confirmation", where, privileged)
		}
		if j.Permissions["id-token"] == "write" {
			finding("%s requests id-token: write, but only %q may", where, privileged)
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
	checkNoCredentialsOutside(path, wf, "push")
	checkNoSudo(path, wf)

	// Firma: la instaladora aprobada y el lanzador verificado. Que el pin concreto sea el
	// aprobado lo dice `check-cosign-pins.sh`, que ya barre todos los workflows; aquí lo
	// que se exige es que la firma EXISTA en el camino que publica, y que no sea una
	// mención en un comentario.
	installer, signed, verified, uploaded := false, false, false, 0
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
				}
			}
			if verifyCommand.MatchString(line) {
				verified = true
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
	if signed && uploaded < 2 {
		finding("%s job \"push\" signs without an explicit --upload=true on both images "+
			"(%d of 2): an inherited COSIGN_UPLOAD=false leaves the signature unpublished and "+
			"the step green", path, uploaded)
	}
	if !verified {
		finding("%s job \"push\" never reads the signature back with cosign-verified.sh verify: "+
			"signing that is not verified against the pushed digest is a claim, not evidence", path)
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
	checkNoCredentialsOutside(tfPath, wf, "apply")
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
		"verified on the way to ECR\n", prefix)
	os.Exit(rcOK)
}
