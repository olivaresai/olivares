// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// paquete escribe un paquete de un solo test y devuelve su directorio.
func paquete(t *testing.T, hook, test string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hook.go"), []byte(hook), 0o644); err != nil {
		t.Fatalf("no puedo escribir hook.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a_test.go"), []byte(test), 0o644); err != nil {
		t.Fatalf("no puedo escribir a_test.go: %v", err)
	}
	return dir
}

const hookEstandar = "package p\n\nvar ganchoObservado string\nvar contador int\n"

// nombres devuelve los `var` marcados, ordenados, para comparar sin depender del orden de mapa.
func nombres(hs []hallazgo) string {
	var n []string
	for _, h := range hs {
		n = append(n, h.varname)
	}
	sort.Strings(n)
	return strings.Join(n, ",")
}

func TestCasos(t *testing.T) {
	t.Parallel()
	casos := []struct {
		nombre string
		test   string
		quiero string // vars marcadas, ordenadas; "" = limpio
	}{
		{
			// El caso base: el padre se declara paralelo y luego pisa el gancho.
			nombre: "padre paralelo",
			test: "package p\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {\n" +
				"\tt.Parallel()\n\tganchoObservado = \"x\"\n\t_ = ganchoObservado\n}\n",
			quiero: "ganchoObservado",
		},
		{
			// ⛔ REGRESIÓN MEDIDA: el gate de awk al que esto sustituye NO lo veía.
			// Usaba la sangría como proxy del ámbito y guardaba la MÍNIMA a la que había
			// visto un t.Parallel(); aquí la llamada está a sangría 2 y la asignación a 1,
			// así que `sang < sang_par` la dejaba pasar. El test SÍ es paralelo a partir
			// de esa llamada. Verificado el 2026-08-25: viejo rc=0, nuevo rc=1.
			nombre: "t.Parallel mas anidado que la asignacion",
			test: "package p\n\nimport \"testing\"\n\nfunc TestB(t *testing.T) {\n" +
				"\tif testing.Short() {\n\t\tt.Parallel()\n\t}\n" +
				"\tganchoObservado = \"pisado\"\n\t_ = ganchoObservado\n}\n",
			quiero: "ganchoObservado",
		},
		{
			// Ámbito real, no sangría: espacios en vez de tabuladores y dentro de un subtest.
			nombre: "subtest paralelo con espacios",
			test: "package p\n\nimport \"testing\"\n\nfunc TestC(t *testing.T) {\n" +
				"    t.Run(\"s\", func(t *testing.T) {\n        t.Parallel()\n" +
				"        contador = 1\n        _ = contador\n    })\n}\n",
			quiero: "contador",
		},
		{
			// CONTROL INVERSO: la asignación es SERIAL; el subtest paralelo es ajeno.
			nombre: "serial con subtest paralelo ajeno",
			test: "package p\n\nimport \"testing\"\n\nfunc TestD(t *testing.T) {\n" +
				"\tganchoObservado = \"serial\"\n\tt.Run(\"otro\", func(t *testing.T) {\n" +
				"\t\tt.Parallel()\n\t\t_ = ganchoObservado\n\t})\n}\n",
			quiero: "",
		},
		{
			// CONTROL INVERSO: una LOCAL homónima. `:=` es un TOKEN, no una heurística.
			nombre: "local homonima",
			test: "package p\n\nimport \"testing\"\n\nfunc TestE(t *testing.T) {\n" +
				"\tt.Parallel()\n\tganchoObservado := \"local\"\n\t_ = ganchoObservado\n}\n",
			quiero: "",
		},
		{
			// CONTROL INVERSO: sin t.Parallel() en ninguna parte, aunque asigne.
			nombre: "serial puro",
			test: "package p\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) {\n" +
				"\tganchoObservado = \"serial\"\n\t_ = ganchoObservado\n}\n",
			quiero: "",
		},
		{
			// Una goroutine dentro de un test paralelo sigue pisando la misma variable:
			// el literal HEREDA el ámbito del padre.
			nombre: "goroutine dentro de test paralelo",
			test: "package p\n\nimport \"testing\"\n\nfunc TestG(t *testing.T) {\n" +
				"\tt.Parallel()\n\tdone := make(chan struct{})\n" +
				"\tgo func() { contador = 2; close(done) }()\n\t<-done\n}\n",
			quiero: "contador",
		},
		{
			// Compuesto: `+=` también ESCRIBE la variable de paquete.
			nombre: "asignacion compuesta",
			test: "package p\n\nimport \"testing\"\n\nfunc TestH(t *testing.T) {\n" +
				"\tt.Parallel()\n\tcontador += 1\n\t_ = contador\n}\n",
			quiero: "contador",
		},
		{
			// ⛔ AGUJERO QUE LA BATERÍA HEREDADA DESTAPÓ EN ÉSTA. Mi versión original sólo
			// tenía el `:=`; el caso real es declarar la local y REASIGNARLA con `=` a secas,
			// que es sintácticamente idéntico a pisar la global. Sin `localesDe` esto era un
			// FALSO POSITIVO — el error caro, porque un gate que grita sobre trabajo correcto
			// acaba desactivado.
			nombre: "local declarada y REASIGNADA con =",
			test: "package p\n\nimport \"testing\"\n\nfunc TestI(t *testing.T) {\n" +
				"\tt.Parallel()\n\tganchoObservado := \"a\"\n\tganchoObservado = \"b\"\n\t_ = ganchoObservado\n}\n",
			quiero: "",
		},
		{
			// Local declarada con `var` de cuerpo, no con `:=`.
			nombre: "local con var de cuerpo",
			test: "package p\n\nimport \"testing\"\n\nfunc TestJ(t *testing.T) {\n" +
				"\tt.Parallel()\n\tvar contador int\n\tcontador = 3\n\t_ = contador\n}\n",
			quiero: "",
		},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			t.Parallel()
			dir := paquete(t, hookEstandar, c.test)
			hs, n, err := revisarPaquete(dir)
			if err != nil {
				t.Fatalf("revisarPaquete: %v", err)
			}
			if n != 1 {
				t.Fatalf("ficheros de test = %d, quiero 1 (¿el censo ve el sujeto?)", n)
			}
			if got := nombres(hs); got != c.quiero {
				t.Fatalf("marcadas = %q, quiero %q", got, c.quiero)
			}
		})
	}
}

// Un fichero que no parsea NO puede salir limpio: es la tercera respuesta.
func TestFicheroIlegibleEsTerceraRespuesta(t *testing.T) {
	t.Parallel()
	dir := paquete(t, hookEstandar, "package p\n\nfunc TestRoto(t *testing.T) {\n")
	if _, _, err := revisarPaquete(dir); err == nil {
		t.Fatal("un fichero que no parsea salió sin error: eso sería un verde inventado")
	}
}

// Sin este control, «0 hallazgos» no distingue «limpio» de «no miré nada».
func TestElCensoVeElSujeto(t *testing.T) {
	t.Parallel()
	dir := paquete(t, hookEstandar, "package p\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) { _ = 1 }\n")
	dirs, err := censarPaquetes(dir)
	if err != nil {
		t.Fatalf("censarPaquetes: %v", err)
	}
	if len(dirs) != 1 {
		t.Fatalf("paquetes censados = %d, quiero 1", len(dirs))
	}
}
