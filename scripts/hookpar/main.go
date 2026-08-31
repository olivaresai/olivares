// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// hookpar — un test que asigna a una VARIABLE DE PAQUETE dentro de un ámbito paralelo
// no puede confiar en su propia aserción: otro test del mismo paquete puede haber escrito
// esa variable, y un número ajeno se parece exactamente a uno propio.
//
// Sustituye a scripts/check-test-hook-parallelism.sh, que hacía lo mismo con awk. El shell
// no estaba mal escrito: estaba en la capa equivocada, y se demostró midiendo. Tres arreglos
// correctos seguidos —comentario de línea, bloque /* */, cadena raw multilínea— cerraron tres
// casos y ninguno cerró la CLASE, porque los tres eran del mismo tipo: reconstruir el léxico
// de Go a mano. Aquí no se reconstruye nada.
//
// Las dos aproximaciones que desaparecen, y son las que importan:
//
//	ÁMBITO PARALELO. El shell usaba la SANGRÍA como proxy: guardaba la indentación mínima a
//	la que había visto un t.Parallel() y marcaba las asignaciones a sangría >=. Funciona con
//	tabuladores y una llave por línea, y falla en cuanto alguien escribe de otra forma.
//	Aquí el ámbito es el anidamiento léxico real del AST.
//
//	VARIABLE DE PAQUETE. El shell recogía las declaradas con `:=` en la función y restaba.
//	Eso confunde una local declarada en un bloque interior, un parámetro y un named return.
//	Aquí la pertenencia sale de las declaraciones reales del paquete, y `=` frente a `:=` es
//	un token, no una expresión regular.
//
// CENSUS-SUBJECT: internal — su sujeto son los ficheros `.go` del árbol. Un árbol sin Go pasa,
// y es correcto; lo que NO puede hacer es pasar sin haber podido leerlos: eso es la tercera
// respuesta (NO HE PODIDO MIRAR, salida 2), nunca un verde.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type hallazgo struct {
	fichero string
	linea   int
	varname string
	test    string
	motivo  string
}

func main() {
	raiz := flag.String("raiz", ".", "raíz del árbol a censar")
	flag.Parse()

	paquetes, err := censarPaquetes(*raiz)
	if err != nil {
		// Tercera respuesta: no he podido mirar. NUNCA un verde.
		fmt.Fprintf(os.Stderr, "hookpar: ⛔ NO HE PODIDO MIRAR: %v\n", err)
		os.Exit(2)
	}
	if len(paquetes) == 0 {
		fmt.Println("hookpar: limpio — el árbol no tiene ficheros Go de test.")
		return
	}

	var todos []hallazgo
	nFich := 0
	for _, dir := range paquetes {
		hs, n, err := revisarPaquete(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "hookpar: ⛔ NO HE PODIDO MIRAR %s: %v\n", dir, err)
			os.Exit(2)
		}
		nFich += n
		todos = append(todos, hs...)
	}

	sort.Slice(todos, func(i, j int) bool {
		if todos[i].fichero != todos[j].fichero {
			return todos[i].fichero < todos[j].fichero
		}
		return todos[i].linea < todos[j].linea
	})

	if len(todos) == 0 {
		fmt.Printf("hookpar: CLEAN — %d paquete(s), %d fichero(s) de test, 0 hallazgos.\n",
			len(paquetes), nFich)
		return
	}
	for _, h := range todos {
		fmt.Fprintf(os.Stderr, "hookpar: ⛔ %s:%d — %s asigna la variable de paquete `%s` %s\n",
			h.fichero, h.linea, h.test, h.varname, h.motivo)
	}
	fmt.Fprintf(os.Stderr, "hookpar: %d hallazgo(s) en %d paquete(s), %d fichero(s) de test.\n",
		len(todos), len(paquetes), nFich)
	os.Exit(1)
}

// censarPaquetes devuelve los directorios que contienen algún *_test.go.
func censarPaquetes(raiz string) ([]string, error) {
	vistos := map[string]bool{}
	err := filepath.WalkDir(raiz, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "testdata", "dist":
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), "_test.go") {
			vistos[filepath.Dir(p)] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	dirs := make([]string, 0, len(vistos))
	for d := range vistos {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	return dirs, nil
}

// revisarPaquete parsea TODOS los .go del directorio para saber qué nombres son variables de
// paquete —eso no se puede deducir de un fichero suelto— y luego recorre sólo los _test.go.
func revisarPaquete(dir string) ([]hallazgo, int, error) {
	fset := token.NewFileSet()
	entradas, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, err
	}
	var ficheros []string
	for _, e := range entradas {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		ficheros = append(ficheros, filepath.Join(dir, e.Name()))
	}

	varsPaquete := map[string]bool{}
	archivos := map[string]*ast.File{}
	for _, f := range ficheros {
		a, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			// Un fichero que no parsea es «no he podido mirar» para TODO el paquete: sin él
			// no sé qué nombres son de paquete, así que un verde aquí sería inventado.
			return nil, 0, fmt.Errorf("%s no parsea: %w", f, err)
		}
		archivos[f] = a
		for _, d := range a.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, s := range gd.Specs {
				vs, ok := s.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, n := range vs.Names {
					if n.Name != "_" {
						varsPaquete[n.Name] = true
					}
				}
			}
		}
	}

	var hs []hallazgo
	nTest := 0
	for f, a := range archivos {
		if !strings.HasSuffix(f, "_test.go") {
			continue
		}
		nTest++
		hs = append(hs, revisarFichero(fset, f, a, varsPaquete)...)
	}
	return hs, nTest, nil
}

// esParallel reconoce `x.Parallel()` — el receptor se comprueba por nombre porque en un test
// SIEMPRE es el *testing.T del ámbito, y comprobarlo con go/types costaría arrastrar el
// grafo de dependencias entero para no cambiar ni un veredicto.
func esParallel(n ast.Node) bool {
	c, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Parallel" || len(c.Args) != 0 {
		return false
	}
	_, ok = sel.X.(*ast.Ident)
	return ok
}

// cuerpoLlamaParallel dice si ESTE cuerpo llama a t.Parallel() en su nivel propio, sin entrar
// en literales de función anidados: el t.Parallel() de un subtest marca al SUBTEST, no al padre.
func cuerpoLlamaParallel(b *ast.BlockStmt) bool {
	if b == nil {
		return false
	}
	visto := false
	var camina func(ast.Node) bool
	camina = func(n ast.Node) bool {
		if visto {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false // no desciendo: otro ámbito
		}
		if esParallel(n) {
			visto = true
			return false
		}
		return true
	}
	ast.Inspect(b, camina)
	return visto
}

// localesDe recoge todo nombre declarado DENTRO de la función: `:=`, `var` de cuerpo, parámetros,
// retornos con nombre, variables de `range` y ligaduras de `type switch`.
//
// ⛔ Por qué hace falta y no basta con mirar el token de CADA asignación: una local declarada con
// `:=` se puede reasignar después con `=` a secas, y esa `=` es sintácticamente idéntica a la que
// pisa la global. Este agujero lo destapó la batería heredada del gate anterior
// (`TestParallelShadowsTheName`), que cubría el caso y mi primera batería NO — la mía sólo tenía
// el `:=`, sin la reasignación posterior. Una batería propia más floja que la que sustituye es
// exactamente el defecto que un reemplazo debe descartar antes de sustituir nada.
//
// Es deliberadamente PLANA (no modela bloques): si un nombre se declara en cualquier punto de la
// función, se excluye en toda ella. Eso puede perder un caso patológico —declarar una local `x`
// en un bloque interior y pisar la global `x` fuera de él— y NUNCA produce un falso positivo,
// que es el error caro: un gate que grita sobre trabajo correcto se desactiva.
func localesDe(fd *ast.FuncDecl) map[string]bool {
	loc := map[string]bool{}
	anota := func(e ast.Expr) {
		if id, ok := e.(*ast.Ident); ok && id.Name != "_" {
			loc[id.Name] = true
		}
	}
	if fd.Type.Params != nil {
		for _, f := range fd.Type.Params.List {
			for _, n := range f.Names {
				anota(n)
			}
		}
	}
	if fd.Type.Results != nil {
		for _, f := range fd.Type.Results.List {
			for _, n := range f.Names {
				anota(n)
			}
		}
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.AssignStmt:
			if v.Tok == token.DEFINE {
				for _, l := range v.Lhs {
					anota(l)
				}
			}
		case *ast.GenDecl:
			if v.Tok == token.VAR || v.Tok == token.CONST {
				for _, sp := range v.Specs {
					if vs, ok := sp.(*ast.ValueSpec); ok {
						for _, nm := range vs.Names {
							anota(nm)
						}
					}
				}
			}
		case *ast.RangeStmt:
			anota(v.Key)
			anota(v.Value)
		case *ast.TypeSwitchStmt:
			if a, ok := v.Assign.(*ast.AssignStmt); ok {
				for _, l := range a.Lhs {
					anota(l)
				}
			}
		case *ast.FuncLit:
			if v.Type.Params != nil {
				for _, f := range v.Type.Params.List {
					for _, nm := range f.Names {
						anota(nm)
					}
				}
			}
		}
		return true
	})
	return loc
}

// revisarFichero recorre los `func TestXxx` y marca las asignaciones a variables de paquete
// que caen DENTRO de un ámbito paralelo.
//
// Los tres casos que el gate anterior documentaba, y que aquí salen del anidamiento y no de
// la sangría:
//
//	padre paralelo + asignación en cualquier punto posterior ....... HALLAZGO
//	asignación DENTRO de un subtest que llama a t.Parallel() ....... HALLAZGO
//	asignación serial + subtest paralelo AJENO ..................... limpio
func revisarFichero(fset *token.FileSet, ruta string, a *ast.File, varsPaquete map[string]bool) []hallazgo {
	var hs []hallazgo
	for _, d := range a.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil || fd.Recv != nil {
			continue
		}
		if !strings.HasPrefix(fd.Name.Name, "Test") {
			continue
		}
		padreParalelo := cuerpoLlamaParallel(fd.Body)
		locales := localesDe(fd)
		hs = append(hs, recorrer(fset, ruta, fd.Name.Name, fd.Body, varsPaquete, locales, padreParalelo)...)
	}
	return hs
}

// recorrer camina un cuerpo sabiendo si YA está en un ámbito paralelo, y desciende a los
// literales de función marcando cada uno según llame o no a Parallel en su propio nivel.
func recorrer(fset *token.FileSet, ruta, test string, b *ast.BlockStmt, varsPaquete, locales map[string]bool, paralelo bool) []hallazgo {
	var hs []hallazgo
	var camina func(n ast.Node) bool
	camina = func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.FuncLit:
			// Un literal hereda el ámbito del padre y además puede hacerse paralelo él mismo.
			// Ojo: heredar es correcto — una goroutine dentro de un test paralelo sigue
			// escribiendo la misma variable de paquete.
			hs = append(hs, recorrer(fset, ruta, test, v.Body, varsPaquete, locales, paralelo || cuerpoLlamaParallel(v.Body))...)
			return false
		case *ast.AssignStmt:
			if !paralelo {
				return true
			}
			// `:=` declara una LOCAL: nunca es la variable de paquete, por token y no por
			// heurística. Sólo `=` (y los compuestos: `+=` también escribe) pueden pisarla.
			if v.Tok == token.DEFINE {
				return true
			}
			for _, l := range v.Lhs {
				id, ok := l.(*ast.Ident)
				if !ok || !varsPaquete[id.Name] || locales[id.Name] {
					continue
				}
				hs = append(hs, hallazgo{
					fichero: ruta,
					linea:   fset.Position(id.Pos()).Line,
					varname: id.Name,
					test:    test,
					motivo:  "dentro de un ámbito paralelo: otro test del paquete puede pisarla y la aserción juzgaría un dato ajeno",
				})
			}
		}
		return true
	}
	ast.Inspect(b, camina)
	return hs
}
