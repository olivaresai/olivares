// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// ⛔ POR QUE EXISTE. La consola pide el maximo del LEDGER con `AUDIT_PAGE_MAX`
// (`web/src/lib/api/endpoints.ts`), y ese numero NO es el del almacen generico aunque hoy
// coincidan: `auditListInto` (handlers_audit.go) **cae de vuelta a 100** si le piden mas de su
// tope, mientras `sqlstore/generic.go` ACOTA al suyo. Compartir constante haria que subir el tope
// generico —un cambio legitimo en su familia— convirtiera esta llamada en el minimo justo cuando
// pide el maximo.
//
// ⛔ LA DIRECCION IMPORTA: vive del lado de Go y LEE el fichero de la consola, porque el numero es
// del motor. Es el mismo contrato que `sqlstore/evidence_page_parity_test.go` y
// `sqlstore/core_list_page_parity_test.go`; este cubre la tercera familia, que no lo tenia.
//
// ⛔ NO PUDE MIRAR != COINCIDEN. Si falta cualquiera de las dos mitades, esto FALLA en vez de
// pasar en silencio: un cruce que no puede leer su otro lado no ha comprobado nada.
func TestAuditPageMaxMatchesHandlerBound(t *testing.T) {
	const rutaWeb = "../../web/src/lib/api/endpoints.ts"
	const rutaGo = "handlers_audit.go"

	web, err := os.ReadFile(rutaWeb)
	if err != nil {
		t.Fatalf("NO PUDE MIRAR: no puedo leer %s: %v", rutaWeb, err)
	}
	mw := regexp.MustCompile(`(?m)^export const AUDIT_PAGE_MAX\s*=\s*(\d+)`).FindSubmatch(web)
	if mw == nil {
		t.Fatalf("NO PUDE MIRAR: no encuentro `export const AUDIT_PAGE_MAX = <n>` en %s; "+
			"si la constante se renombro, este contrato hay que reapuntarlo, no borrarlo", rutaWeb)
	}
	consola, err := strconv.Atoi(string(mw[1]))
	if err != nil {
		t.Fatalf("NO PUDE MIRAR: AUDIT_PAGE_MAX no es un entero: %q", mw[1])
	}

	// El tope del handler es un literal en su guarda; se lee de ahi y no de una constante
	// porque no la hay. Si la guarda cambia de forma, esto FALLA y hay que reapuntarlo.
	src, err := os.ReadFile(rutaGo)
	if err != nil {
		t.Fatalf("NO PUDE MIRAR: no puedo leer %s: %v", rutaGo, err)
	}
	mg := regexp.MustCompile(`limit\s*<=\s*0\s*\|\|\s*limit\s*>\s*(\d+)`).FindSubmatch(src)
	if mg == nil {
		t.Fatalf("NO PUDE MIRAR: no encuentro la guarda `limit <= 0 || limit > <n>` en %s; "+
			"si el handler cambio de forma, este contrato hay que reapuntarlo", rutaGo)
	}
	motor, err := strconv.Atoi(string(mg[1]))
	if err != nil {
		t.Fatalf("NO PUDE MIRAR: el tope del handler no es un entero: %q", mg[1])
	}

	if consola != motor {
		t.Fatalf("la consola pide %d y el ledger acepta como maximo %d.\n"+
			"  Y aqui pasarse NO se acota: `auditListInto` vuelve a 100 cuando el valor supera su\n"+
			"  tope, asi que una consola que pida de mas se lleva el MINIMO justo cuando cree pedir\n"+
			"  el maximo — el fallo mas silencioso de los dos posibles.\n"+
			"  Cambia AMBOS: %s y la guarda de %s.", consola, motor, rutaWeb, rutaGo)
	}
}
