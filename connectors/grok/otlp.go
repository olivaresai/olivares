// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package grok

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"

	"github.com/olivaresai/olivares/sdk/model"
)

// Receptor OTLP de Grok Build.
//
// ⭐ MEDIDO EN VIVO, no leído de la documentación, contra `grok 1.0.5 (5115b46bc9) [stable]` el
// 2026-08-19. La fila que pedía esto llevaba semanas marcada BLOQUEADA con la razón escrita: «no
// hemos visto emitir a un binario real», y construir contra la página es el error que este conector
// ya cometió TRES veces (los nombres de evento, la imposición de administrador y el interruptor de
// hooks). La fixture de `testdata/` son los BYTES que emitió esa versión.
//
// ⛔ Y LA PRIMERA MEDIDA YA CORRIGE EL DISEÑO OBVIO: Codex emite **logs** OTLP y su receptor
// (`connectors/codex/otlp.go`) desempaqueta `ExportLogsServiceRequest`. **Grok emite TRAZAS**, a
// `POST /v1/traces` con `application/x-protobuf`. Un receptor copiado del de Codex habría
// contestado 400 «malformed OTLP protobuf» a cada export y el hueco se habría leído como «Grok no
// emite». Son dos señales distintas del mismo producto.
//
// Lo medido en el cable, todo verificado sobre la carga real:
//
//	POST /v1/traces          application/x-protobuf
//	service.name             "grok-cli"   ← FIJO EN EL BINARIO: OTEL_SERVICE_NAME NO lo cambia
//	user.id, team.id         UUID, en los atributos de RECURSO
//	session_id               snake_case, coherente con el resto del cable de Grok
//	spans                    agent.prompt · session.spawn · session.handle_prompt ·
//	                         session.prepare_chat_completion · auth.lifecycle ·
//	                         mcp.server_connection · plugin.loaded · record_token_usage
//	tokens                   input_tokens · output_tokens · completion_tokens ·
//	                         cache_read_tokens · reasoning_tokens · token_type · model_id
//
// ⛔ CERO atributos `gen_ai.*`. Grok NO emite en la convención semántica de GenAI, y la fila que
// la pedía tiene ahora su respuesta medida en vez de una suposición: los datos están, con nombres
// propios, y el mapeo es trabajo de traducción — no de esperar a que el proveedor los renombre.
// Verificado con control positivo: `gen_ai` sale 0 mientras `service.name` sale 2 en el mismo
// barrido, así que el cero es del cable y no del método.
//
// ⚠ NO SE GUARDA CONTENIDO, y la medida no cambia esa postura. Hoy el texto del prompt NO viaja
// —buscado literal en la carga: 0, con el control positivo en 4—, pero eso es una propiedad de esta
// versión, no una garantía del formato. Se guarda el NOMBRE del span y la atribución, igual que en
// el receptor de Codex y por la misma razón: un receptor de telemetría que guarda cuerpos es una
// superficie de exfiltración.
const maxGrokOTLPBody = 4 << 20 // 4 MiB — un receptor sin tope es agotamiento de memoria en loopback

// OTLPSpan es un span recibido, reducido a lo que este conector sabe usar.
type OTLPSpan struct {
	Name      string
	Service   string
	Version   string
	UserID    string
	TeamID    string
	SessionID string
	At        time.Time
}

// OTLPReceiver acumula lo recibido hasta que alguien lo drena.
type OTLPReceiver struct {
	mu      sync.Mutex
	spans   []OTLPSpan
	max     int
	dropped int
	now     func() time.Time
	path    string
	lis     net.Listener
	srv     *http.Server
}

// NewOTLPReceiver construye el receptor. `path` vacío usa la ruta MEDIDA (`/v1/traces`).
func NewOTLPReceiver(path string, max int, now func() time.Time) *OTLPReceiver {
	if max <= 0 {
		max = 10000
	}
	if now == nil {
		now = time.Now
	}
	if path == "" {
		path = "/v1/traces"
	}
	return &OTLPReceiver{max: max, now: now, path: path}
}

// ServeHTTP acepta el export de trazas y contesta con el cuerpo VACÍO de OTLP que el exportador
// espera. Un 404 en la ruta equivocada haría que el agente reintente para siempre sin que nadie lo
// note, así que la ruta se declara y no se adivina.
func (r *OTLPReceiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if req.URL.Path != r.path {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxGrokOTLPBody+1))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	if len(body) > maxGrokOTLPBody {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	var export coltracepb.ExportTraceServiceRequest
	if err := proto.Unmarshal(body, &export); err != nil {
		http.Error(w, "malformed OTLP protobuf", http.StatusBadRequest)
		return
	}
	r.Ingest(&export)
	w.WriteHeader(http.StatusOK)
	resp, _ := proto.Marshal(&coltracepb.ExportTraceServiceResponse{})
	_, _ = w.Write(resp)
}

// Ingest reduce el export a spans atribuidos. Exportado para que la prueba pueda alimentarlo con
// los BYTES REALES sin levantar un servidor.
func (r *OTLPReceiver) Ingest(export *coltracepb.ExportTraceServiceRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rs := range export.GetResourceSpans() {
		res := rs.GetResource().GetAttributes()
		base := OTLPSpan{
			Service: attrStr(res, "service.name"),
			Version: attrStr(res, "service.version"),
			UserID:  attrStr(res, "user.id"),
			TeamID:  attrStr(res, "team.id"),
		}
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				s := base
				s.Name = sp.GetName()
				// El id de sesión viaja por span, no por recurso, y en snake_case: se busca
				// donde ESTÁ, y el recurso queda de reserva por si una versión lo sube.
				if v := attrStr(sp.GetAttributes(), "session_id"); v != "" {
					s.SessionID = v
				} else {
					s.SessionID = attrStr(res, "session_id")
				}
				if ns := sp.GetStartTimeUnixNano(); ns > 0 {
					s.At = time.Unix(0, int64(ns)).UTC()
				} else {
					s.At = r.now()
				}
				if len(r.spans) >= r.max {
					r.spans = r.spans[1:]
					r.dropped++
				}
				r.spans = append(r.spans, s)
			}
		}
	}
}

// Drain devuelve lo acumulado y cuántos se descartaron por el tope. El descarte se DEVUELVE en vez
// de callarse: un receptor que pierde en silencio convierte un hueco en un verde.
func (r *OTLPReceiver) Drain() ([]OTLPSpan, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out, drop := r.spans, r.dropped
	r.spans, r.dropped = nil, 0
	return out, drop
}

// Serve ata el receptor a un listener ya abierto. El listener se abre en Open y NO aquí, por la
// misma razón que en Codex y en Claude: un puerto ocupado tiene que fallar donde el SDK espera el
// error, no a mitad de una recogida.
func (r *OTLPReceiver) Serve(lis net.Listener) {
	r.lis = lis
	mux := http.NewServeMux()
	mux.Handle("/", r)
	r.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = r.srv.Serve(lis) }()
}

// Close apaga el receptor. Si nunca llegó a servir, cierra al menos el listener: dejar un puerto
// atado tras un Open fallido es lo que hace que el siguiente arranque falle por la razón equivocada.
func (r *OTLPReceiver) Close(ctx context.Context) error {
	if r.srv == nil {
		if r.lis != nil {
			return r.lis.Close()
		}
		return nil
	}
	return r.srv.Shutdown(ctx)
}

func attrStr(attrs []*commonpb.KeyValue, key string) string {
	for _, kv := range attrs {
		if kv.GetKey() == key {
			return kv.GetValue().GetStringValue()
		}
	}
	return ""
}

// hallazgoOTLP reporta lo que el receptor ha visto desde la última recogida. Es lo que convierte
// `otelSeen` en un hecho medido en vez de una suposición, que es literalmente lo que el watchdog
// necesita (`connectors/claude/watchdog.go`: exige `otelSeen && hookSeen` para poder hablar de un
// hueco).
//
// ⚠ Devuelve `false` cuando el receptor está APAGADO, y eso NO es lo mismo que «cero telemetría».
// Emitir un hallazgo de «0 spans» con el receptor apagado le diría al operador que el agente no
// reporta, cuando lo que pasa es que nadie está escuchando: son las tres respuestas otra vez, y la
// diferencia entre «no he mirado» y «está vacío» es justo la que este producto no puede confundir.
func (s *Source) hallazgoOTLP() (model.FindingReport, bool) {
	if s.otlp == nil {
		return model.FindingReport{}, false
	}
	spans, descartados := s.otlp.Drain()
	at := s.clock().UTC()
	base := model.FindingReport{
		Kind:        findingKindPosture,
		SubjectKind: subjectSandbox,
		SubjectRef:  s.agentRef,
		OccurredAt:  at,
		Severity:    model.SeverityInfo,
	}
	sesiones := map[string]bool{}
	for _, sp := range spans {
		if sp.SessionID != "" {
			sesiones[sp.SessionID] = true
		}
	}
	// El descarte va PRIMERO y con severidad propia: un receptor que pierde en silencio convierte
	// un hueco de telemetría en un verde, que es la forma más cara de fallar que tiene un gate.
	if descartados > 0 {
		base.Severity = model.SeverityHigh
		base.Title = "The Grok Build OTLP receiver DROPPED " + strconv.Itoa(descartados) +
			" span(s) at the retention limit: missing telemetry is not absent telemetry, and such " +
			"a gap looks like an agent that stopped reporting"
		return base, true
	}
	if len(spans) == 0 {
		base.Title = "The Grok Build OTLP receiver is LISTENING on " + s.otlpAddr +
			s.otlpPath + " and has received nothing since the last collection — point the agent's " +
			"OTEL_EXPORTER_OTLP_ENDPOINT here if traces were expected"
		return base, true
	}
	base.Title = "Grok Build reported " + strconv.Itoa(len(spans)) + " span(s) from " +
		strconv.Itoa(len(sesiones)) + " session(s) over OTLP at " + s.otlpAddr + s.otlpPath
	return base, true
}
