# pantech

The Pantech Dynamics CLI: virtual machines, managed databases, volumes,
snapshots, networks, security groups, SSH keys and the catalogue from your
terminal, through the [public API](https://docs.pantechdynamics.com/api).

```sh
curl -fsSL https://pantechdynamics.com/install | bash
pantech auth login
pantech vm create --name web-1 --plan starter --image ubuntu-24-04 --ssh-key deploy
pantech vm ssh web-1
```

macOS (Apple silicon and Intel) and Linux (x86-64 and arm64).

## Signing in

`pantech auth login` opens the console in your browser. You approve the CLI there,
and the console creates an API key for it, named for your machine, which you can
revoke any time under Organization › API keys. The key goes to the CLI by a
one-time code and a PKCE check; it never appears in a URL. It is stored in the
macOS Keychain or the Linux Secret Service, or, with no keychain (a headless
server), in `~/.config/pantech/credentials.json`, readable only by you.

Creating a key needs an owner or admin with two-factor authentication on.

If the console does not offer CLI sign-in yet, `pantech auth login` says so at
once: sign in with a key instead (below).

Without a browser:

```sh
pantech auth login --with-token < key.txt   # a key made in the console
export PANTECH_API_KEY=PAN_…                 # or nothing stored at all (CI)
```

Profiles keep more than one sign-in: `pantech --profile work auth login`, then
`--profile work` (or `PANTECH_PROFILE=work`) on any command. Every profile uses
the production console and API.

## Commands

```
pantech auth login | logout | status
pantech vm list | get | create | start | stop | reboot | delete | ssh    (alias: instances)
pantech vm orders list | get
pantech db engines | list | get | create | start | stop | delete          (alias: database)
pantech db access-rules set | security-groups set | password set | password reset | orders list | get
pantech volumes list | get | create | attach | detach | delete
pantech snapshots list | get | create | delete
pantech networks list | get | create | delete | subnets list | create | delete
pantech public-ips list | get | create | delete
pantech security-groups list | get | create | delete | rules set
pantech ssh-keys list | add | delete
pantech plans | images | regions
pantech operations get | wait
pantech api <METHOD> <path> [--data JSON] [-f key=value]   anything else in the API
```

Every command takes `--json` (the API's own JSON), `--quiet` (ids only) and `--yes`
(no question before something that deletes or costs money; required when there
is no terminal to ask in). Writes wait for their operation to finish unless you
pass `--no-wait`.

`pantech vm ssh` opens port 22 to your address for 15 minutes through the API
(SSH access is closed by default), then runs your own `ssh`; `--revoke` closes it
again when the session ends.

Database passwords are never taken as an argument: `--password-stdin` reads one
from a hidden prompt or a pipe. A password the platform generates (on `db create`
without `--password-stdin`, or `db password reset`) is printed once, alone on
stdout, and can never be read again.

The CLI sends an `Idempotency-Key` with every write and retries network errors,
429s and 5xxs with the same key, so a retried change never happens twice.

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Done |
| 1 | Anything else |
| 2 | The command was used wrongly |
| 3 | Not signed in, or the key was refused or lacks the scope |
| 4 | Not found |
| 5 | An operation or order finished, but failed |
| 130 | Interrupted |

## Development

```sh
make build       # ./pantech
make test        # go vet and go test
make dist        # release archives for every platform, in dist/
```

The CLI always signs in through `https://console.pantechdynamics.com` and calls
`https://api.pantechdynamics.com`. `PANTECH_CONFIG_DIR` points the CLI at
another config directory, and `PANTECH_NO_KEYRING=1` keeps it out of your
keychain, for testing.

The console's half of the sign-in is specified in
[`docs/cli-auth-protocol.md`](docs/cli-auth-protocol.md): the routes, parameters,
PKCE check and token format pantech-console must implement.

## Releasing

```sh
git tag v0.2.0 && git push origin v0.2.0
```

`.github/workflows/release.yml` tests and builds every platform on a GitHub
runner (`make dist`), then a runner on the website server, labelled
`pantech-downloads`, publishes the build with `scripts/publish-release.sh` into
`/srv/pantech-downloads/cli`. Nginx there serves that directory:

```
https://pantechdynamics.com/install              the install script
https://pantechdynamics.com/cli/latest.txt       the newest version (never cached)
https://pantechdynamics.com/cli/<version>/       pantech_<os>_<arch>.tar.gz, SHA256SUMS
```

A published version is never replaced: its files are cached for a year, so
fix forward with a new version. A tag with a hyphen (`v0.2.0-rc.1`) is
published at its own URL but does not become the latest. Every version
carries its own installer, so a pre-release can be tried end to end before
anything public changes:

```sh
curl -fsSL https://pantechdynamics.com/cli/v0.2.0-rc.1/install | bash -s -- v0.2.0-rc.1
```

The server side (the directory, the runner, the nginx locations) is set up
once; pantech-web-new's `deploy/README.md` covers it.
