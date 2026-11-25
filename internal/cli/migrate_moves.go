package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

// The clouds Migrate reads, the Pantech Migrate agent, invites, and the
// moves: bucket imports, server copies, database imports and Kubernetes
// moves. Each move is free; what it creates on Pantech bills as usual, so
// the steps that create a billed resource or delete data ask first.

var (
	setupFilePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,100}$`)
	importIDPattern  = regexp.MustCompile(`^[a-z]+_[A-Za-z0-9]+$`)
)

// setupExt is the file extension of a setup's main file.
func setupExt(format string) string {
	switch format {
	case "cloudformation":
		return ".json"
	case "shell":
		return ".sh"
	}
	return ".md"
}

// saveSetup writes a non-AWS connection's setup: its main file and further
// files, all checked before any is written. It returns the paths written.
func saveSetup(conn *api.CloudConnection, templateOut string, force bool) ([]string, error) {
	if !connectionIDPattern.MatchString(conn.ID) {
		return nil, fmt.Errorf("the API answered with an unexpected connection id %q", output.Clean(conn.ID))
	}
	main := templateOut
	if main == "" {
		main = "pantech-migrate-" + conn.ID + setupExt(conn.Setup.TemplateFormat)
	}
	files := []pendingFile{{path: main, data: textFile(conn.Setup.Template)}}
	for _, f := range conn.Setup.Files {
		if !setupFilePattern.MatchString(f.Name) {
			return nil, fmt.Errorf("the API named a setup file %q, which is not a plain file name: nothing was written", output.Clean(f.Name))
		}
		files = append(files, pendingFile{path: "pantech-migrate-" + conn.ID + "-" + f.Name, data: textFile(f.Content)})
	}
	paths := make([]string, len(files))
	for i, f := range files {
		if err := checkTarget(f.path, force); err != nil {
			if errors.Is(err, errFileExists) {
				return nil, fmt.Errorf("%w: pass --force to overwrite it", err)
			}
			return nil, err
		}
		paths[i] = f.path
	}
	if err := writeFiles(files, 0o644); err != nil {
		return nil, err
	}
	return paths, nil
}

func textFile(s string) []byte {
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return []byte(s)
}

// setupNext is what to do after connecting a provider other than AWS.
func setupNext(conn *api.CloudConnection) [][2]string {
	id := conn.ID
	switch conn.Provider {
	case "gcp":
		return [][2]string{{"Run the gcloud script, then", "pantech migrate set-federation " + id + " --gcp-project-number N --gcp-pool-id ID --gcp-provider-id ID --gcp-service-account EMAIL --gcp-project PROJECT --wait"}}
	case "azure":
		return [][2]string{{"Run the Azure CLI script, then", "pantech migrate set-federation " + id + " --azure-tenant-id ID --azure-client-id ID --azure-subscription ID --wait"}}
	case "huawei":
		return [][2]string{{"Follow the steps, then", "pantech migrate set-federation " + id + " --huawei-idp-id ID --huawei-project REGION=PROJECT_ID --wait"}}
	}
	return [][2]string{{"Make a read-only token, then", "pantech migrate agent enroll " + id}}
}

// --- providers, federation, agent, invites ---

func newMigrateProvidersCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "providers",
		Short: "List the clouds Migrate can read, and how",
		Long: `List every cloud Pantech Migrate can connect. federated: Pantech signs
short-lived tokens your cloud trusts (AWS, Google Cloud, Azure, Huawei Cloud).
agent: you run the Pantech Migrate agent, which keeps your read-only token on
your machine. "spend" and "people" say whether a scan reads them.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			list, raw, err := listItems[api.CloudProvider](cmd, c, "/cloud-providers", nil)
			if err != nil {
				return err
			}
			if printList(a, list, raw, func(p api.CloudProvider) string { return p.Slug }, "No clouds are available.", "") {
				return nil
			}
			yes := func(b bool) string {
				if b {
					return "yes"
				}
				return "no"
			}
			rows := make([][]string, len(list))
			for i, p := range list {
				rows[i] = []string{p.Slug, p.Name, p.Access, yes(p.CostReadable), yes(p.PeopleReadable), yes(p.Available)}
			}
			a.out.Table([]string{"provider", "name", "access", "spend", "people", "available"}, rows)
			a.out.Next("Connect one", `pantech migrate connect --provider <provider> --name "Production"`)
			return nil
		},
	}
}

func newMigrateSetFederationCmd(a *app) *cobra.Command {
	var gcpNumber, gcpPool, gcpProvider, gcpSA, azTenant, azClient, huaweiIDP string
	var gcpProjects, azSubs, huaweiProjects []string
	var wait bool
	cmd := &cobra.Command{
		Use:   "set-federation <connection id> [flags]",
		Short: "Send what a Google Cloud, Azure or Huawei Cloud setup printed, and check it",
		Long: `Send what the connection's setup printed, for exactly one cloud:

  Google Cloud  --gcp-project-number --gcp-pool-id --gcp-provider-id
                --gcp-service-account and --gcp-project (1 to 20, repeat it)
  Azure         --azure-tenant-id --azure-client-id and --azure-subscription
                (1 to 20, repeat it)
  Huawei Cloud  --huawei-idp-id and --huawei-project REGION=PROJECT_ID
                (1 to 20, repeat it)

These are ids, not secrets. Pantech then checks its read-only access, usually
within a minute. --wait follows the check.`,
		Example: `  pantech migrate set-federation ccn_123 --gcp-project-number 123456789012 --gcp-pool-id pantech-migrate \
    --gcp-provider-id ccn-123 --gcp-service-account pantech-migrate@web.iam.gserviceaccount.com --gcp-project web --wait
  pantech migrate set-federation ccn_456 --azure-tenant-id <tenant> --azure-client-id <app> --azure-subscription <sub>`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fl := cmd.Flags()
			gcp := fl.Changed("gcp-project-number") || fl.Changed("gcp-pool-id") || fl.Changed("gcp-provider-id") || fl.Changed("gcp-service-account") || len(gcpProjects) > 0
			azure := fl.Changed("azure-tenant-id") || fl.Changed("azure-client-id") || len(azSubs) > 0
			huawei := fl.Changed("huawei-idp-id") || len(huaweiProjects) > 0
			n := 0
			for _, set := range []bool{gcp, azure, huawei} {
				if set {
					n++
				}
			}
			if n != 1 {
				return &usageError{errors.New("pass the --gcp-*, the --azure-* or the --huawei-* flags: one cloud's setup output"), cmd}
			}
			var body map[string]any
			switch {
			case gcp:
				if gcpNumber == "" || gcpPool == "" || gcpProvider == "" || gcpSA == "" || len(gcpProjects) == 0 {
					return &usageError{errors.New("Google Cloud needs --gcp-project-number, --gcp-pool-id, --gcp-provider-id, --gcp-service-account and --gcp-project"), cmd}
				}
				body = map[string]any{"gcp": map[string]any{"project_number": gcpNumber, "pool_id": gcpPool, "provider_id": gcpProvider,
					"service_account_email": gcpSA, "project_ids": gcpProjects}}
			case azure:
				if azTenant == "" || azClient == "" || len(azSubs) == 0 {
					return &usageError{errors.New("Azure needs --azure-tenant-id, --azure-client-id and --azure-subscription"), cmd}
				}
				body = map[string]any{"azure": map[string]any{"tenant_id": azTenant, "client_id": azClient, "subscription_ids": azSubs}}
			default:
				if huaweiIDP == "" || len(huaweiProjects) == 0 {
					return &usageError{errors.New("Huawei Cloud needs --huawei-idp-id and --huawei-project REGION=PROJECT_ID"), cmd}
				}
				projects := make([]map[string]string, len(huaweiProjects))
				for i, p := range huaweiProjects {
					region, id, ok := strings.Cut(p, "=")
					if !ok || region == "" || id == "" {
						return &usageError{fmt.Errorf("--huawei-project %q: use REGION=PROJECT_ID", output.Clean(p)), cmd}
					}
					projects[i] = map[string]string{"region": region, "id": id}
				}
				body = map[string]any{"huawei": map[string]any{"identity_provider_id": huaweiIDP, "projects": projects}}
			}
			return a.updateConnection(cmd, args[0], body, wait, "Sent the setup of %s; checking it.")
		},
	}
	f := cmd.Flags()
	f.StringVar(&gcpNumber, "gcp-project-number", "", "Google Cloud: the project number the script printed")
	f.StringVar(&gcpPool, "gcp-pool-id", "", "Google Cloud: the workload identity pool id")
	f.StringVar(&gcpProvider, "gcp-provider-id", "", "Google Cloud: the pool provider id")
	f.StringVar(&gcpSA, "gcp-service-account", "", "Google Cloud: the service account email")
	f.StringArrayVar(&gcpProjects, "gcp-project", nil, "Google Cloud: a project id to scan (repeat it)")
	f.StringVar(&azTenant, "azure-tenant-id", "", "Azure: the tenant id")
	f.StringVar(&azClient, "azure-client-id", "", "Azure: the app registration's application (client) id")
	f.StringArrayVar(&azSubs, "azure-subscription", nil, "Azure: a subscription id to scan (repeat it)")
	f.StringVar(&huaweiIDP, "huawei-idp-id", "", "Huawei Cloud: the identity provider's id")
	f.StringArrayVar(&huaweiProjects, "huawei-project", nil, "Huawei Cloud: REGION=PROJECT_ID to scan (repeat it)")
	f.BoolVar(&wait, "wait", false, "wait for the check to finish")
	return cmd
}

func newMigrateAgentCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Enroll or revoke the Pantech Migrate agent of a connection",
		Long: `Any connection can have one Pantech Migrate agent, which you run in your own
network. On DigitalOcean, Hetzner, Linode, Vultr, Contabo and OpenStack it scans,
with a read-only token in its environment. On AWS, Google Cloud, Azure and Huawei
Cloud Pantech scans the cloud itself, and the agent, which needs no token there,
runs the copy steps that read from inside your network: server copies, database
imports and Kubernetes moves. Tokens, database connection strings and
kubeconfigs never leave the agent's machine.

A connection has one agent at a time: enrolling one on a server to copy it
replaces the connection's long-running agent until you enroll that one again.`,
	}
	cmd.AddCommand(newMigrateAgentEnrollCmd(a), newMigrateAgentRevokeCmd(a))
	return cmd
}

func newMigrateAgentEnrollCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "enroll <connection id>",
		Short: "Issue a one-time enrollment code and print the command that uses it",
		Long: `Issue a one-time enrollment code for a connection's agent (any connection)
and print the command that uses it, alone on stdout: run it where the agent will
run. The code works once, for an hour, and is shown only now. Issuing a code
replaces any unused one; using it replaces the connection's earlier agent.
Owners and admins only.`,
		Example: `  pantech migrate agent enroll ccn_123
  pantech migrate agent enroll ccn_123 --json   # the code, its expiry and the command`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var e api.CloudAgentEnrollment
			// Each call issues a new code, so it is never retried.
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: connectionPath(args[0]) + "/agent-enrollments", NoRetry: true}, &e)
			if err != nil {
				return migrateError(err, args[0])
			}
			if a.out.JSON {
				a.out.RawJSON(res.Body)
				return nil
			}
			a.out.Line("%s", output.Clean(e.EnrollCommand))
			if !a.out.Quiet {
				a.out.Note("\nRun that where the agent will run. The code works once, until %s, and is not shown again.", output.When(&e.ExpiresAt))
				a.out.Note("Then keep it running (pantech-migrate-agent run), with your read-only token in its environment where the agent scans; to copy a server, run it on that server as root with run --server-copy <copy id>.")
				a.out.Next("Then", "pantech migrate connection "+output.Clean(args[0]))
			}
			return nil
		},
	}
}

func newMigrateAgentRevokeCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <connection id>",
		Short: "End a connection's agent at once",
		Long:  `End a connection's Pantech Migrate agent: its credential stops working on its next request, and any server copy upload it was running ends. The connection then waits for a new agent. Asks first (--yes to skip).`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Revoke the agent of %s? It stops working at once.", output.Clean(args[0]))); err != nil {
				return err
			}
			var conn api.CloudConnection
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodDelete, Path: connectionPath(args[0]) + "/agent"}, &conn)
			if err != nil {
				return migrateError(err, args[0])
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Revoked the agent of %s.", conn.ID)
				a.out.Note("Stop it on its machine and delete its state file, and delete any read-only token it used in the other cloud.")
			}
			a.printConnection(&conn, res.Body)
			return nil
		},
	}
}

func newMigrateInviteCmd(a *app) *cobra.Command {
	var keys []string
	cmd := &cobra.Command{
		Use:   "invite <migration id> --key KEY [--key KEY]...",
		Short: "Invite a plan's people to your organization, with their planned roles",
		Long: `Send Pantech invites to the people a plan lists (its user items), by key
(pantech migrate show lists them), each with the role the plan gives it,
through the ordinary invite flow. An admin can invite members only. Safe to
repeat: nobody is invited twice. Asks first (--yes to skip). Owners and admins only.`,
		Example: `  pantech migrate invite mig_123 --key user:global:alice --key user:global:bob`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(keys) == 0 || len(keys) > 50 {
				return &usageError{errors.New("pass 1 to 50 --key: the plan's user items"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Invite %s from %s to your organization?", count(len(keys), "person"), output.Clean(args[0]))); err != nil {
				return err
			}
			var out struct {
				Results []api.MigrationInviteResult `json:"results"`
			}
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: migrationPath(args[0]) + "/invites", Body: map[string]any{"keys": keys}}, &out)
			if err != nil {
				return migrateError(api.AsFlags(err, map[string]string{"keys": "--key"}), args[0])
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
				return nil
			case a.out.Quiet:
				for _, r := range out.Results {
					a.out.Line("%s %s", output.Clean(r.Key), output.Clean(r.Outcome))
				}
				return nil
			}
			rows := make([][]string, len(out.Results))
			for i, r := range out.Results {
				outcome := r.Outcome
				if r.Reason != nil {
					outcome += " (" + *r.Reason + ")"
				}
				rows[i] = []string{r.Key, output.Or(r.Email), output.Or(r.Role), outcome}
			}
			a.out.Table([]string{"key", "email", "role", "outcome"}, rows)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&keys, "key", nil, "a user item's key (repeat it; required)")
	return cmd
}

// --- moves: shared ---

// moveFamily is one kind of move: where it lives and how it is shown.
type moveFamily struct {
	what     string // "bucket import"
	path     string // "/bucket-imports"
	listHead []string
}

func (m moveFamily) item(id string) string { return m.path + "/" + url.PathEscape(id) }

// failurePair is a failure as a detail line, or nothing.
func failurePair(f *api.CloudFailure) []output.Pair {
	if f == nil {
		return nil
	}
	return []output.Pair{{"Failure", failureText(f)}}
}

// actOn sends one POST to a move and prints the move it answers with.
func (a *app) actOn(cmd *cobra.Command, path string, body any, out any, show func([]byte)) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: path, Body: body}, out)
	if err != nil {
		return err
	}
	show(res.Body)
	return nil
}

// --- buckets ---

var bucketImports = moveFamily{"bucket import", "/bucket-imports", []string{"id", "source", "destination", "status", "pass", "copied", "created"}}

func bucketImportBusy(s string) bool { return s == "queued" || s == "running" }

func bucketImportDetail(out *output.Printer, b *api.BucketImport) output.Detail {
	p := &b.Progress
	d := output.Detail{
		Title:    "Bucket import of " + b.Source.Bucket,
		State:    out.State(b.Status),
		Subtitle: b.ID,
		Sections: [][]output.Pair{
			failurePair(b.Failure),
			{{"Source", b.Source.Provider + " " + b.Source.Bucket}, {"Region", output.Or(b.Source.Region)}, {"Into", b.Destination.BucketName + " (" + b.Destination.BucketID + ")"}},
			{{"Pass", strconv.Itoa(b.Pass)}, {"Copied", fmt.Sprintf("%d objects, %s", p.ObjectsCopied, bytesText(p.BytesCopied))},
				{"Unchanged", fmt.Sprintf("%d objects", p.ObjectsSkipped)}, {"Failed", fmt.Sprintf("%d objects", p.ObjectsFailed)},
				{"Every pass", bytesText(b.TotalBytesCopied)}},
			{{"Created", output.When(b.CreatedAt)}, {"Finished", output.When(b.FinishedAt)}},
		},
	}
	for i, f := range b.Failures {
		if i == 10 {
			d.Sections = append(d.Sections, []output.Pair{{"…", fmt.Sprintf("%d more failed objects (--json lists them)", len(b.Failures)-10)}})
			break
		}
		d.Sections = append(d.Sections, []output.Pair{{"Failed", f.Key + ": " + f.Message + " (" + f.Reason + ")"}})
	}
	switch b.Status {
	case "queued", "running":
		d.Next = [][2]string{{"Follow it", "pantech migrate buckets show " + b.ID + " --wait"}}
	case "succeeded":
		d.Next = [][2]string{{"Copy what changed since", "pantech migrate buckets resync " + b.ID}}
	}
	return d
}

func (a *app) printBucketImport(b *api.BucketImport, raw []byte) {
	switch {
	case a.out.JSON:
		a.out.RawJSON(raw)
	case a.out.Quiet:
		a.out.Line("%s", output.Clean(b.ID))
	default:
		a.out.Print(bucketImportDetail(a.out, b))
	}
}

func newMigrateBucketsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "buckets",
		Short: "Copy buckets into Pantech object storage",
		Long: `Copy a source bucket's objects into a Pantech bucket. First choose the buckets
Pantech may copy with "access": it gives a CloudFormation template for a copy
role that may read only those buckets; create a stack from it, then send its
CopyRoleArn output. Once a bucket is verified, "start" copies it; "resync"
copies only what changed since. The move is free; storage bills as usual.`,
	}
	cmd.AddCommand(newMigrateBucketsAccessCmd(a), newMigrateMoveListCmd(a, bucketImports),
		newMigrateBucketsShowCmd(a), newMigrateBucketsStartCmd(a), newMigrateBucketsResyncCmd(a), newMigrateBucketsCancelCmd(a))
	return cmd
}

func newMigrateBucketsAccessCmd(a *app) *cobra.Command {
	var buckets []string
	var roleARN, templateOut string
	var retry, force bool
	cmd := &cobra.Command{
		Use:   "access <connection id> [--bucket NAME]... [--role-arn ARN] [--retry]",
		Short: "Show or set which buckets Pantech may copy, and the copy role",
		Long: `Show a connection's copy access, or change it: --bucket (repeat it, at most 50)
chooses the buckets Pantech may copy and replaces the list; --role-arn sends
the copy stack's CopyRoleArn output; --retry verifies the role again. Pantech
verifies the role against each bucket without reading any object.

When the copy role's template is ready it is saved to
./pantech-migrate-<connection id>-copy.json (--template-out to choose); a file
already there is kept unless you pass --force.`,
		Example: `  pantech migrate buckets access ccn_123 --bucket logs --bucket assets
  pantech migrate buckets access ccn_123 --role-arn arn:aws:iam::123456789012:role/PantechCopy`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			body := map[string]any{}
			if len(buckets) > 0 {
				body["buckets"] = buckets
			}
			if roleARN = strings.TrimSpace(roleARN); roleARN != "" {
				body["role_arn"] = roleARN
			}
			if retry {
				body["retry"] = true
			}
			req := api.Request{Method: http.MethodGet, Path: connectionPath(args[0]) + "/copy-access"}
			if len(body) > 0 {
				req.Method, req.Body = http.MethodPut, body
			}
			var ca api.CopyAccess
			res, err := c.Do(ctx(cmd), req, &ca)
			if err != nil {
				return migrateError(api.AsFlags(err, map[string]string{"buckets": "--bucket", "role_arn": "--role-arn"}), args[0])
			}
			saved := ""
			if ca.Setup.Template != nil && *ca.Setup.Template != "" && (len(buckets) > 0 || templateOut != "") {
				path := templateOut
				if path == "" {
					if !connectionIDPattern.MatchString(ca.ConnectionID) {
						return fmt.Errorf("the API answered with an unexpected connection id %q: pass --template-out", output.Clean(ca.ConnectionID))
					}
					path = "pantech-migrate-" + ca.ConnectionID + "-copy.json"
				}
				if err := checkTarget(path, force); err != nil {
					if errors.Is(err, errFileExists) {
						return &usageError{fmt.Errorf("%w: pass --force to overwrite it, or another --template-out", err), cmd}
					}
					return err
				}
				if err := writeFiles([]pendingFile{{path: path, data: textFile(*ca.Setup.Template)}}, 0o644); err != nil {
					return err
				}
				saved = path
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
				return nil
			case a.out.Quiet:
				a.out.Line("%s", output.Clean(ca.Status))
				return nil
			}
			rows := make([][]string, len(ca.Buckets))
			for i, b := range ca.Buckets {
				state := "not verified"
				if b.Ready {
					state = "verified"
				}
				if b.Failure != nil {
					state = failureText(b.Failure)
				}
				rows[i] = []string{b.Name, output.Or(b.Region), output.Or(b.Versioning), state}
			}
			a.out.Print(output.Detail{Title: "Copy access", State: a.out.State(ca.Status), Subtitle: ca.ConnectionID, Sections: [][]output.Pair{
				failurePair(ca.Failure), {{"Copy role", output.Or(ca.RoleARN)}, {"Verified", output.When(ca.VerifiedAt)}},
			}})
			if len(rows) > 0 {
				a.out.Table([]string{"bucket", "region", "versioning", "status"}, rows)
			}
			if saved != "" {
				a.out.Note("\nSaved the copy role's CloudFormation template to %s. Create a stack from it, then send its CopyRoleArn output:", saved)
				a.out.Next("Then", "pantech migrate buckets access "+ca.ConnectionID+" --role-arn <CopyRoleArn output>")
			} else if ca.Status == "verified" {
				a.out.Next("Copy one", "pantech migrate buckets start --connection "+ca.ConnectionID+" --source <bucket> --to-new <name>")
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&buckets, "bucket", nil, "a source bucket Pantech may copy (repeat it; replaces the list)")
	f.StringVar(&roleARN, "role-arn", "", "the copy stack's CopyRoleArn output")
	f.BoolVar(&retry, "retry", false, "verify the copy role again")
	f.StringVar(&templateOut, "template-out", "", "where to save the copy role's template (default ./pantech-migrate-<id>-copy.json)")
	f.BoolVar(&force, "force", false, "overwrite a template file already there")
	return cmd
}

// newMigrateMoveListCmd lists one family of moves.
func newMigrateMoveListCmd(a *app, fam moveFamily) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List " + fam.what + "s, newest first",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			switch fam.path {
			case "/bucket-imports":
				items, raw, err := listItems[api.BucketImport](cmd, c, fam.path, nil)
				if err != nil || printList(a, items, raw, func(b api.BucketImport) string { return b.ID }, "No "+fam.what+"s yet.", "") {
					return err
				}
				rows := make([][]string, len(items))
				for i, b := range items {
					rows[i] = []string{a.out.Dim(b.ID), b.Source.Bucket, b.Destination.BucketName, a.out.State(b.Status), strconv.Itoa(b.Pass), bytesText(b.TotalBytesCopied), output.Ago(b.CreatedAt)}
				}
				a.out.Table(fam.listHead, rows)
				a.out.Summary(count(len(items), fam.what))
			case "/server-copies":
				items, raw, err := listItems[api.ServerCopy](cmd, c, fam.path, nil)
				if err != nil || printList(a, items, raw, func(s api.ServerCopy) string { return s.ID }, "No "+fam.what+"s yet.", "") {
					return err
				}
				rows := make([][]string, len(items))
				for i, s := range items {
					rows[i] = []string{a.out.Dim(s.ID), s.Instance.Name, s.Instance.PlanSlug, a.out.State(s.Status), copyProgress(&s), output.Ago(s.CreatedAt)}
				}
				a.out.Table(fam.listHead, rows)
				a.out.Summary(count(len(items), fam.what))
			case "/database-imports":
				items, raw, err := listItems[api.DatabaseImport](cmd, c, fam.path, nil)
				if err != nil || printList(a, items, raw, func(d api.DatabaseImport) string { return d.ID }, "No "+fam.what+"s yet.", "") {
					return err
				}
				rows := make([][]string, len(items))
				for i, d := range items {
					rows[i] = []string{a.out.Dim(d.ID), d.Engine, d.Mode, d.DestinationDatabaseID, a.out.State(d.Status), output.Ago(d.CreatedAt)}
				}
				a.out.Table(fam.listHead, rows)
				a.out.Summary(count(len(items), fam.what))
			default:
				items, raw, err := listItems[api.KubernetesMove](cmd, c, fam.path, nil)
				if err != nil || printList(a, items, raw, func(m api.KubernetesMove) string { return m.ID }, "No "+fam.what+"s yet.", "") {
					return err
				}
				rows := make([][]string, len(items))
				for i, m := range items {
					rows[i] = []string{a.out.Dim(m.ID), m.SourceName, m.ClusterID, a.out.State(m.Status), strconv.Itoa(m.Summary.Objects), output.Ago(m.CreatedAt)}
				}
				a.out.Table(fam.listHead, rows)
				a.out.Summary(count(len(items), fam.what))
			}
			return nil
		},
	}
}

func newMigrateBucketsShowCmd(a *app) *cobra.Command {
	var wait bool
	cmd := &cobra.Command{
		Use:   "show <import id>",
		Short: "Show a bucket import: progress, failed objects, audit trail",
		Long:  `Show a bucket import: its status and its pass's progress, and the first objects that failed it and why. --wait follows one that is queued or running (checking every 3 seconds); a failed import exits with 5.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var raw []byte
			var b *api.BucketImport
			if wait {
				steps := a.out.Steps()
				steps.Start("Copying")
				b, err = api.WaitMove[api.BucketImport](ctx(cmd), c, bucketImports.item(args[0]), "bucket import",
					func(b *api.BucketImport) (string, string, *api.CloudFailure) { return b.ID, b.Status, b.Failure }, bucketImportBusy, nil, &raw)
				if err != nil {
					steps.Fail("", "")
					if b == nil {
						return err
					}
				} else {
					steps.Done("", "")
				}
			} else {
				b = &api.BucketImport{}
				res, err2 := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: bucketImports.item(args[0])}, b)
				if err2 != nil {
					return err2
				}
				raw = res.Body
			}
			a.printBucketImport(b, raw)
			return err
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait while the import is queued or running")
	return cmd
}

func newMigrateBucketsStartCmd(a *app) *cobra.Command {
	var connection, source, toNew, to, migration string
	var versioning, publicRead bool
	cmd := &cobra.Command{
		Use:   "start --connection ID --source BUCKET (--to-new NAME | --to BUCKET_ID)",
		Short: "Copy a source bucket into a new or existing Pantech bucket",
		Long: `Copy one source bucket's objects into a Pantech bucket: a new one (--to-new,
created now and billed as any bucket) or one you have (--to). The bucket must
be verified in "pantech migrate buckets access". Objects are checked against
their MD5s and keep their metadata and tags; only current versions are copied
and nothing is deleted on either side. Asks first (--yes to skip).`,
		Example: `  pantech migrate buckets start --connection ccn_123 --source logs --to-new acme-logs
  pantech migrate buckets start --connection ccn_123 --source assets --to bkt_456 --migration mig_789`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if (toNew == "") == (to == "") {
				return &usageError{errors.New("pass --to-new NAME (a new bucket) or --to BUCKET_ID, not both"), cmd}
			}
			body := map[string]any{"connection_id": connection, "source_bucket": source}
			if toNew != "" {
				body["destination_bucket_name"] = toNew
			} else {
				body["destination_bucket_id"] = to
			}
			if versioning {
				body["versioning"] = true
			}
			if publicRead {
				body["public_read"] = true
			}
			if migration != "" {
				body["migration_id"] = migration
			}
			question := fmt.Sprintf("Copy bucket %s into Pantech? The copy is free; storage is billed per GiB-month.", output.Clean(source))
			if publicRead {
				question += " The new bucket will be readable by anyone."
			}
			if err := a.confirm(question); err != nil {
				return err
			}
			var b api.BucketImport
			return a.actOn(cmd, bucketImports.path, body, &b, func(raw []byte) {
				if !a.out.JSON && !a.out.Quiet {
					a.out.Success("Started bucket import %s.", b.ID)
				}
				a.printBucketImport(&b, raw)
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&connection, "connection", "", "the connection whose copy access covers the bucket (required)")
	f.StringVar(&source, "source", "", "the source bucket's name (required)")
	f.StringVar(&toNew, "to-new", "", "a new Pantech bucket to create and copy into")
	f.StringVar(&to, "to", "", "an existing Pantech bucket to copy into (bkt_…)")
	f.BoolVar(&versioning, "versioning", false, "turn versioning on in a new bucket")
	f.BoolVar(&publicRead, "public-read", false, "make a new bucket readable by anyone")
	f.StringVar(&migration, "migration", "", "the plan the move belongs to (mig_…)")
	_ = cmd.MarkFlagRequired("connection")
	_ = cmd.MarkFlagRequired("source")
	return cmd
}

func newMigrateBucketsResyncCmd(a *app) *cobra.Command {
	var mirror bool
	cmd := &cobra.Command{
		Use:   "resync <import id> [--mirror-deletes]",
		Short: "Copy what changed since the last pass",
		Long: `Queue another pass: objects already copied unchanged are skipped. Run a last
one after writes to the source stop, then switch over. --mirror-deletes also
deletes, in the Pantech bucket, the objects this import copied that the source
no longer has (never anything else); it needs a key with the delete scope and
asks first (--yes to skip).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			if mirror {
				if err := a.confirm(fmt.Sprintf("Resync %s and delete, in the Pantech bucket, what the source no longer has? Deleted objects cannot be recovered.", output.Clean(args[0]))); err != nil {
					return err
				}
				body["mirror_deletes"] = true
			}
			var b api.BucketImport
			return a.actOn(cmd, bucketImports.item(args[0])+"/resync", body, &b, func(raw []byte) { a.printBucketImport(&b, raw) })
		},
	}
	cmd.Flags().BoolVar(&mirror, "mirror-deletes", false, "also delete what the source no longer has")
	return cmd
}

func newMigrateBucketsCancelCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <import id>",
		Short: "Stop a bucket import; objects already copied stay",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var b api.BucketImport
			return a.actOn(cmd, bucketImports.item(args[0])+"/cancel", nil, &b, func(raw []byte) { a.printBucketImport(&b, raw) })
		},
	}
}

// --- servers ---

var serverCopies = moveFamily{"server copy", "/server-copies", []string{"id", "instance", "plan", "status", "progress", "created"}}

func serverCopyBusy(s string) bool {
	return s == "waiting" || s == "copying" || s == "importing" || s == "creating_instance"
}

func copyProgress(s *api.ServerCopy) string {
	p := &s.Progress
	if p.BytesTotal == 0 {
		return "—"
	}
	return fmt.Sprintf("%s of %s", bytesText(p.BytesUploaded), bytesText(p.BytesTotal))
}

func serverCopyDetail(out *output.Printer, s *api.ServerCopy) output.Detail {
	pass := "—"
	if s.Pass != nil {
		pass = fmt.Sprintf("%d (%s)", s.Pass.Number, s.Pass.Kind)
	}
	left := "—"
	if s.Progress.ETASeconds != nil {
		left = output.Elapsed(time.Duration(*s.Progress.ETASeconds) * time.Second)
	}
	d := output.Detail{
		Title:    "Server copy to " + s.Instance.Name,
		State:    out.State(s.Status),
		Subtitle: s.ID,
		Sections: [][]output.Pair{
			failurePair(s.Failure),
			{{"Plan", s.MigrationID}, {"Item", s.ItemKey}, {"Source", s.Source}, {"OS", s.OSFamily}},
			{{"Instance plan", s.Instance.PlanSlug}, {"Region", s.Instance.Region}, {"Instance", output.Or(s.Instance.ID)}},
			{{"Pass", pass}, {"Progress", copyProgress(s)}, {"Time left", left}, {"Synced", output.When(s.SyncedAt)}, {"Expires", output.When(s.ExpiresAt)}},
		},
	}
	var disks []output.Pair
	for _, dk := range s.Disks {
		disks = append(disks, output.Pair{fmt.Sprintf("Disk %d", dk.Index), dk.Role + " · " + dk.Source + " · " + bytesText(dk.SizeBytes)})
	}
	d.Sections = append(d.Sections, disks)
	var flags []output.Pair
	for _, f := range s.Flags {
		mark := ""
		if f.Blocking {
			mark = "! "
		}
		flags = append(flags, output.Pair{"Flag", mark + f.Message + " (" + f.Code + ")"})
	}
	d.Sections = append(d.Sections, flags)
	switch s.Status {
	case "waiting", "copying", "importing", "creating_instance":
		d.Next = [][2]string{{"Follow it", "pantech migrate servers show " + s.ID + " --wait"}}
	case "synced":
		d.Next = [][2]string{{"Copy what changed", "pantech migrate servers sync " + s.ID}, {"Stop the application, then", "pantech migrate servers cutover " + s.ID}}
	}
	return d
}

func (a *app) printServerCopy(s *api.ServerCopy, raw []byte) {
	switch {
	case a.out.JSON:
		a.out.RawJSON(raw)
	case a.out.Quiet:
		a.out.Line("%s", output.Clean(s.ID))
	default:
		a.out.Print(serverCopyDetail(a.out, s))
	}
}

func newMigrateServersCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "servers",
		Short: "Copy planned servers' disks into new Pantech instances",
		Long: `Copy a planned server's disks into a new Pantech instance. "start" creates
nothing yet: the first pass copies every disk, "sync" copies what changed
since, and "cutover" runs the last pass after you stop the application and
builds the instance, billed like any new instance. The copy is free.

The Pantech Migrate agent on the server sends its disks (--source agent, the
default, on any cloud), or Pantech reads AWS EBS snapshots through a separate
copy role (--source aws_snapshot; "template" saves its CloudFormation template).

With the agent: enroll it on the server itself for the plan's connection
("pantech migrate agent enroll"), start the copy, then run it there as root
(Administrator on Windows) with the copy's id:

  pantech-migrate-agent run --server-copy sco_…

Only an agent started that way is handed the copy, and it reads only that
server's own disks: the boot disk, unless --disk names each one (boot disk
first). A connection has one agent, so this replaces its long-running agent
until you enroll that one again.`,
	}
	cmd.AddCommand(newMigrateMoveListCmd(a, serverCopies), newMigrateServersShowCmd(a), newMigrateServersStartCmd(a),
		newMigrateServersTemplateCmd(a), newMigrateServersActCmd(a, "sync"), newMigrateServersActCmd(a, "cutover"), newMigrateServersActCmd(a, "cancel"))
	return cmd
}

func newMigrateServersShowCmd(a *app) *cobra.Command {
	var wait bool
	cmd := &cobra.Command{
		Use:   "show <copy id>",
		Short: "Show a server copy: its pass, progress and findings",
		Long:  `Show a server copy. --wait follows it while a pass is running or the instance is being made (checking every 3 seconds); a failed copy exits with 5.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var raw []byte
			s := &api.ServerCopy{}
			if wait {
				steps := a.out.Steps()
				steps.Start("Copying")
				s, err = api.WaitMove[api.ServerCopy](ctx(cmd), c, serverCopies.item(args[0]), "server copy",
					func(s *api.ServerCopy) (string, string, *api.CloudFailure) { return s.ID, s.Status, s.Failure }, serverCopyBusy,
					func(st string) { steps.Start(strings.ReplaceAll(st, "_", " ")) }, &raw)
				if err != nil {
					steps.Fail("", "")
					if s == nil {
						return err
					}
				} else {
					steps.Done("", "")
				}
			} else {
				res, err2 := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: serverCopies.item(args[0])}, s)
				if err2 != nil {
					return err2
				}
				raw = res.Body
			}
			a.printServerCopy(s, raw)
			return err
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait while a pass is running")
	return cmd
}

func newMigrateServersStartCmd(a *app) *cobra.Command {
	var key, source, name, plan, subnet, sg, sshKey, roleARN string
	var snapshots []string
	var createSnapshots bool
	cmd := &cobra.Command{
		Use:   "start <migration id> --key KEY [flags]",
		Short: "Start copying a planned server (nothing is created or billed yet)",
		Long: `Start copying one planned server (--key, from pantech migrate show) into a new
Pantech instance with the item's plan (or --plan), and --subnet or
--security-group, and optionally --ssh-key. Nothing is created yet and the
copy is free.

--source agent (the default): the Pantech Migrate agent on the server checks it
and sends its disks; run it there with --server-copy and this copy's id. --source aws_snapshot: Pantech reads EBS snapshots
through the copy role from "pantech migrate servers template" (--copy-role-arn),
and either snapshots the volumes itself (--create-snapshots) or reads the ones
you name (--snapshot VOLUME_ID=SNAPSHOT_ID, repeat it). Windows servers are
copied with the agent only. Owners and admins only.`,
		Example: `  pantech migrate servers start mig_123 --key instance:us-east-1:i-0abc --security-group sg_web
  pantech migrate servers start mig_123 --key instance:us-east-1:i-0abc --subnet snet_1 \
    --source aws_snapshot --copy-role-arn arn:aws:iam::123456789012:role/PantechCopy --create-snapshots`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{"item_key": key}
			switch source {
			case "", "agent":
				if roleARN != "" || createSnapshots || len(snapshots) > 0 {
					return &usageError{errors.New("--copy-role-arn, --create-snapshots and --snapshot are for --source aws_snapshot"), cmd}
				}
			case "aws_snapshot":
				if strings.TrimSpace(roleARN) == "" {
					return &usageError{errors.New("--source aws_snapshot needs --copy-role-arn: the copy stack's CopyRoleArn output"), cmd}
				}
				awsBody := map[string]any{"copy_role_arn": strings.TrimSpace(roleARN)}
				if createSnapshots {
					awsBody["create_snapshots"] = true
				}
				if len(snapshots) > 0 {
					m, err := snapshotMap(snapshots)
					if err != nil {
						return &usageError{err, cmd}
					}
					awsBody["snapshots"] = m
				}
				body["source"] = source
				body["aws"] = awsBody
			default:
				return &usageError{errors.New("--source must be agent or aws_snapshot"), cmd}
			}
			for k, v := range map[string]string{"name": name, "plan_slug": plan, "subnet_id": subnet, "security_group_id": sg, "ssh_key_id": sshKey} {
				if v != "" {
					body[k] = v
				}
			}
			var s api.ServerCopy
			return a.actOn(cmd, migrationPath(args[0])+"/server-copies", body, &s, func(raw []byte) {
				if !a.out.JSON && !a.out.Quiet {
					a.out.Success("Started server copy %s. Nothing is created until the cut-over.", s.ID)
					if source == "" || source == "agent" {
						a.out.Note("On the server, as root: pantech-migrate-agent run --server-copy %s", output.Clean(s.ID))
					}
				}
				a.printServerCopy(&s, raw)
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&key, "key", "", "the planned server's item key (required)")
	f.StringVar(&source, "source", "agent", "agent or aws_snapshot")
	f.StringVar(&name, "name", "", "the new instance's name (default: the source's)")
	f.StringVar(&plan, "plan", "", "another Pantech plan than the item's")
	f.StringVar(&subnet, "subnet", "", "the new instance's subnet (snet_…)")
	f.StringVar(&sg, "security-group", "", "the new instance's security group (sg_…)")
	f.StringVar(&sshKey, "ssh-key", "", "an SSH key to add (sshk_…); the disk keeps its own users and keys")
	f.StringVar(&roleARN, "copy-role-arn", "", "aws_snapshot: the copy stack's CopyRoleArn output")
	f.BoolVar(&createSnapshots, "create-snapshots", false, "aws_snapshot: the copy role snapshots the volumes itself")
	f.StringArrayVar(&snapshots, "snapshot", nil, "aws_snapshot: VOLUME_ID=SNAPSHOT_ID for the first pass (repeat it)")
	_ = cmd.MarkFlagRequired("key")
	return cmd
}

func snapshotMap(pairs []string) (map[string]string, error) {
	m := map[string]string{}
	for _, p := range pairs {
		vol, snap, ok := strings.Cut(p, "=")
		if !ok || vol == "" || snap == "" {
			return nil, fmt.Errorf("--snapshot %q: use VOLUME_ID=SNAPSHOT_ID", output.Clean(p))
		}
		m[vol] = snap
	}
	return m, nil
}

func newMigrateServersTemplateCmd(a *app) *cobra.Command {
	var key, out string
	var createSnapshots, force bool
	cmd := &cobra.Command{
		Use:   "template <migration id> --key KEY",
		Short: "Save the AWS copy role's CloudFormation template for one planned server",
		Long: `Save the CloudFormation template of the AWS copy role for one planned server,
for --source aws_snapshot: it may read only snapshots tagged with this
connection's id, and with --create-snapshots also snapshot that server's own
volumes and delete the snapshots it made. Deploy it after the connect stack;
the read-only role is never changed. Saved to
./pantech-migrate-<migration id>-server-copy.json unless --out names a file.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := out
			if path == "" {
				if !migrationIDPattern.MatchString(args[0]) {
					return &usageError{fmt.Errorf("%q is not a migration id (mig_…): pass --out to name the file", output.Clean(args[0])), cmd}
				}
				path = "pantech-migrate-" + args[0] + "-server-copy.json"
			}
			if err := checkTarget(path, force); err != nil {
				if errors.Is(err, errFileExists) {
					return &usageError{fmt.Errorf("%w: pass --force to overwrite it, or --out", err), cmd}
				}
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			q := url.Values{"item_key": {key}}
			if createSnapshots {
				q.Set("create_snapshots", "true")
			}
			var t api.ServerCopyTemplate
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: migrationPath(args[0]) + "/server-copy-template", Query: q}, &t)
			if err != nil {
				return migrateError(err, args[0])
			}
			if err := writeFiles([]pendingFile{{path: path, data: textFile(t.Template)}}, 0o644); err != nil {
				return err
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			default:
				a.out.Line("%s", path)
				if !a.out.Quiet {
					a.out.Success("Saved the copy role's template. Create a stack from it in the AWS account, then pass its CopyRoleArn output to --copy-role-arn.")
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&key, "key", "", "the planned server's item key (required)")
	f.BoolVar(&createSnapshots, "create-snapshots", false, "let the role snapshot the server's volumes itself")
	f.StringVar(&out, "out", "", "the file to save (default ./pantech-migrate-<id>-server-copy.json)")
	f.BoolVar(&force, "force", false, "overwrite a file already there")
	_ = cmd.MarkFlagRequired("key")
	return cmd
}

func newMigrateServersActCmd(a *app, action string) *cobra.Command {
	var snapshots []string
	short := map[string]string{
		"sync":    "Copy what changed since the last pass",
		"cutover": "Run the last pass and build the instance (billed)",
		"cancel":  "Stop a server copy and delete what Pantech staged",
	}[action]
	cmd := &cobra.Command{
		Use:   action + " <copy id>",
		Short: short,
		Long: short + `. "cutover" asks first: stop the application first, and the
instance is created and billed like any new instance. "cancel" asks first too
(the source server is not touched). For an AWS copy that makes no snapshots
itself, name each disk's new snapshot with --snapshot VOLUME_ID=SNAPSHOT_ID.
--yes skips the question.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			if len(snapshots) > 0 {
				m, err := snapshotMap(snapshots)
				if err != nil {
					return &usageError{err, cmd}
				}
				body["snapshots"] = m
			}
			switch action {
			case "cutover":
				if err := a.confirm(fmt.Sprintf("Cut over %s? Stop the application first: the instance is then created and billed like any new instance.", output.Clean(args[0]))); err != nil {
					return err
				}
			case "cancel":
				if err := a.confirm(fmt.Sprintf("Cancel %s and delete everything Pantech staged for it?", output.Clean(args[0]))); err != nil {
					return err
				}
			}
			var s api.ServerCopy
			return a.actOn(cmd, serverCopies.item(args[0])+"/"+action, body, &s, func(raw []byte) { a.printServerCopy(&s, raw) })
		},
	}
	if action != "cancel" {
		cmd.Flags().StringArrayVar(&snapshots, "snapshot", nil, "VOLUME_ID=SNAPSHOT_ID for this pass (repeat it)")
	}
	return cmd
}

// --- databases ---

var databaseImports = moveFamily{"database import", "/database-imports", []string{"id", "engine", "mode", "into", "status", "created"}}

func databaseImportBusy(s string) bool {
	switch s {
	case "awaiting_agent", "preflight", "copying", "cutting_over", "verifying", "cancelling":
		return true
	}
	return false
}

func databaseImportDetail(out *output.Printer, d *api.DatabaseImport) output.Detail {
	lag := "—"
	if d.LagBytes != nil {
		lag = bytesText(*d.LagBytes)
	}
	agent := "not connected"
	if d.AgentConnected {
		agent = "connected"
	}
	det := output.Detail{
		Title:    "Database import into " + d.TargetDatabase,
		State:    out.State(d.Status),
		Subtitle: d.ID,
		Sections: [][]output.Pair{
			failurePair(d.Failure),
			{{"Plan", d.MigrationID}, {"Item", d.ItemKey}, {"Source", d.SourceRef}, {"Engine", d.Engine}, {"Mode", d.Mode}},
			{{"Into", d.DestinationDatabaseID}, {"Agent", agent}, {"Lag", lag}},
			{{"Created", output.When(d.CreatedAt)}, {"Finished", output.When(d.FinishedAt)}},
		},
	}
	if d.Readiness != nil {
		for _, is := range d.Readiness.Issues {
			mark := ""
			if is.Blocking {
				mark = "! "
			}
			line := mark + is.Message + " (" + is.Code + ")"
			if is.Fix != nil && *is.Fix != "" {
				line += "\n  fix: " + *is.Fix
			}
			det.Sections = append(det.Sections, []output.Pair{{"Issue", line}})
		}
	}
	var tables []output.Pair
	for _, t := range d.Tables {
		state := t.Status
		if t.Verification != nil && !t.Verification.Passed {
			state += ", verification failed"
		}
		name := t.Name
		if t.Schema != nil && *t.Schema != "" {
			name = *t.Schema + "." + t.Name
		}
		tables = append(tables, output.Pair{name, fmt.Sprintf("%s, %d rows", state, t.RowsCopied)})
	}
	det.Sections = append(det.Sections, tables)
	switch d.Status {
	case "blocked":
		det.Next = [][2]string{{"Fix the issues, then", "pantech migrate databases check " + d.ID}}
	case "replicating":
		det.Next = [][2]string{{"Stop writes to the source, then", "pantech migrate databases cutover " + d.ID}}
	case "completed":
		det.Next = [][2]string{{"Remove the import's login", "pantech migrate databases revoke-access " + d.ID}}
	}
	return det
}

func (a *app) printDatabaseImport(d *api.DatabaseImport, raw []byte) {
	switch {
	case a.out.JSON:
		a.out.RawJSON(raw)
	case a.out.Quiet:
		a.out.Line("%s", output.Clean(d.ID))
	default:
		a.out.Print(databaseImportDetail(a.out, d))
	}
}

func newMigrateDatabasesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "databases",
		Aliases: []string{"dbs"},
		Short:   "Import planned databases into your Pantech databases, through the agent",
		Long: `Move one of a plan's databases into one of your Pantech databases (same
engine, created first) through the Pantech Migrate agent in your network, on
any cloud. Name each source in the agent's configuration file, with its
connection string in an environment variable or a file only the agent's user
can read; Pantech never receives it:

  {"database_sources": {"prod-db": {"engine": "postgres", "dsn_env": "PROD_DB_DSN"}}}

"pantech-migrate-agent check" tests each source. The agent runs pg_dump or
mysqldump, so run the agent program on a host that has them, not the container
image. It checks the source first and reports what to fix. --mode dump
copies once (stop writes first); --mode replicate (PostgreSQL) follows changes
until "cutover". The move is free; the destination bills as usual.`,
	}
	cmd.AddCommand(newMigrateMoveListCmd(a, databaseImports), newMigrateDatabasesShowCmd(a), newMigrateDatabasesStartCmd(a),
		newMigrateDatabasesActCmd(a, "check", "readiness", "Run the readiness check again, after fixing what it reported"),
		newMigrateDatabasesActCmd(a, "cutover", "cutover", "Finish the import (for replicate, stop writes to the source first)"),
		newMigrateDatabasesActCmd(a, "cancel", "cancel", "Stop the import and remove its access; what was copied stays"),
		newMigrateDatabasesActCmd(a, "revoke-access", "revoke-access", "Remove the import's login from your database now; the data stays"))
	return cmd
}

func newMigrateDatabasesShowCmd(a *app) *cobra.Command {
	var wait bool
	cmd := &cobra.Command{
		Use:   "show <import id>",
		Short: "Show a database import: readiness, tables, lag",
		Long:  `Show a database import with its readiness issues (each with a copy-paste fix), per-table progress and verification. --wait follows it while it is checking, copying or cutting over (checking every 3 seconds); a failed import exits with 5.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var raw []byte
			d := &api.DatabaseImport{}
			if wait {
				steps := a.out.Steps()
				steps.Start("Importing")
				d, err = api.WaitMove[api.DatabaseImport](ctx(cmd), c, databaseImports.item(args[0]), "database import",
					func(d *api.DatabaseImport) (string, string, *api.CloudFailure) { return d.ID, d.Status, d.Failure }, databaseImportBusy,
					func(st string) { steps.Start(strings.ReplaceAll(st, "_", " ")) }, &raw)
				if err != nil {
					steps.Fail("", "")
					if d == nil {
						return err
					}
				} else {
					steps.Done("", "")
				}
			} else {
				res, err2 := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: databaseImports.item(args[0])}, d)
				if err2 != nil {
					return err2
				}
				raw = res.Body
			}
			a.printDatabaseImport(d, raw)
			return err
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait while it is checking, copying or cutting over")
	return cmd
}

func newMigrateDatabasesStartCmd(a *app) *cobra.Command {
	var key, into, sourceRef, target, mode string
	cmd := &cobra.Command{
		Use:   "start <migration id> --key KEY --into DATABASE_ID --source-ref NAME",
		Short: "Start importing one of a plan's databases",
		Long: `Start moving one of a plan's database items (--key) into one of your Pantech
databases (--into, db_…, same engine). --source-ref names the source in your
Migrate agent's configuration; its address and login stay on the agent.
--target names the database to create on the destination (it must be empty;
default: the source's name). --mode dump (default) or replicate (PostgreSQL).
At most 3 imports in progress. Owners and admins only.`,
		Example: `  pantech migrate databases start mig_123 --key database:us-east-1:orders --into db_456 --source-ref prod-db --mode replicate`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if mode != "" && mode != "dump" && mode != "replicate" {
				return &usageError{errors.New("--mode must be dump or replicate"), cmd}
			}
			body := map[string]any{"item_key": key, "destination_database_id": into, "source_ref": sourceRef}
			if target != "" {
				body["target_database"] = target
			}
			if mode != "" {
				body["mode"] = mode
			}
			var d api.DatabaseImport
			return a.actOn(cmd, migrationPath(args[0])+"/database-imports", body, &d, func(raw []byte) {
				if !a.out.JSON && !a.out.Quiet {
					a.out.Success("Started database import %s. The agent checks the source first.", d.ID)
				}
				a.printDatabaseImport(&d, raw)
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&key, "key", "", "the plan's database item key (required)")
	f.StringVar(&into, "into", "", "your Pantech database to import into, db_… (required)")
	f.StringVar(&sourceRef, "source-ref", "", "the source's name in the agent's configuration (required)")
	f.StringVar(&target, "target", "", "the database to create on the destination")
	f.StringVar(&mode, "mode", "", "dump (default) or replicate")
	_ = cmd.MarkFlagRequired("key")
	_ = cmd.MarkFlagRequired("into")
	_ = cmd.MarkFlagRequired("source-ref")
	return cmd
}

func newMigrateDatabasesActCmd(a *app, use, action, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <import id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var d api.DatabaseImport
			return a.actOn(cmd, databaseImports.item(args[0])+"/"+action, nil, &d, func(raw []byte) { a.printDatabaseImport(&d, raw) })
		},
	}
}

// --- Kubernetes ---

var kubernetesMoves = moveFamily{"Kubernetes move", "/kubernetes-moves", []string{"id", "source", "cluster", "status", "objects", "created"}}

func kubernetesMoveBusy(s string) bool {
	switch s {
	case "exporting", "previewing", "applying", "syncing", "cutting_over":
		return true
	}
	return false
}

func kubernetesMoveDetail(out *output.Printer, m *api.KubernetesMove) output.Detail {
	o := &m.Options
	d := output.Detail{
		Title:    "Kubernetes move of " + m.SourceName,
		State:    out.State(m.Status),
		Subtitle: m.ID,
		Sections: [][]output.Pair{
			failurePair(m.Failure),
			{{"Plan", m.MigrationID}, {"Item", m.ItemKey}, {"Into cluster", m.ClusterID}},
			{{"Secrets", strconv.FormatBool(o.IncludeSecrets)}, {"Volumes", strconv.FormatBool(o.CopyVolumes)}, {"Load balancers", strconv.FormatBool(o.PublishLoadBalancers)}},
			{{"Objects", strconv.Itoa(m.Summary.Objects)}, {"Refused", strconv.Itoa(m.Summary.Refused)}, {"Skipped", strconv.Itoa(m.Summary.Skipped)},
				{"Flagged", strconv.Itoa(m.Summary.Flagged)}, {"Preview passed", strconv.FormatBool(m.PreviewOK)}},
		},
	}
	var refused []output.Pair
	for _, r := range m.Resources {
		if r.Action == "create" && !r.PolicyRefused {
			continue
		}
		why := r.Action
		if r.PolicyRefused {
			why = "refused by the cluster policy"
		}
		refused = append(refused, output.Pair{r.Kind, r.Namespace + "/" + r.Name + " · " + why})
	}
	d.Sections = append(d.Sections, refused)
	var vols []output.Pair
	for _, v := range m.Volumes {
		vols = append(vols, output.Pair{"Volume", fmt.Sprintf("%s/%s · %s · %s of %s", v.Namespace, v.Claim, v.Status, bytesText(v.BytesDone), bytesText(v.SizeBytes))})
	}
	d.Sections = append(d.Sections, vols)
	switch m.Status {
	case "ready":
		d.Next = [][2]string{{"Check it against the cluster", "pantech migrate kubernetes preview " + m.ID}}
	case "applied":
		d.Next = [][2]string{{"Stop writes on the source, then", "pantech migrate kubernetes cutover " + m.ID}}
	}
	if m.Status == "ready" && m.PreviewOK {
		d.Next = append(d.Next, [2]string{"Then", "pantech migrate kubernetes apply " + m.ID})
	}
	return d
}

func (a *app) printKubernetesMove(m *api.KubernetesMove, raw []byte) {
	switch {
	case a.out.JSON:
		a.out.RawJSON(raw)
	case a.out.Quiet:
		a.out.Line("%s", output.Clean(m.ID))
	default:
		a.out.Print(kubernetesMoveDetail(a.out, m))
	}
}

func newMigrateKubernetesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "kubernetes",
		Aliases: []string{"k8s"},
		Short:   "Move planned Kubernetes clusters into your Pantech clusters",
		Long: `Move a plan's Kubernetes cluster into one of your running Pantech clusters.
Apply the manifests from "source-access" to the source cluster; the Pantech
Migrate agent exports it with that read-only access (the kubeconfig named under
"kubernetes" in its configuration file), and the credential never leaves the
agent. Volume copy pods run only Pantech's copier image
(ghcr.io/pantech-dynamics/pantech-migrate-copier), which the source cluster must
be able to pull. "start" exports, "preview" dry-runs every object against
your cluster, "apply" applies what the preview checked, "sync-volumes" copies
volume data again, and "cutover" (once writes on the source stopped) takes the
last copy and scales the held workloads up. The move is free; the cluster
bills as usual, and so does every load balancer it publishes.`,
	}
	cmd.AddCommand(newMigrateKubernetesSourceAccessCmd(a), newMigrateMoveListCmd(a, kubernetesMoves), newMigrateKubernetesShowCmd(a),
		newMigrateKubernetesStartCmd(a), newMigrateKubernetesPreviewCmd(a),
		newMigrateKubernetesActCmd(a, "apply", "Apply what the last preview checked to your cluster"),
		newMigrateKubernetesActCmd(a, "sync-volumes", "Copy every volume's changes again"),
		newMigrateKubernetesActCmd(a, "cutover", "Take the last copy and scale the held workloads up"),
		newMigrateKubernetesActCmd(a, "cancel", "Stop the move and remove its copy pods, staged data and export"),
		newMigrateKubernetesManifestsCmd(a))
	return cmd
}

func newMigrateKubernetesSourceAccessCmd(a *app) *cobra.Command {
	var dir string
	var force bool
	cmd := &cobra.Command{
		Use:   "source-access",
		Short: "Save the manifests to apply to the source cluster",
		Long: `Save the manifests to apply to the source cluster, in ./pantech-migrate-k8s/
(--out to choose): reader.yaml (read-only: get, list, watch; no Secrets),
secrets-reader.yaml (only to move Secrets) and copier.yaml (only to copy
volumes; bind it in each namespace whose volumes you copy). Files already
there are kept unless you pass --force.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var sa api.KubernetesMoveSourceAccess
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: "/kubernetes-moves/source-access"}, &sa)
			if err != nil {
				return err
			}
			if dir == "" {
				dir = "pantech-migrate-k8s"
			}
			files := []pendingFile{
				{path: dir + string(os.PathSeparator) + "reader.yaml", data: textFile(sa.Reader)},
				{path: dir + string(os.PathSeparator) + "secrets-reader.yaml", data: textFile(sa.SecretsReader)},
				{path: dir + string(os.PathSeparator) + "copier.yaml", data: textFile(sa.Copier)},
			}
			for _, f := range files {
				if err := checkTarget(f.path, force); err != nil {
					if errors.Is(err, errFileExists) {
						return &usageError{fmt.Errorf("%w: nothing was written. Pass --force to overwrite, or --out another directory", err), cmd}
					}
					return err
				}
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			if err := writeFiles(files, 0o644); err != nil {
				return err
			}
			if a.out.JSON {
				a.out.RawJSON(res.Body)
				return nil
			}
			for _, f := range files {
				a.out.Line("%s", f.path)
			}
			if !a.out.Quiet {
				a.out.Next("Apply the reader to the source cluster", "kubectl apply -f "+files[0].path)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "out", "", "the directory to write to (default ./pantech-migrate-k8s/)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite files already there")
	return cmd
}

func newMigrateKubernetesShowCmd(a *app) *cobra.Command {
	var wait bool
	cmd := &cobra.Command{
		Use:   "show <move id>",
		Short: "Show a Kubernetes move and its report",
		Long:  `Show a move with its report: the objects refused or skipped (names only, never values), the volumes and their copy progress. --wait follows it while it exports, previews, applies, copies or cuts over (checking every 3 seconds); a failed move exits with 5.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var raw []byte
			m := &api.KubernetesMove{}
			if wait {
				steps := a.out.Steps()
				steps.Start("Moving")
				m, err = api.WaitMove[api.KubernetesMove](ctx(cmd), c, kubernetesMoves.item(args[0]), "Kubernetes move",
					func(m *api.KubernetesMove) (string, string, *api.CloudFailure) { return m.ID, m.Status, m.Failure }, kubernetesMoveBusy,
					func(st string) { steps.Start(strings.ReplaceAll(st, "_", " ")) }, &raw)
				if err != nil {
					steps.Fail("", "")
					if m == nil {
						return err
					}
				} else {
					steps.Done("", "")
				}
			} else {
				res, err2 := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: kubernetesMoves.item(args[0])}, m)
				if err2 != nil {
					return err2
				}
				raw = res.Body
			}
			a.printKubernetesMove(m, raw)
			return err
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait while the move is busy")
	return cmd
}

func newMigrateKubernetesStartCmd(a *app) *cobra.Command {
	var key, cluster string
	var namespaces []string
	var secrets, volumes, noLB bool
	cmd := &cobra.Command{
		Use:   "start <migration id> --key KEY --cluster CLUSTER_ID",
		Short: "Start moving a planned Kubernetes cluster (nothing is applied yet)",
		Long: `Start moving a plan's kubernetes_cluster item (--key) into one of your running
Pantech clusters (--cluster). Secrets move only with --include-secrets, volume
data only with --copy-volumes; --namespace limits it (repeat it). LoadBalancer
services become Pantech load balancers unless --no-load-balancers. At most 5
active moves, one per cluster. Owners and admins only.`,
		Example: `  pantech migrate kubernetes start mig_123 --key kubernetes_cluster:us-east-1:prod --cluster k8s_456 --copy-volumes`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{"item_key": key, "cluster_id": cluster, "include_secrets": secrets, "copy_volumes": volumes, "publish_load_balancers": !noLB}
			if len(namespaces) > 0 {
				body["namespaces"] = namespaces
			}
			var m api.KubernetesMove
			return a.actOn(cmd, migrationPath(args[0])+"/kubernetes-moves", body, &m, func(raw []byte) {
				if !a.out.JSON && !a.out.Quiet {
					a.out.Success("Started Kubernetes move %s. The agent exports the source first.", m.ID)
				}
				a.printKubernetesMove(&m, raw)
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&key, "key", "", "the plan's kubernetes_cluster item key (required)")
	f.StringVar(&cluster, "cluster", "", "your running Pantech cluster, k8s_… (required)")
	f.StringArrayVar(&namespaces, "namespace", nil, "only this namespace (repeat it)")
	f.BoolVar(&secrets, "include-secrets", false, "also move the source's Secrets")
	f.BoolVar(&volumes, "copy-volumes", false, "also copy persistent volume data")
	f.BoolVar(&noLB, "no-load-balancers", false, "do not create load balancers for LoadBalancer services")
	_ = cmd.MarkFlagRequired("key")
	_ = cmd.MarkFlagRequired("cluster")
	return cmd
}

func newMigrateKubernetesPreviewCmd(a *app) *cobra.Command {
	var allow []string
	cmd := &cobra.Command{
		Use:   "preview <move id> [--allow KIND/NAMESPACE/NAME]...",
		Short: "Dry-run every object against your cluster",
		Long: `Queue a server-side dry run of every object against the target cluster. Only
the move's namespaces are created (empty). --allow includes a workload the
cluster policy refused (KIND/NAMESPACE/NAME, or GROUP/KIND/NAMESPACE/NAME;
repeat it).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			if len(allow) > 0 {
				refs := make([]map[string]string, len(allow))
				for i, s := range allow {
					parts := strings.Split(s, "/")
					switch len(parts) {
					case 3:
						refs[i] = map[string]string{"group": "", "kind": parts[0], "namespace": parts[1], "name": parts[2]}
					case 4:
						refs[i] = map[string]string{"group": parts[0], "kind": parts[1], "namespace": parts[2], "name": parts[3]}
					default:
						return &usageError{fmt.Errorf("--allow %q: use KIND/NAMESPACE/NAME or GROUP/KIND/NAMESPACE/NAME", output.Clean(s)), cmd}
					}
				}
				body["allow_privileged"] = refs
			}
			var m api.KubernetesMove
			return a.actOn(cmd, kubernetesMoves.item(args[0])+"/preview", body, &m, func(raw []byte) { a.printKubernetesMove(&m, raw) })
		},
	}
	cmd.Flags().StringArrayVar(&allow, "allow", nil, "a refused workload to include anyway (repeat it)")
	return cmd
}

func newMigrateKubernetesActCmd(a *app, action, short string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   action + " <move id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var body any
			id := output.Clean(args[0])
			switch action {
			case "apply":
				if err := a.confirm(fmt.Sprintf("Apply %s to your cluster? Published load balancers are billed and reachable from the internet.", id)); err != nil {
					return err
				}
			case "cutover":
				if err := a.confirm(fmt.Sprintf("Have writes on the source cluster stopped? Cut over %s now.", id)); err != nil {
					return err
				}
				body = map[string]any{"source_writes_stopped": true}
			case "cancel":
				if err := a.confirm(fmt.Sprintf("Cancel %s and remove its staged data? Objects already applied stay.", id)); err != nil {
					return err
				}
			}
			var m api.KubernetesMove
			return a.actOn(cmd, kubernetesMoves.item(args[0])+"/"+action, body, &m, func(raw []byte) { a.printKubernetesMove(&m, raw) })
		},
	}
	if action == "apply" || action == "cutover" || action == "cancel" {
		cmd.Long = short + ". Asks first (--yes to skip)."
	}
	return cmd
}

func newMigrateKubernetesManifestsCmd(a *app) *cobra.Command {
	var out string
	var force bool
	cmd := &cobra.Command{
		Use:   "manifests <move id>",
		Short: "Save the transformed manifests as one YAML file, for GitOps",
		Long: `Save a move's transformed manifests as one YAML file, to
./pantech-migrate-<move id>.yaml unless --out names a file ("-" for stdout).
Secret values are never included: the file lists the Secrets the move carries
by name.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := out
			if path == "" {
				if !importIDPattern.MatchString(args[0]) {
					return &usageError{fmt.Errorf("%q is not a move id (kmv_…): pass --out to name the file", output.Clean(args[0])), cmd}
				}
				path = "pantech-migrate-" + args[0] + ".yaml"
			}
			if path != "-" {
				if err := checkTarget(path, force); err != nil {
					if errors.Is(err, errFileExists) {
						return &usageError{fmt.Errorf("%w: pass --force to overwrite it, or --out", err), cmd}
					}
					return err
				}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: kubernetesMoves.item(args[0]) + "/manifests", Header: http.Header{"Accept": {"application/yaml"}}}, nil)
			if err != nil {
				return err
			}
			if path == "-" {
				_, err := os.Stdout.Write(res.Body)
				return err
			}
			if err := writeFiles([]pendingFile{{path: path, data: res.Body}}, 0o644); err != nil {
				return err
			}
			a.out.Line("%s", path)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", `the file to save ("-" for stdout; default ./pantech-migrate-<id>.yaml)`)
	cmd.Flags().BoolVar(&force, "force", false, "overwrite a file already there")
	return cmd
}
