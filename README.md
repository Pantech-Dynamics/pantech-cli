# pantech

The Pantech Dynamics CLI: virtual machines, managed databases, managed
Kubernetes, volumes, snapshots, networks, public IPs, load balancers, security
groups, SSH keys, webhooks, DNS zones and domains, a private container registry,
teams and access policies, TLS certificates, migrations from AWS and the catalogue from your terminal, through the [public API](https://docs.pantechdynamics.com/api).

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

`pantech auth logout` forgets the profile's key on this machine and revokes it,
so it stops working everywhere. `--keep-key` only forgets it. A key in
`PANTECH_API_KEY` is never touched.

## Commands

```
pantech auth login | logout | status
pantech vm list | get | create | start | stop | reboot | delete | ssh | change-ssh-key    (alias: instances)
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
pantech webhooks list | get | create | update | delete | test | rotate-secret | event-types
pantech webhooks deliveries list | get | redeliver
pantech migrate connect | connections | connection | set-role | retry | rename-connection | disconnect   (alias: mig)
pantech migrate scan | scans | scan-show | plan | plans | show | item | rename | delete | terraform
pantech dns zones list | create | get | update | delete | verify
pantech dns records list | create | get | update | delete
pantech dns import <zone> --file f.zone [--replace] | export <zone>
pantech domains tlds | search | transfer-check | list | get | register | transfer | renew
pantech domains set | nameservers | contact | auth-code | orders
pantech registry namespace [get | set <name>] | login                (alias: cr)
pantech registry repos list | create | get | update | delete
pantech registry tags list | get | delete
pantech registry tokens list | create | revoke
pantech iam actions | members [--q] | settings [get | set] | access <member> | simulate
pantech iam teams list | create | get | update | delete | members <team> [add | remove <member>]
pantech iam policies list | create | get | update | delete | attachments | attach | detach
pantech certificates list | request | import | get | update | verify | reimport | export | revoke | delete   (alias: certs)
pantech plans | images | regions
pantech operations get | wait
pantech upgrade [version] [--check]                       (alias: update)
pantech api <METHOD> <path> [--data JSON] [-f key=value]   anything else in the API
pantech mcp install <client> [--local] [--print] | status | list-clients
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

### Encrypted VMs

An encrypted VM keeps its disk, its snapshots and backups encrypted at rest,
each VM with its own key. `pantech images` marks the encrypted images in its
`encrypted` column; they are listed only once encrypted VMs are enabled for
your organization (on request), and only in the zones that carry them. Create
one with the encrypted image's slug, or with `--encrypted`, which picks the
encrypted variant of `--image`:

```sh
pantech vm create --name vault-1 --plan starter --image ubuntu-24-04 --encrypted --ssh-key deploy
pantech vm create --name vault-1 --plan starter --image ubuntu-24-04-encrypted --ssh-key deploy   # the same
```

`--encrypted` fails, before ordering anything, when the image has no encrypted
variant available to your organization. `pantech vm get` shows an encrypted
VM's image as `ubuntu-24-04-encrypted (encrypted)`. See
[Encrypted VMs](https://docs.pantechdynamics.com/compute/encrypted-vms) for how
they boot and how to encrypt data volumes.

`pantech vm change-ssh-key <vm> <key>` gives a running VM another of your SSH
keys, for example after losing the private key. The VM restarts to take it
(about two minutes); afterwards only the new key logs in, and keys added to
`~/.ssh/authorized_keys` by hand keep working.

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

An appliance load balancer (`--zone` in a standard zone) can end HTTPS:
`--protocol https --certificate cert_…` serves an issued certificate from
`pantech certificates` (renewals reach it by themselves) and speaks plain HTTP to
your VMs; `--tls-policy modern|tls13` (default `modern`) and `--redirect-http`
(port 80 answers with a redirect to HTTPS). `update` takes the same three flags.
A certificate in use cannot be revoked or deleted; vpc_native load balancers
pass TLS through as `tcp`.

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

`pantech webhooks create --url https://… --event 'instance.*' [--event …]
[--description …]` sends your organization's events to an https URL as signed
JSON POSTs; `pantech webhooks event-types` lists what can be subscribed to
(an exact type, `<resource>.*` or `*`). The signing secret (`whsec_…`) is
printed once, alone on stdout, and never again; `rotate-secret` makes a new one
the same way, and the old one keeps signing for 24 hours. `update` changes only
the flags given (`--event` replaces the whole list; `--enable` / `--disable`).
`test` queues a `webhook.test` event, `deliveries list` shows one page at a time
(`--limit`, `--cursor`, or `--all`), `deliveries get` shows the exact payload
sent, and `deliveries redeliver` sends one again with the same event id.

### Migrating from AWS

`pantech migrate` reads an AWS account and plans its move to Pantech. Nothing is
created on Pantech or changed in AWS: a plan is something to review, edit and
export as Terraform.

```sh
pantech migrate connect --name "Production AWS"      # saves ./pantech-migrate-<id>.json
pantech migrate set-role ccn_… --role-arn arn:aws:iam::123456789012:role/… --wait
pantech migrate scan ccn_… --wait                     # what it runs and what it costs
pantech migrate plan csn_… --name "Move to Pantech" [--region <region>]
pantech migrate show mig_…                            # every item, its flags, the price comparison
pantech migrate item mig_… --key <key> --exclude      # or --include, --plan, --storage-gb, --cidr, --role, --email
pantech migrate terraform mig_… [--out DIR] [--force] # main.tf and variables.tf
```

`connect` saves a CloudFormation template that creates, in your account, a
read-only role only Pantech can assume, with short-lived tokens; no access keys
are ever sent to Pantech. It prints AWS's one-click create-stack link and opens
it in your browser (`--no-browser` to only print it; a link to anywhere but
`console.aws.amazon.com` is never shown or opened). `--template-out PATH` saves
the template elsewhere; a file already there is kept unless you pass `--force`,
and a symbolic link is never written through. Create the stack, then send its `RoleArn` output
with `set-role`; `--wait` (or `connection <id> --watch`) follows the check, and
`retry` checks again after a failure, and `rename-connection --name` renames one. `disconnect` asks first and removes the
connection and its scans; delete the stack in AWS to revoke the role there too.

`scan --wait` checks every 3 seconds until the scan has succeeded or failed, then
shows what it found per kind, the regions read, anything it could not read, and
the account's monthly bill. Reading spend costs USD 0.01 on your AWS bill, so
`scan` asks first (`--yes` to skip).
`scans <connection>` lists them, and `scan-show <scan> [--inventory] [--wait]`
shows one, with every item it found.

`plan` maps every item to its Pantech equivalent with a price comparison:
Pantech's monthly price for the planned servers, disks and networks next to the
account's average monthly bill. `show` lists the items grouped by kind, with
their status (`planned`, `excluded` or `unsupported`) and flags (`!` marks one
that makes the item unsupported); use an item's key with `item` to change it.
`plans` lists them, `rename --name` renames one, and `delete` asks first.
`terraform` writes the files to `./pantech-migrate-<id>/` and never overwrites
one without `--force` (nor writes through a symbolic link, even with it); every
file is checked before any is written, and each is written whole or not at all. A connection check or scan that fails exits with 5.

Every API error is printed with the API's own message, each invalid field and
what it must be (on a `422`), and the error code and request id to quote to
support.

The CLI sends an `Idempotency-Key` with every write and retries network errors,
429s and 5xxs with the same key, so a retried change never happens twice.

### DNS and domains

`pantech dns` hosts zones on Pantech DNS, on Pantech's own name servers, and manages
their records; `pantech domains` buys domains, paid from your organization's
credit. Zones and domains are named by id (`dnsz_…`, `dom_…`) or by name.

```sh
pantech dns zones create example.com                  # prints the 2 name servers to set at your registrar
pantech dns zones quote                               # 5 zones free; what further ones cost a month
pantech dns zones verify example.com                  # check the delegation now
pantech dns records create example.com --name www --type A --value 203.0.113.10 [--ttl 300] [--wait]
pantech dns records update example.com dnsr_… --value 203.0.113.20 [--if-match <version>]
pantech dns export example.com > example.com.zone
pantech dns import example.com --file example.com.zone [--replace]

pantech domains search example                        # availability and prices
pantech domains register example.com --years 1 --contact-file contact.json --accept-terms [--wait]
pantech domains transfer example.com --auth-code-stdin --contact-file contact.json --accept-terms < code.txt
pantech domains renew example.com --years 1
pantech domains set example.com --auto-renew=on --lock=on --privacy=on
pantech domains nameservers example.com --pantech     # or --ns ns1.example.net --ns ns2.example.net
pantech domains contact example.com [--set contact.json]
pantech domains auth-code example.com > code.txt      # to move the domain elsewhere
pantech domains orders example.com
pantech domains retry-payment dor_… [--wait]          # an order waiting for its payment
```

A new zone is `awaiting_delegation` until the domain's registrar points at
exactly its two name servers (records added meanwhile stay `pending` until it
is active); one left undelegated for 14 days is removed. A
domain bought through Pantech gets an active zone at once. Record changes are
`pending` until live, within about a minute: `--wait` follows one, and a change
that is refused exits with 5. `--value` is repeated for each value (commas in a
value are kept). `--if-match` changes or deletes a record only if its version
is still the one you read. `import --replace` deletes the records not in the
file, so it asks first; `zones delete` and `records delete` ask too.

`register`, `transfer` and `renew` show the price (before VAT) and that it is
charged to your organization's credit, and ask first (`--yes` to skip); a
failed order returns it to credit, and `--wait` exits with 5 when the order
fails. When your credit falls short, the order waits an hour for the rest: in
US dollars your saved card is charged the difference, and the command prints a
link only if your bank asks you to confirm the payment (on stderr; with
`--quiet`, the link alone); in naira, add credit. `domains retry-payment
<order>` tries again at once (after a declined card, a new saved card or new
credit) and waits another hour; it never charges twice. `--wait` follows the
order until the payment lands and it finishes. Unpaid when the window ends, it
fails `insufficient_credit` and nothing is charged.
`--contact-file` is a JSON file with the registrant (`type`, `first_name`,
`last_name`, `organization_name` for a company, `email`, `phone` as
`+CC.NUMBER`, `address_line1`, `city`, `state`, `country_code`, `postal_code`);
it is sent to the API and never printed, and only `domains contact` shows it,
to owners and admins. `--accept-terms` accepts the registrant agreement.
Privacy protection, auto-renew and the transfer lock are on by default
(`--no-privacy`, `--auto-renew=false`; `domains set --lock=off` before moving away).
The .ng registry hides every registrant's contact details, so privacy is always
on for .ng domains (`domains tlds` shows "always on") and `--no-privacy` or
`--privacy=off` is refused; .ng contacts need `state` and `postal_code`, and a
name.ng registrant must be a person. `auth-code` asks first, then prints the code once,
alone on stdout: anyone with it can move the domain away from Pantech.

### Container registry

`pantech registry` manages your organization's private registry at
`registry.pantechdynamics.com`. Repositories are named by id (`crr_…`) or by
name (`api/server`, `acme/api/server`, or the full image name).

```sh
pantech registry namespace set acme                   # claim your namespace (once)
pantech registry login                                # docker login with your API key, on stdin
docker push registry.pantechdynamics.com/acme/api/server:v1

pantech registry repos create api/server --keep-last 50 --keep-pattern "v*" [--immutable-tags]
pantech registry repos update api/server --visibility public
pantech registry tags list api/server
pantech registry tags delete api/server v1
pantech registry tokens create --name github-actions --permission push --repo api/server > token.txt
pantech registry login --token-stdin < token.txt      # how CI logs in
pantech registry tokens revoke prt_…
```

`login` runs `docker login <registry> -u pantech --password-stdin` and gives it
the CLI's API key, or a registry token with `--token-stdin` (or `--token`), on
stdin: the credential is never on a command line. `--engine podman` uses
podman, and `--registry` (or `$PANTECH_REGISTRY`) another registry host. Without
docker or podman installed it says how to get one.

Registry tokens log in to the registry only, never to the API: `pull`, or `push`
(which includes pull), for every repository or those named with `--repo`, and
`--expires 90d` (or a date) to end one. `tokens create` prints the secret once,
alone on stdout. `repos delete`, `tags delete`, `tokens revoke` and making a
repository public ask first (`--yes` to skip). A retention flag of `0` (or
`--keep-pattern ""`) removes that rule, and `--no-retention` removes them all.

### Teams and access policies

`pantech iam` decides who in your organization may do what. Owners are allowed
everything; a matching deny wins, for admins too; admins are otherwise allowed
everything; members get what their policies (attached to them or their teams)
allow, plus every Read action while the member baseline is `read`. Teams and
policies are named by id or by name, members by id (`usr_…`) or email.

```sh
pantech iam actions --service instances               # what policies are written in
pantech iam policies create --file staging-operators.json
pantech iam teams create Platform
pantech iam teams members Platform add ada@example.com
pantech iam policies attach staging-operators --team Platform
pantech iam access ada@example.com                    # role, teams, policies, baseline
pantech iam simulate --user ada@example.com --action instances:Delete --resource-name prod-api
pantech iam settings set --member-baseline none       # members get only what policies allow
```

A policy file is `{"name", "description", "statements": [...]}`, or a bare array
of statements with `--name`; `pantech iam policies --help` shows one, and a
field the CLI does not know is refused rather than ignored. `policies update`
replaces the statements and keeps the name and description unless given; it
sends `If-Match` with the version it just read (or `--if-match`), so a change
made meanwhile is never overwritten. Pantech's own policies (`pol_pantech_…`)
can be attached but not changed.

`attach`, `detach`, `policies update`, `policies delete`, `teams delete`, adding
and removing team members, and `settings set --member-baseline none` ask first
(`--yes` to skip): each changes what people may do within seconds. An email
must match exactly one member, ignoring case; `pantech iam members [--q]` lists
them.

### Certificates

`pantech certificates` gets TLS certificates issued free by a publicly trusted CA
and renewed automatically 30 days before they expire.

```sh
pantech certificates request example.com '*.example.com' api.example.org --exportable --wait
pantech certificates verify cert_…                  # check the CNAMEs now
pantech certificates export cert_… --out-dir /etc/ssl/example
pantech certificates import --cert cert.pem --chain chain.pem --key key.pem
pantech certificates reimport cert_… --cert new.pem --key new-key.pem
```

Names in an active Pantech DNS zone are validated automatically. For any other
name, `request` (and `get` and `verify`) print the CNAME record to add at its
DNS host, once (`_acme-challenge.<name> CNAME <token>.acme.pantechdynamics.com`):
it keeps every renewal automatic while it stays. `--wait` follows a request
until it is issued; one that fails exits with 5.

`export` works only for a certificate created with `--exportable`. It writes
`cert.pem`, `chain.pem`, `fullchain.pem` and `privkey.pem` to `--out-dir`
(default `./<certificate id>`) with mode 0600, never over a file without
`--force` nor through a symbolic link, and prints the key only with `--stdout`.
Every export emails your owners and admins. `export`, `revoke` and `delete`
ask first (`--yes` to skip).

### MCP

`pantech mcp install <client>` connects an AI app to Pantech through the Model
Context Protocol, so it can list, create and manage your cloud for you. The
clients are `claude-code`, `claude`, `cursor`, `vscode`, `windsurf`, `codex`,
`gemini`, `chatgpt` and `grok` (`pantech mcp list-clients`).

By default the app uses the hosted server, `https://mcp.pantechdynamics.com/mcp`:
the first time, it opens your browser and you approve it in the console, choosing
the organization, what it may do (read only, manage, or manage and delete) and
for how long. Revoke it there any time. `--local` runs
[`pantech-mcp`](https://pantechdynamics.com/mcp) on this machine instead
(`pantech-mcp stdio`), with your signed-in profile or `PANTECH_API_KEY`.

| Client | What `install` does |
| --- | --- |
| `claude-code` | `claude mcp add --scope user …`, or edits `~/.claude.json` |
| `claude` | Prints the connector steps; `--local` edits Claude Desktop's `claude_desktop_config.json` |
| `cursor` | Edits `~/.cursor/mcp.json` |
| `vscode` | Edits your user `mcp.json` (`~/Library/Application Support/Code/User`, `~/.config/Code/User`, `%APPDATA%\Code\User`) |
| `windsurf` | Edits `~/.codeium/windsurf/mcp_config.json` |
| `codex` | Edits `~/.codex/config.toml` (or `$CODEX_HOME`); then run `codex mcp login pantech` |
| `gemini` | `gemini mcp add --scope user …`, or edits `~/.gemini/settings.json` |
| `chatgpt` | Prints the connector steps |
| `grok` | Prints the xAI API tool to add, with a Pantech API key (read-only recommended) |

A config file keeps every other server and setting; the old one is saved as
`<file>.bak`, and the new one is readable only by you. A file the CLI cannot read
is left alone. `--print` shows what would be added and changes nothing.
`pantech mcp status` shows the hosted server, whether `pantech-mcp` is
installed, and which apps already have Pantech.

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Done |
| 1 | Anything else |
| 2 | The command was used wrongly |
| 3 | Not signed in, or the key was refused or lacks the scope |
| 4 | Not found |
| 5 | An operation, order (including a domain order), connection check, scan, DNS record change or certificate request finished, but failed |
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
and date (`## v0.3.0 — 2026-11-02`), keep an empty `## Unreleased` above it, and
merge to `main`. `.github/workflows/auto-release.yml` sees a version that has no
tag yet, tags it and runs the release; a push whose newest version is already
tagged releases nothing.

Tagging by hand still works, for a pre-release for example:

```sh
git tag v0.3.0-rc.1 && git push origin v0.3.0-rc.1
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
