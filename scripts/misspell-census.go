// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

// misspell-census classifies every misspell finding by WHERE it sits, using go/scanner
// rather than a regexp — a comment, a string in a _test.go file, a string in production
// code, or an identifier. It exists because the hand-written table it replaces claimed to
// partition 817 occurrences and its rows summed to 1022: a partition that does not add up
// to the total it partitions is not a partition, and nobody noticed because both numbers
// were typed rather than derived.
//
// Input: golangci-lint text output on stdin (file:line:col: message (misspell)).
// Output: the census, and a non-zero exit if the rows do not sum to the input count.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var linea = regexp.MustCompile("^(.+?):(\\d+):(\\d+):.*\\(misspell\\)\\s*$")

// La palabra concreta, para poder contestar la OTRA mitad de la afirmacion publicada: no solo
// donde cae cada hallazgo, sino cual es. "no contiene ni una errata" es una afirmacion sobre el
// VOCABULARIO, y un desglose por sitio no la prueba ni la refuta.
var palabra = regexp.MustCompile("`([^`]+)` is a misspelling of `([^`]+)`")

type sitio struct {
	fichero string
	l, c    int
}

// clasificar devuelve dónde cae cada posición, leyendo el fichero UNA vez por fichero.
func clasificar(fichero string, puntos []sitio) map[sitio]string {
	out := make(map[sitio]string, len(puntos))
	src, err := os.ReadFile(fichero)
	if err != nil {
		// ⛔ UN ILEGIBLE CASI NUNCA ES UN FICHERO QUE FALTA: es la CACHE COMPARTIDA de
		//    golangci-lint devolviendote la ruta del PRIMER worktree que analizo ese
		//    contenido. Medido el 2026-08-20 sobre `cmd/olivares`: con la cache por defecto,
		//    5 de 107 hallazgos traian rutas `../otro-worktree/...` que no existen desde
		//    aqui; con `GOLANGCI_LINT_CACHE` fresco, CERO — y esos cinco resultaron ser
		//    COMENTARIOS (la particion pasa de 75 a 80). O sea que el defecto no imprime una
		//    ruta rara: hace que el censo CLASIFIQUE MAL, en silencio, hacia una clase que se
		//    lee como «no pude mirar».
		//
		//    Por eso la clase se separa y se NOMBRA. Un `ILEGIBLE` generico invita a pensar
		//    en un fichero borrado; `RUTA-DE-OTRO-ARBOL` te dice que repitas con cache limpia.
		//    ⚠ Y el detector NO puede mirar si la ruta trae `..`: `filepath.Join` la LIMPIA
		//       antes de llegar aqui, asi que `../otro/x.go` ya es `/workspace/otro/x.go` y el
		//       `..` no existe. Mi primera version comprobaba eso y no disparo ni una vez. Lo
		//       que distingue el caso es que resuelve FUERA de la raiz analizada.
		clase := "ILEGIBLE"
		if raiz, rerr := os.Getwd(); rerr == nil {
			if rel, relerr := filepath.Rel(raiz, fichero); relerr == nil && strings.HasPrefix(rel, "..") {
				clase = "RUTA-DE-OTRO-ARBOL"
			}
		}
		for _, p := range puntos {
			out[p] = clase
		}
		return out
	}
	fset := token.NewFileSet()
	f := fset.AddFile(fichero, fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(f, src, nil, scanner.ScanComments)
	prueba := strings.HasSuffix(fichero, "_test.go")

	type tramo struct {
		ini, fin int
		clase    string
	}
	var tramos []tramo
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		var clase string
		switch tok {
		case token.COMMENT:
			clase = "comentario"
		case token.STRING, token.CHAR:
			if prueba {
				clase = "cadena de test"
			} else {
				clase = "cadena de produccion"
			}
		case token.IDENT:
			clase = "identificador"
		default:
			continue
		}
		ini := f.Offset(pos)
		tramos = append(tramos, tramo{ini, ini + len(lit), clase})
	}
	for _, p := range puntos {
		// ⛔ `LineStart` HACE PANIC si la linea no existe en el fichero, y hasta hoy este censo
		//    moria con un panic de Go —«invalid line number 5 (should be < 4)»— en vez de dar un
		//    veredicto. Reproducido con una sola linea de entrada apuntando fuera de rango, y es
		//    PRE-EXISTENTE: la version de `main` cae igual.
		//
		//    No es teorico: el censo lee la salida del linter y luego ABRE EL FICHERO. Si el
		//    arbol cambia entre las dos cosas —un rebase, un `--write` de otro gate, un fichero
		//    que encoge— las lineas dejan de existir y el censo entero se cae. Un instrumento
		//    que muere no dice «no se»: no dice nada, y quien lo llama se queda sin numero.
		//
		//    Va a una clase DECLARADA, como su hermana ILEGIBLE, para que el desglose siga
		//    particionando: el invariante de que las filas sumen la entrada es lo que impide que
		//    un hallazgo se pierda en silencio.
		// ⛔ `f.LineStart` PANICA con una linea 0 o mayor que el fichero, y hasta aqui se le
		// pasaba lo que viniera. Medido por el contraste Codex `sol max` del 2026-08-20
		// (VERIFICADO): la entrada `scripts/misspell-census.go:0:1: ... (misspell)` produjo
		// `panic: invalid line number 0 (should be >= 1)`.
		//
		// Un panic NO es rehusar: no dice que clase de fallo es y no respeta el contrato de
		// tres respuestas de este repositorio. Se convierte en una clase con nombre que la
		// comprobacion final trata como «no he podido mirar».
		if p.l < 1 || p.l > f.LineCount() || p.c < 1 {
			out[p] = "FUERA-DE-RANGO"
			continue
		}
		off := f.Offset(f.LineStart(p.l)) + p.c - 1
		clase := "otro"
		for _, t := range tramos {
			if off >= t.ini && off < t.fin {
				clase = t.clase
				break
			}
		}
		out[p] = clase
	}
	return out
}

// clase pide el DETALLE de una clase concreta. El censo sabía cuántos y no cuáles, así que la
// campaña no era ejecutable: «95 identificadores» no dice sobre qué fichero abrir el editor. La
// clasificación ya se calcula — lo único que faltaba era no tirar la clave al contarla.
var clase = flag.String("clase", "", "imprime fichero:linea:columna de los hallazgos de esta clase")

// ⛔ Y UN NIVEL POR DEBAJO, EL MISMO DEFECTO QUE ESTE FICHERO VINO A ARREGLAR. Su cabecera
// reprocha «tirar la clave al contarla»; el bucle de abajo hacia exactamente eso con la
// CORRECCION: `palabra` captura el par —`X` is a misspelling of `Y`— y solo se guardaba la X.
//
// No es cosmetico, y lo midio otro carril sobre `cmd/olivares`: de 107 hallazgos de misspell,
// SESENTA son palabras ESPAÑOLAS en comentarios españoles, y su «correccion» no arregla una
// errata sino que dice otra cosa — `comando`→`commando` (un soldado), `producto`→`production`,
// `decisiones`→`decisions`. Sin la Y en la salida, esas sesenta son indistinguibles de las
// britanicas de verdad (`cancelled`, `licence`, `behaviour`), y un barrido mecanico sobre el
// total del arbol no seria «de spec cerrada»: seria editar prosa española a ingles roto.
//
// ⇒ `--formas` imprime el par COMPLETO y SIN TRUNCAR, ordenado por frecuencia. Es la entrada de
// una adjudicacion humana de ~120 filas, no un veredicto mio: yo no decido que es español —lo
// haria con un vocabulario inventado, que es como se fabrican los censos falsos—, emito la
// evidencia para que la lista de ignorados de misspell se alimente DESDE aqui.
var formas = flag.Bool("formas", false, "imprime TODAS las formas como `original → correccion` con su recuento")

func main() {
	flag.Parse()
	porFichero := map[string][]sitio{}
	vocab := map[string]int{}
	correccion := map[string]string{}
	total := 0
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	raiz, _ := os.Getwd()
	// ⛔ EL CONTRATO ERA ESTRECHO: «las filas suman» solo protege lo que LLEGO a contarse, y
	// habia cuatro formas de encoger la entrada ANTES de esa comparacion y salir 0. Las midio el
	// contraste Codex `sol max` del 2026-08-20, todas VERIFICADAS. Un instrumento al que se le
	// puede quitar la entrada y sigue diciendo `TOTAL 0` con exito certifica en vez de fallar,
	// que es exactamente el defecto que este fichero existe para no repetir.
	descartadas := 0
	sinPalabra := 0
	for sc.Scan() {
		bruta := strings.TrimRight(sc.Text(), "\r")
		m := linea.FindStringSubmatch(bruta)
		if m == nil {
			// Una linea que TERMINA en `(misspell)` y no casa con fichero:linea:columna no es
			// ruido: es una fila que el censo deberia contar y no sabe leer. Se ignoraba.
			if strings.HasSuffix(bruta, "(misspell)") {
				descartadas++
			}
			continue
		}
		l, errL := strconv.Atoi(m[2])
		c, errC := strconv.Atoi(m[3])
		if errL != nil || errC != nil {
			// Los errores de Atoi se descartaban, asi que un entero fuera de rango convergia
			// al caso del panic de arriba por otro camino.
			descartadas++
			continue
		}
		fich := m[1]
		if !filepath.IsAbs(fich) {
			fich = filepath.Join(raiz, fich)
		}
		if w := palabra.FindStringSubmatch(bruta); w != nil {
			vocab[w[1]]++
			// La correccion es la mitad que decide si el hallazgo es una errata o un cambio
			// de significado, y se tiraba aqui. Un mismo original siempre trae la misma
			// correccion (misspell es una tabla fija), asi que asignar es suficiente.
			correccion[w[1]] = w[2]
		} else {
			// Ubicacion valida y mensaje que no es `x is a misspelling of y`: contaba en TOTAL
			// y no en vocabulario, asi que «las filas suman» NO protegia las 138 formas que
			// gobiernan la campana — que es para lo que se usa este censo.
			sinPalabra++
		}
		s := sitio{fich, l, c}
		porFichero[fich] = append(porFichero[fich], s)
		total++
	}
	// `sc.Err()` no se consultaba nunca: una linea de mas de 1 MiB rompe el Scanner y el bucle
	// termina como si la entrada se hubiera acabado. Medido: `TOTAL 0`, rc 0.
	if err := sc.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "misspell-census: NO HE PODIDO MIRAR - la entrada no se pudo leer entera: %v\n", err)
		os.Exit(2)
	}
	if descartadas > 0 {
		fmt.Fprintf(os.Stderr, "misspell-census: NO HE PODIDO MIRAR - %d fila(s) de misspell que no supe leer.\n", descartadas)
		os.Exit(2)
	}
	if sinPalabra > 0 {
		fmt.Fprintf(os.Stderr, "misspell-census: NO HE PODIDO MIRAR - %d fila(s) sin la forma corregida; el vocabulario estaria incompleto.\n", sinPalabra)
		os.Exit(2)
	}

	cuenta := map[string]int{}
	var detalle []string
	for fich, puntos := range porFichero {
		for s, c := range clasificar(fich, puntos) {
			cuenta[c]++
			if *clase != "" && c == *clase {
				detalle = append(detalle, fmt.Sprintf("%s:%d:%d", s.fichero, s.l, s.c))
			}
		}
	}
	sort.Strings(detalle)
	claves := make([]string, 0, len(cuenta))
	for k := range cuenta {
		claves = append(claves, k)
	}
	sort.Slice(claves, func(i, j int) bool { return cuenta[claves[i]] > cuenta[claves[j]] })
	suma := 0
	for _, k := range claves {
		fmt.Printf("%-24s %d\n", k, cuenta[k])
		suma += cuenta[k]
	}
	fmt.Printf("%-24s %d\n", "TOTAL", suma)

	tipos := make([]string, 0, len(vocab))
	for k := range vocab {
		tipos = append(tipos, k)
	}
	sort.Slice(tipos, func(i, j int) bool {
		if vocab[tipos[i]] != vocab[tipos[j]] {
			return vocab[tipos[i]] > vocab[tipos[j]]
		}
		return tipos[i] < tipos[j]
	})
	fmt.Printf("\nvocabulario distinto: %d\n", len(tipos))
	if *formas {
		// SIN TRUNCAR y con el par completo: esto es lo que se adjudica. El resumen de abajo
		// corta a 15 y lo dice, que esta bien para un log; una lista de ignorados alimentada
		// con quince de ciento veinte formas seria peor que ninguna.
		for _, t := range tipos {
			fmt.Printf("  %-24s → %-24s %d\n", t, correccion[t], vocab[t])
		}
	} else {
		for i, t := range tipos {
			if i >= 15 {
				fmt.Printf("  ... y %d formas mas (usa --formas para verlas TODAS con su correccion)\n", len(tipos)-15)
				break
			}
			fmt.Printf("  %-22s %d\n", t, vocab[t])
		}
	}
	if *clase != "" {
		fmt.Printf("\ndetalle de la clase %q: %d sitio(s)\n", *clase, len(detalle))
		for _, d := range detalle {
			fmt.Printf("  %s\n", d)
		}
	}
	// ILEGIBLE y FUERA-DE-RANGO son «no he podido mirar», no clases del desglose: SUMAN igual que
	// las buenas, asi que la particion cuadraba y el censo salia 0 habiendo fallado. Un fichero
	// inexistente clasificaba sus filas como ILEGIBLE y salia 0 (VERIFICADO por el contraste).
	for _, mala := range []string{"ILEGIBLE", "FUERA-DE-RANGO"} {
		if cuenta[mala] > 0 {
			fmt.Fprintf(os.Stderr, "misspell-census: NO HE PODIDO MIRAR - %d fila(s) en la clase %s.\n", cuenta[mala], mala)
			os.Exit(2)
		}
	}
	if suma != total {
		fmt.Fprintf(os.Stderr, "misspell-census: las filas suman %d y la entrada trae %d — el desglose NO particiona su total\n", suma, total)
		os.Exit(1)
	}
}
