# Read-only API tokens

A dashboard such as [Homepage](https://gethomepage.dev) can't log in to Drawbridge: it can send a
header, but it can't fill in a form, keep a cookie, or send the `X-Drawbridge` header. An API
token is a credential for that. It's read-only, it shows a few numbers, and you can revoke it.

## What a token can read

A token works on these `GET` routes and no others:

| Route | What it returns |
|---|---|
| `/api/server/status` | whether the tunnel is up, and how many clients there are, online, and paused |
| `/api/clients` and `/api/clients/{id}` | the clients, with their addresses, public keys, and when each last connected |
| `/api/clients/{id}/traffic` and `/sessions` | one client's traffic history and connections |
| `/api/traffic` and `/api/traffic/clients` | traffic history, in total and per client |

Everything else answers `403`, with no exceptions for a token that looks right. In particular a
token **can't** read a client's config (it holds the client's private key) or its DNS queries, the
event log, or the settings. It can't change anything, and it can't make, list, or revoke tokens.
Those routes need a login.

The client names, addresses, and endpoints in `/api/clients` are still information about your
household. Treat a token like a password you've written in another program's config file.

## Make one

1. Open the Account page (the person icon, then Settings) and find **API Tokens**.
2. Give the token a name, such as `Homepage`, so you know which to revoke later.
3. Type your password again. A token outlives the session that made it, and a password change,
   so a logged-in browser alone can't make one.
4. Press **Make Token**, and **copy the token now**. It looks like `dbt_` and 43 more characters.
   Drawbridge keeps only a hash of it, so it can't be shown again. If you lose it, revoke it and
   make another.

You can have 20 tokens, and each needs a different name.

## Try it

```bash
curl --cacert cert.pem -H 'Authorization: Bearer dbt_…' https://your-host:51821/api/server/status
```

```json
{"tunnel_up":true,"clients":4,"paused":1,"online":2}
```

(`--cacert` is explained below. `curl -k` skips the check, and is fine for a quick look.)

## Homepage

In `services.yaml`, under the service that should show it:

```yaml
widget:
  type: customapi
  url: https://your-host:51821/api/server/status
  headers:
    Authorization: Bearer dbt_…
  mappings:
    - field: online
      label: Online
    - field: clients
      label: Clients
    - field: paused
      label: Paused
```

The Account page shows this with the real address and the new token filled in, to copy.

## When Homepage can't reach it

Three things can stop it, and each gives a different message in Homepage's log.

**`403` and "only reachable from the home network and the VPN".** Drawbridge only answers
requests from the home network, the VPN, and any extra sources you've added, tokens included. A
Homepage in Docker on the same machine connects from Docker's own network, which isn't one of
them. Add it:

```bash
sudo drawbridge server set --admin-allow 172.17.0.0/16
```

(Use the subnet your Docker network has: `docker network inspect bridge`.)

**`DEPTH_ZERO_SELF_SIGNED_CERT`, or "self signed certificate".** Drawbridge makes its own
certificate, and Homepage, which runs on Node, doesn't trust it. Copy the certificate to where
Homepage can read it, and tell Node to trust it:

```bash
sudo cp /var/lib/drawbridge/tls/cert.pem /path/to/homepage/config/drawbridge.pem
```

and in Homepage's environment: `NODE_EXTRA_CA_CERTS=/app/config/drawbridge.pem` (the path inside
its container). This trusts that one certificate, and checks it. Drawbridge replaces the
certificate about 30 days before it expires, which is about every two years; copy it again then.

**`ERR_TLS_CERT_ALTNAME_INVALID`.** Node trusts the certificate, but the name in the `url` isn't
one the certificate was made for. It covers the host's name, that name with `.local`,
`localhost`, and the host's addresses when it was made:

```bash
openssl x509 -in /var/lib/drawbridge/tls/cert.pem -noout -ext subjectAltName
```

Use one of those in the `url`. A public name that your router resolves to the host, such as
`vpn.example.com`, isn't on the list.

As a last resort, `NODE_TLS_REJECT_UNAUTHORIZED=0` makes Node skip the check. It does so for
every request that Homepage makes, not just this one, so prefer the two steps above.

| What Homepage logs | Meaning |
|---|---|
| `401` "invalid API token" | The token is wrong, was revoked, or isn't there. Check for a stray space or line break in the file. |
| `403` "an API token can't use this endpoint" | The `url` is a route a token can't read. |
| `403` "only reachable from the home network and the VPN" | The address Homepage connects from isn't allowed (above). |

## Revoke, and replace

On the Account page, **Revoke** ends a token at once, and anything using it gets `401`. To replace
one: make the new token, put it in the dashboard, then revoke the old one.

Each token shows when it was last used, to the hour. A token that was never used, or hasn't been
for months, is one to revoke.

- Changing your password doesn't revoke tokens, so a routine change doesn't break your
  dashboards. If you changed it because someone else had access, revoke the tokens too.
- Resetting the password from the command line (`drawbridge admin reset-password`) revokes every
  token, along with every session. That's the "I've lost control, take it back" path.

The event log records a token being made, revoked, or refused a password, never the token.
