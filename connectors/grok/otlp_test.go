// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package grok

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// La fixture son los BYTES que emitió `grok 1.0.5 (5115b46bc9) [stable]` el 2026-08-19 en una
// sesión real (`grok -p`), capturados con OTEL_EXPORTER_OTLP_ENDPOINT apuntando a un receptor de
// medida. Sus tres identificadores reales están sustituidos por sintéticos DE LA MISMA LONGITUD —
// protobuf lleva prefijo de longitud, así que cambiarla habría roto la carga—, y no queda ningún
// UUID de la cuenta: este repositorio se exporta.
// Son DOS exports de UNA sesión, y se guardan los dos a propósito: la emisión llega por fases y
// cada fase lleva cosas distintas. El arranque trae `session.spawn`; el turno trae `agent.prompt`,
// `session.handle_prompt` e `input_tokens`. Medirlo sobre uno solo y hablar de «lo que Grok emite»
// es el error que ya cometí al derivar la lista de atributos de los dos ficheros JUNTOS y
// atribuírsela a uno: la prueba falló nombrando exactamente los dos spans que faltaban.
const (
	fixtureArranque = "testdata/otlp-traces-grok-1.0.5-startup.bin"
	fixtureTurno    = "testdata/otlp-traces-grok-1.0.5-turn.bin"
)

var fixtures = []string{fixtureArranque, fixtureTurno}

func realExport(t *testing.T, path string) *coltracepb.ExportTraceServiceRequest {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("sin la fixture no se mide nada: %v", err)
	}
	var e coltracepb.ExportTraceServiceRequest
	if err := proto.Unmarshal(b, &e); err != nil {
		t.Fatalf("los bytes REALES de grok 1.0.5 deben desempaquetar como trazas OTLP: %v", err)
	}
	return &e
}

func TestOTLPReceiverAgainstRealGrokBytes(t *testing.T) {
	r := NewOTLPReceiver("", 0, nil)
	for _, f := range fixtures {
		r.Ingest(realExport(t, f))
	}
	spans, dropped := r.Drain()
	if len(spans) == 0 {
		t.Fatal("36 KB de una sesión real no pueden producir CERO spans")
	}
	if dropped != 0 {
		t.Fatalf("nada debería descartarse con el tope por defecto, se descartaron %d", dropped)
	}

	// Atribución: lo medido, no lo documentado.
	for _, s := range spans {
		if s.Service != "grok-cli" {
			t.Fatalf("service.name medido es %q, no %q — y lo fija el BINARIO: "+
				"OTEL_SERVICE_NAME no lo cambia, comprobado en vivo", "grok-cli", s.Service)
		}
	}
	if spans[0].UserID == "" || spans[0].TeamID == "" {
		t.Fatalf("user.id y team.id viajan en los atributos de RECURSO: %+v", spans[0])
	}

	// session_id es lo que permite CORRELACIONAR esta traza con el hook de la misma sesión, que es
	// literalmente lo que `watchdog.sweep` exige (otelSeen && hookSeen). Sin esta aserción el
	// receptor podía dejarlo vacío y las cuatro pruebas seguían verdes.
	conSesion := 0
	for _, sp := range spans {
		if sp.SessionID != "" {
			conSesion++
		}
	}
	if conSesion == 0 {
		t.Fatal("ningún span trae session_id: la traza no se puede casar con su hook, " +
			"y el watchdog nunca vería la pareja que busca")
	}

	// Los nombres de span que la cadena OTLP→semconv→SDK→watchdog necesita reconocer.
	vistos := map[string]bool{}
	for _, s := range spans {
		vistos[s.Name] = true
	}
	for _, quiero := range []string{"agent.prompt", "session.spawn", "session.handle_prompt"} {
		if !vistos[quiero] {
			t.Errorf("el span %q estaba en la emisión real y el receptor no lo reporta", quiero)
		}
	}
}

// El contrato NEGATIVO que decide la fila «GenAI semconv», con su control positivo al lado: sin él,
// un cero podría ser del método y no del cable.
func TestRealGrokEmissionCarriesNoGenAISemconv(t *testing.T) {
	// el cero se exige en LAS DOS fases…
	for _, f := range fixtures {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%v", err)
		}
		if bytes.Contains(b, []byte("gen_ai")) {
			t.Fatalf("%s: grok 1.0.5 NO emitía convención GenAI; si ahora sí, el mapeo cambia de forma", f)
		}
	}
	// …y el control positivo se exige donde CADA cosa está de verdad, no en el corpus junto.
	arranque, _ := os.ReadFile(fixtureArranque)
	turno, _ := os.ReadFile(fixtureTurno)
	for _, c := range []struct {
		datos []byte
		clave string
	}{
		{arranque, "grok-cli"}, {arranque, "session_id"}, {arranque, "model_id"},
		{turno, "input_tokens"}, {turno, "agent.prompt"}, {turno, "session.handle_prompt"},
	} {
		if !bytes.Contains(c.datos, []byte(c.clave)) {
			t.Fatalf("control positivo caído en %q: sin él, el cero de gen_ai no probaría nada", c.clave)
		}
	}
}

// La postura de privacidad del receptor NO depende de que el proveedor sea amable.
func TestReceiverKeepsNoContent(t *testing.T) {
	r := NewOTLPReceiver("", 0, nil)
	for _, f := range fixtures {
		r.Ingest(realExport(t, f))
	}
	spans, _ := r.Drain()
	for _, s := range spans {
		for _, campo := range []string{s.Name, s.Service, s.Version, s.UserID, s.TeamID, s.SessionID} {
			if len(campo) > 200 {
				t.Fatalf("un campo de %d bytes no es atribución, es contenido: %.60q", len(campo), campo)
			}
		}
	}
	// Y el prompt de aquella sesión no viaja: medido, y fijado aquí para que se note si cambia.
	b, _ := os.ReadFile(fixtureTurno)
	if bytes.Contains(b, []byte("responde solamente")) {
		t.Fatal("el texto del prompt ha empezado a viajar en la traza: el receptor debe redactarlo")
	}
}

func TestReceiverHTTPContract(t *testing.T) {
	r := NewOTLPReceiver("", 0, nil)
	b, _ := os.ReadFile(fixtureTurno)

	// la ruta MEDIDA acepta y contesta el cuerpo OTLP vacío que el exportador espera
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(b)))
	if w.Code != http.StatusOK {
		t.Fatalf("POST /v1/traces con bytes reales debe ser 200, fue %d", w.Code)
	}
	if spans, _ := r.Drain(); len(spans) == 0 {
		t.Fatal("y debe haber ingerido")
	}

	// otra ruta NO, y se dice: un 404 silencioso hace reintentar al agente para siempre
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader(b)))
	if w.Code != http.StatusNotFound {
		t.Fatalf("la ruta de LOGS no es la de este receptor: esperaba 404, fue %d", w.Code)
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/traces", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET no exporta nada: esperaba 405, fue %d", w.Code)
	}

	w = httptest.NewRecorder()
	grande := strings.Repeat("x", maxGrokOTLPBody+1)
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/traces", strings.NewReader(grande)))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("un cuerpo por encima del tope es 413, fue %d", w.Code)
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/traces", strings.NewReader("no es protobuf")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("basura es 400, fue %d", w.Code)
	}
}

// El cableado, extremo a extremo: el conector abre el puerto en Open, el agente exporta, y Gather
// convierte lo recibido en una observación. Sin esto el receptor era código que nadie llamaba.
func TestReceiverWiredThroughOpenGatherClose(t *testing.T) {
	dir := t.TempDir()
	s := New()
	s.now = func() time.Time { return time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC) }
	cfg := map[string]string{
		"config_path":         filepath.Join(dir, "no-existe.toml"),
		"requirements_path":   filepath.Join(dir, "no-existe-req.toml"),
		"disabled_hooks_path": filepath.Join(dir, "no-existe-hooks"),
		"otlp_http":           "true",
		"otlp_http_addr":      "127.0.0.1:0", // puerto efímero: dos pruebas no pueden chocar
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("Open con el receptor encendido: %v", err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	if s.otlpLis == nil {
		t.Fatal("Open dijo que sí y no dejó ningún listener: el puerto se ata en Open, no en Gather")
	}

	// el agente exporta EXACTAMENTE los bytes que emitió grok 1.0.5
	b, err := os.ReadFile(fixtureTurno)
	if err != nil {
		t.Fatalf("%v", err)
	}
	url := "http://" + s.otlpLis.Addr().String() + "/v1/traces"
	resp, err := http.Post(url, "application/x-protobuf", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("el exportador no pudo entregar en %s: %v", url, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("el exportador esperaba 200, recibió %d", resp.StatusCode)
	}

	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var visto string
	for _, f := range sink.findings() {
		if strings.Contains(f.Title, "OTLP") {
			visto = f.Title
		}
	}
	if visto == "" {
		t.Fatal("llegó un export real y Gather no emitió ninguna observación de OTLP")
	}
	if !strings.Contains(visto, "session") || strings.Contains(visto, " 0 span") {
		t.Fatalf("la observación debe contar spans y sesiones de lo recibido, dijo: %q", visto)
	}
}

// ⛔ La distinción que este conector NO puede perder: apagado ≠ vacío. Un hallazgo de «0 spans» con
// el receptor apagado le diría al operador que su agente dejó de reportar cuando lo que pasa es que
// nadie escucha. Son las tres respuestas, en el sitio donde más barato es confundirlas.
func TestSilentWhenTheReceiverIsOff(t *testing.T) {
	dir := t.TempDir()
	s := New()
	s.now = func() time.Time { return time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC) }
	cfg := map[string]string{
		"config_path":         filepath.Join(dir, "no-existe.toml"),
		"requirements_path":   filepath.Join(dir, "no-existe-req.toml"),
		"disabled_hooks_path": filepath.Join(dir, "no-existe-hooks"),
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.otlpLis != nil {
		t.Fatal("el receptor va APAGADO por defecto: atar un puerto es decisión del operador")
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range sink.findings() {
		if strings.Contains(f.Title, "OTLP") {
			t.Fatalf("con el receptor apagado no se habla de telemetría, y dijo: %q", f.Title)
		}
	}
	// …y encendido SÍ habla, aunque no haya llegado nada: eso es «escuchando y vacío», que es
	// una observación distinta y útil. Sin esta mitad, la de arriba la pasaría un conector mudo.
	s2 := New()
	s2.now = s.now
	cfg["otlp_http"], cfg["otlp_http_addr"] = "true", "127.0.0.1:0"
	if err := s2.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("Open encendido: %v", err)
	}
	defer func() { _ = s2.Close(context.Background()) }()
	sink2 := &captureSink{}
	if err := s2.Gather(context.Background(), sink2); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	hallado := false
	for _, f := range sink2.findings() {
		if strings.Contains(f.Title, "LISTENING") {
			hallado = true
		}
	}
	if !hallado {
		t.Fatal("encendido y sin tráfico debe decir que está ESCUCHANDO: si callara, " +
			"la prueba de arriba la pasaría un conector que nunca habla de OTLP")
	}
}

// El camino de MAYOR severidad, que sobrevivió a la primera vuelta de mutación: si el receptor
// llega a su tope y descarta, eso tiene que salir por la observación. Un receptor que pierde en
// silencio convierte un hueco de telemetría en un verde, que es la forma más cara de fallar.
func TestDroppedSpansAreReportedAndLoud(t *testing.T) {
	s := New()
	s.now = func() time.Time { return time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC) }
	s.otlpAddr, s.otlpPath = "127.0.0.1:0", "/v1/traces"
	s.otlp = NewOTLPReceiver("", 2, s.now) // tope diminuto contra una carga real
	s.otlp.Ingest(realExport(t, fixtureTurno))

	f, ok := s.hallazgoOTLP()
	if !ok {
		t.Fatal("con receptor encendido siempre hay observación")
	}
	if !strings.Contains(f.Title, "DROPPED") {
		t.Fatalf("el descarte debe NOMBRARSE, no deducirse de un conteo bajo: %q", f.Title)
	}
	if f.Severity != model.SeverityHigh {
		t.Fatalf("perder telemetría en silencio no es informativo, es alto: %v", f.Severity)
	}
	// y la CIFRA, porque «hubo descartes» sin cuántos no dimensiona nada
	if !strings.ContainsAny(f.Title, "0123456789") {
		t.Fatalf("la observación debe decir CUÁNTOS se descartaron: %q", f.Title)
	}
}
