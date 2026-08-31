// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package grok

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// hooktrust.go mide el ÚNICO camino por el que un hook gobernado de Grok Build deja de correr
// sin que nadie se entere.
//
// ⛔ POR QUÉ ES UN HALLAZGO Y NO UN DETALLE. Verificado el 2026-08-19 en el fuente público, en el
//    commit ya anclado en `session/hook.go`:
//
//      · `xai-grok-hooks/src/trust.rs:127-129` → el fichero es
//        `<user_grok_home>/disabled-hooks`, es decir **`~/.grok/disabled-hooks`**: texto plano,
//        en el HOME del usuario, **un nombre de hook por línea** (`#` comenta).
//      · `trust.rs:42-57`  → `is_hook_disabled(name)` compara el NOMBRE, y nada más. No mira de
//        qué capa salió el hook, ni si lo puso un administrador.
//      · `dispatcher.rs:27` → `if !spec.enabled || is_hook_disabled(&spec.name) { … }`, en el
//        DESPACHO. No es una guarda de camino rápido: es la ejecución.
//      · `trust.rs:60`     → y hay API pública `disable_hook(name)` para escribirlo.
//
//    ⇒ **Un usuario puede desactivar el hook que puso el administrador**, y el fichero que lo
//    hace vive en su propio HOME, fuera del alcance de `/etc/grok/requirements.toml` y de MDM —
//    que acotan CONFIGURACIÓN, y esto no es configuración: es otro fichero, leído directamente.
//
//    Es la asimetría que importa para un operador: el perfil de sandbox SÍ se puede imponer
//    (ver `sandbox.go`), y la ejecución del hook NO. Un plano de gobierno que sólo mirase el
//    sandbox diría «impuesto» de una sesión cuyo hook está apagado.
//
// La misma clase de fallo que Codex documenta en `cmd/olivares/cmd_codexhook.go`: un hook que no
// corre **no avisa**. La diferencia es dónde está el interruptor.

// defaultDisabledHooksPath es el fichero que `trust.rs:127-129` construye.
const defaultDisabledHooksPath = "~/.grok/disabled-hooks"

// maxDisabledHooksBytes acota la lectura. Un fichero enorme aquí no es un caso legítimo.
const maxDisabledHooksBytes = 1 << 20

// hooksDesactivados devuelve los nombres listados, en orden estable.
//
// El estado se distingue de los nombres a propósito: «no hay fichero» y «hay fichero y no se ha
// podido leer» son hechos distintos, y colapsarlos daría un verde sobre algo no mirado.
func (s *Source) hooksDesactivados() ([]string, estadoConfig) {
	ruta, err := expandirHome(s.disabledHooksPath)
	if err != nil {
		return nil, configIlegible
	}
	info, err := os.Stat(ruta)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, configAusente
	case err != nil:
		return nil, configIlegible
	case info.Size() > maxDisabledHooksBytes:
		return nil, configIlegible
	}
	f, err := os.Open(ruta) //nolint:gosec // ruta declarada por el operador
	if err != nil {
		return nil, configIlegible
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// Se replica el filtro del fuente: línea vacía o que empieza por `#` no cuenta, y la
		// comparación es sobre la línea RECORTADA (`l.trim() == hook_name`).
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		out = append(out, l)
	}
	if sc.Err() != nil {
		return nil, configIlegible
	}
	sort.Strings(out)
	return out, configLeido
}

// hallazgoHooksDesactivados traduce eso a la observación que un operador puede accionar.
func (s *Source) hallazgoHooksDesactivados(nombres []string, estado estadoConfig, at time.Time) model.FindingReport {
	base := model.FindingReport{
		Kind:        findingKindPosture,
		SubjectKind: subjectSandbox,
		SubjectRef:  s.agentRef,
		OccurredAt:  at,
	}
	switch estado {
	case configLeido:
		if len(nombres) == 0 {
			base.Severity = model.SeverityInfo
			base.Title = s.disabledHooksPath + " exists and disables no hooks — but it remains " +
				"user-writable, and neither requirements.toml nor MDM constrains it"
			return base
		}
		// ⛔ La LISTA, no la cifra. Una fila que diga «3 hooks desactivados» obliga a otra
		//    sesión a ir a buscar cuáles, y el nombre es justo lo que decide si es el nuestro.
		base.Severity = model.SeverityHigh
		base.Title = "Hooks DISABLED in Grok Build by " + s.disabledHooksPath + ": " +
			strings.Join(nombres, ", ") + " — dispatch skips them BY NAME without checking which " +
			"layer set them, so this also disables administrator hooks"
		return base
	case configIlegible:
		base.Severity = model.SeverityMedium
		base.Title = s.disabledHooksPath + " exists and could NOT be read: no claim is made about " +
			"which hooks are disabled — this is not \"none\"; it is \"not measured\""
		return base
	default:
		base.Severity = model.SeverityInfo
		base.Title = "There is no " + s.disabledHooksPath + ": no hook is disabled by name " +
			"(the user can create the file at any time; no layer prevents it)"
		return base
	}
}
