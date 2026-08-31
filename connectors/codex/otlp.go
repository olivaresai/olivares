// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0
//
// otlp.go — el receptor OTLP/HTTP de Codex (AGT-02).
//
// ⛔ POR QUÉ EXISTE Y POR QUÉ SÓLO HTTP. El censo de AGT-02 dejó el receptor OTLP como «el único
//
//	hueco de peso» del lado Codex, y arrastra dos más: sin él el watchdog no puede armarse
//	(`connectors/claude/watchdog.go:93` exige `otelSeen && hookSeen`) y GenAI semconv no tiene
//	dónde aterrizar.
//
// ⭐ Y NO SE CONSTRUYE CONTRA LA DOCUMENTACIÓN: el contrato del cable está MEDIDO EN VIVO contra
//
//	`codex-cli 0.147.0` (`an internal design note (not shipped)` §2026-08-18), con una sonda
//	que volcaba los bytes sin parsearlos y un control positivo antes de creerse nada:
//
//	  · POST a **`/`**, NO a `/v1/logs`. El endpoint autorizado se usa TAL CUAL, así que un
//	    colector que sólo sirva la ruta canónica de OTLP no recibe nada — y ésa es exactamente la
//	    clase de fallo que se ve como «el agente no emite» en vez de como «no escucho donde habla».
//	  · `Content-Type: application/x-protobuf`.
//	  · `service.name = codex_exec`, `service.version = 0.147.0`.
//	  · Sólo LOGS: la marca `codex_otel.log_only` viaja en el registro.
//
// ⚠ RECIBE Y GUARDA; NO EMITE. Este conector es de PULL —`Gather` tira de APIs— y un receptor es
//
//	PUSH. Mezclar los dos ciclos aquí obligaría a sostener un `sdk.Sink` fuera de su llamada, que
//	es donde nacen las fugas de contexto. Lo recibido se acumula y `Gather` lo drena: el contrato
//	del conector no cambia.
package codex

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"
)

// Límite de cuerpo. Un receptor sin tope es un vector de agotamiento de memoria en un puerto que,
// como el de Claude, va SIN autenticar en loopback.
const maxOTLPBody = 4 << 20 // 4 MiB

// otlpEvent es un registro recibido, reducido a lo que este conector sabe usar hoy.
//
// ⚠ Deliberadamente NO guarda el cuerpo del log. Lo medido incluye `codex.user_prompt`, y el texto
// de un prompt es contenido del cliente: capturarlo por defecto convertiría un receptor de
// telemetría en una superficie de exfiltración. Se guarda el NOMBRE del evento y la atribución.
type otlpEvent struct {
	Name      string
	Service   string
	Version   string
	At        time.Time
	SessionID string
}

// otlpReceiver acumula lo recibido hasta que Gather lo drena.
type otlpReceiver struct {
	mu     sync.Mutex
	events []otlpEvent
	// tope de retención: si nadie drena, se descartan los MÁS VIEJOS y se cuenta cuántos.
	max      int
	dropped  int
	now      func() time.Time
	lis      net.Listener
	srv      *http.Server
	pathOnly string
}

func newOTLPReceiver(path string, max int, now func() time.Time) *otlpReceiver {
	if max <= 0 {
		max = 10000
	}
	if now == nil {
		now = time.Now
	}
	if path == "" {
		path = "/"
	}
	return &otlpReceiver{max: max, now: now, pathOnly: path}
}

// ServeHTTP acepta el export de logs. Contesta 200 con un cuerpo de respuesta VACÍO de OTLP, que es
// lo que el exportador espera; un 404 en la ruta equivocada haría que el agente reintente para
// siempre sin que nadie lo note.
func (r *otlpReceiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// ⛔ La ruta se compara EXACTA contra la configurada. Aceptar cualquiera parece cómodo y borra
	//    la señal de que el exportador cambió de sitio — que es justo el hecho que hubo que medir.
	if req.URL.Path != r.pathOnly {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxOTLPBody+1))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	if len(body) > maxOTLPBody {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	var export collogspb.ExportLogsServiceRequest
	if err := proto.Unmarshal(body, &export); err != nil {
		// ⚠ Un cuerpo ilegible NO es un 200. Aceptarlo en silencio dejaría el receptor «verde» sin
		//    haber recibido nada, que es la forma de fallo que este trabajo entero viene evitando.
		http.Error(w, "malformed OTLP protobuf", http.StatusBadRequest)
		return
	}
	r.ingest(&export)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	resp, _ := proto.Marshal(&collogspb.ExportLogsServiceResponse{})
	_, _ = w.Write(resp)
}

func (r *otlpReceiver) ingest(export *collogspb.ExportLogsServiceRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rl := range export.GetResourceLogs() {
		svc := attrString(rl.GetResource().GetAttributes(), "service.name")
		ver := attrString(rl.GetResource().GetAttributes(), "service.version")
		for _, sl := range rl.GetScopeLogs() {
			for _, rec := range sl.GetLogRecords() {
				name := attrString(rec.GetAttributes(), "event.name")
				if name == "" {
					continue
				}
				at := time.Unix(0, int64(rec.GetTimeUnixNano())).UTC()
				if rec.GetTimeUnixNano() == 0 {
					at = r.now().UTC()
				}
				ev := otlpEvent{
					Name:      name,
					Service:   svc,
					Version:   ver,
					At:        at,
					SessionID: attrString(rec.GetAttributes(), "session.id"),
				}
				if len(r.events) >= r.max {
					// Se descarta el más VIEJO y se cuenta: un receptor que se queda mudo al
					// llenarse miente por omisión.
					r.events = r.events[1:]
					r.dropped++
				}
				r.events = append(r.events, ev)
			}
		}
	}
}

// drain devuelve lo acumulado y cuántos se descartaron, y deja el buffer vacío.
func (r *otlpReceiver) drain() ([]otlpEvent, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	evs, dropped := r.events, r.dropped
	r.events, r.dropped = nil, 0
	return evs, dropped
}

// serve arranca el servidor sobre el listener ya admitido en Open.
func (r *otlpReceiver) serve(lis net.Listener) {
	r.lis = lis
	mux := http.NewServeMux()
	mux.Handle("/", r)
	r.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = r.srv.Serve(lis) }()
}

func (r *otlpReceiver) close(ctx context.Context) error {
	if r.srv == nil {
		if r.lis != nil {
			return r.lis.Close()
		}
		return nil
	}
	return r.srv.Shutdown(ctx)
}

func attrString(attrs []*commonpb.KeyValue, key string) string {
	for _, kv := range attrs {
		if kv.GetKey() != key {
			continue
		}
		if s := kv.GetValue().GetStringValue(); s != "" {
			return s
		}
		return strings.TrimSpace(fmt.Sprint(kv.GetValue().GetValue()))
	}
	return ""
}
