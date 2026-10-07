# Changelog

What changed in each version of the Pantech CLI. `pantech upgrade` installs the
latest.

Every pull request that changes what the CLI does adds a line under
**Unreleased**. Releasing turns that heading into the version and its date; the
release workflow refuses a tag that has no section here.

## Unreleased

## v0.2.0 — 2026-10-08

### Added

- `pantech kubernetes` (or `k8s`) runs managed Kubernetes clusters:
  `versions`, `list`, `get`, `create`, `configure`, `upgrade`, `start`, `stop`,
  `delete` and `kubeconfig`. `create` and `configure` take a fixed `--workers`
  count or `--autoscale MIN:MAX`, and `--api-allow` limits which addresses
  reach the API server (VPC zones only). `kubeconfig` downloads the cluster's
  admin kubeconfig.
- `pantech load-balancers` (or `lb`) spreads one TCP port of a
  `load_balancer` public IP across VMs in a VPC subnet: `list`, `get`,
  `create`, `update` (name, algorithm or the whole set of targets) and
  `delete`.
- `pantech public-ips attach` and `detach` move a static_nat address between
  VMs, or hold it without one, keeping the same address. A held address is
  billed once, attached or not, until `public-ips delete` releases it.
- `pantech public-ips create --purpose load_balancer` reserves an address for
  load balancers, and `--vm` can be left out to reserve a static_nat address
  detached.

### Changed

- `pantech public-ips list` and `get` show `detached` for an address held
  without a VM, and `applying` while an attach or detach is on its way.
- `pantech vm delete` refused because the VM still has a public IP says how
  to detach the address to keep it, or release it.
- A change that changes nothing now says so instead of waiting for an
  operation that never starts.

## v0.1.6 — 2026-10-06

### Fixed

- A profile signed in on a development API by v0.1.2 or older now says so,
  and how to sign in again, instead of failing with "the API key is not
  valid".

## v0.1.5 — 2026-10-05

### Added

- `pantech upgrade` (or `update`) installs the latest version, or the one
  given, after checking it against the release's checksums. `--check` only
  reports.
- After a command, the CLI says when a newer version is out. It checks at most
  once a day, never in CI, with `--json` or `--quiet`, or without a terminal,
  and `PANTECH_NO_UPDATE_NOTIFIER=1` turns it off.
- `pantech security-groups rules add | remove` and
  `pantech db access-rules add | remove` change some rules and keep the rest.
  `rules set` and `access-rules set` still replace the whole list.

### Changed

- `pantech vm private-network attach` suggests `access-rules add` and
  `rules remove | add`, not the `set` commands, which would have dropped every
  other rule.

### Fixed

- `pantech vm ssh` connects to the VM's address when the platform will not open
  SSH access (the VM shares its security group, or the key is read-only),
  instead of stopping.
- `pantech vm ssh --revoke` closes SSH access when the session is ended with
  Ctrl+C. It used to leave port 22 open until it expired, without a word.
- A subnet can be given by name, as the help said, to `vm create --subnet`,
  `db create --subnet` and `networks subnets delete`.
- `pantech db password reset --no-wait` no longer suggests following an
  operation with no id.

## v0.1.4 — 2026-10-05

### Added

- Managed databases: `pantech db engines | list | get | create | start | stop |
  delete`, `db access-rules set`, `db security-groups set`,
  `db password set | reset`, `db storage resize`, `db snapshots`, and
  `db orders`. Passwords are read only from stdin or a hidden prompt, and a
  generated one is printed once.
- `pantech volumes`, `pantech snapshots`, `pantech networks` (with
  `networks subnets`), `pantech public-ips` and `pantech security-groups`.
- `pantech vm private-network attach | detach`, to reach databases over the
  zone's private network, and `pantech vm orders list | get`.
- `pantech vm create --security-group` and `--tags`.
- A failed order exits with code 5, like a failed operation.

### Changed

- `pantech vm ssh` asks the platform to open port 22 to your address for 15
  minutes before connecting; SSH is closed by default. `--revoke` closes it when
  the session ends.

## v0.1.3 — 2026-10-05

### Removed

- `--console-url`, `--api-url`, `PANTECH_CONSOLE_URL` and `PANTECH_API_URL`.
  The CLI always uses the production console and API.

## v0.1.2 — 2026-10-04

### Fixed

- `pantech vm list` and `vm get` show a standard VM's address, and
  `pantech vm ssh` connects to it.

## v0.1.1 — 2026-10-04

### Changed

- `--console-url` and `--api-url` are no longer listed in `--help`.

## v0.1.0 — 2026-10-04

The first release, for macOS and Linux (x86-64 and arm64), installed with
`curl -fsSL https://pantechdynamics.com/install | bash`.

- `pantech auth login` signs in through the console in your browser, or takes a
  key with `--with-token`; `PANTECH_API_KEY` works with nothing stored.
  Profiles keep more than one sign-in.
- `pantech vm list | get | create | start | stop | reboot | delete | ssh`.
- `pantech ssh-keys list | add | delete`.
- `pantech plans`, `images` and `regions`.
- `pantech operations get | wait`.
- `pantech api` calls anything else in the API.
- `--json`, `--quiet`, `--yes` and `--no-wait` on every command; writes are
  retried safely with an `Idempotency-Key`.
