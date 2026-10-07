# pantech-cli — guide for coding agents

The `pantech` CLI, in Go. How to use it, build it and release it is in `README.md`.

```sh
make build && make test
gofmt -l .        # must print nothing
```

## Layout

```
cmd/pantech/        main: runs the command tree, maps errors to exit codes
internal/api/       the public API: client (auth, Idempotency-Key, retries, problems), types, waiting
internal/auth/      browser sign-in through the console (PKCE, loopback callback)
internal/cli/       the cobra commands
internal/config/    profiles in ~/.config/pantech/config.json; keys in the keychain or credentials.json
internal/output/    tables, colour, --json / --quiet
install.sh          what https://pantechdynamics.com/install serves
```

## Rules

- **The API is the source of truth.** Its reference is
  https://docs.pantechdynamics.com/api, and the contract is
  https://docs.pantechdynamics.com/openapi/public-api.yaml. Types in
  `internal/api/types.go` carry only what the CLI shows; `--json` prints the API's
  body, so nothing is lost by leaving a field out.
- **Every write goes through `api.Client.Do`**, which sends the Idempotency-Key
  and retries with the same one. Never retry a write any other way.
- **Ask before anything that deletes or costs money** (`app.confirm`), and honour
  `--yes`. With no terminal, refuse rather than guess.
- **stdout is for results, stderr for progress and hints**, so output pipes cleanly.
- **The sign-in has a second half in pantech-console** (`/cli/authorize`,
  `/api/cli/token`), specified in `docs/cli-auth-protocol.md`. A change to the
  exchange is a change to both, and to that document.
- **Passwords never in argv, files, config or logs.** Read them from stdin
  (`--password-stdin`); print a generated one once, alone on stdout.
- **A change people will notice goes in `CHANGELOG.md`**, under Unreleased, in
  the same pull request.
- **No attribution trailers** in commits or pull requests.
