# `cernbox` — command line client design

## 1. Context and goals

CERNBox is Reva plus the plugin set in the `cernbox` GitHub organisation. Today an end user reaches it through the web UI, the Nextcloud desktop sync client, or — on lxplus — indirectly via the EOS FUSE mount. There is no first-class command line client. `reva-cli` (`cmd/reva`) exists but is an operator/debug tool: it speaks CS3APIs gRPC straight to the gateway, defaults to an interactive REPL, and exposes CS3 concepts rather than user concepts.

This document designs `cernbox`, a user-facing CLI.

### Goals

- A user logged into lxplus, who already has a Kerberos TGT from their session, can run `cernbox ls /eos/user/g/gdelmont` and it just works — no login step, no configuration, no secrets on disk. This is the "like the `eos` command" requirement.
- The same binary works from a laptop, from a CERN service account, and from a batch job, degrading gracefully to other credentials when Kerberos is unavailable.
- Full coverage of what a user can do in the web UI: browse, transfer, share, link, trash, versions, spaces, locks.
- Scriptable: stable exit codes, `--output json`, no interactive prompts unless a TTY is attached.
- Efficient for large data: resumable chunked uploads, parallel transfers, server-side archiving for recursive downloads.

### Non-goals

- Replacing the EOS FUSE mount for POSIX-style access. `cernbox` is a protocol client, not a filesystem.
- Operator/admin functions. Those stay in `reva-cli` and `cernboxcop`.
- A general-purpose WebDAV client. It targets CERNBox specifically and uses CERNBox-specific APIs (ocgraph, archiver, capabilities) where they are better than plain WebDAV.

### Settled decisions

| Decision | Choice |
| --- | --- |
| API surface | Public HTTPS: ocdav (WebDAV/TUS), ocgraph, OCS, datagateway, archiver |
| Kerberos | Phase 1 via CERN SSO OIDC (no server change); Phase 2 native SPNEGO in Reva |
| Packaging | New `cernbox-cli` repository in the `cernbox` organisation |
| Path model | Absolute CS3 paths canonical, `space:` prefixes as an accepted alias |

## 2. API surface the CLI binds to

Everything the CLI needs is already exposed over HTTPS by the CERNBox frontend. No new user-facing endpoint is required for phase 1.

| Concern | Endpoint | Notes |
| --- | --- | --- |
| Capability discovery | `GET /ocs/v1.php/cloud/capabilities` | Advertises TUS support and `max_chunk_size`, checksum algorithms, archiver URL and formats, spaces/projects flags. Defined in [capabilities.go](https://github.com/cs3org/reva/blob/master/internal/http/services/owncloud/ocs/data/capabilities.go) |
| Namespace browse, path-addressed | `PROPFIND /remote.php/dav/files/{user}/{cs3-path}` | The bare root already lists the top-level CS3 roots (`eos`, `winspaces`) plus a `home` alias — see [dav_files_roots.go:36](https://github.com/cs3org/reva/blob/master/internal/http/services/owncloud/ocdav/dav_files_roots.go#L36) |
| Namespace browse, id-addressed | `PROPFIND /remote.php/dav/spaces/{space-id}/{rel}` | Used when the CLI holds a space ID rather than a path |
| File I/O | `GET`/`PUT`/`DELETE`/`MKCOL`/`MOVE`/`COPY` on the same prefixes | Data flows through the datagateway |
| Resumable upload | TUS `POST` on the dav prefix | [tus.go:44](https://github.com/cs3org/reva/blob/master/internal/http/services/owncloud/ocdav/tus.go#L44) |
| Search / filter | `REPORT` with `search-files` / `filter-files` | [report.go:41](https://github.com/cs3org/reva/blob/master/internal/http/services/owncloud/ocdav/report.go#L41) |
| Trash | `/remote.php/dav/trash-bin/...` | |
| Versions | `/remote.php/dav/meta/{id}/v` | |
| Spaces | `GET /graph/v1beta1/me/drives` | [ocgraph.go:115](https://github.com/cs3org/reva/blob/master/internal/http/services/owncloud/ocgraph/ocgraph.go#L115) |
| Shares | `POST /graph/v1beta1/drives/{space}/items/{id}/invite`, `GET|PATCH|DELETE .../permissions` | [ocgraph.go:123](https://github.com/cs3org/reva/blob/master/internal/http/services/owncloud/ocgraph/ocgraph.go#L123) |
| Public links | `POST .../createLink`, `POST .../permissions/{id}/setPassword` | |
| Received shares | `GET /graph/v1beta1/me/drive/sharedWithMe`, `PATCH .../items/{id}` | |
| Identity | `GET /graph/v1.0/me` | |
| Recursive download | archiver service, URL and formats from capabilities | One request for a whole tree |
| App tokens | OCS connected-clients API | Backed by [appauth](https://github.com/cs3org/reva/blob/master/pkg/auth/manager/appauth/appauth.go) |

Locks (`LOCK`/`UNLOCK`) are available on ocdav but are lower priority for a CLI.

The CLI probes capabilities once per endpoint and caches the result, so it adapts to what a given CERNBox deployment actually supports rather than hard-coding assumptions.

## 3. Authentication

This is the core of the design. Reva has no Kerberos support anywhere today — the only matches for `krb5` in the tree are in the EOS binary client, where Reva shells out to `eos` with a user's credential cache. So Kerberos for the CLI is greenfield, and the question is where the krb5 machinery lives.

### 3.1 Credential chain

The CLI resolves credentials through an ordered chain, first success wins. Each provider yields a bearer credential plus an expiry.

1. **Explicit token** — `--token`, or `$CERNBOX_TOKEN`. Escape hatch and CI mechanism.
2. **Cached session** — a previously obtained, still-valid token from the local token cache (§3.4).
3. **Kerberos** — a TGT is present (`KRB5CCNAME` or the default ccache) and usable. This is the lxplus path and the one that must be silent. The ticket is presented straight to CERNBox, which verifies it against its own keytab (§3.3); exchanging it for an SSO token first (§3.2) is the fallback for deployments without the Kerberos auth provider.
4. **App token** — a CERNBox app password from `$CERNBOX_APP_TOKEN` or a file referenced by `--app-token-file`. For batch jobs and cron where no ticket is forwarded.
5. **OIDC device flow** — prints a URL and a user code, polls for completion. For laptops and non-CERN accounts. This is the same shape as the existing Nextcloud-client enrolment flow in [loginflow.go](https://github.com/cs3org/reva/blob/master/internal/http/services/loginflow/loginflow.go), and should reuse that server side rather than adding a second flow.

`cernbox login` forces a specific provider (`cernbox login --method kerberos|device|app-token`) and is only needed when the automatic chain picks the wrong thing. `cernbox status` prints which provider produced the current credential, the principal or subject it maps to, and when it expires. `cernbox logout` clears the cache.

### 3.2 Phase 1 — Kerberos via CERN SSO

The CLI never sends Kerberos material to CERNBox. It uses the TGT to authenticate to CERN SSO and obtains an OIDC access token audience-restricted to CERNBox, then presents it as `Authorization: Bearer <token>`. Reva's existing [oidc auth manager](https://github.com/cs3org/reva/blob/master/pkg/auth/manager/oidc/oidc.go) validates it against the configured issuer and maps claims to a user, exactly as it does for the web UI.

```
klist                      →  TGT for gdelmont@CERN.CH
SPNEGO to auth.cern.ch     →  authorization code
token endpoint + PKCE      →  access token (+ refresh token)
Authorization: Bearer ...  →  CERNBox
```

Concretely: a `GET` of the SSO authorization endpoint carrying `Authorization: Negotiate <AP-REQ>` for `HTTP/auth.cern.ch@CERN.CH`, redirect followed to extract the code, then a PKCE code exchange at the token endpoint. This is mechanically what `auth-get-sso-token` does on lxplus.

**Implement this natively in Go, do not shell out to `auth-get-sso-token`.** Three reasons: the binary must work off lxplus where that RPM is absent; shelling out makes error handling and token lifetime management opaque; and the SPNEGO client code written here is the same code phase 2 needs, so it is not wasted work. Use `github.com/jcmturner/gokrb5/v8` — it reads `/etc/krb5.conf` and the ccache directly, with no cgo and no MIT libs.

Prerequisites, all of which are registration work rather than code:

- A public OIDC client registered in CERN SSO for the CLI (`cernbox-cli`), with PKCE required, no client secret, and permission to obtain tokens targeting the CERNBox application.
- Kerberos authentication enabled for that client's flow in the SSO realm.
- The CERNBox `oidc` auth manager configured to accept the resulting audience.

Access tokens from CERN SSO are short-lived (minutes). The CLI requests `offline_access` so it holds a refresh token, and refreshes transparently when the access token is within 60s of expiry. If the refresh token is also expired and a TGT is still present, it silently re-runs the whole flow — the user never sees an auth error on lxplus.

**Why this is the right phase 1:** zero server-side change, so the CLI can ship against production CERNBox today. It inherits SSO account policy, 2FA, lockout, and central audit for free. It also covers accounts that have no Kerberos principal at all (lightweight and external accounts) through the same code path via the device flow.

### 3.3 Phase 2 — native SPNEGO in Reva

Phase 1 has one structural weakness: CERNBox authentication for CLI users becomes hard-dependent on `auth.cern.ch`. If SSO is degraded, `cernbox` stops working even though EOS, which validates Kerberos itself, keeps working. Phase 2 removes that dependency and makes Reva a first-class Kerberos service principal.

Four pieces, all small and all generically useful upstream — none of this is CERN-specific, so it belongs in Reva proper rather than reva-plugins.

**1. Auth manager** — `pkg/auth/manager/kerberos/kerberos.go`, implementing `auth.Manager`:

```go
// Authenticate validates a base64-encoded SPNEGO token presented as
// clientSecret against the service keytab, and maps the client principal
// to a CERNBox user. clientID is ignored: the principal is authoritative.
func (m *manager) Authenticate(ctx context.Context, clientID, clientSecret string) (*user.User, map[string]*authpb.Scope, error)
```

It validates the AP-REQ with gokrb5's service-side SPNEGO, strips the realm from the client principal, resolves the user through the gateway with `GetUserByClaim{Claim: "username"}` — the same pattern [appauth.go:69](https://github.com/cs3org/reva/blob/master/pkg/auth/manager/appauth/appauth.go#L69) already uses — and returns owner scope. Registered in [pkg/auth/manager/loader/loader.go](https://github.com/cs3org/reva/blob/master/pkg/auth/manager/loader/loader.go).

**2. HTTP credential strategy** — `internal/http/interceptors/auth/credential/strategy/spnego/spnego.go`, implementing `auth.CredentialStrategy`: `GetCredentials` parses `Authorization: Negotiate <b64>` into `Credentials{Type: "kerberos", ClientSecret: <b64>}`; `AddWWWAuthenticate` emits `WWW-Authenticate: Negotiate`. Registered in the credential loader and added to the frontend's `credential_chain`.

**3. Registry and service config:**

```toml
[grpc.services.authregistry.drivers.static.rules]
basic        = "localhost:9142"
bearer       = "localhost:9142"
kerberos     = "localhost:9155"
publicshares = "localhost:9142"

# a dedicated authprovider instance, since one instance serves one driver
[grpc.services.authprovider]
auth_manager = "kerberos"

[grpc.services.authprovider.auth_managers.kerberos]
keytab                 = "/etc/cernbox/krb5.keytab"
service_principal      = "HTTP/cernbox.cern.ch"
realm                  = "CERN.CH"
user_claim             = "username"   # which user field the principal names
max_clock_skew_seconds = 300

[http.middlewares.auth]
credential_chain = ["spnego", "basic", "bearer", "publicshares"]
```

**4. CLI provider** — the Kerberos chain entry gains a second mode: `GET https://cernbox.cern.ch/auth/kerberos` with `Authorization: Negotiate <AP-REQ>`, reading the Reva JWT back from the `x-access-token` response header written by the existing token writer. Which mode is used is a config toggle (`auth.kerberos.mode = sso|spnego|auto`).

**This is what shipped, and `spnego` is the default.** The original plan was to default to `auto` and treat SSO as the safe fallback, on the reasoning that the server side did not exist yet. It does now — all four pieces above are implemented and tested against a real KDC — and with them in place the argument reverses: `auto` makes a successful login depend on either CERNBox or auth.cern.ch being reachable, which is two things that can be down instead of one. Native Kerberos keeps the identity path as short as it can be, so `spnego` is the default and `sso` is what a deployment without the auth provider selects explicitly.

#### Design constraints worth stating explicitly

**Mutual authentication is not supported, by construction.** Reva's HTTP frontend does not hold the keytab — the AP-REQ is forwarded over gRPC to the authprovider service, which is a different process. A SPNEGO mutual-auth response token produced during `gss_accept_sec_context` there has no path back into the HTTP response, because the CS3 `Authenticate` RPC returns a token and a user, with no field for a negotiation response. The CLI therefore must not set `GSS_C_MUTUAL_FLAG`. This is acceptable: the channel is TLS with a validated server certificate, which is how the large majority of SPNEGO-over-HTTPS deployments run. Changing this would mean either shipping the keytab to every frontend or extending the CS3 auth API, and neither is justified.

**Replay cache is per-process.** gokrb5's service-side replay cache is in-memory and local. With multiple authprovider replicas behind a load balancer, an attacker who can capture an AP-REQ could replay it against a different replica. Under TLS the AP-REQ is not observable on the wire, so the practical exposure is low, but it should be a documented limitation, and a shared (Redis-backed) replay cache is the mitigation if it ever matters.

**SPN on a load-balanced alias.** `cernbox.cern.ch` fronts several nodes. The keytab must contain the SPN for the alias, and clients must not canonicalise the hostname via reverse DNS — `rdns = false` in `krb5.conf` — or they will request a ticket for the node name and fail. The CLI should detect this failure mode specifically and print an actionable message rather than a raw GSS error, because it is the single most likely deployment problem.

**Scopes.** A Kerberos-authenticated user gets owner scope, like a password login. Ticket lifetime does not constrain the issued Reva token; the Reva JWT's own TTL applies.

### 3.4 Token cache

Tokens are cached so that a shell loop does not re-authenticate on every invocation.

Default location is `/tmp/cernbox_cc_$(id -u)`, mode `0600`, overridable with `$CERNBOX_TOKEN_CACHE`. **Not** the home directory: on lxplus, home is a network filesystem shared across every node in the cluster, and a long-lived bearer token sitting there has a materially different exposure profile from one in node-local `/tmp`. This deliberately mirrors where `KRB5CCNAME` points, so the token's blast radius matches the ticket's.

Each cache entry is keyed by `(endpoint, principal-or-subject)`. A user who does `kinit` as a different principal gets a different entry rather than silently reusing the previous identity's token — a real failure mode for anyone with a service principal alongside their personal one. Entries store the access token, refresh token if any, expiry, and the provider that produced them. Expired entries are pruned on write.

### 3.5 Batch and automation

- **HTCondor with ticket forwarding**: works with no configuration — the chain finds the forwarded ccache.
- **No ticket available**: create a scoped app token, `cernbox token create --path /eos/project/x --permission read --expiry 2026-12-31`, and expose it via `$CERNBOX_APP_TOKEN`. Reva already supports path- and share-scoped app tokens — see `getPathScope` in [app-tokens-create.go:199](https://github.com/cs3org/reva/blob/master/cmd/reva/app-tokens-create.go#L199) — so these can be least-privilege rather than full-account credentials, and the CLI should make the scoped form the documented default.
- **Service accounts**: a keytab plus `kinit -kt` before invoking, or an app token. Both work unchanged.

## 4. Path and namespace model

Absolute CS3 paths are canonical, matching what the dav files root already exposes and what users already type for `eos`:

```
/eos/user/g/gdelmont/Documents
/eos/project/c/cernbox/data
/home/Documents                    # alias for the caller's own home space
```

Space-qualified aliases are accepted and resolve through the locally cached `me/drives` listing:

```
home:Documents
project/cernbox:data
<space-id>:relative/path           # for scripts holding an ID
```

Resolution strategy: path-addressed operations go to `/remote.php/dav/files/{user}/{path}`, which accepts absolute CS3 paths at CERN directly. Id-addressed operations — anything where the CLI already holds a drive ID from ocgraph, which is all of sharing — go to `/remote.php/dav/spaces/{space-id}/{rel}`. The spaces listing is cached locally with a short TTL and refreshed on a resolution miss.

### The lxplus ambiguity, and how transfer commands resolve it

On lxplus, `/eos/user/g/gdelmont` is *both* a valid CERNBox remote path and a real local FUSE mount point. `cernbox cp /eos/user/g/gdelmont/a.txt /eos/user/g/gdelmont/b.txt` is genuinely ambiguous, and guessing would be worse than either answer. So:

- **Namespace commands** — `ls`, `stat`, `find`, `du`, `mkdir`, `rm`, `mv`, `touch`, `cat`, `share`, `link`, `trash`, `versions` — take bare remote paths. There is no local side, so there is no ambiguity.
- **Transfer commands** — `cp`, `sync` — require the remote side to carry a `cb:` prefix:

  ```bash
  cernbox cp ./report.pdf cb:/eos/user/g/gdelmont/Documents/
  cernbox cp -r cb:/eos/project/c/cernbox/data ./data
  ```

- **`get` and `put`** are unambiguous by position and are what most users will actually type:

  ```bash
  cernbox put ./report.pdf /eos/user/g/gdelmont/Documents/
  cernbox get /eos/user/g/gdelmont/Documents/report.pdf .
  ```

`cb:` is accepted everywhere a remote path is, so a script can be explicit throughout if it prefers.

## 5. Command surface

```
cernbox login [--method kerberos|device|app-token]
cernbox logout
cernbox status                         # provider, identity, expiry, endpoint
cernbox whoami [--output json]

cernbox ls [-l] [-r] [--all] PATH
cernbox stat PATH
cernbox find PATH --name PATTERN [--mtime ...] [--size ...]
cernbox du [-h] [--depth N] PATH
cernbox cat PATH
cernbox mkdir [-p] PATH
cernbox touch PATH
cernbox rm [-r] [-f] PATH...
cernbox mv SRC DST
cernbox cp [-r] SRC DST                # cb: prefix marks the remote side
cernbox get [-r] REMOTE [LOCAL]
cernbox put [-r] LOCAL REMOTE
cernbox sync LOCAL cb:REMOTE [--delete] [--dry-run]

cernbox space list [--type personal|project]
cernbox space info SPACE

cernbox share create PATH --with USER|GROUP --role viewer|editor|collab [--expiry DATE]
cernbox share list [PATH]
cernbox share received [--accept ID] [--decline ID]
cernbox share update ID --role ...
cernbox share remove ID

cernbox link create PATH [--role viewer|editor] [--password] [--expiry DATE]
cernbox link list [PATH]
cernbox link update ID ...
cernbox link remove ID

cernbox trash list [PATH]
cernbox trash restore ID [--to PATH]
cernbox trash purge [ID | --all]

cernbox versions list PATH
cernbox versions restore PATH VERSION
cernbox versions download PATH VERSION

cernbox lock PATH / cernbox unlock PATH

cernbox token create [--path PATH --permission read|write] [--expiry DATE] [--label L]
cernbox token list
cernbox token revoke ID

cernbox open PATH [--app APP] [--print-url]
cernbox config get|set|list
cernbox completion bash|zsh|fish
cernbox version
```

Naming follows coreutils and `eos` where an equivalent exists, and `git`-style noun-verb grouping where it does not. Every command accepts `--output table|json|csv`, `-q`, and `--endpoint`.

Deliberately **not** a REPL. `reva-cli` opens an interactive prompt when given no arguments, which suits a debugging tool but is wrong for something users will pipe, script, and put in Makefiles. `cernbox` with no arguments prints help.

## 6. Client architecture

New repository `cernbox/cernbox-cli`, importing `github.com/cs3org/reva/v3` for shared types and the CERN plugin set where needed.

```
cmd/cernbox/main.go
internal/cli/            one file per command group; cobra
pkg/client/
    capabilities.go      discovery + cache
    dav.go               PROPFIND/GET/PUT/MOVE/COPY/DELETE/MKCOL/REPORT/LOCK
    tus.go               resumable chunked upload
    graph.go             spaces, shares, links, identity
    ocs.go               capabilities, app tokens
    archiver.go          recursive download
pkg/auth/
    chain.go             provider ordering and fallback
    kerberos_sso.go      TGT → SSO → OIDC access token
    kerberos_spnego.go   TGT → Reva JWT (phase 2)
    device.go            OIDC device flow
    apptoken.go
    cache.go             token cache, keyed by (endpoint, principal)
pkg/pathspec/            path parsing, cb: handling, space resolution
pkg/transfer/            parallel engine, recursion, checksums, resume, progress
pkg/output/              table / json / csv renderers
```

`cobra` for commands, `gokrb5/v8` for Kerberos, `coreos/go-oidc` for discovery and validation. All pure Go — the binary stays statically linkable, which matters for distributing one artifact across AlmaLinux versions.

### Configuration precedence

Flags, then `$CERNBOX_*` environment variables, then `$XDG_CONFIG_HOME/cernbox/config.yaml`, then `/etc/cernbox/config.yaml`.

The site-wide file is what makes the lxplus experience zero-config: the RPM ships `/etc/cernbox/config.yaml` with the endpoint and SSO client already set, so a user runs `cernbox ls` on a fresh login and it works. Users override per-account only if they need a different instance.

## 7. Transfers

**Upload.** TUS when capabilities advertise it, honouring the advertised `max_chunk_size`, with resume across invocations — the upload URL and offset are persisted so an interrupted 50 GB upload continues rather than restarts. Plain `PUT` for small files below one chunk, avoiding the two extra round trips. Checksums are computed during upload and sent so the server verifies rather than trusting the client.

**Download.** Single files stream via `GET` through the datagateway with Range-based resume. Recursive downloads use the archiver service (URL and supported formats come from capabilities) so an entire tree is one request producing one tar or zip stream, rather than a PROPFIND walk plus N round trips. Fall back to the walk if the archiver is unavailable or the caller passes `--no-archive`.

**Parallelism.** `--jobs N`, default a small multiple of CPU count, capped. Per-file retry with exponential backoff on 5xx and connection resets. `--dry-run` on every mutating command. Progress rendering only when stderr is a TTY.

## 8. Output and scripting

`--output json` emits one JSON document, or newline-delimited JSON under `--output json --stream` for large listings. Human table output is aligned and colourised only on a TTY.

Exit codes: `0` success, `1` generic failure, `2` usage error, `3` authentication failure, `4` permission denied, `5` not found, `6` conflict (lock, etag mismatch, quota). Distinguishing 3 from 4 matters for scripts that should retry after `kinit` versus ones that should give up.

Errors are mapped from CS3 status codes and HTTP status to human sentences, with `--debug` exposing the underlying status, trace ID, and request. Reva already returns a support trace in its error status — surfacing it is what makes a user's bug report actionable.

## 9. Distribution

- RPM for AlmaLinux 9/10, in the CERN repositories, installed by default on lxplus so the "log in and it works" story holds.
- Static binaries for Linux/macOS, amd64/arm64, attached to GitHub releases, for laptops.
- Container image for CI use.
- Shell completions installed by the RPM.
- Version and build metadata via `-ldflags`, reported by `cernbox version` and sent in the `User-Agent` so server-side telemetry can see client version distribution — useful when a deprecation needs to be timed.

## 10. Security considerations

- Tokens land in node-local `/tmp` at mode `0600`, never in the network home directory by default (§3.4).
- The SSO access token is audience-restricted to CERNBox, so a leaked CLI token does not grant access to other CERN applications.
- App tokens are created scoped by default; the unscoped form requires an explicit `--all`.
- `--insecure` and `--skip-verify` exist for dev instances, print a warning to stderr on every use, and are refused when the endpoint is a `cern.ch` host.
- No credential is ever passed as a command line argument in a way that would appear in `ps` output or shell history; `--password` prompts rather than accepting a value.
- Phase 2 caveats — no mutual authentication, per-process replay cache — are documented, not silently assumed away (§3.3).

## 11. Testing

- Unit tests for path parsing, `cb:` disambiguation, space resolution, credential chain ordering and fallback, and token cache keying. The chain is the part most likely to regress subtly, and the part whose failure is most visible to users.
- Integration tests against a containerised Reva, reusing the fixtures under `tests/`.
- A containerised MIT KDC for the phase 2 SPNEGO tests, covering: valid ticket, expired ticket, wrong SPN, alias-versus-node-name mismatch, missing keytab entry. The alias case is the one that will actually bite in production and must be a test, not a runbook note.
- Transfer tests covering interrupt-and-resume for both upload and download, and checksum mismatch handling.
- A smoke suite runnable against pre-production CERNBox from CI.

## 12. Phasing

| Phase | Content |
| --- | --- |
| **P0** | Repo, cobra skeleton, config, capabilities discovery, credential chain with Kerberos-via-SSO plus device flow, token cache, `ls stat get put rm mkdir mv whoami status login logout` |
| **P1** | TUS resumable upload, archiver recursive download, parallel transfer engine, `cp find du cat touch`, JSON output, completions, RPM |
| **P2** | `share link trash versions space token open`, received-share handling |
| **P3** | Native SPNEGO: Reva auth manager, credential strategy, registry config, CLI `spnego` mode with SSO fallback; upstream the Reva side |
| **P4** | `sync`, lock/unlock, OCM |

P0 through P2 require no Reva change at all and can ship against production CERNBox. Only P3 touches the server.

## 13. Open questions

1. **SSO client registration** — who owns registering `cernbox-cli` as a public client, and does the CERNBox `oidc` auth manager need an audience-list change to accept it? This blocks P0 and is registration work, not code.
2. **Kerberos for non-CERN accounts** — lightweight and external accounts have no principal. The device flow covers them, but should `cernbox` advertise that in its error message when Kerberos is unavailable, or stay silent and just fall through?
3. **Archiver limits** — is there a size or file-count ceiling on the archiver service that would make recursive download fall back to walking, and can the CLI discover it rather than hard-coding a threshold?
4. **Namespace roots** — the dav files root currently lists `eos` and `winspaces`. Does the heterogeneous-spaces work change what a CLI should present at `/`, and should `cernbox ls /` show CS3 roots or spaces?
5. **`sync` semantics** — one-way mirror only, or bidirectional? Bidirectional needs conflict resolution and local state, which is a substantially larger project and arguably belongs to the desktop client rather than the CLI.
6. **Reva JWT TTL** — the default expiry from the jwt token manager determines how often the CLI re-authenticates in phase 2. Worth confirming it is long enough that a shell loop is not re-authenticating constantly, and short enough that a leaked cache file expires quickly.
