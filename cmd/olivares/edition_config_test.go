// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "testing"

// TestTheEditionSeamGetsTheResolvedDataDir is the witness for the one link of this chain that can
// be wrong in silence.
//
// ⛔ QUE LA COSTURA RECIBA UN `EditionConfig` LO GARANTIZA EL COMPILADOR; QUE RECIBA EL CAMPO
// CORRECTO NO LO GARANTIZA NADIE. `editionConfigFrom` podría devolver otro directorio —el de
// trabajo, el de configuración, una cadena vacía— y todo compilaría igual. La consecuencia no sería
// un fallo: sería el store privado del cockpit creado EN OTRO SITIO que los datos del motor, con su
// propio `SELECT` en verde y en el directorio equivocado.
//
// ⛔ Y ESTA COSTURA EXISTE POR UNA AUSENCIA MEDIDA. `Migrate` tiene una sola aparición en el árbol
// comercial —su propia definición—, así que un cliente que instala no tiene base del cockpit. La
// causa no era descuido: NINGUNA costura `edition*` recibía configuración, y el overlay no puede
// re-derivar el directorio sin convertirse en un segundo productor de la decisión que la base ya
// toma con `--data-dir` y su cadena de defectos.
func TestTheEditionSeamGetsTheResolvedDataDir(t *testing.T) {
	const dir = "/var/lib/olivares-elegido-por-el-operador"
	got := editionConfigFrom(bootConfig{DataDir: dir})
	if got.DataDir != dir {
		t.Fatalf("la costura recibe %q y el operador pidió %q: el store del add-on aterrizaría "+
			"fuera de los datos del motor, y su propia comprobación saldría verde allí",
			got.DataDir, dir)
	}
	// ⛔ CONTROL POSITIVO, sin el cual lo de arriba lo satisface un mapeo que devuelva SIEMPRE esa
	// cadena. Un segundo valor distinto es lo que separa «lee el campo» de «devuelve una constante».
	const otro = "/srv/olivares"
	if second := editionConfigFrom(bootConfig{DataDir: otro}); second.DataDir != otro {
		t.Fatalf("con otro directorio devuelve %q: el mapeo no lee su entrada", second.DataDir)
	}
	// Y el cero se propaga como cero: una edición sin directorio no debe recibir uno inventado.
	if empty := editionConfigFrom(bootConfig{}); empty.DataDir != "" {
		t.Fatalf("sin directorio la costura recibe %q: alguien lo está inventando", empty.DataDir)
	}
}
