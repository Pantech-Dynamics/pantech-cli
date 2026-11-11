# Changelog

What changed in each version of the Pantech CLI. `pantech upgrade` installs the
latest.

Every pull request that changes what the CLI does adds a line under
**Unreleased**. Releasing turns that heading into the version and its date; the
release workflow refuses a tag that has no section here.

## Unreleased

## v0.8.0 — 2026-11-10

### Added

- `pantech dns` manages zones on Pantech DNS: `zones list | create | get |
  update | delete | verify` (`create` prints the name servers to set at your
  registrar; `delete` asks first), `records list | create | get | update |
  delete` (`--value` repeatable, `--ttl`, `--allow-private`, `--comment`,
  `--wait` until the change is live, `--if-match <version>`; `delete` asks
  first), `import <zone> --file f.zone [--replace]` (`--replace` asks first;
  `-` reads stdin) and `export <zone>`, which prints the BIND zone file.
- `pantech domains`: `tlds`, `search`, `transfer-check`, `list`, `get`;
  `register` and `transfer` (`--years`, `--contact-file`, `--auto-renew`,
  `--no-privacy`, `--accept-terms`, `--wait`; `transfer` takes the auth code
  with `--auth-code-stdin` or `--auth-code`) and `renew --years`, which show
  the price charged to your credit and ask first; `set --auto-renew --lock
  --privacy` (on/off), `nameservers --pantech [--zone] | --ns …`, `contact
  [--set file]`, `auth-code` (asks first, prints the code once) and `orders`.
  Zones and domains are named by id or by name. A domain order or a DNS record
  change that fails exits with 5.
- `pantech registry` (alias `cr`) manages the private container registry:
  `namespace [get | set <name>] [--create-on-push]`; `repos list | create |
  get | update | delete` (`--description`, `--visibility`, `--immutable-tags`,
  `--keep-last`, `--untagged-after-days`, `--keep-pattern`, `--no-retention`;
  making one public and deleting ask first); `tags list | get | delete <repo>
  <tag>`; `tokens list | create | revoke` (`--permission pull|push`, `--repo`,
  `--expires`; `create` prints the secret once, alone on stdout). Repositories
  are named by id or by name. `registry login` runs `docker login` (or podman,
  `--engine podman`) with the API key or a registry token (`--token-stdin`,
  `--token`) on stdin, never on the command line; `--registry` picks another
  host.
- `pantech iam` manages teams and access policies: `actions [--service]`;
  `members [--q]`;
  `settings [get | set --member-baseline read|none]` (`none` asks first);
  `teams list | create | get | update | delete` and `teams members <team>
  [add | remove <member>]`; `policies list [--managed | --custom] | create
  --file | get | update --file [--if-match] | delete | attachments | attach |
  detach` (`--member` or `--team`); `access <member>` for a member's effective
  access; `simulate --user --action [--resource] [--resource-name] [--ip]
  [--mfa]`. Teams and policies are named by id or name, members by id or
  email. Attaching, detaching, replacing or deleting a policy, deleting a team
  and adding or removing a team member ask first.
- `pantech certificates` (alias `certs`): `list [--q --status --kind]`,
  `request <name>... [--key-type] [--exportable] [--wait]` (prints the CNAME
  records to add for names not in a Pantech DNS zone), `import` and `reimport`
  (`--cert`, `--chain`, `--key`), `get`, `update --description`, `verify
  [--wait]`, `export [--out-dir] [--force] [--stdout]` (writes cert, chain,
  full chain and key files with mode 0600; prints the key only with
  `--stdout`), `revoke [--reason]` and `delete`. Export, revoke and delete ask
  first. A certificate that fails while waited for exits with 5.
- `dns zones get` shows when a zone awaiting delegation is removed, and
  policies show `mfa: false` conditions as "without MFA".
- `pantech dns zones quote`: five zones are free; it shows what further zones
  cost a month in your currency.
- Payment for domain orders: when credit falls short, the order waits an hour
  for the rest. In US dollars your saved card is charged the difference, and
  `domains register`, `transfer` and `renew` print a link only if your bank
  asks you to confirm the payment (with `--quiet`, the link alone on stderr);
  in naira they say to add credit. `--wait` follows the order through the
  payment until it finishes, or fails `insufficient_credit` with nothing
  charged. Prices are shown before VAT.
- `pantech domains retry-payment <order> [--wait]` tries a waiting order's
  payment again now (the saved card once more, or credit just added) and gives
  it another hour; it asks first and never charges twice. `domains orders`
  says what to do for an order that is `awaiting_payment`.
- .ng domains: `domains tlds` shows privacy as "always on" for TLDs whose
  registry hides every registrant's contact details, and `--no-privacy` or
  `domains set --privacy=off` is refused for them.
- HTTPS on appliance load balancers: `load-balancers create --protocol https
  --certificate cert_… [--tls-policy modern|tls13] [--redirect-http]`, the same
  TLS flags on `update`, and `get` shows the certificate, the fingerprint being
  served, the TLS policy and the redirect.

### Changed

- Pantech DNS runs on Pantech's own name servers: a zone gets two name
  servers, and records added while it awaits delegation stay `pending` until it
  is active. The help and README say so.

## v0.7.1 — 2026-11-06

### Changed

- `pantech kubernetes resize` says what a node plan change does: nodes are
  resized one at a time and restart, the cluster's API is briefly unavailable
  while the control node restarts, a smaller plan keeps the nodes' disk (disks
  only grow), and the cluster is billed at the new plan only once every node
  runs it.

## v0.7.0 — 2026-10-28

### Added

- `pantech migrate` (alias `mig`) plans a move from AWS to Pantech, through the
  public API's Migrate endpoints. `connect --name` starts a connection, saves
  the read-only role's CloudFormation template to
  `./pantech-migrate-<id>.json` (`--template-out`), and prints and opens AWS's
  one-click create-stack link (`--no-browser`); `set-role <id> --role-arn ARN
  [--wait]` sends the stack's RoleArn and follows the check; `connections`,
  `connection <id> [--watch]`, `retry [--wait]`, `rename-connection --name` and
  `disconnect` (asks first)
  manage connections. `scan <connection> [--wait]` (asks first: reading spend
  costs USD 0.01 on your AWS bill) reads the account and shows
  what it found, the regions, warnings and the monthly bill; `scans` and
  `scan-show [--inventory] [--wait]` list and show scans. `plan <scan> --name
  [--region]` builds a plan with a monthly price comparison; `plans`, `show`
  (items grouped by kind, with status and flags), `item --key` (`--exclude`,
  `--include`, `--plan`, `--storage-gb`, `--cidr`, `--role`, `--email`),
  `rename` and `delete` (asks first) manage it, and `terraform [--out DIR]
  [--force]` writes `main.tf` and `variables.tf`, never overwriting a file
  without `--force`. A connection check or scan that fails exits with 5.
- Names, messages and other text from the API or a scanned account are printed
  without control characters, so they cannot steer your terminal (`--json` is
  unchanged).

## v0.6.0 — 2026-10-24

### Added

- `pantech mcp install <client>` connects an AI app to Pantech through MCP:
  `claude-code`, `claude`, `cursor`, `vscode`, `windsurf`, `codex` and `gemini`
  get a `pantech` server in their own config (every other server kept, the old
  file saved as `<file>.bak`), or are set up with their own `claude mcp add` /
  `gemini mcp add`; `chatgpt`, `grok` and Claude's hosted connector get the
  steps to follow. The hosted server is the default; `--local` runs
  `pantech-mcp stdio`, `--print` only shows what would be added, and `--url`
  points at another hosted server.
- `pantech mcp status` shows the hosted server, whether `pantech-mcp` is
  installed and which apps already have Pantech; `pantech mcp list-clients`
  lists the apps. Client names complete in the shell.
- `pantech auth logout` now also revokes the key, so it stops working
  everywhere; `--keep-key` only forgets it on this machine.

## v0.5.1 — 2026-10-22

### Fixed

- Clearer errors: a gateway's HTML error page is never printed; a write that times out or loses its connection says the change may have gone through and to check before retrying; field errors name the flag you used (`--ssh-key`, not `ssh_key_id`) on `vm create` and `webhooks create`; and the request id is always shown.
- `pantech ssh-keys add` and `delete` are no longer retried automatically, since the API does not replay them: a retry could lose a generated private key.

## v0.5.0 — 2026-10-22

### Added

- `pantech vm change-ssh-key <vm> <key>` gives a running VM another of your
  SSH keys, by id or name. It asks first (`--yes` to skip), restarts the VM to
  take the key and follows the change; `--no-wait` prints the operation id
  instead. Afterwards only the new key logs in; keys added on the server by
  hand keep working.
- `pantech webhooks` sends your organization's events to an https URL:
  `create --url … --event 'instance.*'` (repeatable; prints the signing secret
  once, alone on stdout), `list`, `get`, `update` (`--url`, `--description`,
  `--event`, `--enable` / `--disable`), `delete`, `test`, `rotate-secret`
  (asks first; prints the new secret once) and `event-types`.
  `webhooks deliveries list | get | redeliver` shows what was sent, page by
  page (`--limit`, `--cursor`, `--all`), with the exact payload.

### Changed

- `pantech vm create` now requires `--ssh-key`. Images have no password login, and the platform refuses a VM without a key; before, the CLI only warned and the API answered `500`.

## v0.4.0 — 2026-10-20

### Added

- Load balancers in standard zones: `pantech load-balancers create --zone
  af-abj-1 --port 80 --target web-1` makes an appliance load balancer (a VM
  with its own public address, billed as that VM). `--kind vpc_native|appliance`
  picks the kind explicitly; it is otherwise worked out from `--subnet` /
  `--zone`. `list` and `get` show the kind and zone.
- `pantech kubernetes create --cni flannel|wireguard|calico --pod-cidr …
  --service-cidr …` chooses the cluster's pod network; `get` shows it and the
  endpoint access.
- `pantech kubernetes resize <cluster> --node-plan …` moves every node to
  another plan; `network` changes the CNI or address ranges;
  `endpoint-access <cluster> public|private|dual` sets where the API server
  is reachable.
- Live cluster commands: `nodes`, `workloads` (services and pods; `-A` for
  system namespaces), `apply <cluster> -f app.yaml` (server-side apply, `-f -`
  for stdin), `manifests` (export your objects as YAML), `expose <cluster>
  <service> --port 80` (route a public port to a service) and `sync-routing`.
- `pantech kubernetes node-pools` (list, create, scale, delete): groups of
  workers with their own plan, labels and taints; `list` shows how many have
  joined. `node-pools scale` keeps whatever it is not asked to change.
- `pantech kubernetes addons` (list, enable, disable): metrics-server,
  csi-driver (node-local volumes) and ingress-controller, with their status.

## v0.3.0 — 2026-10-11

### Added

- Encrypted VMs: `pantech images` has an `encrypted` column, `pantech vm create
  --encrypted` picks the encrypted variant of `--image` (or says why there is
  none), and `pantech vm get` marks an encrypted VM's image. An encrypted
  image's slug, such as `ubuntu-24-04-encrypted`, also works with `--image`.

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
