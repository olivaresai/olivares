// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"sort"
	"testing"
)

// ⛔ UNA CLAVE DE `commandGroups` ES UNA AFIRMACIÓN SOBRE UN COMANDO QUE EXISTE, y hasta hoy una
// era falsa: `"enterprise": "release"`. No hay ningún comando llamado `enterprise` — `enterprise`
// es un FLAG de `upgrade` (`cmd_upgrade.go:119`). La asignación se hace con
// `commandGroups[c.Name()]` sobre los comandos REALES, así que la entrada nunca casaba: no
// rompía nada, y por eso llevaba ahí sin que nadie la viera.
//
// El daño no es el `GroupID` que no se asigna: es que el mapa se lee como un censo. Al contarlo
// da 38 «comandos» cuando 37 existen, y ese es el tercer denominador distinto que este repo tiene
// para «cuántos comandos tiene el CLI» — junto a los 24 que se registran en la raíz y los 143
// verbos de cobra del árbol completo. Un número público no puede salir de un mapa que puede
// mentir en silencio.
//
// Este test recorre el ÁRBOL REAL, no el fichero: una expresión regular sobre `Use:` cuenta
// también los subcomandos y no distingue un comando registrado de uno declarado y nunca añadido.
//
// LA MUTACIÓN: añadir una clave inventada al mapa. Se enciende.
func TestCadaGrupoDeAyudaNombraUnComandoQueExiste(t *testing.T) {
	t.Parallel()

	root := newRootCmd()
	reales := map[string]bool{}
	for _, c := range root.Commands() {
		reales[c.Name()] = true
	}
	if len(reales) < 5 {
		t.Fatalf("el árbol de comandos dio %d entradas: la sonda no mide y nada queda aprobado", len(reales))
	}

	var huerfanas []string
	for nombre := range commandGroups {
		if !reales[nombre] {
			huerfanas = append(huerfanas, nombre)
		}
	}
	sort.Strings(huerfanas)
	if len(huerfanas) > 0 {
		t.Fatalf("commandGroups nombra %d comando(s) que no existen en la raíz: %v — "+
			"la entrada no asigna ningún grupo y ademas infla el censo de comandos del CLI",
			len(huerfanas), huerfanas)
	}
}
