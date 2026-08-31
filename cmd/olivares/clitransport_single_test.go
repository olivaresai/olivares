// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// transportExemptMarker is how a network path declares, in the source, that it
// is deliberately NOT a CLI-to-control-plane call.
const transportExemptMarker = "cli-transport-exempt:"

// TestOnlyOneCLITransport enforces the rule the brief states as a grep:
// no ad-hoc http.Client in this package except the one cliTransport builds and
// the ones that carry a written reason.
//
// It is a test rather than a line in a document because a grep in a document is
// run by whoever remembers it. Four CLI paths had drifted away from
// cliTransport before this — cmd_agent.go (every session and workspace verb),
// cmd_evals.go, cmd_hookpep.go and, worst, the shell-completion helper in
// cmd_completion.go, which fires on TAB, sent the OLIVARES_TOKEN bearer to real
// routes, and disabled TLS verification from an environment variable that
// appears in no help text. Each silently lost --ca-cert, --pin-sha256, the
// active client context, and the --insecure warning.
//
// The marker requires a REASON, not just an opt-out: a bare marker with nothing
// after it fails, because "someone typed the magic word" is not a justification.
func TestOnlyOneCLITransport(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) < 50 {
		t.Fatalf("globbed only %d files; the scan is not seeing the package", len(files))
	}
	var offenders []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == "clitransport.go" {
			continue
		}
		raw, rerr := os.ReadFile(name) //nolint:gosec // fixed package-local path
		if rerr != nil {
			t.Fatalf("read %s: %v", name, rerr)
		}
		lines := strings.Split(string(raw), "\n")
		for i, line := range lines {
			if !strings.Contains(line, "http.Client{") {
				continue
			}
			if !declaredExempt(lines, i) {
				offenders = append(offenders, name+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
	}
	if len(offenders) > 0 {
		// La regla va en el MENSAJE y no solo en el docstring de `declaredExempt`: quien
		// tropieza lee el fallo, no el ayudante. Antes decia «SIX lines above», que era la
		// ventana vieja; hoy no hay ventana, asi que un comentario largo ya no rompe su
		// propia exencion.
		//
		// ⛔ Y la cita de este comentario era «(:68-69)» — un NUMERO DE LINEA contra un
		// fichero que crece. Un merge movio el docstring a otra linea y la cita quedo
		// apuntando a codigo ajeno, sin que nada enrojeciera. Se cita por NOMBRE.
		t.Fatalf("%d network path(s) build their own http.Client without going through "+
			"cliTransport and without a `// %s <reason>` marker: the marker may sit on the "+
			"client's own line or anywhere in the comment block directly above it, and a "+
			"line of code ends the block:\n  %s",
			len(offenders), transportExemptMarker, strings.Join(offenders, "\n  "))
	}
}

// declaredExempt reports whether the marker, followed by an actual reason, sits on the
// client's own line or anywhere in the comment block directly above it. A line of code
// ends the block.
//
// ⛔ LA REGLA YA NO TIENE NUMERO, Y ESO SALE DE UNA MEDIDA. Antes eran «las seis lineas
// de encima» (`i > idx-7`, o sea idx..idx-6). En las tres exenciones que existian, la
// distancia al cliente ERA el tamano del bloque:
//
//	cmd_upgrade.go:649    distancia=5  bloque=5
//	haleaderlabel.go:198  distancia=4  bloque=4
//	mcpgateway.go:429     distancia=3  bloque=3
//
// ⇒ mismo veredicto sobre el corpus medido (3/3) y ESTRICTAMENTE MAS EXIGENTE fuera de el:
// un marcador separado por una linea de CODIGO ya no cuenta, a ninguna distancia. Es una
// mejora, no un refactor — decirlo asi importa, porque un «es equivalente» a secas se
// citaria manana para no re-medir el primer caso divergente.
//
// Y disuelve el problema que la ventana creaba: `cmd_upgrade.go` estaba a UNA linea de
// romperse por alargar su propio comentario. Ya no hay ventana que agotar.
//
// El gate rapido `lint:cli-transport-exempt` aplica esta MISMA regla; si divergen, es un
// hecho en dos sitios y uno de los dos miente.
func declaredExempt(lines []string, idx int) bool {
	for i := idx; i >= 0; i-- {
		// La propia linea del cliente cuenta (un marcador al final esta pegado al
		// enunciado); de ahi hacia arriba, solo mientras siga siendo bloque de comentario.
		if i != idx {
			trimmed := strings.TrimSpace(lines[i])
			if !strings.HasPrefix(trimmed, "//") && trimmed != "" {
				return false
			}
		}
		pos := strings.Index(lines[i], transportExemptMarker)
		if pos < 0 {
			continue
		}
		reason := strings.TrimSpace(lines[i][pos+len(transportExemptMarker):])
		return len(reason) >= 10
	}
	return false
}
