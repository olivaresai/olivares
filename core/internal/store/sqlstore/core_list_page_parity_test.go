// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// ⛔ POR QUE EXISTE, y es EL MISMO defecto que ya cazo el contraste del 2026-08-26 sobre
// `EVIDENCE_PAGE` — repetido por mi, en con otro nombre.
//
// La consola pide el maximo del store con `LIST_CEILING` (`web/src/lib/api/endpoints.ts`),
// y escribi una celda TypeScript cuyo comentario decia «ata el numero a la razon por la que es
// ese y no otro»... comparandolo con OTRO LITERAL DE TYPESCRIPT (`expect(LIST_CEILING)
// .toBe(1000)`). Eso no ata nada: mutar `maxLimit` aqui de 1000 a 500 dejaba aquella bateria
// entera en verde. Lo nombro el contraste `the model` del 2026-08-27 (hallazgo F), y el
// fichero de al lado —`evidence_page_parity_test.go`— ya llevaba escrita la leccion. Un
// comentario no es un contrato; el contrato es esta lectura.
//
// ⛔ LA DIRECCION IMPORTA. Vive del lado de Go y LEE el fichero de la consola, no al reves,
// porque el numero es del store: si el maximo cambia aqui, es la consola la que se queda pidiendo
// un techo que ya no existe.
//
// ⛔ NO PUDE MIRAR != COINCIDEN. Si el fichero no esta o el literal no aparece, esto FALLA en vez
// de pasar en silencio: un cruce que no puede leer su otra mitad no ha comprobado nada.
func TestCoreListPageMaxMatchesStoreMaxLimit(t *testing.T) {
	const ruta = "../../../../web/src/lib/api/endpoints.ts"

	datos, err := os.ReadFile(ruta)
	if err != nil {
		t.Fatalf("NO PUDE MIRAR: no puedo leer %s: %v", ruta, err)
	}

	re := regexp.MustCompile(`(?m)^export const LIST_CEILING\s*=\s*(\d+)`)
	m := re.FindSubmatch(datos)
	if m == nil {
		t.Fatalf("NO PUDE MIRAR: no encuentro `export const LIST_CEILING = <n>` en %s; "+
			"si la constante se renombro, este contrato hay que reapuntarlo, no borrarlo", ruta)
	}

	consola, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatalf("NO PUDE MIRAR: LIST_CEILING no es un entero: %q", m[1])
	}

	if consola != maxLimit {
		t.Fatalf("la consola pide %d y el store sirve como maximo %d.\n"+
			"  `LIST_CEILING` es el techo que la consola manda en `?limit` a las listas del\n"+
			"  NUCLEO (`/v1/users`, `/v1/agents`), que llegan a este repositorio generico. Si los dos\n"+
			"  numeros se separan, o pide de menos —lista mas corta de lo que el motor daria— o de\n"+
			"  mas, y entonces %s:maxLimit lo recorta en silencio y el techo escrito en la consola\n"+
			"  deja de ser el que se aplica.\n"+
			"  Cambia AMBOS: %s y sqlstore/generic.go:maxLimit.", consola, maxLimit, "generic.go", ruta)
	}
}
