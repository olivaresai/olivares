// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// TestLastActivityNeverGoesBackwards es el testigo OBLIGATORIO de la decisión 2 de K2.
//
// El sello de un frame se toma AL RECIBIRLO (`runtime_bridge.go:33`), no al escribirlo. En cuanto
// un efecto se DIFIERE —que es lo que hace la opción 4 durante la ventana de reserva— entre esos
// dos instantes cabe otra escritura: la transición de reserva pone un sello más nuevo
// (`runtime.go:719`, `:960`) y el volcado diferido lo pisaría con el viejo.
//
// ⇒ `last_activity_at` RETROCEDE y la corrida parece OCIOSA antes de tiempo. Un barrido de
// inactividad que lea esa fila puede matar una sesión que está viva.
//
// ⛔ ESTE DEFECTO LO ENCONTRÓ UN CONTRASTE SOBRE LA NOTA DE DISEÑO, ANTES DE QUE EXISTIERA EL
// CÓDIGO — y por eso la decisión se tomó antes de escribirlo en vez de descubrirse después.
func TestLastActivityNeverGoesBackwards(t *testing.T) {
	t.Parallel()

	nuevo := model.NewTimestamp(time.Date(2026, 8, 25, 12, 0, 30, 0, time.UTC)).String()
	viejo := model.NewTimestamp(time.Date(2026, 8, 25, 12, 0, 10, 0, time.UTC)).String()

	for _, row := range []struct {
		nombre           string
		enLaFila         string
		escribeElVolcado string
		quiero           string
	}{
		{"el volcado trae un sello VIEJO -> la fila conserva el nuevo",
			nuevo, viejo, nuevo},
		{"el volcado trae uno MAS NUEVO -> avanza, que es lo normal",
			viejo, nuevo, nuevo},
		{"iguales -> se queda igual",
			nuevo, nuevo, nuevo},
		{"la fila no tenía sello -> se acepta lo que escriba el volcado",
			"", viejo, viejo},
	} {
		t.Run(row.nombre, func(t *testing.T) {
			rec := model.Record{}
			if row.enLaFila != "" {
				rec[colLastActivityAt] = row.enLaFila
			}
			antes := rec.String(colLastActivityAt)
			rec[colLastActivityAt] = row.escribeElVolcado // lo que hace fn(rec)
			conservaElSelloMasNuevo(rec, antes)
			if got := rec.String(colLastActivityAt); got != row.quiero {
				t.Fatalf("last_activity_at = %q, quería %q.\n"+
					"  Un sello que RETROCEDE hace que la corrida parezca ociosa antes de "+
					"tiempo, y un barrido de inactividad puede matar una sesión viva.", got, row.quiero)
			}
		})
	}
}

// TestTheMonotonicStampDoesNotGuessWhenItCannotCompare — el control de la TERCERA respuesta.
//
// Si un sello no es legible, `max` no puede decidir. La respuesta correcta NO es inventarse un
// orden: es dejar lo que escribió el llamante, que es el comportamiento anterior. Un `max` que
// adivina entre dos sellos incomparables sería peor que no tenerlo.
func TestTheMonotonicStampDoesNotGuessWhenItCannotCompare(t *testing.T) {
	t.Parallel()
	rec := model.Record{colLastActivityAt: "esto-no-es-un-sello"}
	antes := rec.String(colLastActivityAt)
	nuevo := model.NewTimestamp(time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)).String()
	rec[colLastActivityAt] = nuevo
	conservaElSelloMasNuevo(rec, antes)
	if got := rec.String(colLastActivityAt); got != nuevo {
		t.Fatalf("con un sello ilegible devolvió %q: no se puede ordenar lo que no se puede "+
			"leer, así que se deja lo que escribió el llamante en vez de inventar un orden", got)
	}
}
