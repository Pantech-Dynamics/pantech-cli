package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

var databaseKind = kind{prefix: "db_", path: "/databases", noun: "database", listCmd: "pantech db list"}

func resolveDatabase(cmd *cobra.Command, c *api.Client, ref string) (string, error) {
	return resolve(cmd, c, databaseKind, ref, func(d api.Database) (string, string) { return d.ID, d.Name })
}

func newDBCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "db",
		Aliases: []string{"database", "databases"},
		Short:   "Create and manage PostgreSQL, MySQL and MariaDB databases",
		Long: `Create and manage single-node PostgreSQL, MySQL and MariaDB databases. A
database is never on a public address: it is reached from the CIDRs in its
access rules. It is paid for upfront and billed like a VM of the same plan.`,
	}
	cmd.AddCommand(
		newDBEnginesCmd(a),
		newDBListCmd(a),
		newDBGetCmd(a),
		newDBCreateCmd(a),
		newDBPowerCmd(a, "start", "Start a stopped database", "", verb{"Starting", "Started"}),
		newDBPowerCmd(a, "stop", "Stop a running database", "Stop %s? Connections to it are dropped, and it stays billed.", verb{"Stopping", "Stopped"}),
		deleteCmd(a, databaseKind, "Its data is erased.", resolveDatabase),
		newDBAccessRulesCmd(a),
		newDBSecurityGroupsCmd(a),
		newDBPasswordCmd(a),
		newDBStorageCmd(a),
		newDBSnapshotsCmd(a),
		newOrdersCmd(a, "/database-orders", "Database"),
	)
	return cmd
}

func newDBEnginesCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "engines",
		Short: "List the engines and versions you can create",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			engines, raw, err := listItems[api.DatabaseEngine](cmd, c, "/database-engines", nil)
			if err != nil {
				return err
			}
			if printList(a, engines, raw, func(e api.DatabaseEngine) string { return e.Engine }, "No engines are offered right now.", "") {
				return nil
			}
			var rows [][]string
			for _, e := range engines {
				if len(e.Versions) == 0 {
					rows = append(rows, []string{e.Engine, e.DisplayName, a.out.Dim("none offered"), "", ""})
				}
				for i, v := range e.Versions {
					engine, name := e.Engine, e.DisplayName
					if i > 0 {
						engine, name = "", ""
					}
					rows = append(rows, []string{engine, name, v.Version, output.Or(v.EOLDate), a.out.Dim(strings.Join(v.Zones, ", "))})
				}
			}
			a.out.Table([]string{"engine", "name", "version", "end of life", "zones"}, rows)
			if storage := engineStorageRows(a.out, engines); len(storage) > 0 {
				a.out.Line("")
				a.out.Table([]string{"engine", "zone", "storage", "per GB a month"}, storage)
			}
			return nil
		},
	}
}

// engineStorageRows is each engine's data disk sizes per zone: from the
// plan's disk (or the zone's minimum) to the maximum, in steps, and the price
// of a GB, or that it is not priced.
func engineStorageRows(out *output.Printer, engines []api.DatabaseEngine) [][]string {
	var rows [][]string
	for _, e := range engines {
		for i, o := range e.Storage {
			from := "plan's disk"
			if o.MinGB > 0 {
				from = fmt.Sprintf("plan's disk (at least %d GB)", o.MinGB)
			}
			price := out.Dim("not priced")
			if o.PricePerGBMonthMinor != nil && o.Currency != nil {
				price = money(*o.PricePerGBMonthMinor, *o.Currency)
			}
			engine := e.Engine
			if i > 0 {
				engine = ""
			}
			rows = append(rows, []string{engine, o.ZoneID, fmt.Sprintf("%s to %d GB, in %d GB steps", from, o.MaxGB, o.StepGB), price})
		}
	}
	return rows
}

func newDBListCmd(a *app) *cobra.Command {
	var stateFilter, search string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List databases",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			q := url.Values{}
			if stateFilter != "" {
				q.Set("observed_state", stateFilter)
			}
			if search != "" {
				q.Set("q", search)
			}
			dbs, raw, err := listItems[api.Database](cmd, c, "/databases", q)
			if err != nil {
				return err
			}
			if printList(a, dbs, raw, func(d api.Database) string { return d.ID }, "No databases yet.", "pantech db create --name app --engine postgresql --version 18 --plan starter --zone af-abj-1") {
				return nil
			}
			rows := make([][]string, len(dbs))
			counts := map[string]int{}
			for i, d := range dbs {
				rows[i] = []string{d.Name, a.out.State(d.ObservedState), d.Engine + " " + d.Version, output.Or(d.Hostname), a.out.Dim(d.ID)}
				counts[d.ObservedState]++
			}
			a.out.Table([]string{"name", "state", "engine", "hostname", "id"}, rows)
			a.out.Summary(append([]string{count(len(dbs), "database")}, stateCounts(counts)...)...)
			return nil
		},
	}
	cmd.Flags().StringVar(&stateFilter, "state", "", "only databases in this state, e.g. running or stopped")
	cmd.Flags().StringVar(&search, "search", "", "only databases whose name contains this")
	return cmd
}

func newDBGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <database>",
		Aliases: []string{"show"},
		Short:   "Show a database and how to connect to it, by id or name",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveDatabase(cmd, c, args[0])
			if err != nil {
				return err
			}
			var d api.Database
			if ok, err := getOne(a, cmd, c, "/databases/"+url.PathEscape(id), &d); !ok {
				return err
			}
			a.out.Print(dbDetail(a.out, &d))
			return nil
		},
	}
}

func dbDetail(out *output.Printer, d *api.Database) output.Detail {
	endpoint := "—"
	if d.Hostname != nil && *d.Hostname != "" {
		endpoint = fmt.Sprintf("%s:%d", *d.Hostname, d.Port)
	}
	rules := make([]string, len(d.AccessRules))
	for i, r := range d.AccessRules {
		rules[i] = r.CIDR
	}
	access := strings.Join(rules, ", ")
	if access == "" {
		access = "none: nothing can connect"
	}
	if d.Generation != d.ObservedGeneration {
		access += out.Dim(" (applying)")
	}
	groups := "none"
	if len(d.SecurityGroupIDs) > 0 {
		groups = strings.Join(d.SecurityGroupIDs, ", ")
	}
	location := d.ZoneID
	if d.SubnetID != nil && *d.SubnetID != "" {
		location += out.Dim(" (subnet " + *d.SubnetID + ")")
	}
	det := output.Detail{
		Title:    d.Name,
		State:    state(out, d.ObservedState, d.DesiredState),
		Subtitle: d.ID,
		Sections: [][]output.Pair{
			{{"Engine", d.Engine + " " + d.Version}, {"Storage", dbStorage(out, d)}, {"Location", location}},
			{{"Endpoint", endpoint}, {"Private IP", output.Or(d.PrivateIP)}, {"User", d.AdminUsername}, {"Access from", access}, {"Security groups", groups}},
			{{"Created", output.When(d.CreatedAt)}},
		},
	}
	if len(d.SecurityGroupIDs) > 0 {
		var applied []output.Pair
		for _, r := range d.EffectiveAccessRules {
			applied = append(applied, output.Pair{"Allows", r.CIDR + out.Dim(" ("+r.Source+")")})
		}
		for _, r := range d.IgnoredSecurityGroupRules {
			rule := api.SecurityGroupRule{Direction: r.Direction, Protocol: r.Protocol, CIDR: r.CIDR}
			if r.PortRange != nil {
				rule.PortRange = *r.PortRange
			}
			applied = append(applied, output.Pair{"Ignores", formatRule(rule) + out.Dim(" ("+r.SecurityGroupID+": "+r.Reason+")")})
		}
		det.Sections = append(det.Sections[:2], append([][]output.Pair{applied}, det.Sections[2:]...)...)
	}
	if d.FailureCode != nil && *d.FailureCode != "" {
		det.Sections = append(det.Sections, []output.Pair{{"Failed", *d.FailureCode}})
	}
	if d.ObservedState == "running" && d.Hostname != nil && *d.Hostname != "" {
		det.Next = [][2]string{{"Connect", connectCommand(d)}}
	}
	return det
}

func dbStorage(out *output.Printer, d *api.Database) string {
	s := fmt.Sprintf("%d GB", d.DataVolumeSizeGB)
	if d.PendingDataVolumeSizeGB != nil && *d.PendingDataVolumeSizeGB != d.DataVolumeSizeGB {
		s += out.Dim(fmt.Sprintf(" (growing to %d GB)", *d.PendingDataVolumeSizeGB))
	}
	return s
}

func connectCommand(d *api.Database) string {
	if d.Engine == "postgresql" {
		return fmt.Sprintf("psql \"host=%s port=%d user=%s dbname=postgres sslmode=require\"", *d.Hostname, d.Port, d.AdminUsername)
	}
	return fmt.Sprintf("mysql -h %s -P %d -u %s -p", *d.Hostname, d.Port, d.AdminUsername)
}

func newDBCreateCmd(a *app) *cobra.Command {
	var name, engine, version, plan, zone, subnet, adminUsername string
	var accessRules []string
	var storageGB int
	var noAccessRules, passwordStdin, noWait bool
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a database",
		Long: `Create a database. The first payment is taken from your credit, or else your
organization's default card, then the database is provisioned; this waits
until it is running unless you pass --no-wait.

The admin login is --admin-username (default dbadmin): 3 to 32 lowercase
letters, digits or underscores, starting with a letter; root, postgres,
public, none, all, user, sys, system, replication, current_user, current_role,
session_user and names starting pg_, mysql, mariadb or pantech are reserved.

The admin password: pass --password-stdin to choose it (16 to 128 printable
ASCII characters, no spaces, quotes or backslashes), typed at a hidden prompt
or piped in. Without it one is generated and printed ONCE, alone on stdout:
it is never shown again (pantech db password reset makes a new one). With
--quiet it is the line before the id; with --json it is the "password" field
of the order.

In a VPC zone --subnet is required and the subnet's range may connect by
default; in a standard zone nothing may connect until you add access rules.
--storage-gb sizes the data disk (default: the plan's disk_gb; above it, a
multiple of the zone's step, at most its maximum: 10 GB and 2000 GB today, as
"pantech db engines" shows); a size the zone does not allow is refused before
anything is ordered. "pantech db storage resize" grows it later.
Find engines and versions with "pantech db engines", plans with
"pantech plans", zones with "pantech regions".`,
		Example: `  pantech db create --name app --engine postgresql --version 18 --plan starter --zone af-abj-1 --access-rule 203.0.113.4/32
  pantech db create --name app --engine mysql --version 8.4 --plan starter --zone af-abj-2 --subnet snet_123 --password-stdin < pw.txt`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var password string
			if passwordStdin {
				var err error
				if password, err = readPassword(true); err != nil {
					return err
				}
			}
			if noAccessRules && len(accessRules) > 0 {
				return &usageError{errors.New("--no-access-rules and --access-rule do not go together"), cmd}
			}
			if adminUsername != "" {
				if err := checkAdminUsername(adminUsername); err != nil {
					return &usageError{err, cmd}
				}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			body := map[string]any{"name": name, "engine": engine, "version": version, "plan_slug": plan, "zone_id": zone}
			if subnet != "" {
				body["subnet_id"] = subnet
			}
			if adminUsername != "" {
				body["admin_username"] = adminUsername
			}
			switch {
			case noAccessRules:
				body["access_rules"] = []string{}
			case len(accessRules) > 0:
				body["access_rules"] = accessRules
			}
			if password != "" {
				body["password"] = password
			}
			size := plan
			if storageGB > 0 {
				body["storage_gb"] = storageGB
				size = fmt.Sprintf("%s, %d GB storage", plan, storageGB)
			}

			p := findPlan(cmd, c, plan, "vpc")
			if storageGB > 0 {
				planDiskGB := 0
				if p != nil {
					planDiskGB = p.DiskGB
				}
				if err := checkCreateStorage(dbStorageLimits(cmd, c, engine, zone), planDiskGB, storageGB); err != nil {
					return err
				}
			}

			question := fmt.Sprintf("Create database %s (%s %s, %s)?", name, engine, version, size)
			if price := priceOf(p); price != "" {
				amount := "about " + price
				if storageGB > 0 {
					amount = "from " + price
				}
				question = fmt.Sprintf("Create database %s (%s %s, %s) for %s a month?", name, engine, version, size, amount)
			}
			if err := a.confirm(question); err != nil {
				return err
			}

			var accepted api.DatabaseOrderAccepted
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: "/databases", Body: body}, &accepted)
			if err != nil {
				return err
			}
			if !a.out.JSON {
				a.showPassword(accepted.AdminUsername, accepted.Password, accepted.PasswordReturned, password != "", accepted.DatabaseID)
			}
			if noWait {
				switch {
				case a.out.JSON:
					a.out.RawJSON(res.Body)
				case a.out.Quiet:
					a.out.Line("%s", accepted.DatabaseID)
				default:
					a.out.Note("order     %s", accepted.OrderID)
					a.out.Note("database  %s", accepted.DatabaseID)
					a.out.Next("Follow it", "pantech db orders get "+accepted.OrderID)
				}
				return nil
			}

			d, dbBody, err := a.waitDatabase(cmd, c, name, &accepted)
			if err != nil {
				if a.out.JSON {
					a.out.RawJSON(res.Body)
				}
				return err
			}
			switch {
			case a.out.JSON:
				a.out.Value(map[string]json.RawMessage{"order": res.Body, "database": dbBody})
			case a.out.Quiet:
				a.out.Line("%s", d.ID)
			default:
				a.out.Note("")
				a.out.Success("%s is %s.", a.out.Bold(d.Name), d.ObservedState)
				a.out.Print(dbDetail(a.out, d))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "its name (required)")
	f.StringVar(&engine, "engine", "", "postgresql, mysql or mariadb (required)")
	f.StringVar(&version, "version", "", "a version from pantech db engines, e.g. 18 (required)")
	f.StringVar(&plan, "plan", "", "plan slug, from pantech plans: vCPU, memory and storage (required)")
	f.StringVar(&zone, "zone", "", "zone, e.g. af-abj-1 (standard) or af-abj-2 (VPC), from pantech regions (required)")
	f.StringVar(&subnet, "subnet", "", "VPC subnet id: required in a VPC zone, not allowed otherwise")
	f.StringVar(&adminUsername, "admin-username", "", "the admin login: 3 to 32 lowercase letters, digits or underscores, starting with a letter (default: dbadmin)")
	f.IntVar(&storageGB, "storage-gb", 0, "data disk size in GB (default: the plan's disk); it can grow later, never shrink")
	f.StringArrayVar(&accessRules, "access-rule", nil, "an IPv4 CIDR allowed to connect (repeatable; default: the zone's default)")
	f.BoolVar(&noAccessRules, "no-access-rules", false, "start with no access rules at all")
	f.BoolVar(&passwordStdin, "password-stdin", false, "read the admin password from stdin instead of having one generated")
	f.BoolVar(&noWait, "no-wait", false, "return once ordered, without waiting for it to provision")
	for _, req := range []string{"name", "engine", "version", "plan", "zone"} {
		_ = cmd.MarkFlagRequired(req)
	}
	return cmd
}

func (a *app) waitDatabase(cmd *cobra.Command, c *api.Client, name string, accepted *api.DatabaseOrderAccepted) (*api.Database, []byte, error) {
	steps := a.out.Steps()
	steps.Mark("Ordered "+name, money(accepted.AmountMinor, accepted.Currency))
	steps.Start("Taking payment")
	paid := false
	order, err := api.WaitDatabaseOrder(ctx(cmd), c, accepted.OrderID, func(o *api.DatabaseOrder) {
		if !paid && o.Status != "awaiting_payment" && o.Status != "payment_failed" {
			paid = true
			steps.Done("Payment taken", "")
			steps.Start("Starting provisioning")
		}
	})
	if err != nil {
		steps.Fail("", "")
		return nil, nil, err
	}
	if !paid {
		steps.Done("Payment taken", "")
	} else {
		steps.Done("", "")
	}
	if order.OperationID != nil && *order.OperationID != "" {
		steps.Start("Provisioning " + name)
		if _, err := api.WaitOperation(ctx(cmd), c, *order.OperationID, nil); err != nil {
			steps.Fail("", "")
			return nil, nil, err
		}
		steps.Done("Provisioned "+name, "")
	}
	var d api.Database
	body, err := api.Get(ctx(cmd), c, "/databases/"+url.PathEscape(accepted.DatabaseID), &d)
	if err != nil {
		return nil, nil, err
	}
	return &d, body, nil
}

func (a *app) showPassword(user string, password *string, returned, chosen bool, dbRef string) {
	switch {
	case returned && password != nil && *password != "":
		a.out.Note("Admin password for %s, shown once and never again. Save it now:", user)
		a.out.Line("%s", *password)
	case chosen:
	default:
		a.out.Warn("The generated password was returned to an earlier attempt of this request and cannot be shown again. Set a new one: pantech db password reset %s", dbRef)
	}
}

func newDBPowerCmd(a *app, action, short, question string, v verb) *cobra.Command {
	var noWait bool
	cmd := &cobra.Command{
		Use:   action + " <database>",
		Short: short + ", by id or name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveDatabase(cmd, c, args[0])
			if err != nil {
				return err
			}
			if question != "" {
				if err := a.confirm(fmt.Sprintf(question, args[0])); err != nil {
					return err
				}
			}
			return a.runWrite(cmd, c, api.Request{Method: http.MethodPost, Path: "/databases/" + url.PathEscape(id) + "/" + action}, noWait, v, args[0])
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	return cmd
}

func newDBAccessRulesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "access-rules",
		Aliases: []string{"access"},
		Short:   "Change who may connect to a database",
	}
	var none, noWait bool
	set := &cobra.Command{
		Use:   "set <database> [cidr…]",
		Short: "Replace a database's access rules",
		Long: `Replace the whole allow-list of a database with the IPv4 CIDRs given. Each
allows TCP to the engine's port. Prefixes run from /8 to /32 and 0.0.0.0/0 is
refused; host bits are cleared (10.0.1.7/24 is 10.0.1.0/24), and a range may
appear once. At most 50 rules, and at most 1024 ranges together with those
the database's security groups add. Connections from a range you remove are
ended. --none removes every rule.`,
		Example: `  pantech db access-rules set app 203.0.113.4/32 10.0.1.0/24
  pantech db access-rules set app --none`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cidrs := args[1:]
			if len(cidrs) == 0 && !none {
				return &usageError{errors.New("give the CIDRs allowed to connect, or --none to remove every rule"), cmd}
			}
			if len(cidrs) > 0 && none {
				return &usageError{errors.New("--none takes no CIDRs"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveDatabase(cmd, c, args[0])
			if err != nil {
				return err
			}
			rules := make([]map[string]string, len(cidrs))
			for i, cidr := range cidrs {
				rules[i] = map[string]string{"cidr": cidr}
			}
			question := fmt.Sprintf("Allow only %s to connect to %s? Connections from anywhere else are ended.", strings.Join(cidrs, ", "), args[0])
			if none {
				question = fmt.Sprintf("Remove every access rule of %s? Nothing will be able to connect.", args[0])
			}
			if err := a.confirm(question); err != nil {
				return err
			}
			req := api.Request{Method: http.MethodPut, Path: "/databases/" + url.PathEscape(id) + "/access-rules", Body: map[string]any{"rules": rules}}
			return a.runWrite(cmd, c, req, noWait, verb{"Updating access to", "Updated access to"}, args[0])
		},
	}
	set.Flags().BoolVar(&none, "none", false, "remove every rule")
	set.Flags().BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	cmd.AddCommand(set)
	return cmd
}

func newDBSecurityGroupsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "security-groups",
		Aliases: []string{"security-group", "sg"},
		Short:   "Let a database's allow-list follow security groups",
	}
	var none, noWait bool
	set := &cobra.Command{
		Use:   "set <database> [security group…]",
		Short: "Replace the security groups attached to a database",
		Long: `Replace the security groups attached to a database (at most 5, in its zone),
by id or name. A group only adds address ranges to the database's allow-list:
its ingress rules for tcp or all whose ports include the engine's, from an IPv4
CIDR of /8 or longer. Its other rules are ignored, and "pantech db get" lists
them with the reason. The database follows later changes to the groups' rules.
--none detaches every group.`,
		Example: `  pantech db security-groups set app office vpn
  pantech db security-groups set app --none`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			refs := args[1:]
			if len(refs) == 0 && !none {
				return &usageError{errors.New("give the security groups to attach, or --none to detach every group"), cmd}
			}
			if len(refs) > 0 && none {
				return &usageError{errors.New("--none takes no security groups"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveDatabase(cmd, c, args[0])
			if err != nil {
				return err
			}
			ids := make([]string, len(refs))
			for i, ref := range refs {
				if ids[i], err = resolveSecurityGroup(cmd, c, ref); err != nil {
					return err
				}
			}
			req := api.Request{Method: http.MethodPut, Path: "/databases/" + url.PathEscape(id) + "/security-groups", Body: map[string]any{"security_group_ids": ids}}
			return a.runWrite(cmd, c, req, noWait, verb{"Updating the security groups of", "Updated the security groups of"}, args[0])
		},
	}
	set.Flags().BoolVar(&none, "none", false, "detach every security group")
	set.Flags().BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	cmd.AddCommand(set)
	return cmd
}

func newDBPasswordCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "password",
		Short: "Set or reset a database's admin password",
	}
	var setNoWait, passwordStdin bool
	set := &cobra.Command{
		Use:   "set <database> --password-stdin",
		Short: "Set the admin password to one you choose",
		Long: `Set the admin password to one you choose: 16 to 128 printable ASCII
characters, with no spaces, quotes or backslashes. It is read from stdin
(a hidden prompt at a terminal), never from an argument. The database must be
running.`,
		Example: `  pantech db password set app --password-stdin
  pantech db password set app --password-stdin < pw.txt`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !passwordStdin {
				return &usageError{errors.New("pass --password-stdin: the password is read from stdin, never from an argument"), cmd}
			}
			password, err := readPassword(true)
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveDatabase(cmd, c, args[0])
			if err != nil {
				return err
			}
			req := api.Request{Method: http.MethodPut, Path: "/databases/" + url.PathEscape(id) + "/password", Body: map[string]string{"password": password}}
			return a.runWrite(cmd, c, req, setNoWait, verb{"Setting the password of", "Set the password of"}, args[0])
		},
	}
	set.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin (required)")
	set.Flags().BoolVar(&setNoWait, "no-wait", false, "return the operation id without waiting")

	var resetNoWait bool
	reset := &cobra.Command{
		Use:   "reset <database>",
		Short: "Replace the admin password with a generated one, shown once",
		Long: `Replace the admin password with a generated one, printed once, alone on
stdout: it is never shown again. The old password stops working. The
database must be running.`,
		Example: `  pantech db password reset app`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveDatabase(cmd, c, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Reset the admin password of %s? The current one stops working.", args[0])); err != nil {
				return err
			}
			var accepted api.DatabasePasswordAccepted
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: "/databases/" + url.PathEscape(id) + "/reset-password"}, &accepted)
			if err != nil {
				return err
			}
			if a.out.JSON {
				a.out.RawJSON(res.Body)
			} else if accepted.PasswordReturned && accepted.Password != nil {
				a.out.Note("New admin password for %s, shown once and never again. Save it now:", args[0])
				a.out.Line("%s", *accepted.Password)
			} else {
				return fmt.Errorf("the new password was returned to an earlier attempt of this request and cannot be shown again: run pantech db password reset %s again", args[0])
			}
			if resetNoWait || accepted.OperationID == "" {
				a.out.Next("Follow it", "pantech operations wait "+accepted.OperationID)
				return nil
			}
			steps := a.out.Steps()
			steps.Start("Applying the new password")
			if _, err := api.WaitOperation(ctx(cmd), c, accepted.OperationID, nil); err != nil {
				steps.Fail("", "")
				return err
			}
			steps.Done("Password reset", "")
			return nil
		},
	}
	reset.Flags().BoolVar(&resetNoWait, "no-wait", false, "return without waiting for the password to apply")
	cmd.AddCommand(set, reset)
	return cmd
}

var stdin io.Reader = os.Stdin

func readPassword(confirm bool) (string, error) {
	var password string
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(os.Stderr, "Admin password: ")
		first, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		if confirm {
			fmt.Fprint(os.Stderr, "Again: ")
			again, err := term.ReadPassword(int(f.Fd()))
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return "", err
			}
			if string(again) != string(first) {
				return "", errors.New("the passwords do not match")
			}
		}
		password = string(first)
	} else {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", errors.New("no password on stdin")
		}
		password = strings.TrimRight(line, "\r\n")
	}
	return password, checkPassword(password)
}

// reservedAdminUsernames and reservedAdminUsernamePrefixes are the names
// the platform refuses for an admin login.
var (
	reservedAdminUsernames = map[string]bool{
		"root": true, "postgres": true, "public": true, "none": true, "all": true,
		"current_user": true, "current_role": true, "session_user": true, "user": true,
		"sys": true, "system": true, "replication": true,
	}
	reservedAdminUsernamePrefixes = []string{"pg_", "mysql", "mariadb", "pantech"}
)

// checkAdminUsername is the platform's rule for an admin login: 3 to 32
// lowercase letters, digits or underscores, starting with a letter, and not
// a reserved name.
func checkAdminUsername(name string) error {
	if len(name) < 3 || len(name) > 32 {
		return fmt.Errorf("--admin-username must be 3 to 32 characters (it is %d)", len(name))
	}
	if name[0] < 'a' || name[0] > 'z' {
		return errors.New("--admin-username must start with a lowercase letter")
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return errors.New("--admin-username may only have lowercase letters, digits and underscores")
		}
	}
	if reservedAdminUsernames[name] {
		return fmt.Errorf("--admin-username %s is reserved: choose another name", name)
	}
	for _, p := range reservedAdminUsernamePrefixes {
		if strings.HasPrefix(name, p) {
			return fmt.Errorf("--admin-username may not start with %s: choose another name", p)
		}
	}
	return nil
}

func checkPassword(p string) error {
	if len(p) < 16 || len(p) > 128 {
		return fmt.Errorf("the password must be 16 to 128 characters (it is %d)", len(p))
	}
	for _, r := range p {
		switch {
		case r < 0x21 || r > 0x7e:
			return errors.New("the password must be printable ASCII, with no spaces")
		case r == '\'' || r == '"' || r == '\\':
			return errors.New("the password must not contain quotes or backslashes")
		}
	}
	return nil
}
