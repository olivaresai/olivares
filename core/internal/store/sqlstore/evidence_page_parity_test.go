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

// ⛔ POR QUE EXISTE, y es un defecto MIO cazado por el contraste `the model` del 2026-08-26.
//
// La consola pide el maximo del store con `EVIDENCE_PAGE`
// (`web/src/features/models/api.ts`), y yo escribi un caso en TypeScript que decia «ata la
// constante al maximo REAL del store» comparandola con... OTRO LITERAL DE TYPESCRIPT. El
// comentario afirmaba un enlace Go<->TS que el codigo no creaba: mutar `maxLimit` aqui de 1000 a
// 999 dejaba aquella bateria **4/4 en verde, rc 0 y sin un mensaje**. Un comentario no es un
// contrato; el contrato es esta lectura.
//
// ⛔ LA DIRECCION IMPORTA. Este test vive del lado de Go y LEE el fichero de la consola, no al
//
//	reves, porque el numero es del store: si el maximo cambia aqui, es la consola la que se queda
//	pidiendo un techo que ya no existe. La paridad se afirma donde nace el valor.
//
// ⛔ NO PUDE MIRAR != COINCIDEN. Si el fichero de la consola no esta o el literal no se encuentra,
//
//	esto FALLA en vez de pasar en silencio: un chequeo cruzado que no puede leer su otra mitad no
//	ha comprobado nada, y un verde por no haber mirado es el fallo que esta casa lleva meses
//	cazando. Misma clase que las paridades de `cmd/olivares/openapi_beta_coverage_test.go`.
func TestEvidencePageMatchesStoreMaxLimit(t *testing.T) {
	const ruta = "../../../../web/src/features/models/api.ts"

	datos, err := os.ReadFile(ruta)
	if err != nil {
		t.Fatalf("NO PUDE MIRAR: no puedo leer %s: %v", ruta, err)
	}

	re := regexp.MustCompile(`(?m)^export const EVIDENCE_PAGE\s*=\s*(\d+)`)
	m := re.FindSubmatch(datos)
	if m == nil {
		t.Fatalf("NO PUDE MIRAR: no encuentro `export const EVIDENCE_PAGE = <n>` en %s; "+
			"si la constante se renombro, este contrato hay que reapuntarlo, no borrarlo", ruta)
	}

	consola, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatalf("NO PUDE MIRAR: EVIDENCE_PAGE no es un entero: %q", m[1])
	}

	if consola != maxLimit {
		t.Fatalf("la consola pide %d y el store sirve como maximo %d.\n"+
			"  La consola manda `limit=EVIDENCE_PAGE` para pedir la MAXIMA PAGINA PERMITIDA — no la\n"+
			"  lista completa: por encima del maximo el store sigue recortando y eso se declara con\n"+
			"  `has_more`, no con este numero. Si los dos\n"+
			"  numeros se separan, o pide de menos (lista recortada en silencio) o de mas (el store\n"+
			"  la recorta igual y nadie lo dice).\n"+
			"  Cambia AMBOS: %s y sqlstore/generic.go:maxLimit.", consola, maxLimit, ruta)
	}
}
