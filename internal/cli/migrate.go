package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/auth"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

// openBrowser opens a URL in the browser, and canOpenBrowser says whether
// anyone is there to see it. Tests replace both.
var (
	openBrowser    = auth.OpenBrowser
	canOpenBrowser = output.Interactive
)

func newMigrateCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "migrate",
		Aliases: []string{"mig"},
		Short:   "Plan a move from AWS to Pantech: connect, scan, plan, export Terraform",
		Long: `Pantech Migrate reads another cloud account and plans its move to Pantech.

  1. pantech migrate connect --name "Production AWS"
     creates a connection and the CloudFormation template for a read-only role
     in your AWS account. No access keys are ever sent to Pantech.
  2. Create the stack in AWS, then:
     pantech migrate set-role <connection id> --role-arn <RoleArn output> --wait
  3. pantech migrate scan <connection id> --wait
     reads servers, disks, networks, firewall groups, public addresses, load
     balancers, databases, buckets, users, SSH keys and three months of spend.
  4. pantech migrate plan <scan id> --name "Move to Pantech"
     maps each item to its Pantech equivalent, with a monthly price comparison.
  5. pantech migrate show <migration id>, then edit items with
     pantech migrate item, and export with pantech migrate terraform.

Nothing is created on Pantech or changed in AWS: a plan is something to
review, edit and export. Owners and admins only, for every change.`,
	}
	cmd.AddCommand(
		newMigrateConnectCmd(a),
		newMigrateConnectionsCmd(a),
		newMigrateConnectionCmd(a),
		newMigrateSetRoleCmd(a),
		newMigrateRetryCmd(a),
		newMigrateRenameConnectionCmd(a),
		newMigrateDisconnectCmd(a),
		newMigrateScanCmd(a),
		newMigrateScansCmd(a),
		newMigrateScanShowCmd(a),
		newMigratePlanCmd(a),
		newMigratePlansCmd(a),
		newMigrateShowCmd(a),
		newMigrateItemCmd(a),
		newMigrateRenameCmd(a),
		newMigrateDeleteCmd(a),
		newMigrateTerraformCmd(a),
	)
	return cmd
}

var (
	connectionIDPattern  = regexp.MustCompile(`^ccn_[A-Za-z0-9]+$`)
	migrationIDPattern   = regexp.MustCompile(`^mig_[A-Za-z0-9]+$`)
	terraformFilePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+\.tf$`)
)

// safeQuickCreateURL accepts only an https link to the AWS console (or a
// regional subdomain of it), with no user info and no port: anything else is
// neither shown nor opened.
func safeQuickCreateURL(raw string) bool {
	if raw == "" || strings.HasPrefix(raw, "-") || output.Clean(raw) != raw || strings.ContainsAny(raw, " \\") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Opaque != "" {
		return false
	}
	host := u.Hostname()
	return u.Host == host && (host == "console.aws.amazon.com" || strings.HasSuffix(host, ".console.aws.amazon.com"))
}

func connectionPath(id string) string { return "/cloud-connections/" + url.PathEscape(id) }
func scanPath(id string) string       { return "/cloud-scans/" + url.PathEscape(id) }
func migrationPath(id string) string  { return "/migrations/" + url.PathEscape(id) }

// migrateError adds what to run next to the API's Migrate errors.
func migrateError(err error, id string) error {
	switch {
	case api.IsCode(err, "CLOUD_CONNECTION_NOT_FOUND"):
		return fmt.Errorf("%w\nSee: pantech migrate connections", err)
	case api.IsCode(err, "CLOUD_SCAN_NOT_FOUND"):
		return fmt.Errorf("%w\nSee: pantech migrate scans <connection id>", err)
	case api.IsCode(err, "MIGRATION_NOT_FOUND"):
		return fmt.Errorf("%w\nSee: pantech migrate plans", err)
	case api.IsCode(err, "CLOUD_CONNECTION_NOT_CONNECTED"):
		return fmt.Errorf("%w\nFinish connecting: pantech migrate set-role %s --role-arn <RoleArn output> --wait", err, id)
	case api.IsCode(err, "CLOUD_CONNECTION_VERIFYING"):
		return fmt.Errorf("%w\nWait for the check: pantech migrate connection %s --watch", err, id)
	case api.IsCode(err, "CLOUD_SCAN_IN_PROGRESS"):
		return fmt.Errorf("%w\nSee: pantech migrate scans %s", err, id)
	case api.IsCode(err, "CLOUD_SCAN_NOT_READY"):
		return fmt.Errorf("%w\nWait for it: pantech migrate scan-show %s --wait", err, id)
	}
	return err
}

// --- connections ---

func connectionNext(c *api.CloudConnection) [][2]string {
	switch c.Status {
	case "awaiting_role":
		return [][2]string{{"Then", "pantech migrate set-role " + c.ID + " --role-arn <RoleArn output> --wait"}}
	case "verifying":
		return [][2]string{{"Wait for the check", "pantech migrate connection " + c.ID + " --watch"}}
	case "connected":
		return [][2]string{{"Scan it", "pantech migrate scan " + c.ID + " --wait"}}
	case "failed":
		return [][2]string{{"Check again", "pantech migrate retry " + c.ID + " --wait"}}
	}
	return nil
}

func failureText(f *api.CloudFailure) string {
	if f == nil {
		return "—"
	}
	if f.Message == "" {
		return f.Code
	}
	return f.Message + " (" + f.Code + ")"
}

func connectionDetail(out *output.Printer, c *api.CloudConnection) output.Detail {
	d := output.Detail{
		Title:    c.Name,
		State:    out.State(c.Status),
		Subtitle: c.ID,
		Sections: [][]output.Pair{
			{{"Provider", strings.ToUpper(c.Provider)}, {"Account", output.Or(c.AWSAccountID)}, {"Role", output.Or(c.RoleARN)}, {"Verified", output.When(c.VerifiedAt)}},
			{{"Created", output.When(c.CreatedAt)}, {"Updated", output.When(c.UpdatedAt)}},
		},
		Next: connectionNext(c),
	}
	if c.Failure != nil {
		d.Sections = append([][]output.Pair{{{"Failure", failureText(c.Failure)}}}, d.Sections...)
	}
	return d
}

func newMigrateConnectCmd(a *app) *cobra.Command {
	var name, templateOut string
	var noBrowser, force bool
	cmd := &cobra.Command{
		Use:   "connect --name NAME",
		Short: "Start connecting an AWS account; saves the role's CloudFormation template",
		Long: `Start a connection to an AWS account. Nothing is shared yet: this saves a
CloudFormation template that creates, in your account, a read-only role only
Pantech can assume, with short-lived tokens for this one connection. No access
keys are ever sent to Pantech.

The template is saved to ./pantech-migrate-<connection id>.json (--template-out
to choose); a file already there is kept unless you pass --force. When AWS's
one-click create-stack link is offered, it is printed and opened in your
browser (--no-browser to only print it); a link to anywhere but
console.aws.amazon.com is never shown. Create the stack,
then send its RoleArn output with pantech migrate set-role.

At most 10 connections per organization. Owners and admins only.`,
		Example: `  pantech migrate connect --name "Production AWS"
  pantech migrate connect --name staging --template-out role.json --no-browser`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// A --template-out already there is refused before anything is created.
			if templateOut != "" {
				if err := checkTarget(templateOut, force); err != nil {
					if errors.Is(err, errFileExists) {
						return &usageError{fmt.Errorf("%w: pass --force to overwrite it, or another --template-out", err), cmd}
					}
					return err
				}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var conn api.CloudConnection
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: "/cloud-connections", Body: map[string]any{"provider": "aws", "name": name}}, &conn)
			if err != nil {
				return api.AsFlags(err, map[string]string{"name": "--name"})
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%s", output.Clean(conn.ID))
			}
			id := output.Clean(conn.ID)
			saveErr := func() error {
				path := templateOut
				if path == "" {
					// The id names a file here: only ever a plain one.
					if !connectionIDPattern.MatchString(conn.ID) {
						return fmt.Errorf("the API answered with an unexpected connection id %q: pass --template-out to name the file", id)
					}
					path = "pantech-migrate-" + conn.ID + ".json"
				}
				if err := checkTarget(path, force); err != nil {
					if errors.Is(err, errFileExists) {
						return fmt.Errorf("%w: pass --force to overwrite it, or --template-out", err)
					}
					return err
				}
				template := conn.Setup.Template
				if template != "" && !strings.HasSuffix(template, "\n") {
					template += "\n"
				}
				if err := writeFiles([]pendingFile{{path: path, data: []byte(template)}}, 0o644); err != nil {
					return err
				}
				templateOut = path
				return nil
			}()
			if saveErr != nil {
				return fmt.Errorf("connection %s was created, but its template was not saved: %w\nSee it: pantech migrate connection %s --json", id, saveErr, id)
			}
			path := templateOut
			if a.out.JSON || a.out.Quiet {
				return nil
			}
			a.out.Success("Created connection %s (%s).", conn.ID, conn.Name)
			a.out.Note("Saved the CloudFormation template to %s", path)
			quick := ""
			if conn.Setup.QuickCreateURL != nil && safeQuickCreateURL(*conn.Setup.QuickCreateURL) {
				quick = *conn.Setup.QuickCreateURL
			}
			if quick != "" {
				opened := !noBrowser && canOpenBrowser() && openBrowser(quick) == nil
				if opened {
					a.out.Note("\nOpened AWS's create-stack page in your browser. If it did not open, go to:\n\n  %s\n", quick)
				} else {
					a.out.Note("\nCreate the role in your AWS account with one click:\n\n  %s\n", quick)
				}
				a.out.Note("Or create a stack from %s in the CloudFormation console.", path)
			} else {
				a.out.Note("\nCreate a stack from %s in the CloudFormation console of your AWS account.", path)
			}
			a.out.Note("When the stack is created, copy its RoleArn output.")
			a.out.Next("Then", "pantech migrate set-role "+id+" --role-arn <RoleArn output> --wait")
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", `a name for the connection, e.g. "Production AWS" (required)`)
	f.StringVar(&templateOut, "template-out", "", "where to save the CloudFormation template (default ./pantech-migrate-<id>.json)")
	f.BoolVar(&noBrowser, "no-browser", false, "print the create-stack link instead of opening a browser")
	f.BoolVar(&force, "force", false, "overwrite a template file already there")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newMigrateConnectionsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "connections",
		Aliases: []string{"conns"},
		Short:   "List cloud connections",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			conns, raw, err := listItems[api.CloudConnection](cmd, c, "/cloud-connections", nil)
			if err != nil {
				return err
			}
			if printList(a, conns, raw, func(c api.CloudConnection) string { return c.ID }, "No cloud connections yet.", `pantech migrate connect --name "Production AWS"`) {
				return nil
			}
			rows := make([][]string, len(conns))
			for i := range conns {
				cn := &conns[i]
				rows[i] = []string{a.out.Dim(cn.ID), cn.Name, a.out.State(cn.Status), output.Or(cn.AWSAccountID), output.Ago(cn.CreatedAt)}
			}
			a.out.Table([]string{"id", "name", "status", "account", "created"}, rows)
			a.out.Summary(count(len(conns), "connection"))
			return nil
		},
	}
}

// printConnection prints a connection the way get does: its JSON, its id,
// or its detail.
func (a *app) printConnection(conn *api.CloudConnection, raw []byte) {
	switch {
	case a.out.JSON:
		a.out.RawJSON(raw)
	case a.out.Quiet:
		a.out.Line("%s", output.Clean(conn.ID))
	default:
		a.out.Print(connectionDetail(a.out, conn))
	}
}

// waitConnection follows a connection's check until it is done, with a
// spinner, and prints the connection.
func (a *app) waitConnection(cmd *cobra.Command, c *api.Client, id string) error {
	steps := a.out.Steps()
	steps.Start("Checking the role")
	var raw []byte
	conn, err := api.WaitCloudConnection(ctx(cmd), c, id, &raw)
	if err != nil {
		steps.Fail("", "")
		if conn != nil && raw != nil && a.out.JSON {
			a.out.RawJSON(raw)
		}
		var failed *api.MigrateFailed
		if errors.As(err, &failed) {
			return fmt.Errorf("%w\nCheck again: pantech migrate retry %s --wait", err, id)
		}
		return migrateError(err, id)
	}
	switch conn.Status {
	case "connected":
		steps.Done("Connected", "")
	default:
		steps.Done("", "")
	}
	a.printConnection(conn, raw)
	return nil
}

func newMigrateConnectionCmd(a *app) *cobra.Command {
	var watch bool
	cmd := &cobra.Command{
		Use:   "connection <connection id>",
		Short: "Show a cloud connection",
		Long: `Show a connection. Its status is awaiting_role until you send the role ARN,
verifying while Pantech tries it, then connected or failed. --watch follows a
connection being verified until the check is done.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if watch {
				return a.waitConnection(cmd, c, args[0])
			}
			var conn api.CloudConnection
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: connectionPath(args[0])}, &conn)
			if err != nil {
				return migrateError(err, args[0])
			}
			a.printConnection(&conn, res.Body)
			return nil
		},
	}
	cmd.Flags().BoolVar(&watch, "watch", false, `wait while the connection is "verifying"`)
	return cmd
}

// updateConnection sends a PATCH and, with wait, follows the check it queues.
func (a *app) updateConnection(cmd *cobra.Command, id string, body map[string]any, wait bool, done string) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	var conn api.CloudConnection
	res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPatch, Path: connectionPath(id), Body: body}, &conn)
	if err != nil {
		return migrateError(api.AsFlags(err, map[string]string{"role_arn": "--role-arn", "name": "--name"}), id)
	}
	if !a.out.JSON && !a.out.Quiet {
		a.out.Success(done, conn.ID)
	}
	if wait && conn.Status == "verifying" {
		return a.waitConnection(cmd, c, conn.ID)
	}
	a.printConnection(&conn, res.Body)
	return nil
}

func newMigrateSetRoleCmd(a *app) *cobra.Command {
	var roleARN string
	var wait bool
	cmd := &cobra.Command{
		Use:   "set-role <connection id> --role-arn ARN",
		Short: "Send the RoleArn your CloudFormation stack created, and check it",
		Long: `Send the RoleArn output of the stack made from the connection's template.
Pantech then assumes the role and reads which AWS account it belongs to,
usually within a minute. --wait follows the check.`,
		Example: `  pantech migrate set-role ccn_123 --role-arn arn:aws:iam::123456789012:role/PantechMigrateReadOnly --wait`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(roleARN) == "" {
				return &usageError{errors.New("--role-arn needs a value: the stack's RoleArn output"), cmd}
			}
			return a.updateConnection(cmd, args[0], map[string]any{"role_arn": strings.TrimSpace(roleARN)}, wait, "Sent the role of %s; checking it.")
		},
	}
	cmd.Flags().StringVar(&roleARN, "role-arn", "", "the stack's RoleArn output, like arn:aws:iam::123456789012:role/name (required)")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the check to finish")
	_ = cmd.MarkFlagRequired("role-arn")
	return cmd
}

func newMigrateRetryCmd(a *app) *cobra.Command {
	var wait bool
	cmd := &cobra.Command{
		Use:   "retry <connection id>",
		Short: "Check a connection's role again",
		Long:  `Check a connection's role again, after fixing what made the last check fail. --wait follows the check.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.updateConnection(cmd, args[0], map[string]any{"retry": true}, wait, "Checking the role of %s again.")
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the check to finish")
	return cmd
}

func newMigrateRenameConnectionCmd(a *app) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "rename-connection <connection id> --name NAME",
		Short: "Rename a cloud connection",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var conn api.CloudConnection
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPatch, Path: connectionPath(args[0]), Body: map[string]any{"name": name}}, &conn)
			if err != nil {
				return migrateError(api.AsFlags(err, map[string]string{"name": "--name"}), args[0])
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%s", output.Clean(conn.ID))
			default:
				a.out.Success("Renamed %s to %s.", conn.ID, conn.Name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "the new name, 1 to 100 characters (required)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newMigrateDisconnectCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect <connection id>",
		Short: "Remove a cloud connection and its scans",
		Long: `Remove a connection and its scans from Pantech. Migration plans made from it
stay. To revoke access on the AWS side too, delete the CloudFormation stack
you created for it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			// Read it first: the question names it, and a 404 from the delete
			// below then means a retried attempt already removed it.
			var conn api.CloudConnection
			if _, err := api.Get(ctx(cmd), c, connectionPath(args[0]), &conn); err != nil {
				return migrateError(err, args[0])
			}
			if err := a.confirm(fmt.Sprintf("Disconnect %s (%s) and delete its scans? Migration plans made from it stay.", conn.ID, conn.Name)); err != nil {
				return err
			}
			_, err = c.Do(ctx(cmd), api.Request{Method: http.MethodDelete, Path: connectionPath(conn.ID)}, nil)
			if err != nil && !api.IsCode(err, "CLOUD_CONNECTION_NOT_FOUND") && !api.IsCode(err, "RESOURCE_NOT_FOUND") {
				return err
			}
			switch {
			case a.out.JSON:
				a.out.Value(map[string]any{"id": conn.ID, "disconnected": true})
			case a.out.Quiet:
				a.out.Line("%s", output.Clean(conn.ID))
			default:
				a.out.Success("Disconnected %s.", conn.ID)
				a.out.Note("Pantech can no longer use it. To revoke the role in AWS too, delete the CloudFormation stack you created for this connection.")
			}
			return nil
		},
	}
}

// --- scans ---

type kindLabel struct{ key, label string }

// scanKinds are the inventory's kinds, in the order shown.
var scanKinds = []kindLabel{
	{"instances", "Servers"},
	{"volumes", "Disks"},
	{"networks", "Networks"},
	{"subnets", "Subnets"},
	{"security_groups", "Firewall groups"},
	{"public_ips", "Public IPs"},
	{"load_balancers", "Load balancers"},
	{"databases", "Databases"},
	{"buckets", "Buckets"},
	{"users", "Users"},
	{"ssh_keys", "SSH keys"},
}

func scanCount(c *api.CloudScanCounts, kind string) int {
	switch kind {
	case "instances":
		return c.Instances
	case "volumes":
		return c.Volumes
	case "networks":
		return c.Networks
	case "subnets":
		return c.Subnets
	case "security_groups":
		return c.SecurityGroups
	case "public_ips":
		return c.PublicIPs
	case "load_balancers":
		return c.LoadBalancers
	case "databases":
		return c.Databases
	case "buckets":
		return c.Buckets
	case "users":
		return c.Users
	case "ssh_keys":
		return c.SSHKeys
	}
	return 0
}

// scanWarning says a scan warning code in words.
func scanWarning(w string) string {
	switch {
	case w == "cost_unavailable":
		return "spend could not be read (cost_unavailable)"
	case strings.HasPrefix(w, "region_denied:"):
		return "region " + strings.TrimPrefix(w, "region_denied:") + " could not be read (" + w + ")"
	}
	return w
}

func scanItems(s *api.CloudScan) string {
	if s.Counts == nil {
		return "—"
	}
	return strconv.Itoa(s.Counts.Total())
}

func scanCost(s *api.CloudScan) string {
	if s.Cost == nil {
		return "—"
	}
	return money(s.Cost.AverageMonthlyMinor, s.Cost.Currency) + "/month"
}

func scanDetail(out *output.Printer, s *api.CloudScan) output.Detail {
	title := "Scan"
	if s.AWSAccountID != nil && *s.AWSAccountID != "" {
		title = "Scan of AWS account " + *s.AWSAccountID
	}
	regions := "—"
	if len(s.Regions) > 0 {
		regions = strings.Join(s.Regions, ", ")
	}
	d := output.Detail{
		Title:    title,
		State:    out.State(s.Status),
		Subtitle: s.ID,
		Sections: [][]output.Pair{
			{{"Connection", s.ConnectionID}, {"Regions", regions}},
		},
	}
	if s.Failure != nil {
		d.Sections = append(d.Sections, []output.Pair{{"Failure", failureText(s.Failure)}})
	}
	if s.Counts != nil {
		var found []output.Pair
		for _, k := range scanKinds {
			if n := scanCount(s.Counts, k.key); n > 0 {
				found = append(found, output.Pair{k.label, strconv.Itoa(n)})
			}
		}
		if len(found) == 0 {
			found = []output.Pair{{"Found", "nothing"}}
		}
		d.Sections = append(d.Sections, found)
	}
	if s.Cost != nil {
		cost := []output.Pair{{"Monthly bill", money(s.Cost.AverageMonthlyMinor, s.Cost.Currency) + out.Dim(fmt.Sprintf(" (average of %s, whole account)", count(len(s.Cost.Months), "month")))}}
		for _, m := range s.Cost.Months {
			cost = append(cost, output.Pair{"  " + m.Month, money(m.AmountMinor, s.Cost.Currency)})
		}
		d.Sections = append(d.Sections, cost)
	}
	var notes []output.Pair
	for _, w := range s.Warnings {
		notes = append(notes, output.Pair{"Warning", scanWarning(w)})
	}
	if len(s.Truncated) > 0 {
		notes = append(notes, output.Pair{"Cut at 2,000", strings.Join(s.Truncated, ", ")})
	}
	d.Sections = append(d.Sections, notes,
		[]output.Pair{{"Created", output.When(s.CreatedAt)}, {"Started", output.When(s.StartedAt)}, {"Finished", output.When(s.FinishedAt)}})
	switch s.Status {
	case "succeeded":
		d.Next = [][2]string{{"Plan the move", "pantech migrate plan " + s.ID + ` --name "Move to Pantech"`}}
	case "queued", "running":
		d.Next = [][2]string{{"Wait for it", "pantech migrate scan-show " + s.ID + " --wait"}}
	case "failed":
		d.Next = [][2]string{{"Scan again", "pantech migrate scan " + s.ConnectionID + " --wait"}}
	}
	return d
}

// printScan prints a scan: its JSON, its id, or its detail.
func (a *app) printScan(s *api.CloudScan, raw []byte) {
	switch {
	case a.out.JSON:
		a.out.RawJSON(raw)
	case a.out.Quiet:
		a.out.Line("%s", output.Clean(s.ID))
	default:
		a.out.Print(scanDetail(a.out, s))
	}
}

// waitScan follows a scan to its end with a spinner, then prints it.
func (a *app) waitScan(cmd *cobra.Command, c *api.Client, id string) error {
	steps := a.out.Steps()
	steps.Start("Scanning")
	var raw []byte
	s, err := api.WaitCloudScan(ctx(cmd), c, id, func(s *api.CloudScan) {
		if s.Status == "queued" {
			steps.Start("Waiting for a scanner")
		} else if s.Status == "running" {
			steps.Start("Scanning")
		}
	}, &raw)
	if err != nil {
		steps.Fail("", "")
		var failed *api.MigrateFailed
		if errors.As(err, &failed) && s != nil {
			if a.out.JSON {
				a.out.RawJSON(raw)
			}
			return fmt.Errorf("%w\nScan again: pantech migrate scan %s --wait", err, s.ConnectionID)
		}
		return migrateError(err, id)
	}
	steps.Done("Scanned", "")
	a.printScan(s, raw)
	return nil
}

func newMigrateScanCmd(a *app) *cobra.Command {
	var wait bool
	cmd := &cobra.Command{
		Use:   "scan <connection id>",
		Short: "Scan a connected account: what it runs and what it costs",
		Long: `Scan a connected account: servers, disks, networks, firewall groups, public
addresses, load balancers, databases, bucket names, users, SSH keys and the
last three months of spend, in every enabled region. Most accounts take under
five minutes. Reading spend costs USD 0.01 on your AWS bill, so it asks
first (--yes to skip).

Prints the scan's id; --wait follows it (checking every 3 seconds) and shows
what it found. Owners and admins only.`,
		Example: `  pantech migrate scan ccn_123 --wait
  pantech migrate scan ccn_123 --yes   # in a script`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Scan connection %s? Reading its spend costs USD 0.01 on your AWS bill.", args[0])); err != nil {
				return err
			}
			var s api.CloudScan
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: connectionPath(args[0]) + "/scans"}, &s)
			if err != nil {
				return migrateError(err, args[0])
			}
			if wait && !s.Done() {
				if !a.out.JSON && !a.out.Quiet {
					a.out.Note("Scan %s started. Reading spend costs USD 0.01 on your AWS bill.", s.ID)
				}
				return a.waitScan(cmd, c, s.ID)
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case wait:
				a.printScan(&s, res.Body)
			default:
				a.out.Line("%s", output.Clean(s.ID))
				if !a.out.Quiet {
					a.out.Success("Started scan %s. Reading spend costs USD 0.01 on your AWS bill.", s.ID)
					a.out.Next("Follow it", "pantech migrate scan-show "+s.ID+" --wait")
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the scan to finish and show what it found")
	return cmd
}

func newMigrateScansCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "scans <connection id>",
		Short: "List a connection's scans, newest first",
		Long:  `List a connection's scans, newest first. The 10 newest finished scans are kept.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			scans, raw, err := listItems[api.CloudScan](cmd, c, connectionPath(args[0])+"/scans", nil)
			if err != nil {
				return migrateError(err, args[0])
			}
			if printList(a, scans, raw, func(s api.CloudScan) string { return s.ID }, "No scans yet.", "") {
				if len(scans) == 0 && !a.out.JSON && !a.out.Quiet {
					a.out.Next("Scan it", "pantech migrate scan "+args[0]+" --wait")
				}
				return nil
			}
			rows := make([][]string, len(scans))
			for i := range scans {
				s := &scans[i]
				rows[i] = []string{a.out.Dim(s.ID), a.out.State(s.Status), strconv.Itoa(len(s.Regions)), scanItems(s), scanCost(s), output.Ago(s.CreatedAt)}
			}
			a.out.Table([]string{"id", "status", "regions", "items", "monthly bill", "created"}, rows)
			a.out.Summary(count(len(scans), "scan"))
			return nil
		},
	}
}

// inventoryField is one field of an inventory item as text, "" when absent or null.
func inventoryField(item map[string]json.RawMessage, key string) string {
	raw, ok := item[key]
	if !ok {
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case []any:
		return strconv.Itoa(len(x))
	}
	return ""
}

// inventoryDetail is what matters about an item of one kind.
func inventoryDetail(kind string, item map[string]json.RawMessage) string {
	f := func(key string) string { return inventoryField(item, key) }
	var parts []string
	add := func(s string) {
		if s != "" {
			parts = append(parts, s)
		}
	}
	switch kind {
	case "instances":
		add(f("instance_type"))
		add(f("state"))
		add(f("private_ip"))
	case "volumes":
		if gb := f("size_gb"); gb != "" {
			add(gb + " GB")
		}
		add(f("type"))
		if att := f("attached_to"); att != "" {
			add("on " + att)
		}
	case "networks", "subnets":
		add(f("cidr"))
	case "security_groups":
		if n := f("rules"); n != "" {
			add(n + " rules")
		}
	case "public_ips":
		add(f("address"))
		if id := f("instance_id"); id != "" {
			add("on " + id)
		}
	case "load_balancers":
		add(f("type"))
		add(f("scheme"))
	case "databases":
		add(strings.TrimSpace(f("engine") + " " + f("engine_version")))
		add(f("instance_class"))
		if gb := f("storage_gb"); gb != "" {
			add(gb + " GB")
		}
	case "users":
		add(f("email"))
	case "ssh_keys":
		add(f("fingerprint"))
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " · ")
}

func (a *app) printInventory(s *api.CloudScan) {
	if len(s.Inventory) == 0 {
		a.out.Note("\nThis scan has no inventory.")
		return
	}
	for _, k := range scanKinds {
		items := s.Inventory[k.key]
		if len(items) == 0 {
			continue
		}
		rows := make([][]string, len(items))
		for i, it := range items {
			id := inventoryField(it, "id")
			name := inventoryField(it, "name")
			if id == "" {
				id = name
			}
			if name == "" || name == id {
				name = "—"
			}
			region := inventoryField(it, "region")
			if region == "" {
				region = "—"
			}
			rows[i] = []string{a.out.Dim(id), name, region, inventoryDetail(k.key, it)}
		}
		a.out.Line("\n%s %s", a.out.Bold(output.Clean(k.label)), a.out.Dim("("+strconv.Itoa(len(items))+")"))
		a.out.Table([]string{"id", "name", "region", "detail"}, rows)
	}
}

func newMigrateScanShowCmd(a *app) *cobra.Command {
	var inventory, wait bool
	cmd := &cobra.Command{
		Use:   "scan-show <scan id>",
		Short: "Show a scan: what it found, regions, warnings and spend",
		Long: `Show a scan: its status, how many of each kind it found, the regions read,
the parts it could not read, and the account's monthly spend. --inventory
lists every item it found; names and tags are your own, unchanged. --wait
follows a scan still running.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if wait {
				var s api.CloudScan
				if _, err := api.Get(ctx(cmd), c, scanPath(args[0]), &s); err != nil {
					return migrateError(err, args[0])
				}
				if !s.Done() {
					steps := a.out.Steps()
					steps.Start("Scanning")
					// Followed here; read once more below, with --inventory if asked.
					_, err := api.WaitCloudScan(ctx(cmd), c, s.ID, nil, nil)
					var failed *api.MigrateFailed
					switch {
					case errors.As(err, &failed):
						steps.Fail("", "")
					case err != nil:
						steps.Fail("", "")
						return migrateError(err, args[0])
					default:
						steps.Done("Scanned", "")
					}
				}
			}
			q := url.Values{}
			if inventory {
				q.Set("include", "inventory")
			}
			var s api.CloudScan
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: scanPath(args[0]), Query: q}, &s)
			if err != nil {
				return migrateError(err, args[0])
			}
			a.printScan(&s, res.Body)
			if inventory && !a.out.JSON && !a.out.Quiet {
				a.printInventory(&s)
			}
			if wait && s.Status == "failed" {
				return &api.MigrateFailed{What: "scan", ID: s.ID, Failure: s.Failure}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&inventory, "inventory", false, "list every item the scan found")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for a running scan to finish")
	return cmd
}

// --- migrations ---

// itemKinds are a plan's kinds, in the order shown.
var itemKinds = []kindLabel{
	{"network", "Networks"},
	{"subnet", "Subnets"},
	{"security_group", "Firewall groups"},
	{"instance", "Servers"},
	{"volume", "Disks"},
	{"public_ip", "Public IPs"},
	{"load_balancer", "Load balancers"},
	{"database", "Databases"},
	{"ssh_key", "SSH keys"},
	{"user", "Users"},
	{"bucket", "Buckets"},
}

// kindNoun is one item of a plan's kind, for counts: "2 servers".
var kindNoun = map[string]string{
	"network": "network", "subnet": "subnet", "security_group": "firewall group",
	"instance": "server", "volume": "disk", "public_ip": "public IP",
	"load_balancer": "load balancer", "database": "database", "ssh_key": "SSH key",
	"user": "user", "bucket": "bucket",
}

func kindCount(n int, kind string) string {
	noun, ok := kindNoun[kind]
	if !ok {
		noun = strings.ReplaceAll(kind, "_", " ")
	}
	return count(n, noun)
}

// itemTarget is what an item becomes, in a few words.
func itemTarget(it *api.MigrationItem) string {
	t := &it.Target
	var parts []string
	add := func(s string) {
		if s != "" {
			parts = append(parts, s)
		}
	}
	str := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	add(str(t.PlanSlug))
	if t.Engine != nil {
		add(strings.TrimSpace(*t.Engine + " " + str(t.EngineVersion)))
	}
	if t.DiskGB != nil {
		add(strconv.Itoa(*t.DiskGB) + " GB disk")
	}
	if t.StorageGB != nil {
		add(strconv.Itoa(*t.StorageGB) + " GB")
	}
	add(str(t.CIDR))
	if len(t.Rules) > 0 {
		add(count(len(t.Rules), "rule"))
	}
	if len(t.Listeners) > 0 {
		ls := make([]string, len(t.Listeners))
		for i, l := range t.Listeners {
			ls[i] = l.Protocol + "/" + strconv.Itoa(l.Port)
		}
		add(strings.Join(ls, ", "))
	}
	add(str(t.Role))
	add(str(t.Email))
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " · ")
}

func itemSource(it *api.MigrationItem) string {
	s := it.Source.ID
	if it.Source.Name != nil && *it.Source.Name != "" && *it.Source.Name != it.Source.ID {
		s = *it.Source.Name
	}
	if it.Source.Summary != "" {
		s += " " + "(" + it.Source.Summary + ")"
	}
	return s
}

func itemFlags(it *api.MigrationItem) string {
	if len(it.Flags) == 0 {
		return "—"
	}
	codes := make([]string, len(it.Flags))
	for i, f := range it.Flags {
		codes[i] = f.Code
		if f.Blocking {
			codes[i] = "!" + f.Code
		}
	}
	return strings.Join(codes, ", ")
}

// estimatePairs is a plan's price next to the source account's bill.
func estimatePairs(out *output.Printer, e *api.MigrationEstimate) []output.Pair {
	if e == nil {
		return []output.Pair{{"Estimate", "—"}}
	}
	pantech := "could not price all of it"
	if e.PantechPriced && e.PantechMonthlyMinor != nil {
		pantech = money(*e.PantechMonthlyMinor, e.Currency)
	}
	pairs := []output.Pair{{"On Pantech", pantech + out.Dim(" a month")}}
	if len(e.Covers) > 0 {
		pairs = append(pairs, output.Pair{"Includes", strings.Join(e.Covers, ", ")})
	}
	if len(e.NotCovered) > 0 {
		pairs = append(pairs, output.Pair{"Not priced", strings.Join(e.NotCovered, ", ")})
	}
	source := "unknown"
	if e.SourceKnown && e.SourceMonthlyMinor != nil {
		cur := e.Currency
		if e.SourceCurrency != nil && *e.SourceCurrency != "" {
			cur = *e.SourceCurrency
		}
		scope := ""
		if e.SourceScope == "whole_account" {
			scope = ", whole account"
		}
		source = money(*e.SourceMonthlyMinor, cur) + out.Dim(fmt.Sprintf(" a month (average of %s%s)", count(e.SourceMonths, "month"), scope))
		if e.PantechPriced && e.PantechMonthlyMinor != nil && cur == e.Currency {
			diff := *e.SourceMonthlyMinor - *e.PantechMonthlyMinor
			pairs = append(pairs, output.Pair{"Today", source})
			switch {
			case diff > 0:
				pairs = append(pairs, output.Pair{"Difference", money(diff, cur) + " less a month on Pantech"})
			case diff < 0:
				pairs = append(pairs, output.Pair{"Difference", money(-diff, cur) + " more a month on Pantech"})
			default:
				pairs = append(pairs, output.Pair{"Difference", "the same"})
			}
			return pairs
		}
	}
	return append(pairs, output.Pair{"Today", source})
}

func byKind(s *api.MigrationSummary) string {
	if len(s.ByKind) == 0 {
		return "—"
	}
	keys := make([]string, 0, len(s.ByKind))
	for k := range s.ByKind {
		keys = append(keys, k)
	}
	order := map[string]int{}
	for i, k := range itemKinds {
		order[k.key] = i + 1
	}
	sort.Slice(keys, func(i, j int) bool {
		oi, oj := order[keys[i]], order[keys[j]]
		if oi == 0 {
			oi = len(itemKinds) + 1
		}
		if oj == 0 {
			oj = len(itemKinds) + 1
		}
		if oi != oj {
			return oi < oj
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = kindCount(s.ByKind[k], k)
	}
	return strings.Join(parts, ", ")
}

func migrationDetail(out *output.Printer, m *api.Migration) output.Detail {
	return output.Detail{
		Title:    m.Name,
		State:    out.State(m.Stage),
		Subtitle: m.ID,
		Sections: [][]output.Pair{
			{{"Region", m.Region}, {"Scan", m.ScanID}, {"Connection", m.ConnectionID}},
			{{"Planned", strconv.Itoa(m.Summary.Planned)}, {"Excluded", strconv.Itoa(m.Summary.Excluded)}, {"Unsupported", strconv.Itoa(m.Summary.Unsupported)}, {"By kind", byKind(&m.Summary)}},
			estimatePairs(out, m.Estimate),
			{{"Created", output.When(m.CreatedAt)}, {"Updated", output.When(m.UpdatedAt)}},
		},
	}
}

// printItems prints a plan's items, a table per kind, each followed by
// what its flags say.
func (a *app) printItems(items []api.MigrationItem) {
	groups := map[string][]*api.MigrationItem{}
	for i := range items {
		groups[items[i].Kind] = append(groups[items[i].Kind], &items[i])
	}
	kinds := append([]kindLabel{}, itemKinds...)
	for k := range groups {
		known := false
		for _, il := range itemKinds {
			known = known || il.key == k
		}
		if !known {
			kinds = append(kinds, kindLabel{k, strings.ReplaceAll(k, "_", " ")})
		}
	}
	for _, k := range kinds {
		group := groups[k.key]
		if len(group) == 0 {
			continue
		}
		rows := make([][]string, len(group))
		var notes []string
		for i, it := range group {
			rows[i] = []string{it.Key, itemSource(it), a.out.State(it.Status), itemTarget(it), itemFlags(it)}
			for _, f := range it.Flags {
				mark := " "
				if f.Blocking {
					mark = "!"
				}
				notes = append(notes, fmt.Sprintf("  %s %s %s: %s", mark, a.out.Dim(output.Clean(it.Key)), output.Clean(f.Code), output.Clean(f.Message)))
			}
		}
		a.out.Line("\n%s %s", a.out.Bold(output.Clean(k.label)), a.out.Dim("("+strconv.Itoa(len(group))+")"))
		a.out.Table([]string{"key", "source", "status", "on pantech", "flags"}, rows)
		for _, n := range notes {
			a.out.Line("%s", n)
		}
	}
}

func newMigratePlanCmd(a *app) *cobra.Command {
	var name, region string
	cmd := &cobra.Command{
		Use:   "plan <scan id> --name NAME",
		Short: "Plan a migration from a scan, with a monthly price comparison",
		Long: `Plan a move from a scan that succeeded: each server, disk, network, firewall
group, database, key and person mapped to its Pantech equivalent, sized
against today's plans, with anything that cannot move yet flagged and
explained. Nothing is created: review the plan with pantech migrate show, edit
it with pantech migrate item, and export it with pantech migrate terraform.

--region is a Pantech region code (pantech regions); the first region when
left out. At most 25 migrations per organization. Owners and admins only.`,
		Example: `  pantech migrate plan csn_123 --name "Move to Pantech" --region ng-lagos-1`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			body := map[string]any{"scan_id": args[0], "name": name}
			if region != "" {
				body["region"] = region
			}
			var m api.Migration
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: "/migrations", Body: body}, &m)
			if err != nil {
				return migrateError(api.AsFlags(err, map[string]string{"name": "--name", "region": "--region"}), args[0])
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%s", output.Clean(m.ID))
			default:
				a.out.Success("Planned %s: %d to move, %d excluded, %d unsupported.", m.ID, m.Summary.Planned, m.Summary.Excluded, m.Summary.Unsupported)
				d := migrationDetail(a.out, &m)
				d.Next = [][2]string{
					{"See every item", "pantech migrate show " + m.ID},
					{"Export it", "pantech migrate terraform " + m.ID},
				}
				a.out.Print(d)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "a name for the plan (required)")
	cmd.Flags().StringVar(&region, "region", "", "the Pantech region to move to (default: the first region)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newMigratePlansCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "plans",
		Aliases: []string{"migrations", "list", "ls"},
		Short:   "List migration plans",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			plans, raw, err := listItems[api.Migration](cmd, c, "/migrations", nil)
			if err != nil {
				return err
			}
			if printList(a, plans, raw, func(m api.Migration) string { return m.ID }, "No migration plans yet.", `pantech migrate plan <scan id> --name "Move to Pantech"`) {
				return nil
			}
			rows := make([][]string, len(plans))
			for i := range plans {
				m := &plans[i]
				rows[i] = []string{a.out.Dim(m.ID), m.Name, m.Region, strconv.Itoa(m.Summary.Planned), strconv.Itoa(m.Summary.Excluded), strconv.Itoa(m.Summary.Unsupported), output.Ago(m.CreatedAt)}
			}
			a.out.Table([]string{"id", "name", "region", "planned", "excluded", "unsupported", "created"}, rows)
			a.out.Summary(count(len(plans), "plan"))
			return nil
		},
	}
}

func newMigrateShowCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "show <migration id>",
		Aliases: []string{"get"},
		Short:   "Show a migration plan: every item, its flags, and the price comparison",
		Long: `Show a migration plan: every item grouped by kind, with its status (planned,
excluded or unsupported), what it becomes on Pantech and its flags (a "!" flag
makes it unsupported), and a price comparison computed from today's prices.
Use an item's key with pantech migrate item to change it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var m api.Migration
			if ok, err := getOne(a, cmd, c, migrationPath(args[0]), &m); !ok {
				return migrateError(err, args[0])
			}
			if a.out.Quiet {
				a.out.Line("%s", output.Clean(m.ID))
				return nil
			}
			a.out.Print(migrationDetail(a.out, &m))
			if len(m.Items) == 0 {
				a.out.Note("\nThis plan has no items.")
			} else {
				a.printItems(m.Items)
			}
			if !a.out.Quiet {
				a.out.Note("")
				a.out.Next("Change an item", "pantech migrate item "+m.ID+" --key <key> --exclude")
				a.out.Next("Export it", "pantech migrate terraform "+m.ID)
			}
			return nil
		},
	}
}

func newMigrateItemCmd(a *app) *cobra.Command {
	var key, planSlug, cidr, role, email string
	var storageGB int
	var exclude, include bool
	cmd := &cobra.Command{
		Use:   "item <migration id> --key KEY [changes]",
		Short: "Change one item of a plan: leave it out, or its plan, size, range, role or email",
		Long: `Change one item of a plan, named by its key (pantech migrate show lists them):
--exclude leaves it out of the move and --include puts it back; --plan picks
another plan (servers and databases), --storage-gb a size (disks and
databases), --cidr an address range (networks and subnets), and --role and
--email a person's. Items flagged as unsupported stay unsupported.`,
		Example: `  pantech migrate item mig_123 --key instance:us-east-1:i-0abc --plan standard-4
  pantech migrate item mig_123 --key volume:us-east-1:vol-0abc --exclude
  pantech migrate item mig_123 --key user:alice --role admin --email alice@example.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fl := cmd.Flags()
			body := map[string]any{"key": key}
			switch {
			case exclude && include:
				return &usageError{errors.New("pass --exclude or --include, not both"), cmd}
			case exclude:
				body["excluded"] = true
			case include:
				body["excluded"] = false
			}
			if fl.Changed("plan") {
				body["plan_slug"] = planSlug
			}
			if fl.Changed("storage-gb") {
				if storageGB < 1 {
					return &usageError{errors.New("--storage-gb must be at least 1"), cmd}
				}
				body["storage_gb"] = storageGB
			}
			if fl.Changed("cidr") {
				body["cidr"] = cidr
			}
			if fl.Changed("role") {
				if role != "admin" && role != "member" {
					return &usageError{errors.New("--role must be admin or member"), cmd}
				}
				body["role"] = role
			}
			if fl.Changed("email") {
				body["email"] = email
			}
			if len(body) == 1 {
				return &usageError{errors.New("nothing to change: pass --exclude, --include, --plan, --storage-gb, --cidr, --role or --email"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var m api.Migration
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPatch, Path: migrationPath(args[0]) + "/items", Body: body}, &m)
			if err != nil {
				return migrateError(api.AsFlags(err, map[string]string{"key": "--key", "excluded": "--exclude", "plan_slug": "--plan", "storage_gb": "--storage-gb", "cidr": "--cidr", "role": "--role", "email": "--email"}), args[0])
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
				return nil
			case a.out.Quiet:
				a.out.Line("%s", output.Clean(key))
				return nil
			}
			a.out.Success("Updated %s in %s.", key, m.ID)
			for i := range m.Items {
				if m.Items[i].Key == key {
					a.printItems(m.Items[i : i+1])
					break
				}
			}
			a.out.Line("")
			a.out.Print(output.Detail{Title: m.Name, Subtitle: m.ID, Sections: [][]output.Pair{
				{{"Planned", strconv.Itoa(m.Summary.Planned)}, {"Excluded", strconv.Itoa(m.Summary.Excluded)}, {"Unsupported", strconv.Itoa(m.Summary.Unsupported)}},
				estimatePairs(a.out, m.Estimate),
			}})
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&key, "key", "", "the item's key, from pantech migrate show (required)")
	f.BoolVar(&exclude, "exclude", false, "leave the item out of the move")
	f.BoolVar(&include, "include", false, "put an excluded item back in the move")
	f.StringVar(&planSlug, "plan", "", "another Pantech plan, for a server or database (pantech plans)")
	f.IntVar(&storageGB, "storage-gb", 0, "a size in GB, for a disk or database")
	f.StringVar(&cidr, "cidr", "", "an address range, for a network or subnet")
	f.StringVar(&role, "role", "", "admin or member, for a person")
	f.StringVar(&email, "email", "", "the email to invite, for a person")
	_ = cmd.MarkFlagRequired("key")
	return cmd
}

func newMigrateRenameCmd(a *app) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "rename <migration id> --name NAME",
		Short: "Rename a migration plan",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var m api.Migration
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPatch, Path: migrationPath(args[0]), Body: map[string]any{"name": name}}, &m)
			if err != nil {
				return migrateError(api.AsFlags(err, map[string]string{"name": "--name"}), args[0])
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%s", output.Clean(m.ID))
			default:
				a.out.Success("Renamed %s to %s.", m.ID, m.Name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "the new name (required)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newMigrateDeleteCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <migration id>",
		Aliases: []string{"rm"},
		Short:   "Delete a migration plan",
		Long:    `Delete a migration plan. Nothing on Pantech or in your other cloud is touched.`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var m api.Migration
			if _, err := api.Get(ctx(cmd), c, migrationPath(args[0]), &m); err != nil {
				return migrateError(err, args[0])
			}
			if err := a.confirm(fmt.Sprintf("Delete migration plan %s (%s)? Nothing on Pantech or in AWS is touched.", m.ID, m.Name)); err != nil {
				return err
			}
			_, err = c.Do(ctx(cmd), api.Request{Method: http.MethodDelete, Path: migrationPath(m.ID)}, nil)
			// A 404 after a retry means the first attempt worked.
			if err != nil && !api.IsCode(err, "MIGRATION_NOT_FOUND") && !api.IsCode(err, "RESOURCE_NOT_FOUND") {
				return err
			}
			switch {
			case a.out.JSON:
				a.out.Value(map[string]any{"id": m.ID, "deleted": true})
			case a.out.Quiet:
				a.out.Line("%s", output.Clean(m.ID))
			default:
				a.out.Success("Deleted migration plan %s.", m.ID)
			}
			return nil
		},
	}
}

func newMigrateTerraformCmd(a *app) *cobra.Command {
	var dir string
	var force bool
	cmd := &cobra.Command{
		Use:   "terraform <migration id>",
		Short: "Export a plan as Terraform: main.tf and variables.tf",
		Long: `Write a plan's planned items as Terraform for the Pantech-Dynamics/pantechdynamics
provider: main.tf and variables.tf, in ./pantech-migrate-<migration id>/ unless
--out names another directory. Files already there are not overwritten unless
you pass --force.

Values Pantech cannot know (the image to start from, an SSH key, disk
offerings, database passwords) are variables without defaults. Review it, then
run terraform init and terraform plan. With --json the API's answer, with
every file's content, is printed too.`,
		Example: `  pantech migrate terraform mig_123
  pantech migrate terraform mig_123 --out infra/pantech --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var tf api.MigrationTerraform
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: migrationPath(args[0]) + "/terraform"}, &tf)
			if err != nil {
				return migrateError(err, args[0])
			}
			if len(tf.Files) == 0 {
				return fmt.Errorf("the plan has nothing to export: is every item excluded or unsupported?\nSee: pantech migrate show %s", args[0])
			}
			out := dir
			if out == "" {
				// The id names a directory here: only ever a plain one.
				if !migrationIDPattern.MatchString(args[0]) {
					return &usageError{fmt.Errorf("%q is not a migration id (mig_…): pass --out to name the directory", output.Clean(args[0])), cmd}
				}
				out = "pantech-migrate-" + args[0]
			}
			// Every name and every target is checked before anything is written.
			files := make([]pendingFile, len(tf.Files))
			paths := make([]string, len(tf.Files))
			seen := map[string]bool{}
			var existing []string
			for i, f := range tf.Files {
				if !terraformFilePattern.MatchString(f.Name) {
					return fmt.Errorf("the API named a file %q, which is not a plain .tf file name: nothing was written", output.Clean(f.Name))
				}
				if seen[strings.ToLower(f.Name)] {
					return fmt.Errorf("the API sent %s twice: nothing was written", f.Name)
				}
				seen[strings.ToLower(f.Name)] = true
				paths[i] = filepath.Join(out, f.Name)
				files[i] = pendingFile{path: paths[i], data: []byte(f.Content)}
				if err := checkTarget(paths[i], force); errors.Is(err, errFileExists) {
					existing = append(existing, paths[i])
				} else if err != nil {
					return fmt.Errorf("%w: nothing was written", err)
				}
			}
			if len(existing) > 0 {
				return &usageError{fmt.Errorf("%s already exists: nothing was written. Pass --force to overwrite, or --out another directory", strings.Join(existing, ", ")), cmd}
			}
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
			if err := writeFiles(files, 0o644); err != nil {
				return err
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				for _, p := range paths {
					a.out.Line("%s", p)
				}
			default:
				for _, p := range paths {
					a.out.Line("%s", p)
				}
				a.out.Success("Wrote %s to %s.", count(len(paths), "file"), out)
				a.out.Note("Variables without defaults (image, SSH key, disk offerings, database passwords) need values before it applies.")
				a.out.Next("Review it, then", "cd "+out+" && terraform init && terraform plan")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "out", "", "the directory to write to (default ./pantech-migrate-<id>/)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite files already there")
	return cmd
}
