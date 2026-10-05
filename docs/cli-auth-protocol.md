# CLI browser sign-in

`pantech auth login` signs the CLI in through the console, the way `gh auth login`
does: the person approves the CLI in their browser, the console sends a one-time
code to a loopback server the CLI runs, and the CLI trades that code, with a PKCE
verifier, for a new API key. The key itself never appears in a URL.

This describes what the CLI and the console send each other. The CLI side is
`internal/auth/browser.go`; the console's is in pantech-console, which is
where its implementation is documented.

The CLI always uses `https://console.pantechdynamics.com`.

## Overview

```
CLI                                   Browser / console                     Control API
 |  listen on 127.0.0.1:<port>          |                                       |
 |  verifier = random, challenge = S256(verifier), state = random               |
 |--- open GET /cli/authorize?port&state&challenge&host -->|                    |
 |                                      |  request -> cookie, redirect to the   |
 |                                      |  bare /cli/authorize                  |
 |                                      |  log in (and back) if needed          |
 |                                      |  on Approve: create API key --------->|
 |                                      |  seal {key, challenge} into the code  |
 |<-- browser navigates to http://127.0.0.1:<port>/callback?state&code         |
 |--- POST /api/cli/token {code, code_verifier} -------->|                      |
 |<-- 200 {api_key, key_id, ..., api_url} ---------------|                      |
 |--- GET /public/v1/me (Bearer api_key) -------------------------------------->|
 |  callback tab answered "You're signed in"                                    |
```

## 1. Probe: `GET /cli/authorize` (no query)

Before opening the browser, the CLI requests `GET <console>/cli/authorize` with no
query and without following redirects. **Only a `404` matters**: it means the
console has no CLI sign-in, and the CLI stops with a message pointing to
`--with-token`. Anything else lets the sign-in go ahead. With no request waiting,
the console answers `200` with a "Start from your terminal" page.

## 2. Authorize: `GET /cli/authorize`

The CLI opens, in the person's browser:

```
https://console.pantechdynamics.com/cli/authorize?port=53682&state=<state>&challenge=<challenge>&host=<host>
```

| Parameter | CLI sends | Console accepts |
| --- | --- | --- |
| `port` | The loopback port the OS gave. | An integer, `1024`–`65535`. |
| `state` | 32 characters, base64url without padding (24 random bytes). | 16–128 base64url characters. Echoed back unchanged. |
| `challenge` | `BASE64URL-NOPAD(SHA-256(code_verifier))`. The method is always S256; there is no `code_challenge_method`. | Exactly 43 base64url characters. |
| `host` | The machine's hostname, reduced to `[A-Za-z0-9 .@_-]`, at most 60 characters. May be empty. | Trimmed, at most 60 of `[\w .@-]`. Used only to name the key. |

The console takes a `GET /cli/authorize` that has a `challenge` and:

1. stores its query string in the cookie `pantech-cli-request`: `httpOnly`,
   `SameSite=Lax`, `Secure` behind https, `Path=/cli`, max age 10 minutes;
2. redirects to `/cli/authorize` with no query, so the request stays out of the
   browser history and survives a detour through log-in and two-factor.

The page then reads the request from the cookie. A missing, expired or
malformed one shows "Start from your terminal": run `pantech auth login` again.
Signed out, the page sends the person to `/login?next=cli/authorize` and back.

The page names the machine and the organization and offers:

- **Access**: read and write (the default) or read only;
- **Expires after**: 7, 30, 60, 90 (the default), 180 or 365 days.

Without two-factor authentication on, the person is told to set it up first. The
API decides who may create keys, as on Organization › API keys, and its refusal
is shown on the page.

**Approve** (rate-limited per person):

1. reads the request from the cookie (gone: "This sign-in request expired");
2. creates an API key in the person's organization, named `CLI on <host>` (or
   `CLI` with no host), with the chosen scope and expiry;
3. seals a one-time code for it (below) and clears the cookie;
4. answers with the callback, which the page navigates to:

   ```
   http://127.0.0.1:<port>/callback?state=<state>&code=<code>
   ```

**Cancel** navigates to
`http://127.0.0.1:<port>/callback?state=<state>&error=access_denied`. Nothing is
created.

Both callbacks are always `http://127.0.0.1`, never `localhost` or another
host.

### The code

The code carries the key, sealed by the console with authenticated
encryption and bound to the PKCE challenge, so the console stores nothing.
It is good for **two minutes**, can be traded **once**, and is worth nothing
without the verifier, which never leaves the CLI.

## 3. Callback: `GET http://127.0.0.1:<port>/callback` (served by the CLI)

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

`code_verifier` is 43 characters of base64url without padding (32 random bytes);
the console accepts 43–128. There are no cookies and no other authentication: the
code and the verifier are the authentication. The route needs no console session
and no CSRF token, and is exempt from the staging access gate.

The console:

1. rate-limits by client IP, answering `429 RATE_LIMITED` with `Retry-After`;
2. checks the code is genuine, unexpired and unused, and that
   `BASE64URL-NOPAD(SHA-256(code_verifier))` equals its challenge;
3. answers `200`, `Cache-Control: no-store`:

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
| `api_key` | string | The full secret, `PAN_…`. The CLI refuses an answer without it. |
| `key_id` | string | Shown to the person if the key then fails, so they can revoke it. |
| `organization_id` | string | |
| `organization_name` | string or null | Shown on sign-in and in `pantech auth status`. |
| `scopes` | string array | `["read"]` or `["write"]`. |
| `expires_at` | RFC 3339 string | |
| `api_url` | string | The API this console talks to, without `/public/v1`. The CLI always uses production and ignores it. |

Every failure is the same `400`, so an expired code, a forged one and a wrong
verifier cannot be told apart:

```json
{"status":400,"code":"INVALID_GRANT","detail":"This sign-in code is invalid or has expired. Run pantech auth login again."}
```

served as `application/problem+json`. The CLI shows `detail` as it is. A `404`
from this route makes the CLI print the `--with-token` advice.

## 5. After the exchange

The CLI calls `GET https://api.pantechdynamics.com/public/v1/me` with the key. If
that fails, it tells the person the key id so they can revoke it, and stores
nothing. If it works, it stores the key in the OS keychain (or a 0600 file) and the
organization in its profile.

The key appears under Organization › API keys like any other, so it can be
revoked there; `pantech auth logout` only forgets it locally.
