// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package servicenow

import "encoding/base64"

const (
	cfgInstanceURL = "instance_url"
	cfgAuthMode    = "auth_mode"
	cfgUsername    = "username"
	cfgPassword    = "password"
	cfgToken       = "token"
	authBasic      = "basic"
	authBearer     = "bearer"
)

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}
