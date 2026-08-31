// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package grok es el conector de Olivares AI para **Grok Build**, el agente de codificación en
// terminal de xAI (`github.com/xai-org/grok-build`), leído a través de su configuración LOCAL.
// Es la mitad de GOBIERNO de la paridad de superficies; la de OBSERVACIÓN va por el ingest OTLP
// que ya existe, porque Grok Build emite OTLP por gRPC y por HTTP (verificado en su Cargo.toml).
//
// ⛔ NO ES EL CONECTOR DE LA API DE xAI. `connectors/xai` lee la API de modelos
// (catálogo y coste, con `grok-build-0.1` entre ellos como MODELO). ÉSTE lee la configuración
// del AGENTE. No se solapan, y confundirlos sería contar una integración que no existe.
//
// PROCEDENCIA. Todo lo que este paquete afirma sale de `docs.x.ai/build/*` leído el 2026-08-18,
// con el repositorio anclado en `xai-org/grok-build @ d71f6e0c1f5acc5469e503e192fe14824e6f8c90`
// (2026-08-17T18:48:25Z) — el único ancla inmutable que hay: **el feed de releases está vacío**
// y el `Cargo.toml` raíz es un manifiesto de workspace sin versión propia.
//
// ⛔⛔ EL HALLAZGO QUE DA FORMA A ESTE CONECTOR, y que decide lo que la consola puede prometer:
//
//	El perfil de sandbox se elige por TRES vías —`grok --sandbox <perfil>`, `[sandbox] profile`
//	en `~/.grok/config.toml`, y la variable `GROK_SANDBOX`— y **que un administrador pueda
//	FORZAR uno no está documentado**.
//
//	Tres vías sin imposición documentada significa que el perfil es una **preferencia de
//	usuario, no un control**: cualquiera de las otras dos puede rebajar lo que el fichero fije,
//	y el propio usuario tiene las tres a mano. Por eso este conector emite el perfil como
//	OBSERVADO y nunca como impuesto, y por eso el hallazgo estructural se emite SIEMPRE — hasta
//	con el perfil más estricto puesto, porque lo que se está diciendo no es «este perfil es
//	débil», es «este perfil no está garantizado».
//
//	Presentarlo como enforcement sería la clase de promesa que el producto no puede cumplir, y
//	el cliente lo descubriría el día que le importa.
//
// La superficie que SÍ es enforcement es otra: Grok Build lee el `managed-settings.json` de
// Claude Code (reglas de permisos, allowlists de MCP y unos flags de telemetría), que es
// exactamente lo que `connectors/managedsettings` ya genera. Este conector informa de su
// presencia; no lo re-implementa.
//
// Sólo importa el SDK, nunca el motor.
package grok

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/olivaresai/olivares/sdk"

	"github.com/olivaresai/olivares/sdk/netbind"
)

// Name es el identificador globalmente único del conector.
const Name = "olivares.grok"

const (
	// MEDIDO, no elegido: `grok 1.0.5 (5115b46bc9)` exporta a `POST /v1/traces` en
	// `application/x-protobuf` y honra OTEL_EXPORTER_OTLP_ENDPOINT. La ruta de Codex es la de
	// LOGS y no vale aquí — está escrito con su consecuencia en otlp.go.
	defaultOTLPAddr = "127.0.0.1:4318"
	defaultOTLPPath = "/v1/traces"
)

const (
	defaultAgentRef   = "grok-build"
	defaultConfigPath = "~/.grok/config.toml"
	// ⛔ LA CAPA QUE MANDA, y es la que este conector no miraba. Verificada el 2026-08-19 en el
	//    fuente público, commit ya anclado: `paths.rs:25-31` devuelve `/etc/grok` en unix, y
	//    `validation.rs:76-86` lee ahí `requirements.toml` marcándolo `is_system: true`. En
	//    `config_layers.rs` los requisitos son la capa MÁS ALTA —por encima del overlay
	//    `GROK_CONFIG`, del usuario, del `managed` y del `system_managed`— y su comentario dice
	//    que requisitos y MDM **acotan** («clamp»), no sólo fusionan.
	defaultRequirementsPath = "/etc/grok/requirements.toml"
	// Ver `hooktrust.go` para por qué este fichero es un hallazgo y no un detalle.
	defaultDisabledPath = defaultDisabledHooksPath
	subjectConfig       = "grok.config"
	subjectSandbox      = "grok.sandbox"
	subjectManaged      = "grok.managed-settings"
	subjectMCP          = "grok.mcp"
	maxConfigBytes      = 1 << 20 // 1 MiB: una config real es diminuta
	descriptorVersion   = "0.1.0"
	findingKindPosture  = "posture"
)

// Source es el conector de gobierno de Grok Build.
type Source struct {
	agentRef          string
	configPath        string
	managedPath       string
	requirementsPath  string
	disabledHooksPath string
	now               func() time.Time

	// Receptor OTLP. Apagado por defecto: atar un puerto es una decisión del operador, no del
	// conector. Ver otlp.go para lo MEDIDO contra `grok 1.0.5` — ruta, formato y atributos.
	otlpEnabled     bool
	otlpAddr        string
	otlpPath        string
	otlpAllowPublic bool
	otlp            *OTLPReceiver
	otlpLis         net.Listener
}

// Prueba en tiempo de compilación de que Source cumple el contrato.
var _ sdk.SourceConnector = (*Source)(nil)

// New devuelve un Source con la configuración por defecto.
func New() *Source {
	return &Source{agentRef: defaultAgentRef, configPath: defaultConfigPath, requirementsPath: defaultRequirementsPath, disabledHooksPath: defaultDisabledPath}
}

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Descriptor devuelve la autodescripción estable del componente.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     descriptorVersion,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Grok Build (governance)",
		Description: "Governs the xAI Grok Build agent from its local configuration (read-only): observed sandbox profile and presence of the managed-settings.json file honored by the agent.",
		ConfigFields: []sdk.ConfigField{
			{Key: "agent_ref", Type: sdk.FieldString, Default: defaultAgentRef, Description: "Stable source reference for this Grok Build installation."},
			{Key: "disabled_hooks_path", Type: sdk.FieldString, Default: defaultDisabledHooksPath, Description: "Path to the disabled-hooks file. It is the only path through which a governed hook stops running without warning, and no administrator layer constrains it."},
			{Key: "requirements_path", Type: sdk.FieldString, Default: defaultRequirementsPath, Description: "Path to the system requirements.toml, the layer that CONSTRAINS the agent configuration. Empty uses the default path."},
			{Key: "config_path", Type: sdk.FieldString, Default: defaultConfigPath, Description: "Path to the user's config.toml. `~` expands relative to HOME."},
			{Key: "otlp_http", Type: sdk.FieldBool, Default: "false", Description: "Starts the Grok Build OTLP/HTTP trace receiver. Disabled by default: binding a port is an operator decision."},
			{Key: "otlp_http_addr", Type: sdk.FieldString, Default: defaultOTLPAddr, Description: "Address of the OTLP/HTTP receiver. Set this value in the agent's OTEL_EXPORTER_OTLP_ENDPOINT."},
			{Key: "otlp_path", Type: sdk.FieldString, Default: defaultOTLPPath, Description: "Receiver path. The default was MEASURED in grok 1.0.5: /v1/traces (Codex uses a different logs path)."},
			{Key: "otlp_allow_public_bind", Type: sdk.FieldBool, Default: "false", Description: "Allows binding outside loopback. The receiver does NOT authenticate: exposing it is an explicit decision."},
			{Key: "managed_settings_path", Type: sdk.FieldString, Description: "Path to the Claude Code managed-settings.json honored by Grok Build. If unset, no claim is made about that surface."},
		},
	}
}

// Open resuelve la configuración. Un error de configuración se devuelve aquí, no en Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if v := strings.TrimSpace(cfg.Get("agent_ref")); v != "" {
		s.agentRef = v
	}
	if v := strings.TrimSpace(cfg.Get("config_path")); v != "" {
		s.configPath = v
	}
	s.managedPath = strings.TrimSpace(cfg.Get("managed_settings_path"))
	if v := strings.TrimSpace(cfg.Get("requirements_path")); v != "" {
		s.requirementsPath = v
	}
	if s.requirementsPath == "" {
		s.requirementsPath = defaultRequirementsPath
	}
	if v := strings.TrimSpace(cfg.Get("disabled_hooks_path")); v != "" {
		s.disabledHooksPath = v
	}
	if s.disabledHooksPath == "" {
		s.disabledHooksPath = defaultDisabledPath
	}
	if s.agentRef == "" {
		return errors.New("grok: agent_ref must not be empty")
	}
	// ⛔ EL RECEPTOR SE LIGA AQUÍ, no en Gather: un puerto ocupado tiene que fallar donde el SDK
	//    espera el error y no a mitad de una recogida. Mismo criterio que `codex` y `claude`.
	s.otlpEnabled = cfg.GetBool("otlp_http", false)
	s.otlpAddr = strings.TrimSpace(cfg.Get("otlp_http_addr"))
	if s.otlpAddr == "" {
		s.otlpAddr = defaultOTLPAddr
	}
	s.otlpPath = strings.TrimSpace(cfg.Get("otlp_path"))
	if s.otlpPath == "" {
		s.otlpPath = defaultOTLPPath
	}
	s.otlpAllowPublic = cfg.GetBool("otlp_allow_public_bind", false)
	if s.otlpEnabled {
		lis, err := netbind.Listen(context.Background(), "tcp", s.otlpAddr, netbind.Policy{
			Component:   Name,
			Purpose:     "OTLP/HTTP trace receiver",
			AllowPublic: s.otlpAllowPublic,
			OptIn:       "otlp_allow_public_bind",
		})
		if err != nil {
			return fmt.Errorf("grok: bind OTLP/HTTP %s: %w", s.otlpAddr, err)
		}
		s.otlpLis = lis
		s.otlp = NewOTLPReceiver(s.otlpPath, 0, s.now)
		s.otlp.Serve(lis)
	}
	return nil
}

// Gather emite las observaciones. Es de lectura y no toca nada del agente.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	cfg, leido, err := s.readConfig()
	if err != nil {
		return err
	}
	req, reqEstado, _ := s.readRequirements()
	if f, ok := s.hallazgoOTLP(); ok {
		if eerr := sink.Emit(ctx, f); eerr != nil {
			return eerr
		}
	}
	desact, desactEstado := s.hooksDesactivados()
	for _, f := range s.findings(cfg, leido, req, reqEstado, desact, desactEstado) {
		if eerr := sink.Emit(ctx, f); eerr != nil {
			return eerr
		}
	}
	return nil
}

// Close suelta el receptor si se levantó. Sin esto, un puerto atado sobrevive al conector y el
// siguiente arranque falla por la razón equivocada.
func (s *Source) Close(ctx context.Context) error {
	if s.otlp != nil {
		return s.otlp.Close(ctx)
	}
	if s.otlpLis != nil {
		return s.otlpLis.Close()
	}
	return nil
}

// grokConfig es el subconjunto de `~/.grok/config.toml` que este conector lee. Se modela sólo
// lo verificado; un campo que no esté en la documentación no se inventa aquí.
type grokConfig struct {
	Sandbox struct {
		Profile string `toml:"profile"`
	} `toml:"sandbox"`
	// MCPServers es la tabla de descubrimiento `[mcp_servers.<nombre>]`. Se lee como mapa a
	// `toml.Primitive`-equivalente —un `map[string]struct{}` vacío— porque lo que gobierna es
	// QUÉ SERVIDORES hay declarados, no cómo se configuran por dentro: sus campos incluyen
	// órdenes y credenciales, y este conector no los mira ni los transporta.
	MCPServers map[string]struct{} `toml:"mcp_servers"`
}

// estadoConfig distingue las TRES situaciones que un booleano colapsaría: no configurado,
// presente y legible, presente e ilegible. La tercera no es «no configurado» — es un fichero
// que el agente sí va a intentar leer y nosotros no hemos podido.
type estadoConfig int

const (
	configAusente estadoConfig = iota
	configLeido
	configIlegible
)

// readConfig lee el config.toml del usuario.
func (s *Source) readConfig() (grokConfig, estadoConfig, error) {
	return s.readTOML(s.configPath)
}

// readRequirements lee el requirements.toml de sistema. Es el MISMO lector a propósito: el
// fichero de requisitos es TOML y se fusiona sobre la configuración, así que `[sandbox] profile`
// aparece con la misma forma. Dos lectores divergirían, y el primer síntoma sería un perfil
// impuesto que el conector no ve.
func (s *Source) readRequirements() (grokConfig, estadoConfig, error) {
	return s.readTOML(s.requirementsPath)
}

func (s *Source) readTOML(p string) (grokConfig, estadoConfig, error) {
	var cfg grokConfig
	ruta, err := expandirHome(p)
	if err != nil {
		return cfg, configIlegible, nil // no poder resolver HOME no es un fallo del motor
	}
	info, err := os.Stat(ruta)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return cfg, configAusente, nil
	case err != nil:
		return cfg, configIlegible, nil
	case info.Size() > maxConfigBytes:
		return cfg, configIlegible, nil
	}
	raw, err := os.ReadFile(ruta) //nolint:gosec // ruta de configuración declarada por el operador
	if err != nil {
		return cfg, configIlegible, nil
	}
	if err := toml.Unmarshal(raw, &cfg); err != nil {
		return cfg, configIlegible, nil
	}
	cfg.Sandbox.Profile = strings.TrimSpace(cfg.Sandbox.Profile)
	return cfg, configLeido, nil
}

// expandirHome resuelve un `~/` inicial. No usa una expansión de shell: la ruta viene de la
// configuración del operador y no debe pasar por ningún intérprete.
func expandirHome(p string) (string, error) {
	if !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~/")), nil
}
