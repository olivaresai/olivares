---
title: Reach the console outside your LAN
description: Check the console's local exposure facts and use a private tailnet or named tunnel for remote access.
---

Open **Settings → Sign-in and security → Console access** as a system administrator.
The card reports the HTTP/HTTPS and gRPC listeners, local addresses, transport,
public URL and administrative step-up policy. `olivares doctor` adds a
`console-exposure` check from the saved startup record. With a saved CLI login
for this local engine, it also reads the current policy; otherwise it reports
the policy as `not read`. The authenticated API is `GET /v1/console/exposure`.

**External reachability is not verified.** A listener or declared URL does not
prove that a phone outside your network can reach the service. Compare the
reported listener addresses with the host's current sockets:

```sh
ss -ltn
```

Before saving a managed public URL or enabling a managed public tunnel, enable
an authenticator code or passkey under **Administrative step-up**. A policy of
`none` is refused. Step-up cannot be lowered to `none` while managed public
exposure remains recorded. Existing initial startup and its protected setup
journey keep working.

Choose the final HTTPS hostname before enrolling passkeys: passkeys are bound
to the relying-party hostname and may not work after that hostname changes.
Use an authenticator code for administrative step-up while configuring a new
address, then enroll a passkey from the final address.

## Private access through a tailnet

1. Install and sign in to a Tailscale client on the server and phone using your
   own tailnet account.
2. Configure [Tailscale Serve](https://tailscale.com/docs/reference/tailscale-cli/serve)
   to proxy the engine's local listener. Use a trusted HTTPS origin and validate
   its certificate; Serve's HTTPS hostname is the address the
   phone opens. Serve is private to your tailnet; Funnel makes a service public.
3. Check the route with `tailscale serve status`. Save the HTTPS address in
   **Console access → Public URL** if you want Olivares to advertise that address.
   Saving it records the address; restart the engine to apply it. Saving does
   not configure Tailscale. An explicit `--public-url` or `OLIVARES_PUBLIC_URL`
   takes precedence over the stored address.
4. On the phone, turn Wi-Fi off, keep Tailscale connected, and open that address.
   Sign in and start a session to check that live activity continues streaming.

The browser must trust the HTTPS certificate. On a supported mobile browser,
use **Install app** or **Add to Home Screen** to install the console PWA.

## Public access through a named Cloudflare Tunnel

Create a remotely managed named tunnel in your Cloudflare account, with a stable
HTTPS hostname routed to the engine's actual local listener. Configure origin
certificate trust in Cloudflare for that listener. Use a named tunnel: Cloudflare's
[Quick Tunnels do not support Server-Sent Events](https://developers.cloudflare.com/tunnel/get-started/quick-tunnels/),
which the console uses for live activity.

On an Olivares Server with the optional app helper installed:

1. Provision the tunnel token through a protected host session or secret manager
   into `/var/lib/olivares-apps/cloudflared/tunnel-token`. The directory must be
   root-owned with mode `0700`; the token must be a nonempty, regular, root-owned
   file with mode `0600`. Keep the token out of commands, environment variables,
   screenshots and logs.
2. Provision `/var/lib/olivares-apps/cloudflared/origin-ca.pem` in the same
   protected host session. Use the engine's public `tls.crt` for its default
   self-signed certificate, or the issuing CA bundle for a CA-signed certificate.
   This must also be a nonempty, regular, root-owned file with mode `0600`,
   not a symlink. Never copy or expose `tls.key`.
   The app mounts this public trust bundle read-only at
   `/run/certs/origin-ca.pem`. In the tunnel route's **Additional application
   settings → TLS**, set **CA Pool** to that mounted path and **Origin Server
   Name** to a DNS name or IP address in the engine certificate. Keep **Disable
   TLS certificate verification** off (`noTLSVerify=false`). See Cloudflare's
   [origin TLS parameters](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/configure-tunnels/origin-parameters/).
3. Enable administrative step-up, then use the **cloudflared** app's install or
   start action. Its existing app lifecycle mounts the token read-only and passes
   the file path to `cloudflared`; it does not pass the token in arguments.
4. Save the tunnel's HTTPS hostname in **Console access → Public URL**. Restart
   the engine when the save response requires it.
5. Open the hostname from the phone on mobile data, sign in, and confirm live
   session activity before installing the PWA.

Cloudflare Tunnel [establishes outbound connections](https://developers.cloudflare.com/tunnel/concepts/).
The optional app publishes no inbound host ports. Check the host firewall before
and after starting it:

```sh
sudo nft list ruleset
```

No WAN ingress rule is needed for the tunnel. Existing rules are not removed
automatically. Stopping or uninstalling the app retains its token and public
trust bundle; neither
container state nor a saved URL proves that the external route is working.

To remove the managed public URL, clear its field, save and restart. An explicit
startup public URL remains in effect until you change that startup option.
Removing the stored address does not stop an independently configured tailnet,
tunnel or reverse proxy; disable that route
through its own lifecycle as well.

Managed exposure registrations keep administrative step-up required after an app
is stopped or uninstalled, or a previously saved public URL is cleared or replaced.
Running nodes may still use the earlier address until restart. Protection stays
in place until a native stop or restart attestation can safely release it; clearing
a field alone does not attest that an external route stopped.
