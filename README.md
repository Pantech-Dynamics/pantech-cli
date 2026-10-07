# pantech

The Pantech Dynamics CLI: virtual machines, managed databases, managed
Kubernetes, volumes, snapshots, networks, public IPs, load balancers, security
groups, SSH keys and the catalogue from your terminal, through the [public API](https://docs.pantechdynamics.com/api).

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
pantech vm private-network attach | detach
pantech vm orders list | get
pantech db engines | list | get | create | start | stop | delete          (alias: database)
pantech db access-rules set | add | remove | security-groups set | password set | password reset | orders list | get
pantech db storage resize | snapshots list | get | create | delete
pantech volumes list | get | create | attach | detach | delete
pantech snapshots list | get | create | delete
pantech networks list | get | create | delete | subnets list | create | delete
pantech public-ips list | get | create | attach | detach | delete     (alias: ips)
pantech load-balancers list | get | create | update | delete           (alias: lb)
pantech kubernetes versions | list | get | create | configure | upgrade | start | stop | delete | kubeconfig   (alias: k8s)
pantech security-groups list | get | create | delete | rules set | add | remove
pantech ssh-keys list | add | delete
pantech plans | images | regions
pantech operations get | wait
pantech upgrade [version] [--check]                       (alias: update)
pantech api <METHOD> <path> [--data JSON] [-f key=value]   anything else in the API
```

`pantech upgrade` replaces the CLI with the latest release, after checking the
download against the release's `SHA256SUMS`. When a newer version is out, the
CLI says so after a command, checking at most once a day; not in CI, not when
stderr is not a terminal, not with `--json` or `--quiet`, and never with
`PANTECH_NO_UPDATE_NOTIFIER=1`.

Every command takes `--json` (the API's own JSON), `--quiet` (ids only) and `--yes`
(no question before something that deletes or costs money; required when there
is no terminal to ask in). Writes wait for their operation to finish unless you
pass `--no-wait`.

`pantech vm ssh` opens port 22 to your address for 15 minutes through the API
(SSH access is closed by default), then runs your own `ssh`; `--revoke` closes it
again when the session ends, even one ended with Ctrl+C. When the platform will
not open access (the VM shares its security group with another, or the key is
read-only), it says why and connects to the VM's address directly, which works
if the security group already allows port 22 from you.

`rules set` and `access-rules set` replace the whole list; `add` and `remove`
change only the rules given and keep the rest.

Database passwords are never taken as an argument: `--password-stdin` reads one
from a hidden prompt or a pipe. A password the platform generates (on `db create`
without `--password-stdin`, or `db password reset`) is printed once, alone on
stdout, and can never be read again.

A database's data disk is sized with `db create --storage-gb` (default: the
plan's disk) and only ever grows: `db storage resize <db> --storage-gb N` refuses
a size that is not larger, waits for the resize, and `db get` shows the size a
resize in flight is growing to. Both check the size against the zone's limits
(minimum, maximum and step, which `db engines` lists with the price per GB)
before anything is sent; when those cannot be read, the API decides. `db snapshots` takes crash-consistent snapshots
of the data disk, billed until deleted.

`pantech vm private-network attach <vm>` gives a standard VM an interface on
its zone's private database network and prints the address to allow on a
database as a `/32` access rule. The VM's security group must allow nothing
from that network's range (the zone's `private network` in `pantech regions`); when it does, the CLI prints the API's message,
naming the rules to narrow, and how to change them with
`pantech security-groups rules remove` and `rules add`. `vm get` shows the
interface and its address; allow it on a database with
`pantech db access-rules add`.

A static_nat public IP keeps its address across VMs: `public-ips create --network
<net>` without `--vm` reserves one detached, `public-ips attach <ip> --vm <vm>`
points it at a VM (moving it if it is attached elsewhere), and
`public-ips detach <ip>` unmaps it. A held address is billed once, attached or
not, until `public-ips delete` releases it. `list` and `get` show `detached` and
`(applying)` while an attach or detach is still reaching the address. A VM with
a static_nat address cannot be deleted until it is detached or released.

`pantech load-balancers create --name web --public-ip <ip> --subnet <subnet>
--port 443 [--private-port 8443] [--algorithm roundrobin|leastconn|source]
[--allow CIDR…] [--target VM…]` spreads a port of a `load_balancer` public IP
across VMs; `update` renames it, changes the algorithm, or replaces its targets
(`--targets`, or `--no-targets`).

`pantech kubernetes create --name prod --zone <zone> --version 1.31.2
--node-plan <plan> [--subnet <subnet>] [--control-nodes 1|3] (--workers N |
--autoscale MIN:MAX) [--api-allow CIDR…]` creates a cluster; every node is
billed as a VM of the plan. `configure` (alias `scale`) sets `--workers`,
`--autoscale MIN:MAX` or `--no-autoscale`, and `--api-allow` or
`--api-allow-any`; `upgrade --version` takes one of the versions `get` lists as
available. `pantech kubernetes kubeconfig <cluster> -o FILE` writes the admin
kubeconfig readable only by you (mode 0600); `--stdout` prints it instead, and
one of the two is required. It is a cluster-admin credential: keep it like a
password.

Every API error is printed with the API's own message, each invalid field and
what it must be (on a `422`), and the error code and request id to quote to
support.

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

The sign-in protocol, both the CLI's half and pantech-console's, is described in
[`docs/cli-auth-protocol.md`](docs/cli-auth-protocol.md).

## Releasing

Pull requests that change what the CLI does add a line under **Unreleased** in
[`CHANGELOG.md`](CHANGELOG.md). To release, rename that heading to the version
and date (`## v0.2.0 — 2026-11-02`) in a pull request, merge it, then tag `main`:

```sh
git tag v0.2.0 && git push origin v0.2.0
```

The release fails if `CHANGELOG.md` has no section for the tag (pre-releases
need none).

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
