// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// TestEveryRejectedCommandNamesTheFieldThatFailed fija lo que un `invalid_command` tiene que
// decir, y existe por una medida, no por gusto.
//
// ⛔ EL 2026-08-25, conduciendo el kernel con un CLI de TERCERO contra un motor vivo:
// `item.block` sin `blocked_code` contestaba `invalid_command` y NADA MÁS. El agente gastó
// TRES viajes deduciéndolo — validó y falló, probó `item.unblock` para distinguir «comando
// desconocido» de «comando mal formado» (recibió `illegal_transition`, o sea el comando SÍ
// existía) y sólo entonces buscó el campo. La asimetría era la peor posible: el camino de
// ÉXITO nombraba su esquema (`work_command_v1`) y el de FALLO no nombraba nada.
//
// **Un error que no nombra lo que falta convierte validar en adivinar**, y eso lo paga cada
// integrador tercero, no sólo un agente.
func TestEveryRejectedCommandNamesTheFieldThatFailed(t *testing.T) {
	t.Parallel()

	// ⛔ EL CASO QUE MOTIVA TODO, Y SU TRAMPA. `normalizeWorkCommand` vuelca `blocked_code`
	// sobre `Code` para `item.block` y `terminal_code` para `item.fail`/`item.cancel`. Un
	// error que dijera «code» a secas mandaría al llamante a la bandera EQUIVOCADA: sería el
	// mismo defecto con una capa más de disfraz. Por eso se comprueba el nombre exacto.
	for _, row := range []struct {
		name  string
		cmd   WorkCommand
		field string
	}{
		{"item.block sin código dice blocked_code, no code",
			WorkCommand{Command: "item.block", WorkItemID: model.NewID(), Reason: "porque sí"},
			"blocked_code (o code)"},
		{"item.fail sin código dice terminal_code, no blocked_code",
			WorkCommand{Command: "item.fail", WorkItemID: model.NewID(), Reason: "porque sí"},
			"terminal_code (o code)"},
		{"item.block sin item nombra el item",
			WorkCommand{Command: "item.block", Code: "x", Reason: "y"},
			"work_item_id"},
		{"un comando que no existe lo dice, en vez de callar",
			WorkCommand{Command: "item.teleport"},
			"command (comando no reconocido)"},
		{"item.create sin título nombra el título",
			WorkCommand{Command: "item.create", WorkspaceID: model.NewID(), WorkKind: "task"},
			"title"},
		// ⛔ REFUTADO POR CONTRASTE Y ARREGLADO: el nombre de un criterio de aceptación es
		// `criterion_key`, la bandera que el CLI ofrece — NO «acceptance[].key», que es la
		// grafía de la rodaja interna y no existe para quien llama. Era el mismo defecto que
		// este cambio arregla para `blocked_code`, cometido una capa más abajo.
		{"un criterio sin clave nombra la BANDERA, no la rodaja interna",
			WorkCommand{Command: "acceptance.add", WorkItemID: model.NewID(),
				Acceptance: []AcceptanceInput{{Statement: "algo"}}},
			"criterion_key (bandera --criterion-key, o acceptance[].key)"},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			err := validateCommandSyntax(row.cmd)
			we := asWorkError(err)
			if we == nil {
				t.Fatalf("%s: esperaba rechazo y no lo hubo", row.cmd.Command)
			}
			if we.field != row.field {
				t.Fatalf("%s rechazado nombrando %q, quería %q — un campo mal nombrado manda "+
					"al llamante a la bandera equivocada, que es el defecto que esto arregla",
					row.cmd.Command, we.field, row.field)
			}
		})
	}
}

// TestNoCommandIsRejectedWithoutNamingAField es la guarda de COMPLETITUD, y es la que sigue
// valiendo cuando alguien añada el comando número 28.
//
// ⛔ SE DERIVA DEL FUENTE, no de una lista tecleada. Una lista que yo escriba se queda corta el
// día que se añade un comando, y entonces la guarda diría VERDE sobre un conjunto que ya no es
// el suyo — el defecto que esta casa tiene fichado como «instrumentos que miden otro conjunto».
func TestNoCommandIsRejectedWithoutNamingAField(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("work_state.go")
	if err != nil {
		t.Fatalf("no puedo leer el fuente: %v — sin él esto no mide nada", err)
	}
	// ⛔ ANTES ESTO ESCANEABA SOLO EL CUERPO DE `validateCommandSyntax`, y por eso un
	// contraste alcanzó un rechazo mudo que la guarda declaraba inexistente: `acceptance.add`
	// con una clave mal formada delega en `validateAcceptanceInput`, que vivía FUERA del
	// trozo escaneado. **Un test que mide un tramo del camino declara limpio el resto.**
	// Ahora el sujeto es el FICHERO ENTERO: la guarda cubre los ayudantes a los que el
	// validador delega, que son parte del mismo contrato desde fuera.
	fn := src
	if !regexp.MustCompile(`func validateCommandSyntax\(`).Match(src) {
		t.Fatal("no encuentro validateCommandSyntax en el fuente: el derivador está roto, " +
			"y un derivador roto da el CERO cómodo")
	}
	// Control positivo del derivador: tiene que ver los comandos que sabemos que hay.
	comandos := regexp.MustCompile(`"([a-z]+\.[a-z_]+)"`).FindAllStringSubmatch(string(fn), -1)
	vistos := map[string]bool{}
	for _, m := range comandos {
		vistos[m[1]] = true
	}
	if len(vistos) < 20 || !vistos["item.block"] || !vistos["lease.acquire"] {
		t.Fatalf("el derivador ve %d comandos y no reconoce los conocidos: no mide lo que dice",
			len(vistos))
	}

	// ⛔ LA GUARDA — Y AHORA SOBRE TODO RECHAZO, NO SOBRE UN LITERAL.
	//
	// La primera versión contaba EXACTAMENTE `broken(400, "invalid_command")`. O sea que
	// certificaba «no quedan rechazos mudos» habiendo mirado **un solo código** de los que el
	// fichero usa, y **su denominador era la parte, no el todo**. Lo destapó seguir el hallazgo
	// de producto de B1 —«un error que no nombra lo que falta convierte una validación en una
	// adivinanza»— hasta el camino de aceptación, que pasaba entero por delante:
	//
	//	:260 evidence_ref · :264 evidence_hash · :268 waiver_decision_id
	//	:336 acceptance (rango) · :356 criterion_key duplicada · :364 required
	//
	// Seis rechazos, y **cinco de ellos contestaban el MISMO código** `acceptance_incomplete`,
	// que es el peor caso de la clase: el código no distingue la causa y el campo no estaba.
	//
	// Por eso la guarda ya no busca un texto: busca **cualquier llamada a `broken`**, que es
	// justo el constructor sin campo. Si mañana hiciera falta un rechazo genuinamente sin
	// campo, que se cambie esta guarda A PROPÓSITO y con su razón — no que quepa por descuido.
	var mudos []string
	for i, linea := range strings.Split(string(fn), "\n") {
		if strings.Contains(linea, "func broken(") {
			continue // la DEFINICIÓN del constructor, no una llamada
		}
		if strings.Contains(linea, "broken(") && !strings.Contains(linea, "brokenField(") {
			mudos = append(mudos, fmt.Sprintf("  work_state.go:%d: %s", i+1, strings.TrimSpace(linea)))
		}
	}
	if len(mudos) != 0 {
		t.Fatalf("quedan %d rechazos SIN campo en work_state.go:\n%s\n"+
			"Un error mudo obliga al llamante a adivinar qué campo sobra o falta; usa "+
			"brokenField/firstBadField y nombra el campo EN EL VOCABULARIO DE QUIEN LLAMA "+
			"(`blocked_code`, no `Code`)", len(mudos), strings.Join(mudos, "\n"))
	}

	// Y cada comando conocido, con el comando VACÍO, tiene que rechazar nombrando algo.
	for c := range vistos {
		err := validateCommandSyntax(WorkCommand{Command: c})
		we := asWorkError(err)
		if we == nil {
			continue // hay comandos que un documento vacío satisface; no es asunto de esta guarda
		}
		// ⛔ Y SIN FILTRAR POR CÓDIGO. Antes sólo miraba `invalid_command`, así que un
		// `acceptance_incomplete` mudo pasaba por aquí igual que por la guarda de arriba:
		// el mismo sesgo dos veces, en el escáner y en el ejercicio.
		if we.field == "" {
			t.Errorf("%s se rechaza con %q y sin nombrar campo", c, we.code)
		}
	}
}

// TestTheFieldSurvivesTheApplyPathToo cubre el hueco que mi propio testigo NO veía y que destapó
// un contraste externo.
//
// ⛔ EL ARREGLO CUBRÍA MEDIO CAMINO DE TRES FASES. `validate` y `plan` salen por
// `assessmentFromError`, que sí pone el campo en `evidence_ref`. `apply` sale por
// `writeWorkError`, que leía `status`, `code` y `verdict` y **tiraba `we.field`**. Resultado: el
// mismo comando que en `validate` te decía «blocked_code (o code)» te contestaba MUDO en `apply`.
//
// Y la razón de que no lo viera es la de siempre: **mi testigo ejercitaba el camino que yo tenía
// en la cabeza.** El e2e que corrí contra el motor vivo fue un `validate`, porque ése era el caso
// que motivó el arreglo. Un test que recorre la mitad del contrato declara limpia la otra.
func TestTheFieldSurvivesTheApplyPathToo(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	writeWorkError(rec, brokenField(400, "invalid_command", "blocked_code (o code)"))

	var cuerpo map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &cuerpo); err != nil {
		t.Fatalf("la respuesta de apply no es JSON legible: %v — %s", err, rec.Body.String())
	}
	if got := cuerpo["evidence_ref"]; got != "blocked_code (o code)" {
		t.Fatalf("apply respondió evidence_ref=%v, quería el campo culpable. Sin esto, la mitad "+
			"del plano de tres fases sigue obligando al llamante a adivinar", got)
	}

	// ⛔ CONTROL NEGATIVO: un error SIN campo no debe inventarse la clave. Sin esta dirección,
	// la de arriba sólo dice «el mapa tiene una clave», no «la lleva cuando toca».
	rec2 := httptest.NewRecorder()
	writeWorkError(rec2, broken(404, "not_found"))
	var sinCampo map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &sinCampo); err != nil {
		t.Fatalf("respuesta ilegible: %v", err)
	}
	if _, hay := sinCampo["evidence_ref"]; hay {
		t.Fatalf("un error sin campo culpable trae evidence_ref=%v: la clave se está "+
			"inventando", sinCampo["evidence_ref"])
	}
}
