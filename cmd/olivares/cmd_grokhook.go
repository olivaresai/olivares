// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/connectors/grok/session"
)

// newGrokHookCmd es el COMANDO DE HOOK que invoca Grok Build. Grok escribe el JSON del hook por
// stdin y lee la decisión de stdout; este comando lo reenvía al PEP gobernado y transmite el
// veredicto, DENY-CLOSED en cada fallo.
//
// Es hermano de `codex-hook` y deliberadamente NO el mismo comando con un flag, porque los dos
// motores difieren justo en lo que más caro sale suponer. Verificado el 2026-08-19 contra el
// repositorio PÚBLICO de xAI (commit anclado en `connectors/grok/session/hook.go`):
//
//   - **El nombre del evento viaja en snake_case.** `event.rs` deriva `Serialize` con
//     `rename_all = "snake_case"` mientras las CLAVES del sobre son camelCase. Un comando que
//     comparase contra los nombres de la página pública no reconocería ni uno.
//   - **Hay una segunda vía para el evento: el entorno.** Grok pone `GROK_HOOK_EVENT` además del
//     cuerpo, y por eso este comando pasa `os.Getenv` al cliente. Sin ella, un stdin cortado
//     —la malformación más probable de todas— dejaría la negativa sin saber en qué forma emitirse.
//   - **El veto sólo existe en `pre_tool_use`.** En el resto, un deny se REGISTRA y no impide; el
//     cliente lo saca por stderr diciéndolo, porque callarlo convertiría «no pude impedirlo» en
//     «no pasó nada».
//
// INSTALACIÓN. Grok descubre hooks por dos clases de fuente (`xai-grok-hooks/src/discovery.rs`):
// un fichero de ajustes JSON del que sólo usa su clave `hooks`, y un DIRECTORIO de `*.json`
// (p. ej. `~/.grok/hooks/`), que se cargan en orden de nombre.
//
// ⛔⛔ ANTI-MANIPULACIÓN, Y AQUÍ GROK NO DA LO QUE DA CODEX. Un hook se puede DESACTIVAR POR
//
//	NOMBRE escribiendo una línea en `~/.grok/disabled-hooks` —texto plano, en el HOME del
//	usuario— y `dispatcher.rs:27` honra ese fichero EN EL DESPACHO, sin mirar de qué capa salió
//	el hook. **Ni `/etc/grok/requirements.toml` ni el MDM de macOS lo acotan**: esas capas
//	acotan CONFIGURACIÓN, y esto es otro fichero leído directamente.
//
//	⇒ El perfil de sandbox SÍ se puede imponer; la ejecución de este hook NO. Un plano que sólo
//	mirase el sandbox diría «impuesto» de una sesión cuyo hook está apagado. `connectors/grok`
//	lo vigila y lo reporta con la LISTA de nombres desactivados, que es lo que permite ver si el
//	apagado es el nuestro.
func newGrokHookCmd() *cobra.Command {
	var (
		endpoint string
		token    string
		tenant   string
		agent    string
		org      string
		account  string
		timeout  time.Duration
	)
	var resolveServer func() string
	cmd := &cobra.Command{
		Use:   "grok-hook",
		Short: "Governed PEP hook client for Grok Build: forward a Grok hook to the control plane and relay the decision (deny-closed)",
		Long: "grok-hook is the managed Grok Build hook command.\n" +
			"It reads the hook payload from stdin, forwards it to the governed PEP and writes the\n" +
			"decision in the shape THAT EVENT honors: a pre_tool_use deny is a decision body with\n" +
			"exit 2, and on any other event a deny is RECORDED but cannot block — the reason then\n" +
			"goes to stderr saying so, because a 2 the agent ignores prevents nothing and would\n" +
			"leave the illusion in the log that it did.\n\n" +
			"It is DENY-CLOSED: endpoint unset, unreachable, a non-2xx status, a body that is not a\n" +
			"verdict, or a verdict it does not recognize all produce a deny of its own.\n\n" +
			"Configuration is read from the environment (overridable by flags):\n" +
			"  OLIVARES_GROK_HOOK_URL      governed PEP endpoint (e.g. http://127.0.0.1:8449/)\n" +
			"  OLIVARES_GROK_HOOK_TOKEN    the agent's PEP bearer credential\n" +
			"  OLIVARES_GROK_HOOK_TENANT   the tenant the agent acts in\n" +
			"  OLIVARES_GROK_HOOK_AGENT    the agent identity hint\n" +
			"  OLIVARES_GROK_HOOK_ORG      the org identity hint\n" +
			"  OLIVARES_GROK_HOOK_ACCOUNT  the account identity hint\n\n" +
			"GROK_HOOK_EVENT is read too, but it is not ours: Grok sets it alongside the stdin\n" +
			"payload, and it is what lets a truncated body still be denied in the right shape.",
		Example: "  # Forward one Grok Build hook payload using the managed environment\n" +
			"  printf '%s' '{\"hookEventName\":\"pre_tool_use\",\"sessionId\":\"s-1\",\"toolName\":\"Bash\"}' | olivares grok-hook",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := session.ClientConfig{
				Endpoint: resolveServer(),
				Token:    firstNonEmptyEnv(token, "OLIVARES_GROK_HOOK_TOKEN"),
				Tenant:   firstNonEmptyEnv(tenant, "OLIVARES_GROK_HOOK_TENANT"),
				Agent:    firstNonEmptyEnv(agent, "OLIVARES_GROK_HOOK_AGENT"),
				Org:      firstNonEmptyEnv(org, "OLIVARES_GROK_HOOK_ORG"),
				Account:  firstNonEmptyEnv(account, "OLIVARES_GROK_HOOK_ACCOUNT"),
				Timeout:  timeout,
				// La segunda vía del evento. Se inyecta aquí y no dentro del paquete para que el
				// conector siga siendo probable sin ensuciar el proceso.
				Env: os.Getenv,
			}
			res := session.RunClient(cmd.Context(), cmd.InOrStdin(), cfg)
			// stdout lleva el veredicto y se escribe SIEMPRE y primero: un hook que no escribe
			// nada el agente lo lee como «sin objeción», así que no hay camino de error en el que
			// callarse sea la respuesta correcta.
			if _, err := cmd.OutOrStdout().Write(res.Stdout); err != nil {
				return err
			}
			if res.Stderr != "" {
				_, _ = cmd.ErrOrStderr().Write([]byte(res.Stderr + "\n"))
			}
			if res.ExitCode != 0 {
				// Cobra imprimiría y envolvería un error; aquí el código de salida ES la señal, y
				// la razón ya está en stderr.
				os.Exit(res.ExitCode)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "governed PEP URL (default $OLIVARES_GROK_HOOK_URL); --server is the canonical alias")
	resolveServer = addServerAliasFlag(cmd, &endpoint, "endpoint", "OLIVARES_GROK_HOOK_URL", false)
	cmd.Flags().StringVar(&token, "token", "", "the agent's PEP bearer credential (default $OLIVARES_GROK_HOOK_TOKEN)")
	cmd.Flags().StringVar(&tenant, "tenant", "", "the tenant the agent acts in (default $OLIVARES_GROK_HOOK_TENANT)")
	cmd.Flags().StringVar(&agent, "agent", "", "agent identity hint (default $OLIVARES_GROK_HOOK_AGENT)")
	cmd.Flags().StringVar(&org, "org", "", "org identity hint (default $OLIVARES_GROK_HOOK_ORG)")
	cmd.Flags().StringVar(&account, "account", "", "account identity hint (default $OLIVARES_GROK_HOOK_ACCOUNT)")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "PEP request timeout")
	return cmd
}
