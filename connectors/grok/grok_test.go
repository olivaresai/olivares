// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package grok

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

type captureSink struct{ obs []model.Observation }

func (s *captureSink) Emit(_ context.Context, o model.Observation) error {
	s.obs = append(s.obs, o)
	return nil
}

func (s *captureSink) findings() []model.FindingReport {
	var out []model.FindingReport
	for _, o := range s.obs {
		if f, ok := o.(model.FindingReport); ok {
			out = append(out, f)
		}
	}
	return out
}

// gather corre el conector con la configuración dada y devuelve lo emitido.
func gather(t *testing.T, cfg map[string]string) []model.FindingReport {
	t.Helper()
	// ⛔ TODA RUTA POR DEFECTO DE ESTE CONECTOR APUNTA FUERA DEL TEMPORAL —`~/.grok/config.toml`,
	//    `/etc/grok/requirements.toml`, `~/.grok/disabled-hooks`— y una celda que no las fije
	//    contesta por la caja donde corre. Aquí ninguna de las tres existe hoy, así que el verde
	//    es cierto **por accidente**: en un portátil con Grok Build instalado, las mismas celdas
	//    dirían otra cosa sin que nadie tocara el código.
	//
	//    Se fijan en el AYUDANTE y no caso por caso a propósito: una celda nueva que se olvide de
	//    una hereda la hermeticidad en vez de heredar el fallo. El caso que quiera medir un
	//    fichero de verdad la sobreescribe, que es lo que hacen las de abajo.
	//
	//    No es una precaución teórica: esta misma jornada, en `cmd/olivares`, un test con
	//    `t.TempDir()` fijaba dos eslabones de una cadena de cuatro y acabó midiendo una
	//    instalación real de 7,9 MB del `$HOME`. Rojo en `main` limpio, verde en CI, una noche
	//    de dos carriles.
	sinTocar := t.TempDir()
	base := map[string]string{
		"config_path":         filepath.Join(sinTocar, "config-inexistente.toml"),
		"requirements_path":   filepath.Join(sinTocar, "requirements-inexistente.toml"),
		"disabled_hooks_path": filepath.Join(sinTocar, "disabled-hooks-inexistente"),
	}
	for k, v := range cfg {
		base[k] = v
	}
	cfg = base
	s := New()
	s.now = func() time.Time { return time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC) }
	if err := s.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.findings()
}

// conConfig escribe un config.toml temporal y devuelve su ruta.
func conConfig(t *testing.T, contenido string) string {
	t.Helper()
	dir := t.TempDir()
	ruta := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(ruta, []byte(contenido), 0o600); err != nil {
		t.Fatalf("no se pudo escribir el config: %v", err)
	}
	return ruta
}

func tieneTitulo(fs []model.FindingReport, sub string) bool {
	for _, f := range fs {
		if strings.Contains(f.Title, sub) {
			return true
		}
	}
	return false
}

// ⛔ LA CELDA QUE SOSTIENE EL CONECTOR, y la razón de que exista en esta forma.
//
// ⛔ ESTA CELDA FIJABA EL COMPORTAMIENTO EQUIVOCADO, y por eso se reescribe entera en vez de
// ajustarle la cadena. Exigía que el conector dijera SIEMPRE «OBSERVADO, no impuesto», con el
// razonamiento de que la documentación no describía forma de imponerlo. El fuente público dice
// que sí la hay —`/etc/grok/requirements.toml` de root, más MDM de macOS, la capa que ACOTA por
// encima de todo—, así que la celda protegía una afirmación falsa. Una prueba que fija un error
// lo vuelve permanente: cualquiera que lo corrigiera veía rojo y lo revertía.
//
// Lo que se fija ahora es la MEDIDA, en sus cuatro estados. Y cada caso fija su
// `requirements_path`: el valor por defecto es una ruta del SISTEMA, así que una caja que la
// tuviera puesta volcaría el resultado de la celda sin que nadie lo notara.
func TestLaImposicionDelPerfilSeMideEnSusCuatroEstados(t *testing.T) {
	t.Parallel()

	// ── ausente: se puede imponer y NO está puesto. Con CUALQUIER perfil, incluido `strict`:
	//    el perfil del usuario no impone nada, que era la mitad cierta del hallazgo viejo.
	for _, perfil := range append(perfilesConocidos(), "") {
		contenido := "[sandbox]\n"
		if perfil != "" {
			contenido += "profile = \"" + perfil + "\"\n"
		}
		fs := gather(t, map[string]string{
			"config_path":       conConfig(t, contenido),
			"requirements_path": filepath.Join(t.TempDir(), "no-existe.toml"),
		})
		if !tieneTitulo(fs, "NOT enforced and CAN be enforced") {
			t.Fatalf("perfil %q: sin requirements.toml el hallazgo tiene que decir que se puede imponer y no lo está: %+v", perfil, fs)
		}
	}

	// ── puesto y fijando perfil: IMPUESTO, y baja a informativo.
	impuesto := gather(t, map[string]string{
		"config_path":       conConfig(t, "[sandbox]\nprofile = \"off\"\n"),
		"requirements_path": conConfig(t, "[sandbox]\nprofile = \"strict\"\n"),
	})
	if !tieneTitulo(impuesto, "is ENFORCED") {
		t.Fatalf("con requirements fijando el perfil, el hallazgo tiene que decir IMPUESTO: %+v", impuesto)
	}
	// Y la dirección que importa: con el perfil impuesto ya NO se afirma que no lo esté.
	if tieneTitulo(impuesto, "NOT enforced") {
		t.Fatal("no puede afirmar las dos cosas a la vez")
	}

	// ── puesto pero SIN fijar perfil: el fichero existe y no acota esto.
	sinPerfil := gather(t, map[string]string{
		"config_path":       conConfig(t, "[sandbox]\nprofile = \"strict\"\n"),
		"requirements_path": conConfig(t, "[otra]\nclave = 1\n"),
	})
	if !tieneTitulo(sinPerfil, "does NOT set [sandbox] profile") {
		t.Fatalf("un requirements.toml que no fija el perfil no impone nada: %+v", sinPerfil)
	}

	// ── ILEGIBLE no es ausente, aquí igual que en el config: el agente sí lo va a leer.
	ilegible := gather(t, map[string]string{
		"config_path":       conConfig(t, "[sandbox]\nprofile = \"strict\"\n"),
		"requirements_path": conConfig(t, "esto no es toml = = [[["),
	})
	if !tieneTitulo(ilegible, "this is not \"not enforced\"; it is \"not measured\"") {
		t.Fatalf("un requirements.toml ilegible no puede leerse como «no impuesto»: %+v", ilegible)
	}
}

// La vía de compatibilidad con Claude es un hecho de inventario y se emite siempre: quien ya
// gobierna Claude Code por su managed-settings.json está gobernando también a este agente, y un
// operador que no lo sepa cree tener dos superficies sin gobernar cuando tiene una.
func TestSeReportaQueGrokLeeElManagedSettingsDeClaude(t *testing.T) {
	t.Parallel()

	fs := gather(t, map[string]string{
		"config_path":       conConfig(t, "[sandbox]\nprofile = \"strict\"\n"),
		"requirements_path": filepath.Join(t.TempDir(), "no-existe.toml"),
	})
	if !tieneTitulo(fs, "Claude Code's managed-settings.json") {
		t.Fatalf("falta el hecho de compatibilidad con Claude: %+v", fs)
	}
}

// ⛔ ILEGIBLE NO ES AUSENTE, y son dos hallazgos distintos a propósito: el agente SÍ va a leer
// ese fichero y nosotros no hemos podido. Colapsarlos daría un verde sobre algo no mirado.
func TestUnConfigIlegibleNoSeReportaComoAusente(t *testing.T) {
	t.Parallel()

	ausentes := gather(t, map[string]string{"config_path": filepath.Join(t.TempDir(), "no-existe.toml")})
	if !tieneTitulo(ausentes, "No Grok Build config.toml exists at the configured path") {
		t.Fatalf("un fichero que no existe debe reportarse como ausente: %+v", ausentes)
	}
	if tieneTitulo(ausentes, "could NOT be read") {
		t.Fatal("un fichero ausente no puede reportarse además como ilegible")
	}

	roto := gather(t, map[string]string{"config_path": conConfig(t, "esto no es toml = = [[[")})
	if !tieneTitulo(roto, "could NOT be read") {
		t.Fatalf("un TOML corrupto debe reportarse como ilegible: %+v", roto)
	}
	if tieneTitulo(roto, "No Grok Build config.toml exists at the configured path") {
		t.Fatal("un fichero presente pero corrupto NO es «no hay config»")
	}
}

// Un perfil que la documentación no declara no se interpreta: se dice que no se sabe qué
// concede, en vez de suponerlo.
func TestUnPerfilDesconocidoNoSeInterpreta(t *testing.T) {
	t.Parallel()

	fs := gather(t, map[string]string{"config_path": conConfig(t, "[sandbox]\nprofile = \"turbo\"\n")})
	if !tieneTitulo(fs, "Unrecognized Grok Build sandbox profile") {
		t.Fatalf("un perfil inventado debe salir como no reconocido: %+v", fs)
	}
	if !tieneTitulo(fs, "no claim is made about what it grants") {
		t.Fatal("debe decirse explícitamente que no se afirma nada sobre lo que concede")
	}
	// Y los documentados sí se interpretan — la otra dirección, sin la cual lo de arriba lo
	// satisface un conector que no reconozca NINGUNO.
	ok := gather(t, map[string]string{"config_path": conConfig(t, "[sandbox]\nprofile = \"strict\"\n")})
	if tieneTitulo(ok, "Unrecognized Grok Build sandbox profile") {
		t.Fatal("strict está documentado y salió como no reconocido")
	}
}

// La semántica de red viaja con su límite: el bloqueo se documenta sólo para Linux, y un
// cliente en macOS tiene otra promesa del mismo perfil.
func TestLaSemanticaDeRedViajaConSuLimite(t *testing.T) {
	t.Parallel()

	bloquean := []string{"read-only", "strict"}
	permiten := []string{"off", "workspace", "devbox"}
	for _, p := range bloquean {
		fs := gather(t, map[string]string{"config_path": conConfig(t, "[sandbox]\nprofile = \""+p+"\"\n")})
		if !tieneTitulo(fs, "network BLOCKED") {
			t.Fatalf("%s bloquea red según x.ai: %+v", p, fs)
		}
		if !tieneTitulo(fs, "enforced on Linux only") {
			t.Fatalf("%s: el bloqueo de red sólo está documentado para Linux y eso debe viajar en el hallazgo", p)
		}
	}
	for _, p := range permiten {
		fs := gather(t, map[string]string{"config_path": conConfig(t, "[sandbox]\nprofile = \""+p+"\"\n")})
		if !tieneTitulo(fs, "network ALLOWED") {
			t.Fatalf("%s permite red según x.ai: %+v", p, fs)
		}
	}
}

// El conjunto de perfiles es cerrado y completo frente a lo documentado.
func TestLosCincoPerfilesDocumentados(t *testing.T) {
	t.Parallel()

	quiere := []string{"devbox", "off", "read-only", "strict", "workspace"}
	got := perfilesConocidos()
	if len(got) != len(quiere) {
		t.Fatalf("x.ai documenta %d perfiles y el conector conoce %d: %v", len(quiere), len(got), got)
	}
	for i := range quiere {
		if got[i] != quiere[i] {
			t.Fatalf("perfil %d: quiere %q, tiene %q", i, quiere[i], got[i])
		}
	}
}

// ⛔ EL INTERRUPTOR QUE APAGA UN HOOK GOBERNADO, y no lo acota ninguna capa de administrador.
//
// Verificado en el fuente público: `trust.rs:127-129` construye `~/.grok/disabled-hooks`,
// `trust.rs:42-57` compara por NOMBRE sin mirar de qué capa salió el hook, y `dispatcher.rs:27`
// aplica ese filtro EN EL DESPACHO. Es la asimetría que un operador necesita ver: el perfil de
// sandbox sí se puede imponer, y la ejecución del hook no.
//
// LA MUTACIÓN que esta celda mata: reportar una CIFRA en vez de los nombres. «3 hooks
// desactivados» obliga a ir a buscar cuáles, y el nombre es justo lo que decide si el apagado es
// el nuestro.
func TestLosHooksDesactivadosSeReportanPorNombre(t *testing.T) {
	t.Parallel()

	fs := gather(t, map[string]string{
		"disabled_hooks_path": conConfig(t, "# comentario\n\nolivares-governed\n  otro-hook  \n"),
	})
	if !tieneTitulo(fs, "olivares-governed, otro-hook") {
		t.Fatalf("los hooks desactivados tienen que salir por NOMBRE y ordenados: %+v", fs)
	}
	if !tieneTitulo(fs, "without checking which layer") {
		t.Fatalf("falta decir que el apagado alcanza a los hooks del administrador: %+v", fs)
	}
	// Las líneas de comentario y las vacías no son nombres: el fuente las salta y aquí también.
	if tieneTitulo(fs, "# comentario") {
		t.Fatal("una línea de comentario no es un hook desactivado")
	}
}

// Y los otros dos estados, porque «no hay fichero» y «hay fichero y no se ha podido leer» son
// hechos distintos. Colapsarlos daría un verde sobre algo no mirado.
func TestElFicheroDeHooksDesactivadosDistingueAusenteDeIlegible(t *testing.T) {
	t.Parallel()

	ausente := gather(t, nil)
	if !tieneTitulo(ausente, "no hook is disabled by name") {
		t.Fatalf("sin fichero, ningún hook desactivado: %+v", ausente)
	}
	if tieneTitulo(ausente, "this is not \"none\"; it is \"not measured\"") {
		t.Fatal("un fichero ausente no es un fichero ilegible")
	}

	// Un directorio donde se espera un fichero: existe y no se puede leer como tal.
	dir := t.TempDir()
	ilegible := gather(t, map[string]string{"disabled_hooks_path": dir})
	if !tieneTitulo(ilegible, "this is not \"none\"; it is \"not measured\"") {
		t.Fatalf("un disabled-hooks ilegible no puede leerse como «ninguno»: %+v", ilegible)
	}

	// Y el fichero vacío: existe, no desactiva nada, y aun así el operador tiene que saber que
	// sigue siendo escribible por el usuario.
	vacio := gather(t, map[string]string{"disabled_hooks_path": conConfig(t, "")})
	if !tieneTitulo(vacio, "disables no hooks") {
		t.Fatalf("un fichero vacío existe y no desactiva nada: %+v", vacio)
	}
}

// ⛔ LA LISTA DE SERVIDORES MCP, NO SU CIFRA. «3 servidores» obliga a otra sesión a ir a buscar
// cuáles, y el nombre es lo único que permite decir si alguno no debería estar ahí.
func TestLosServidoresMCPSeReportanPorNombre(t *testing.T) {
	t.Parallel()

	fs := gather(t, map[string]string{
		"config_path": conConfig(t, "[mcp_servers.github]\ncommand = \"x\"\n"+
			"[mcp_servers.filesystem]\ncommand = \"y\"\n"),
	})
	if !tieneTitulo(fs, "filesystem, github") {
		t.Fatalf("los servidores tienen que salir por NOMBRE y ordenados: %+v", fs)
	}
	// Ordenados de verdad: un texto que cambia de orden entre corridas da otro digest para el
	// mismo hecho y rompe la deduplicación.
	if tieneTitulo(fs, "github, filesystem") {
		t.Fatal("el orden no es estable")
	}
}

// Los tres estados del config, que son hechos distintos.
func TestLaSuperficieMCPDistingueAusenteDeIlegible(t *testing.T) {
	t.Parallel()

	sinTabla := gather(t, map[string]string{
		"config_path": conConfig(t, "[sandbox]\nprofile = \"strict\"\n"),
	})
	if !tieneTitulo(sinTabla, "declares no MCP servers") {
		t.Fatalf("un config sin la tabla no declara servidores: %+v", sinTabla)
	}

	ilegible := gather(t, map[string]string{"config_path": conConfig(t, "esto no es toml = = [[[")})
	if !tieneTitulo(ilegible, "no claim is made about which MCP servers") {
		t.Fatalf("un config ilegible no puede leerse como «ninguno»: %+v", ilegible)
	}
}

// ⛔ Y EL «ILEGIBLE» DEL FICHERO DE REQUISITOS, que es una rama DISTINTA de la del config y que
// yo no cubría: la mutación que la colapsaba SOBREVIVIÓ a las otras tres celdas. Un
// requirements.toml que existe y no se puede leer no es «no hay nada fijado» — es «no lo he
// medido», y decir lo primero es un verde sobre algo no mirado.
func TestUnRequirementsIlegibleNoEsAusenciaDeFijacionMCP(t *testing.T) {
	t.Parallel()

	fs := gather(t, map[string]string{
		"config_path":       conConfig(t, "[mcp_servers.github]\ncommand = \"x\"\n"),
		"requirements_path": conConfig(t, "esto no es toml = = [[["),
	})
	if !tieneTitulo(fs, "no claim is made about whether MCP servers are pinned") {
		t.Fatalf("un requirements ILEGIBLE no puede leerse como «no fijados»: %+v", fs)
	}
	if tieneTitulo(fs, "NOT pinned and CAN be pinned") {
		t.Fatal("«no he podido leerlo» y «no están fijados» son hechos distintos")
	}
}

// ⛔ LOS DOS HECHOS DE SIGNO OPUESTO, y hay que dar los DOS. Sin el primero, el operador cree que
// la lista está fijada cuando no lo está; sin el segundo, gasta esfuerzo defendiendo una puerta
// que el propio agente tiene cerrada por diseño — y puede concluir que la variable de entorno es
// un agujero y desconfiar de todo lo demás.
func TestSeDicenLasDosDireccionesDeLaSuperficieMCP(t *testing.T) {
	t.Parallel()

	fs := gather(t, map[string]string{
		"config_path": conConfig(t, "[mcp_servers.github]\ncommand = \"x\"\n"),
	})
	// La palanca del administrador existe y NO está puesta.
	if !tieneTitulo(fs, "NOT pinned and CAN be pinned") {
		t.Fatalf("falta decir que un admin puede acotarlos y no lo ha hecho: %+v", fs)
	}
	// Y la vía que YA está cerrada por diseño de xAI.
	if !tieneTitulo(fs, "GROK_CONFIG cannot add an MCP server") {
		t.Fatalf("falta el hecho fail-closed del overlay: %+v", fs)
	}

	// Con requisitos que SÍ los fijan, cambia el veredicto y baja a informativo.
	fijados := gather(t, map[string]string{
		"config_path":       conConfig(t, "[mcp_servers.github]\ncommand = \"x\"\n"),
		"requirements_path": conConfig(t, "[mcp_servers.solo-este]\ncommand = \"z\"\n"),
	})
	if !tieneTitulo(fijados, "are PINNED in") {
		t.Fatalf("con requirements fijando la tabla tiene que decirlo: %+v", fijados)
	}
	if tieneTitulo(fijados, "NOT pinned and CAN be pinned") {
		t.Fatal("no puede afirmar las dos cosas a la vez")
	}
}
