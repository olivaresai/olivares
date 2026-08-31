// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// misspell-rename-idents reescribe SÓLO tokens IDENT de ficheros Go.
//
// ⛔ POR QUÉ NO ES UN `sed`. La misma palabra vive en las cuatro clases que el censo separa
// (comentario, cadena de test, cadena de producción, identificador) y **sólo una de ellas es
// segura de reescribir a ciegas**: una cadena puede ser un valor de protocolo —un `status:
// "cancelled"` que el motor emite— y cambiarla rompe el contrato o, peor, deja un test afirmando
// lo que no es. Este programa usa el mismo `go/scanner` que `misspell-census.go`, así que su
// noción de «identificador» es exactamente la del censo que lo mide.
//
// Uso: go run scripts/misspell-rename-idents.go -de cancelled -a canceled <fichero.go>...
//
//	El reemplazo es de SUBCADENA dentro del identificador, para que `cancelledAt` y
//	`numCancelled` viajen con `cancelled` y no queden a medias.
package main

import (
	"flag"
	"fmt"
	"go/scanner"
	"go/token"
	"os"
	"strings"
)

func main() {
	de := flag.String("de", "", "subcadena mal escrita")
	a := flag.String("a", "", "subcadena correcta")
	flag.Parse()
	if *de == "" || *a == "" || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "uso: -de <mal> -a <bien> <fichero.go>...")
		os.Exit(2)
	}
	// Con la primera en mayúscula también, para identificadores camelCase (numCancelled).
	deCap := strings.ToUpper((*de)[:1]) + (*de)[1:]
	aCap := strings.ToUpper((*a)[:1]) + (*a)[1:]

	total := 0
	for _, ruta := range flag.Args() {
		src, err := os.ReadFile(ruta)
		if err != nil {
			fmt.Fprintf(os.Stderr, "misspell-rename-idents: %v\n", err)
			os.Exit(2)
		}
		fs := token.NewFileSet()
		f := fs.AddFile(ruta, fs.Base(), len(src))
		var s scanner.Scanner
		s.Init(f, src, nil, 0)
		type rango struct{ ini, fin int }
		var cambios []rango
		for {
			pos, tok, lit := s.Scan()
			if tok == token.EOF {
				break
			}
			if tok != token.IDENT {
				continue
			}
			if !strings.Contains(lit, *de) && !strings.Contains(lit, deCap) {
				continue
			}
			ini := f.Offset(pos)
			cambios = append(cambios, rango{ini, ini + len(lit)})
		}
		if len(cambios) == 0 {
			continue
		}
		// De atrás hacia delante: los desplazamientos de los anteriores siguen valiendo.
		out := make([]byte, len(src))
		copy(out, src)
		for i := len(cambios) - 1; i >= 0; i-- {
			c := cambios[i]
			viejo := string(out[c.ini:c.fin])
			nuevo := strings.ReplaceAll(viejo, *de, *a)
			nuevo = strings.ReplaceAll(nuevo, deCap, aCap)
			out = append(out[:c.ini], append([]byte(nuevo), out[c.fin:]...)...)
			total++
		}
		if err := os.WriteFile(ruta, out, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "misspell-rename-idents: %v\n", err)
			os.Exit(2)
		}
		fmt.Printf("%-64s %d identificador(es)\n", ruta, len(cambios))
	}
	fmt.Printf("total: %d identificador(es) reescritos\n", total)
}
