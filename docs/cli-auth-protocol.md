# CLI browser sign-in: what the console must implement

`pantech auth login` signs the CLI in through the console, the way `gh auth login`
does: the person approves the CLI in their browser, the console sends a one-time
code to a loopback server the CLI runs, and the CLI trades that code, with a PKCE
verifier, for a new API key. The CLI side is `internal/auth/browser.go`. The console
side does not exist yet; this is its specification.

The CLI always uses `https://console.pantechdynamics.com`. Until the routes below
exist, it detects the `404` and tells the person to use
`pantech auth login --with-token`.

## Overview

```
CLI                                   Browser / console                    Control API
 |  listen on 127.0.0.1:<port>          |                                      |
 |  verifier = random, challenge = S256(verifier), state = random              |
 |--- open GET /cli/authorize?port&state&challenge&host -->|                   |
 |                                      |  sign in if needed, show approval    |
 |                                      |  on Approve: create API key -------->|
 |                                      |  store {code -> key, challenge}      |
 |<-- 302 http://127.0.0.1:<port>/callback?state&code ----|                    |
 |--- POST /api/cli/token {code, code_verifier} -------->|                     |
 |<-- 200 {api_key, key_id, ...} -------------------------|                    |
 |--- GET /public/v1/me (Bearer api_key) ------------------------------------->|
 |  callback tab answered "You're signed in"                                   |
```

## 1. Probe: `GET /cli/authorize` (no query)

Before opening the browser, the CLI requests `GET <console>/cli/authorize` with no
query and without following redirects. **Only a `404` matters**: it means the
console has no CLI sign-in, and the CLI stops with a message pointing to
`--with-token`. Any other answer (200, a 302 to the sign-in page, a 400 for the
missing parameters) lets the sign-in go ahead. So the route must exist and must not
answer `404` when its parameters are missing; a `400` page is fine.

## 2. Authorize page: `GET /cli/authorize`

Opened in the person's browser:

```
https://console.pantechdynamics.com/cli/authorize?port=53682&state=<state>&challenge=<challenge>&host=<host>
```

| Parameter | Value | Console must |
| --- | --- | --- |
| `port` | The loopback port, `1024`–`65535` in practice (any free port the OS gave). | Accept an integer 1–65535. Only ever redirect to `http://127.0.0.1:<port>/callback`: never another host, never `localhost`, never https. |
| `state` | 32 characters, base64url without padding (24 random bytes). | Return it unchanged on the callback. Treat it as opaque. |
| `challenge` | `BASE64URL-NOPAD(SHA-256(code_verifier))`, 43 characters. The method is always S256; there is no `code_challenge_method` parameter. | Store it with the code. Reject a value that is not 43 base64url characters. |
| `host` | The machine's hostname, already reduced to `[A-Za-z0-9 .@_-]`, at most 60 characters. May be empty. | Use it only to name the key, e.g. `CLI on johns-mbp`. Escape it when shown. |

The page:

1. Requires a signed-in console session (redirect to sign-in and back, keeping the
   query).
2. Shows what is being approved: "The Pantech CLI on `<host>` wants an API key for
   `<organization>`", an organization picker if the person has several, and the
   scope. Creating keys follows the console's existing rules: an owner or admin with
   two-factor authentication on. Anyone else sees why they cannot approve and is
   told to ask an admin for a key (`--with-token`).
3. On **Approve**: creates the API key exactly as Organization › API keys does
   (`contracts/api-key.ts` `CreateApiKeyRequest`: a name such as `CLI on <host>`,
   the chosen scope, `read` or `write`, default `write`, and an expiry, default 90
   days), then creates a one-time **code** bound to `{key secret, key id,
   organization id and name, scopes, expires_at, challenge}` and redirects
   (`302`/`303`, or a page that navigates) to:

   ```
   http://127.0.0.1:<port>/callback?state=<state>&code=<code>
   ```

4. On **Cancel**: redirects to
   `http://127.0.0.1:<port>/callback?state=<state>&error=access_denied`. Nothing is
   created.

The code:

- at least 128 bits of randomness, base64url (no `+`, `/` or `=` to escape);
- valid for at most **5 minutes**, and usable **once**: the first exchange consumes
  it, successful or not;
- stored server-side only (never in a cookie or the URL of anything but the
  callback); the key secret it points at is held encrypted or in memory only, and
  erased when the code is consumed or expires. If a code expires unused, revoke the
  key it would have handed over.

The page must send `Cache-Control: no-store` and `Referrer-Policy: no-referrer`, so
neither the state nor the code leaks.

## 3. Callback: `GET http://127.0.0.1:<port>/callback` (served by the CLI)

The CLI's loopback server answers:

| Query | CLI does |
| --- | --- |
| `state` missing or different | `400`, ignores it. |
| `state` and `error=<anything>` | Shows "Sign-in cancelled", stops with "the sign-in was cancelled in the browser". |
| `state` and `code` | Exchanges the code (step 4), checks the key against `GET /public/v1/me`, then answers this request with "You're signed in" or "Sign-in failed". The tab waits up to 45 seconds for that answer. |

## 4. Exchange: `POST /api/cli/token`

```http
POST /api/cli/token HTTP/1.1
Host: console.pantechdynamics.com
Content-Type: application/json

{"code":"<code>","code_verifier":"<verifier>"}
```

`code_verifier` is 43 characters of base64url without padding (32 random bytes).
There are no cookies and no other authentication: the code and the verifier are the
authentication. The route must not require a console session or a CSRF token.

The console must:

1. Look the code up; if it is unknown, used or expired, refuse.
2. Mark it used (atomically, before anything else, so two requests cannot both
   succeed).
3. Check `BASE64URL-NOPAD(SHA-256(code_verifier)) == challenge` in constant time; if
   not, refuse (the code stays used).
4. Answer `200`, `Content-Type: application/json`, `Cache-Control: no-store`:

```json
{
  "api_key": "PAN_…",
  "key_id": "key_…",
  "organization_id": "org_…",
  "organization_name": "Acme Ltd",
  "scopes": ["write"],
  "expires_at": "2027-01-03T10:00:00Z",
  "api_url": "https://api.pantechdynamics.com"
}
```

| Field | Type | Notes |
| --- | --- | --- |
| `api_key` | string, required | The full secret, `PAN_…`. The CLI refuses an answer without it. |
| `key_id` | string, required | Shown to the person if the key later fails, so they can revoke it. |
| `organization_id` | string, required | |
| `organization_name` | string or null | Shown on sign-in and in `pantech auth status`. |
| `scopes` | string array | `["read"]` or `["write"]`. |
| `expires_at` | RFC 3339 string | |
| `api_url` | string | The public API base the key is for (no `/public/v1`). The CLI currently always uses production. |

Refusals: any non-`200` status, ideally `400`, with a problem document. The CLI
shows `detail` verbatim, so write it for a person:

```json
{"type":"about:blank","title":"Invalid sign-in code","status":400,"detail":"This sign-in code is invalid or has expired. Run pantech auth login again."}
```

A `404` from this route makes the CLI print the `--with-token` advice, so do not
use `404` for an unknown code.

## 5. After the exchange

The CLI calls `GET https://api.pantechdynamics.com/public/v1/me` with the key. If
that fails, it tells the person the key id so they can revoke it, and stores
nothing. If it works, it stores the key in the OS keychain (or a 0600 file) and the
organization in its profile.

## Security checklist for the console

- Redirect only to `http://127.0.0.1:<port>/callback`.
- The key secret never appears in a URL, a log, an analytics event or a page.
- Codes: single use, five minutes, constant-time challenge check, consumed even on a
  failed check.
- Rate-limit `POST /api/cli/token` per IP.
- An approved-but-never-exchanged key is revoked when its code expires.
- The created key appears under Organization › API keys like any other, so it can be
  revoked there; `pantech auth logout` only forgets it locally.
