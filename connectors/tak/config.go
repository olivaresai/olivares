// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package tak

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/netbind"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.tak"

const version = "0.1.0"

// Configuration keys.
const (
	cfgCoreConfigPath = "core_config_path"
	cfgServerURL      = "server_url"
	cfgVersionPath    = "version_path"
	cfgClientCert     = "client_cert"
	cfgClientKey      = "client_key"
	cfgCACert         = "ca_cert"
	cfgPosture        = "posture"
	cfgRequestTimeout = "request_timeout"

	cfgFeedRef         = "feed_ref"
	cfgCoTUDPListen    = "cot_udp_listen"
	cfgCoTTCPListen    = "cot_tcp_listen"
	cfgAllowPublicBind = "allow_public_bind"
	cfgCoTMulticast    = "cot_multicast_group"
	cfgCoTMaxEvent     = "cot_max_event_bytes"
	cfgCoTMaxDetail    = "cot_max_detail_bytes"
	cfgCoTRateLimit    = "cot_rate_limit_eps"
	cfgCoTMaxTCPConns  = "cot_max_tcp_conns"
	cfgCoTUIDMode      = "cot_uid_mode"
)

// UID modes. The default hashes a CoT emitter's uid before it leaves the
// connector: a uid names a device, and a device names a person carrying it.
const (
	uidModeHash = "hash"
	uidModeRaw  = "raw"
)

// Defaults chosen from the TAK Server Configuration Guide v5.2 conventions and
// from ordinary safety limits. NOTE on ports: 8087 is the conventional default
// INPUT port, and the guide's own example binds it as UDP (`<input _name="stdudp"
// protocol="udp" port="8087">`); 8088 is the documented unencrypted TCP input and
// 8089 the TLS CoT stream. The port is protocol-configurable, so nothing here
// assumes 8087 means TCP.
const (
	defaultRequestTimeout = 15 * time.Second
	defaultRateLimitEPS   = 500
	defaultMaxTCPConns    = 128
	defaultFeedRef        = "tak"
	// defaultCoreConfigPath is where a package install keeps its configuration:
	// "the /opt/tak/CoreConfig.xml file. If that file does not exist (e.g. on a
	// fresh install), then when TAK Server starts up it will copy
	// /opt/tak/CoreConfig.example.xml" [TAK Server Configuration Guide v5.2].
	defaultCoreConfigPath = "/opt/tak/CoreConfig.xml"
	// defaultVersionPath is the Marti API version endpoint. It is CONFIGURABLE
	// because tak.gov's API reference is account-gated: we will not hard-code a
	// path we cannot cite, and an unknown path degrades to an honest finding
	// rather than a fabricated version.
	defaultVersionPath = "/Marti/api/version"
	// maxCoreConfigBytes caps the configuration file we will read.
	maxCoreConfigBytes = 4 << 20
)

// ErrPostureUnauthenticated is returned at Open when a TAK Server endpoint is
// configured without a client certificate. TAK Server authenticates operators with
// mTLS; probing it anonymously would either fail or, worse, silently succeed
// against a misconfigured server and report a posture we did not authenticate.
// Deny-closed: we refuse to start rather than produce evidence we cannot stand behind.
var ErrPostureUnauthenticated = errors.New("tak: server_url is set without client_cert/client_key — refusing to probe TAK Server unauthenticated")

type config struct {
	coreConfigPath string
	serverURL      string
	versionPath    string
	clientCertPEM  string
	clientKeyPEM   string
	caCertPEM      string
	posture        bool
	requestTimeout time.Duration

	feedRef         string
	udpListen       string
	tcpListen       string
	multicast       string
	allowPublicBind bool
	rateLimitEPS    int
	maxTCPConns     int
	uidMode         string
	limits          Limits
}

// postureEnabled reports whether any posture source is configured: the CoreConfig
// file (offline, the grounded signal) or a live TAK Server probe (version only).
func (c config) postureEnabled() bool {
	return c.posture && (c.coreConfigPath != "" || c.serverURL != "")
}

// probeEnabled reports whether the optional live version probe is configured.
func (c config) probeEnabled() bool { return c.posture && c.serverURL != "" }

// ingestEnabled reports whether at least one CoT listener is configured.
func (c config) ingestEnabled() bool { return c.udpListen != "" || c.tcpListen != "" }

func descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "TAK Server posture and governed Cursor-on-Target ingest",
		Description: "Reports TAK Server posture from its CoreConfig.xml (inputs, TLS/keystore, certificate-signing backend) with an optional live version probe, and ingests CoT events over UDP/TCP as minimal-data governed signal.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgCoreConfigPath, Type: sdk.FieldString, Description: "Path to TAK Server's CoreConfig.xml (package installs: /opt/tak/CoreConfig.xml). This is the grounded, offline posture source."},
			{Key: cfgServerURL, Type: sdk.FieldString, Description: "TAK Server base URL (e.g. https://takserver.example.mil:8443). Optional: enables a live version probe only."},
			{Key: cfgVersionPath, Type: sdk.FieldString, Default: defaultVersionPath, Description: "Path of the Marti version endpoint on server_url. Configurable because tak.gov's API reference is account-gated."},
			{Key: cfgClientCert, Type: sdk.FieldString, Secret: true, Description: "PEM client certificate for TAK Server mTLS, supplied by reference."},
			{Key: cfgClientKey, Type: sdk.FieldString, Secret: true, Description: "PEM private key for the client certificate, supplied by reference."},
			{Key: cfgCACert, Type: sdk.FieldString, Description: "PEM CA bundle that signs the TAK Server certificate. Empty uses the host trust store."},
			{Key: cfgPosture, Type: sdk.FieldBool, Default: "true", Description: "Emit TAK Server posture findings."},
			{Key: cfgRequestTimeout, Type: sdk.FieldDuration, Default: "15s", Description: "Per-request timeout against the TAK Server API."},

			{Key: cfgFeedRef, Type: sdk.FieldString, Default: defaultFeedRef, Description: "Stable reference for this CoT feed. It is the source_ref a sourcescope binding scopes (source_type=data)."},
			{Key: cfgCoTUDPListen, Type: sdk.FieldString, Description: "UDP listen address for CoT (e.g. 127.0.0.1:6969). Empty disables UDP ingest. CoT is carried in the CLEAR, so a non-loopback bind is refused unless allow_public_bind=true."},
			{Key: cfgCoTTCPListen, Type: sdk.FieldString, Description: "TCP listen address for CoT open-squirt-close (e.g. 127.0.0.1:8087). Empty disables TCP ingest. CoT is carried in the CLEAR, so a non-loopback bind is refused unless allow_public_bind=true."},
			{Key: cfgAllowPublicBind, Type: sdk.FieldBool, Default: "false", Description: "DANGEROUS: allow binding the CoT bearers to a non-loopback address, and allow joining a multicast group. A CoT event is a position report keyed by a device uid, carried with no transport protection: off-host, anyone who can route to the host can read where the bearers are, and anyone can inject forged positions into the governed feed."},
			{Key: cfgCoTMulticast, Type: sdk.FieldString, Description: "Optional multicast group to join on the UDP listener (TAK's SA default is 239.2.3.1)."},
			{Key: cfgCoTMaxEvent, Type: sdk.FieldInt, Default: "65536", Description: "Maximum bytes for one CoT event."},
			{Key: cfgCoTMaxDetail, Type: sdk.FieldInt, Default: "32768", Description: "Maximum bytes for the opaque <detail> span of one CoT event."},
			{Key: cfgCoTRateLimit, Type: sdk.FieldInt, Default: "500", Description: "Maximum accepted CoT events per second across all listeners; excess is dropped and counted."},
			{Key: cfgCoTMaxTCPConns, Type: sdk.FieldInt, Default: "128", Description: "Maximum concurrent TCP CoT connections."},
			{Key: cfgCoTUIDMode, Type: sdk.FieldString, Default: uidModeHash, Description: "How a CoT uid leaves the connector: hash (default, one-way) or raw. A uid identifies a device, and a device identifies its bearer."},
		},
	}
}

func loadConfig(c sdk.Config) (config, error) {
	out := config{
		coreConfigPath: strings.TrimSpace(c.Get(cfgCoreConfigPath)),
		serverURL:      strings.TrimSpace(c.Get(cfgServerURL)),
		versionPath:    strings.TrimSpace(c.Get(cfgVersionPath)),
		clientCertPEM:  c.Get(cfgClientCert),
		clientKeyPEM:   c.Get(cfgClientKey),
		caCertPEM:      c.Get(cfgCACert),
		posture:        c.GetBool(cfgPosture, true),
		requestTimeout: c.GetDuration(cfgRequestTimeout, defaultRequestTimeout),

		feedRef:         strings.TrimSpace(c.Get(cfgFeedRef)),
		udpListen:       strings.TrimSpace(c.Get(cfgCoTUDPListen)),
		tcpListen:       strings.TrimSpace(c.Get(cfgCoTTCPListen)),
		multicast:       strings.TrimSpace(c.Get(cfgCoTMulticast)),
		allowPublicBind: c.GetBool(cfgAllowPublicBind, false),
		rateLimitEPS:    c.GetInt(cfgCoTRateLimit, defaultRateLimitEPS),
		maxTCPConns:     c.GetInt(cfgCoTMaxTCPConns, defaultMaxTCPConns),
		uidMode:         strings.TrimSpace(c.Get(cfgCoTUIDMode)),
		limits: Limits{
			MaxEventBytes:  c.GetInt(cfgCoTMaxEvent, DefaultMaxEventBytes),
			MaxDetailBytes: c.GetInt(cfgCoTMaxDetail, DefaultMaxDetailBytes),
		},
	}
	if out.feedRef == "" {
		out.feedRef = defaultFeedRef
	}
	if out.versionPath == "" {
		out.versionPath = defaultVersionPath
	}
	if !strings.HasPrefix(out.versionPath, "/") {
		return config{}, fmt.Errorf("tak: %s must be an absolute path, got %q", cfgVersionPath, out.versionPath)
	}
	if out.uidMode == "" {
		out.uidMode = uidModeHash
	}
	if out.uidMode != uidModeHash && out.uidMode != uidModeRaw {
		return config{}, fmt.Errorf("tak: %s must be %q or %q, got %q", cfgCoTUIDMode, uidModeHash, uidModeRaw, out.uidMode)
	}
	if out.requestTimeout <= 0 {
		out.requestTimeout = defaultRequestTimeout
	}
	if out.rateLimitEPS <= 0 {
		return config{}, fmt.Errorf("tak: %s must be > 0, got %d", cfgCoTRateLimit, out.rateLimitEPS)
	}
	if out.maxTCPConns <= 0 {
		return config{}, fmt.Errorf("tak: %s must be > 0, got %d", cfgCoTMaxTCPConns, out.maxTCPConns)
	}
	out.limits = out.limits.withDefaults()

	if out.serverURL != "" {
		u, err := url.Parse(out.serverURL)
		if err != nil {
			return config{}, fmt.Errorf("tak: %s is not a URL: %w", cfgServerURL, err)
		}
		// Deny-closed: TAK Server posture over plaintext would ship credentials and
		// inventory in the clear. There is no opt-out flag on purpose.
		if u.Scheme != "https" {
			return config{}, fmt.Errorf("tak: %s must be https (got %q)", cfgServerURL, u.Scheme)
		}
		if u.Host == "" {
			return config{}, fmt.Errorf("tak: %s has no host", cfgServerURL)
		}
		if out.posture && (strings.TrimSpace(out.clientCertPEM) == "" || strings.TrimSpace(out.clientKeyPEM) == "") {
			return config{}, ErrPostureUnauthenticated
		}
	}

	for key, addr := range map[string]string{cfgCoTUDPListen: out.udpListen, cfgCoTTCPListen: out.tcpListen} {
		if addr == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return config{}, fmt.Errorf("tak: %s is not a host:port address: %w", key, err)
		}
		// Deny-closed at CONFIG time, before runListeners binds anything: an
		// operator who configured an off-host CoT bearer must be told at startup,
		// not discover it from a packet capture. The bearers enforce the same
		// decision again at the socket itself (listener.go).
		if err := netbind.Check(addr, out.bindPolicy(key)); err != nil {
			return config{}, fmt.Errorf("tak: %w", err)
		}
	}
	if out.multicast != "" {
		if out.udpListen == "" {
			return config{}, fmt.Errorf("tak: %s requires %s", cfgCoTMulticast, cfgCoTUDPListen)
		}
		ip := net.ParseIP(out.multicast)
		if ip == nil || !ip.IsMulticast() {
			return config{}, fmt.Errorf("tak: %s %q is not a multicast IP", cfgCoTMulticast, out.multicast)
		}
		// A group join is off-host BY CONSTRUCTION — the point is to receive what
		// other hosts send — so it needs the declaration even when the listen
		// address is loopback. Without this, "127.0.0.1:6969 + group 239.2.3.1"
		// would slip an off-host receiver past a loopback classification.
		if !out.allowPublicBind {
			return config{}, fmt.Errorf("tak: %s %q joins a multicast group, which receives from OTHER HOSTS by design: CoT arrives with no transport protection and anyone on that group can inject forged position reports. Declare it with %s=true, or remove %s to ingest only what this host receives directly",
				cfgCoTMulticast, out.multicast, cfgAllowPublicBind, cfgCoTMulticast)
		}
	}
	return out, nil
}

// bindPolicy describes one CoT bearer to the single admission point. CoT is
// carried in the clear on both bearers — listener.go frames bytes and never
// negotiates TLS — so every bind is governed by that fact.
func (c config) bindPolicy(cfgKey string) netbind.Policy {
	purpose := "CoT UDP bearer"
	if cfgKey == cfgCoTTCPListen {
		purpose = "CoT TCP bearer"
	}
	return netbind.Policy{
		Component:   "tak",
		Purpose:     purpose,
		AllowPublic: c.allowPublicBind,
		OptIn:       cfgAllowPublicBind,
	}
}
