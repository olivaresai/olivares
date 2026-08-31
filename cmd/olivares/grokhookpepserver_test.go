// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

func writeGrokPEPConfig(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "grokpep.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("OLIVARES_GROK_HOOK_PEP_CONFIG", path)
}

// El valor por defecto SEGURO: un nodo sin aprovisionar no sirve ninguna superficie de imposición
// de Grok. Un agente en ese nodo corre sin gobernar Y sin observar —exactamente como antes de que
// esto existiera— en vez de medio gobernado de una forma que nadie puede caracterizar.
func TestGrokPEPNoMontaNadaSinConfigurar(t *testing.T) {
	t.Setenv("OLIVARES_GROK_HOOK_PEP_CONFIG", "")
	srv, err := buildGrokHookPEPServer(&engine{}, sessions.New(), discardLog())
	if err != nil {
		t.Fatalf("sin configurar no puede ser un error: %v", err)
	}
	if srv != nil {
		t.Fatal("sin configurar no se monta nada")
	}
}

// ⛔ EL CONTROL POSITIVO NO PUEDE SER «no hubo error». Un `(nil, nil)` pasaría esa aserción y NO
// es un montaje — el hermano documenta que un contraste externo cazó exactamente ese hueco en la
// primera versión de su prueba. Aquí se exige SERVIDOR.
func TestGrokPEPRechazaElTenantDeSistemaYMontaConUnoDeNegocio(t *testing.T) {
	writeGrokPEPConfig(t, `{"tenant":"`+model.NewTenantID().String()+`"}`)
	srv, err := buildGrokHookPEPServer(&engine{}, sessions.New(), discardLog())
	if err != nil {
		t.Fatalf("control: un tenant de negocio válido tiene que montar; salió %v", err)
	}
	if srv == nil {
		t.Fatal("control: un tenant válido tiene que producir un SERVIDOR; un nil no es un montaje")
	}

	writeGrokPEPConfig(t, `{"tenant":"`+model.SystemTenantID.String()+`"}`)
	if _, err := buildGrokHookPEPServer(&engine{}, sessions.New(), discardLog()); err == nil {
		t.Error("el tenant de sistema reservado no puede respaldar una superficie gobernada")
	}
}

// Sin plano de identidad NO se monta: un punto gobernado que no puede atribuir sus decisiones es
// peor que no tener punto, porque produce veredictos que no se pueden auditar después.
func TestGrokPEPRechazaSinPlanoDeIdentidad(t *testing.T) {
	writeGrokPEPConfig(t, `{"tenant":"`+model.NewTenantID().String()+`"}`)
	if _, err := buildGrokHookPEPServer(&engine{}, nil, discardLog()); err == nil {
		t.Fatal("sin plano de identidad no se puede montar")
	}
}

// ⛔ CADA MOTOR EN SU PROPIO SOCKET. Los tres dialectos de respuesta son distintos, y un socket que
// contestara a varios significaría que una configuración equivocada recibe una respuesta en una
// forma que el agente ignora EN SILENCIO — el fallo exacto que estos conectores existen para
// impedir.
func TestGrokPEPUsaSuPropioSocket(t *testing.T) {
	// Sin `t.Parallel()`: esta celda usa `t.Setenv` a través del ayudante, y Go prohíbe
	// combinarlos — un test que fija el entorno no puede correr en paralelo con otros que lo
	// leen. Lo cazó el runtime con un pánico, que es la forma correcta de decirlo.
	if defaultGrokHookPEPListen == defaultCodexHookPEPListen {
		t.Fatal("Grok y Codex comparten puerto por defecto")
	}
	// Y el listen configurado manda sobre el de por defecto.
	writeGrokPEPConfig(t, `{"tenant":"`+model.NewTenantID().String()+`","listen":"127.0.0.1:9999"}`)
	srv, err := buildGrokHookPEPServer(&engine{}, sessions.New(), discardLog())
	if err != nil || srv == nil {
		t.Fatalf("tenía que montar: %v", err)
	}
	if srv.Addr != "127.0.0.1:9999" {
		t.Fatalf("el listen configurado manda, salió %q", srv.Addr)
	}
}

// Una configuración ilegible hace fallar el arranque CERRADO: no se monta media superficie.
func TestGrokPEPFallaCerradoConUnaConfigRota(t *testing.T) {
	writeGrokPEPConfig(t, `{"tenant": esto no es json`)
	if _, err := buildGrokHookPEPServer(&engine{}, sessions.New(), discardLog()); err == nil {
		t.Fatal("una configuración ilegible tiene que hacer fallar el arranque, no montarse a medias")
	}
}
