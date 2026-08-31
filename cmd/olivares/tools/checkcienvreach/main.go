// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// checkcienvreach — ¿la variable que enciende una suite llega AL JOB QUE LA EJECUTA?
//
// ⛔ POR QUÉ EXISTE, y no es teórico. Medido el 2026-08-18 sobre `mainline-ci.yml`:
//
//	race-rest      EJECUTA `task test:cloud`  ·  no fija NINGUNA CLOUD_CP_*
//	control-plane  fija CLOUD_CP_INTEGRATION y CLOUD_CP_REQUIRE_INTEGRATION  ·  no ejecuta esos tests
//
// `GITHUB_ENV` es POR JOB y no cruza. Así que el cableado nunca llegó a los tests para los que se
// escribió, y el backlog declaraba «los 21 TestIntegration* ya no se saltan» siendo falso durante
// días. Nadie mintió: se verificó que la variable ESTABA y se dedujo que los tests CORRÍAN. Son
// dos hechos distintos.
//
// ⛔ Y NO ENUMERA NADA: las variables salen de `os.Getenv("CLOUD_CP_…")` en las fuentes del módulo,
// y el job se identifica por ejecutar la tarea que corre ese módulo. Una lista escrita a mano aquí
// envejecería igual que la afirmación que este gate existe para impedir.
//
// ⭐ POR QUÉ ESTÁ EN GO Y NO EN PYTHON, que es el cambio del 2026-08-19. La versión anterior hacía
// `import yaml` (PyYAML). **Ningún contenedor de este proyecto tiene PyYAML, y ninguno tiene `pip`
// ni `ensurepip`**, así que el gate contestaba `2 · NO HE PODIDO MIRAR` en todos ellos y **rechazaba
// TODOS los push de TODOS los carriles** — incluido uno de un solo fichero markdown. El veredicto 2
// era CORRECTO (es lo que hay que contestar cuando no se puede mirar); el problema era que no se
// podía mirar en ninguna parte. Go sí está en todas, y la dependencia YAML ya estaba pagada en
// `cmd/olivares` (`gopkg.in/yaml.v3`), que es justo lo que hace `checkciports`. El camino feliz
// pasa de ser comprobable por nadie a serlo por cualquiera.
//
// Contrato: 0 verde · 1 hallazgo · 2 NO HE PODIDO MIRAR.
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	rcOK      = 0
	rcFinding = 1
	rcBlind   = 2
)

// Las que ENCIENDEN la suite, frente a las que exigen INFRAESTRUCTURA. Sin esta distinción el gate
// obliga a montar pgbouncer y nueve pools para poder estar verde, que es pedir obra por simetría.
var enciendenLaSuite = map[string]bool{
	"CLOUD_CP_INTEGRATION":         true,
	"CLOUD_CP_REQUIRE_INTEGRATION": true,
}

var getenvRe = regexp.MustCompile(`os\.Getenv\("(CLOUD_CP_[A-Z_]+)"\)`)

func blind(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "check-ci-env-reach: NO HE PODIDO MIRAR: "+format+"\n", a...)
	os.Exit(rcBlind)
}

func main() {
	root := os.Getenv("CI_ENV_REACH_ROOT")
	if root == "" {
		root = "."
	}
	wf := filepath.Join(root, ".github", "workflows", "mainline-ci.yml")
	raw, err := os.ReadFile(wf)
	if err != nil {
		blind("falta %s", wf)
	}

	// El documento se conserva como Node para recorrer los jobs EN ORDEN DEL FICHERO: el orden del
	// informe es parte de lo que un lector compara entre corridas.
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		blind("%s no parsea (%v)", wf, err)
	}

	vs := derivarVariables(root)
	if len(vs) < 2 {
		fmt.Fprintf(os.Stderr, "check-ci-env-reach: NO HE PODIDO MIRAR: sólo %d variable(s) derivada(s) de las fuentes.\n", len(vs))
		fmt.Fprintln(os.Stderr, "                    Un conjunto vacío haría que cualquier workflow pareciera correcto.")
		os.Exit(rcBlind)
	}

	nombres, textos := jobs(&doc)
	if len(nombres) < 3 {
		blind("%d job(s) en el workflow.", len(nombres))
	}

	var ejecutan, fijan []string
	for _, n := range nombres {
		t := textos[n]
		if strings.Contains(t, "test:cloud") {
			ejecutan = append(ejecutan, n)
		}
		for _, v := range vs {
			if strings.Contains(t, v) {
				fijan = append(fijan, n)
				break
			}
		}
	}

	fmt.Printf("check-ci-env-reach: %d variable(s) encienden la suite del cloud; la ejecutan %s; la encienden %s\n",
		len(vs), lista(ejecutan), lista(fijan))

	if len(ejecutan) == 0 {
		blind("ningún job ejecuta `test:cloud`.")
	}

	// LA PROPIEDAD QUE SE EXIGE, y ni una más: que el cableado ALCANCE a alguien que ejecuta.
	//
	// No se exige que TODO job que corra la suite tenga las variables: el job de `-race` la corre
	// para sus tests unitarios y no tiene por qué arrastrar Postgres, roles y diez DSN. Lo que NO
	// puede pasar es que NINGÚN job las tenga y ejecute a la vez — ahí la suite se salta entera y
	// se la da por ejecutada.
	var necesarias, infra []string
	for _, v := range vs {
		if enciendenLaSuite[v] {
			necesarias = append(necesarias, v)
		} else {
			infra = append(infra, v)
		}
	}
	sort.Strings(necesarias)
	sort.Strings(infra)

	var completos []string
	for _, n := range ejecutan {
		if contieneTodas(textos[n], necesarias) {
			completos = append(completos, n)
		}
	}

	if len(completos) == 0 {
		fmt.Fprintln(os.Stderr, "check-ci-env-reach: ⛔ NINGUN job ejecuta la suite CON las variables que la encienden:")
		for _, n := range ejecutan {
			var faltan []string
			for _, v := range necesarias {
				if !strings.Contains(textos[n], v) {
					faltan = append(faltan, v)
				}
			}
			fmt.Fprintf(os.Stderr, "    ejecuta %s, le faltan %s\n", n, strings.Join(faltan, ", "))
		}
		for _, n := range fijan {
			if !contiene(ejecutan, n) {
				fmt.Fprintf(os.Stderr, "    las fija %s, que NO ejecuta la suite\n", n)
			}
		}
		fmt.Fprintln(os.Stderr, "    GITHUB_ENV es POR JOB: fijarlas en otro job NO las hace llegar, y la suite se salta")
		fmt.Fprintln(os.Stderr, "    en silencio. Fijar una variable y ejercitar lo que la lee son dos hechos distintos.")
		os.Exit(rcFinding)
	}

	fmt.Printf("check-ci-env-reach: OK — %s ejecuta(n) la suite ENCENDIDA\n", strings.Join(completos, ", "))

	var faltanInfra []string
	for _, v := range infra {
		visto := false
		for _, n := range completos {
			if strings.Contains(textos[n], v) {
				visto = true
				break
			}
		}
		if !visto {
			faltanInfra = append(faltanInfra, v)
		}
	}
	if len(faltanInfra) > 0 {
		fmt.Println("check-ci-env-reach: y estas SIGUEN sin fijarse, asi que su leg se salta (dicho, no aprobado):")
		for _, v := range faltanInfra {
			fmt.Printf("    %s\n", v)
		}
	}
}

// derivarVariables lee las fuentes del módulo, NO una lista. Las de censo/hijo (`_CHILD`) son
// fontanería interna del propio gate y no encienden una suite.
func derivarVariables(root string) []string {
	set := map[string]bool{}
	base := filepath.Join(root, "cloud", "control-plane")
	_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
			return nil //nolint:nilerr // un subárbol ilegible no es un hallazgo; el suelo de población lo cazará
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, m := range getenvRe.FindAllStringSubmatch(string(b), -1) {
			if !strings.HasSuffix(m[1], "_CHILD") {
				set[m[1]] = true
			}
		}
		return nil
	})
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// jobs devuelve los nombres EN ORDEN DEL FICHERO y el texto YAML de cada subárbol. El texto es el
// equivalente del `safe_dump` de la versión python: se busca por subcadena sobre claves Y valores,
// que es como una variable declarada en `env:` cuenta igual que una citada en un `run:`.
func jobs(doc *yaml.Node) ([]string, map[string]string) {
	textos := map[string]string{}
	var nombres []string
	if len(doc.Content) == 0 {
		return nombres, textos
	}
	top := doc.Content[0]
	if top.Kind != yaml.MappingNode {
		return nombres, textos
	}
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value != "jobs" {
			continue
		}
		m := top.Content[i+1]
		if m.Kind != yaml.MappingNode {
			return nombres, textos
		}
		for j := 0; j+1 < len(m.Content); j += 2 {
			name := m.Content[j].Value
			b, err := yaml.Marshal(m.Content[j+1])
			if err != nil {
				continue
			}
			nombres = append(nombres, name)
			textos[name] = string(b)
		}
	}
	return nombres, textos
}

func contieneTodas(t string, vs []string) bool {
	for _, v := range vs {
		if !strings.Contains(t, v) {
			return false
		}
	}
	return true
}

func contiene(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func lista(xs []string) string {
	if len(xs) == 0 {
		return "<nadie>"
	}
	return "[" + strings.Join(xs, " ") + "]"
}
