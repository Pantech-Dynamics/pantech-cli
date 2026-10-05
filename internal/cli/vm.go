package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

func newVMCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "vm",
		Aliases: []string{"vms", "instance", "instances"},
		Short:   "Create, power and delete virtual machines",
	}
	cmd.AddCommand(
		newVMListCmd(a),
		newVMGetCmd(a),
		newVMCreateCmd(a),
		newVMPowerCmd(a, "start", "Start a stopped VM", "", verb{"Starting", "Started"}),
		newVMPowerCmd(a, "stop", "Stop a running VM", "Stop %s? It stays billed for its disk.", verb{"Stopping", "Stopped"}),
		newVMPowerCmd(a, "reboot", "Reboot a VM", "Reboot %s? Anything running on it is interrupted.", verb{"Rebooting", "Rebooted"}),
		newVMDeleteCmd(a),
		newVMSSHCmd(a),
		newOrdersCmd(a, "/instance-orders", "VM"),
	)
	return cmd
}

func newVMListCmd(a *app) *cobra.Command {
	var state, search string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List VMs",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			q := url.Values{}
			if state != "" {
				q.Set("observed_state", state)
			}
			if search != "" {
				q.Set("q", search)
			}
			vms, raw, err := listItems[api.Instance](cmd, c, "/instances", q)
			if err != nil {
				return err
			}
			if a.out.JSON {
				// The API's items as sent, not the fields the table uses.
				printList(a, vms, raw, func(vm api.Instance) string { return vm.ID }, "", "")
				return nil
			}
			if a.out.Quiet {
				for _, vm := range vms {
					a.out.Line("%s", vm.ID)
				}
				return nil
			}
			if len(vms) == 0 {
				a.out.Note("No VMs yet.")
				a.out.Next("Create one", "pantech vm create --name web-1 --plan starter --image ubuntu-24-04")
				return nil
			}
			rows := make([][]string, len(vms))
			counts := map[string]int{}
			for i, vm := range vms {
				addr := vm.Address()
				rows[i] = []string{vm.Name, a.out.State(vm.ObservedState), output.Or(vm.PlanSlug), output.Or(&addr), a.out.Dim(vm.ID)}
				counts[vm.ObservedState]++
			}
			a.out.Table([]string{"name", "state", "plan", "ip", "id"}, rows)
			a.out.Summary(append([]string{count(len(vms), "VM")}, stateCounts(counts)...)...)
			return nil
		},
	}
	cmd.Flags().StringVar(&state, "state", "", "only VMs in this state, e.g. running or stopped")
	cmd.Flags().StringVar(&search, "search", "", "only VMs whose name contains this")
	return cmd
}

func newVMGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <vm>",
		Aliases: []string{"show"},
		Short:   "Show a VM, by id or name",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveVM(cmd, c, args[0])
			if err != nil {
				return err
			}
			var vm api.Instance
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: "/instances/" + url.PathEscape(id)}, &vm)
			if err != nil {
				return err
			}
			if a.out.JSON {
				a.out.RawJSON(res.Body)
				return nil
			}
			a.out.Print(vmDetail(a.out, &vm))
			return nil
		},
	}
}

// vmDetail is one VM: name and state, then what it is, where, and how to reach it.
func vmDetail(out *output.Printer, vm *api.Instance) output.Detail {
	state := out.State(vm.ObservedState)
	if vm.DesiredState != "" && vm.DesiredState != vm.ObservedState && vm.DesiredState != "present" {
		state += out.Dim(" → " + vm.DesiredState)
	}
	size := output.Or(vm.PlanSlug)
	if vm.Spec != nil {
		size += fmt.Sprintf(" · %d vCPU · %s RAM · %d GB disk", vm.Spec.VCPU, memory(vm.Spec.MemoryMB), vm.Spec.DiskGB)
	}
	location := output.Or(vm.Region)
	if vm.Zone != nil {
		location += out.Dim(" (" + *vm.Zone + ")")
	}
	// A standard VM has one address, reachable from outside; a VPC VM has a
	// private one, and a public one only through a static IP.
	addr := vm.Address()
	network := []output.Pair{{"Static IP", output.Or(&addr)}}
	if vm.InVPC() {
		network = []output.Pair{{"Public IP", output.Or(vm.PublicIPv4)}, {"Private IP", output.Or(vm.PrivateIPv4)}, {"Subnet", *vm.SubnetID}}
	}
	d := output.Detail{
		Title:    vm.Name,
		State:    state,
		Subtitle: vm.ID,
		Sections: [][]output.Pair{
			{{"Plan", size}, {"Image", output.Or(vm.ImageSlug)}, {"Location", location}},
			network,
			{{"Created", output.When(vm.CreatedAt)}},
		},
	}
	if vm.Failure != nil {
		d.Sections = append(d.Sections, []output.Pair{{"Failed", vm.Failure.Reason + out.Dim(" ("+vm.Failure.Code+")")}})
	}
	switch {
	case vm.ObservedState == "running":
		d.Next = [][2]string{{"Connect", "pantech vm ssh " + vm.Name}}
	case vm.ObservedState == "stopped":
		d.Next = [][2]string{{"Start it", "pantech vm start " + vm.Name}}
	}
	return d
}

// count is "1 VM", "4 VMs".
func count(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// stateCounts is "2 running", "1 stopped"…, most common first.
func stateCounts(counts map[string]int) []string {
	type kv struct {
		state string
		n     int
	}
	var all []kv
	for s, n := range counts {
		all = append(all, kv{s, n})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].n != all[j].n {
			return all[i].n > all[j].n
		}
		return all[i].state < all[j].state
	})
	out := make([]string, len(all))
	for i, c := range all {
		out[i] = fmt.Sprintf("%d %s", c.n, c.state)
	}
	return out
}

func memory(mb int) string {
	if mb%1024 == 0 {
		return fmt.Sprintf("%d GB", mb/1024)
	}
	return fmt.Sprintf("%d MB", mb)
}

func newVMCreateCmd(a *app) *cobra.Command {
	var name, plan, image, sshKey, region, subnet, securityGroup string
	var tags map[string]string
	var noWait bool
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a VM",
		Long: `Create a VM. The first payment is taken from your credit, or else your
organization's default card, then the VM is provisioned; this waits until it is
ready unless you pass --no-wait.

Find plans with "pantech plans", images with "pantech images" and your keys with
"pantech ssh-keys list".`,
		Example: `  pantech vm create --name web-1 --plan starter --image ubuntu-24-04 --ssh-key deploy`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			body := map[string]any{"name": name, "plan_slug": plan, "image_slug": image}
			if sshKey != "" {
				id, err := resolveSSHKey(cmd, c, sshKey)
				if err != nil {
					return err
				}
				body["ssh_key_id"] = id
			} else {
				a.out.Warn("No --ssh-key: images have no password login, so you will not be able to SSH in.")
			}
			if region != "" {
				body["region"] = region
			}
			placement := "standard"
			if subnet != "" {
				body["subnet_id"] = subnet
				placement = "vpc"
			}
			if securityGroup != "" {
				if subnet != "" {
					return errors.New("--security-group is for a standard VM: a VM in a VPC subnet is protected by the subnet's firewall rules")
				}
				id, err := resolveSecurityGroup(cmd, c, securityGroup)
				if err != nil {
					return err
				}
				body["security_group_id"] = id
			}
			if len(tags) > 0 {
				body["tags"] = tags
			}

			question := fmt.Sprintf("Create %s (%s, %s)?", name, plan, image)
			if price := planPrice(cmd, c, plan, placement); price != "" {
				question = fmt.Sprintf("Create %s (%s, %s) for about %s a month?", name, plan, image, price)
			}
			if err := a.confirm(question); err != nil {
				return err
			}

			// Creating a VM answers with an order to follow, not an operation.
			var accepted struct {
				OrderID     string `json:"order_id"`
				InstanceID  string `json:"instance_id"`
				AmountMinor int64  `json:"amount_minor"`
				Currency    string `json:"currency"`
			}
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: "/instances", Body: body}, &accepted)
			if err != nil {
				return err
			}
			if noWait {
				switch {
				case a.out.JSON:
					a.out.RawJSON(res.Body)
				case a.out.Quiet:
					a.out.Line("%s", accepted.InstanceID)
				default:
					a.out.Line("order     %s", accepted.OrderID)
					a.out.Line("instance  %s", accepted.InstanceID)
					a.out.Next("Follow it", "pantech vm orders get "+accepted.OrderID)
				}
				return nil
			}

			steps := a.out.Steps()
			steps.Mark("Ordered "+name, money(accepted.AmountMinor, accepted.Currency))
			steps.Start("Taking payment")
			paid := false
			_, err = api.WaitOrder(ctx(cmd), c, accepted.OrderID, func(o *api.InstanceOrder) {
				if !paid && o.Status != "awaiting_payment" && o.Status != "payment_failed" {
					paid = true
					steps.Done("Payment taken", "")
					steps.Start("Provisioning")
				}
				if o.Status == "provisioned" {
					steps.Done("Provisioned", "")
				}
			})
			if err != nil {
				steps.Fail("", "")
				return err
			}
			var vm api.Instance
			vres, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: "/instances/" + url.PathEscape(accepted.InstanceID)}, &vm)
			if err != nil {
				return err
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(vres.Body)
			case a.out.Quiet:
				a.out.Line("%s", vm.ID)
			default:
				a.out.Note("")
				if addr := vm.Address(); addr != "" {
					a.out.Line("%s is ready at %s", a.out.Bold(vm.Name), addr)
					a.out.Next("Connect", "pantech vm ssh "+vm.Name)
				} else {
					a.out.Line("%s is ready", a.out.Bold(vm.Name))
					a.out.Next("Details", "pantech vm get "+vm.Name)
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "its name, also its hostname (required)")
	f.StringVar(&plan, "plan", "", "plan slug, from pantech plans (required)")
	f.StringVar(&image, "image", "", "image slug, from pantech images (required)")
	f.StringVar(&sshKey, "ssh-key", "", "SSH key to install, by id or name")
	f.StringVar(&region, "region", "", "region code (default: the platform's region)")
	f.StringVar(&subnet, "subnet", "", "VPC subnet id, for a VM without its own public IPv4")
	f.StringVar(&securityGroup, "security-group", "", "security group, by id or name, for a standard VM (default: your default group)")
	f.StringToStringVar(&tags, "tags", nil, "tags as key=value pairs, e.g. --tags env=prod,team=web")
	f.BoolVar(&noWait, "no-wait", false, "return once ordered, without waiting for it to provision")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("plan")
	_ = cmd.MarkFlagRequired("image")
	return cmd
}

// planPrice is the plan's monthly estimate where it is placed (standard,
// with its own public IPv4, or vpc, without), formatted, or "" when unknown.
func planPrice(cmd *cobra.Command, c *api.Client, slug, placement string) string {
	plans, err := api.ListAll[api.Plan](ctx(cmd), c, "/plans", url.Values{"placement": {placement}})
	if err != nil {
		return ""
	}
	for _, p := range plans {
		if p.Slug == slug && p.Price != nil {
			return money(p.Price.MonthlyEstimateMinor, p.Price.Currency)
		}
	}
	return ""
}

// money formats minor units: 1700000 NGN → NGN 17,000.00.
func money(minor int64, currency string) string {
	whole, cents := minor/100, minor%100
	s := fmt.Sprint(whole)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return fmt.Sprintf("%s %s.%02d", currency, s, cents)
}

// verb is how a change reads while it happens and once it has.
type verb struct{ ing, ed string }

func newVMPowerCmd(a *app, action, short, question string, v verb) *cobra.Command {
	var noWait bool
	cmd := &cobra.Command{
		Use:   action + " <vm>",
		Short: short + ", by id or name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveVM(cmd, c, args[0])
			if err != nil {
				return err
			}
			if question != "" {
				if err := a.confirm(fmt.Sprintf(question, args[0])); err != nil {
					return err
				}
			}
			return a.runWrite(cmd, c, api.Request{Method: http.MethodPost, Path: "/instances/" + url.PathEscape(id) + "/" + action}, noWait, v, args[0])
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	return cmd
}

func newVMDeleteCmd(a *app) *cobra.Command {
	var noWait bool
	cmd := &cobra.Command{
		Use:     "delete <vm>",
		Aliases: []string{"rm"},
		Short:   "Delete a VM and its root disk, by id or name",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveVM(cmd, c, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Delete %s (%s)? Its root disk is erased and cannot be recovered.", args[0], id)); err != nil {
				return err
			}
			return a.runWrite(cmd, c, api.Request{Method: http.MethodDelete, Path: "/instances/" + url.PathEscape(id)}, noWait, verb{"Deleting", "Deleted"}, args[0])
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	return cmd
}

// runWrite sends a write that answers with an operation, then follows it:
// "⠼ Stopping web-1  0:03", then "✓ Stopped web-1  9s".
func (a *app) runWrite(cmd *cobra.Command, c *api.Client, req api.Request, noWait bool, v verb, subject string) error {
	var accepted api.Accepted
	res, err := c.Do(ctx(cmd), req, &accepted)
	if err != nil {
		return err
	}
	if noWait || accepted.OperationID == "" {
		if a.out.JSON {
			a.out.RawJSON(res.Body)
		} else {
			a.out.Line("%s", accepted.OperationID)
			a.out.Next("Follow it", "pantech operations wait "+accepted.OperationID)
		}
		return nil
	}
	steps := a.out.Steps()
	steps.Start(v.ing + " " + subject)
	op, err := api.WaitOperation(ctx(cmd), c, accepted.OperationID, nil)
	if err != nil {
		steps.Fail("", "")
		return err
	}
	steps.Done(v.ed+" "+subject, "")
	if a.out.JSON {
		a.out.Value(op)
	}
	return nil
}

func newVMSSHCmd(a *app) *cobra.Command {
	var user string
	var revoke bool
	cmd := &cobra.Command{
		Use:   "ssh <vm> [-- ssh arguments]",
		Short: "Open SSH access to a VM for 15 minutes and connect",
		Long: `Open SSH access to a VM and connect, with your own ssh and keys.

Port 22 is closed by default. This asks the platform to open it to your
address for 15 minutes, waits for that, and connects to the host the grant
names. The port closes again after 15 minutes; --revoke closes it as soon
as your session ends.

The user is the image's usual cloud user (ubuntu, debian, rocky; root
otherwise); --user changes it. Anything after -- goes to ssh.`,
		Example: `  pantech vm ssh web-1
  pantech vm ssh web-1 --revoke
  pantech vm ssh web-1 -- -i ~/.ssh/deploy -L 8080:localhost:80`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveVM(cmd, c, args[0])
			if err != nil {
				return err
			}
			var vm api.Instance
			if _, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: "/instances/" + url.PathEscape(id)}, &vm); err != nil {
				return err
			}
			sshPath, err := exec.LookPath("ssh")
			if err != nil {
				return errors.New("no ssh on your PATH")
			}
			grant, err := a.openSSHAccess(cmd, c, &vm)
			if err != nil {
				return err
			}
			if user == "" {
				user = defaultUser(vm.ImageSlug)
			}
			argv := sshArgv(user, grant, args[1:])
			a.out.Note("%s", a.out.Dim(strings.Join(argv, " ")))
			if !revoke {
				// Replace this process, so ssh owns the terminal and its exit code is ours.
				return syscall.Exec(sshPath, argv, os.Environ())
			}
			session := exec.Command(sshPath, argv[1:]...)
			session.Stdin, session.Stdout, session.Stderr = os.Stdin, os.Stdout, os.Stderr
			runErr := session.Run()
			if err := a.revokeSSHAccess(cmd, c, vm.ID, grant.ID); err != nil {
				return fmt.Errorf("the session ended but SSH access is still open until it expires: %w", err)
			}
			var exitErr *exec.ExitError
			if errors.As(runErr, &exitErr) {
				return &exitStatus{code: exitErr.ExitCode()}
			}
			return runErr
		},
	}
	cmd.Flags().StringVarP(&user, "user", "l", "", "user to log in as (default: the image's usual one)")
	cmd.Flags().BoolVar(&revoke, "revoke", false, "close SSH access as soon as the session ends")
	return cmd
}

// openSSHAccess opens port 22 to the caller for 15 minutes, waits for it,
// and returns the grant, which names the host to connect to.
func (a *app) openSSHAccess(cmd *cobra.Command, c *api.Client, vm *api.Instance) (*api.SSHAccessGrant, error) {
	base := "/instances/" + url.PathEscape(vm.ID) + "/ssh-access"
	var accepted api.Accepted
	if _, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: base}, &accepted); err != nil {
		return nil, fmt.Errorf("opening SSH access to %s: %w", vm.Name, err)
	}
	steps := a.out.Steps()
	steps.Start("Opening SSH access to " + vm.Name)
	if accepted.OperationID != "" {
		if _, err := api.WaitOperation(ctx(cmd), c, accepted.OperationID, nil); err != nil {
			steps.Fail("", "")
			return nil, err
		}
	}
	var grant api.SSHAccessGrant
	if _, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: base + "/" + url.PathEscape(accepted.ResourceID)}, &grant); err != nil {
		steps.Fail("", "")
		return nil, err
	}
	if grant.Status != "active" {
		steps.Fail("", "")
		return nil, fmt.Errorf("SSH access to %s is %s, not active (grant %s)", vm.Name, grant.Status, grant.ID)
	}
	if grant.Host == nil || *grant.Host == "" {
		// The grant names the host; a standard VM's own address is the fallback.
		addr := vm.Address()
		if addr == "" {
			steps.Fail("", "")
			return nil, fmt.Errorf("SSH access to %s is open but has no host to connect to (grant %s)", vm.Name, grant.ID)
		}
		grant.Host = &addr
	}
	until := ""
	if grant.ExpiresAt != nil {
		until = "until " + output.When(grant.ExpiresAt)
	}
	steps.Done("SSH access open", until)
	return &grant, nil
}

// revokeSSHAccess closes a grant's port and waits for it.
func (a *app) revokeSSHAccess(cmd *cobra.Command, c *api.Client, vmID, grantID string) error {
	var accepted api.Accepted
	path := "/instances/" + url.PathEscape(vmID) + "/ssh-access/" + url.PathEscape(grantID)
	if _, err := c.Do(ctx(cmd), api.Request{Method: http.MethodDelete, Path: path}, &accepted); err != nil {
		return err
	}
	steps := a.out.Steps()
	steps.Start("Closing SSH access")
	if accepted.OperationID != "" {
		if _, err := api.WaitOperation(ctx(cmd), c, accepted.OperationID, nil); err != nil {
			steps.Fail("", "")
			return err
		}
	}
	steps.Done("SSH access closed", "")
	return nil
}

// sshArgv is the ssh command line for a grant: user@host, its port when it
// is not 22, then the caller's own arguments.
func sshArgv(user string, grant *api.SSHAccessGrant, extra []string) []string {
	argv := []string{"ssh"}
	if grant.Port != nil && *grant.Port != 0 && *grant.Port != 22 {
		argv = append(argv, "-p", fmt.Sprint(*grant.Port))
	}
	argv = append(argv, user+"@"+*grant.Host)
	return append(argv, extra...)
}

// defaultUser is the usual cloud user of an image, by its slug.
func defaultUser(image *string) string {
	if image == nil {
		return "root"
	}
	for _, distro := range []string{"ubuntu", "debian", "rocky", "almalinux", "fedora"} {
		if strings.HasPrefix(*image, distro) {
			return distro
		}
	}
	return "root"
}

// resolveVM takes an id (vm_…) or a name and returns the id.
func resolveVM(cmd *cobra.Command, c *api.Client, ref string) (string, error) {
	if strings.HasPrefix(ref, "vm_") {
		return ref, nil
	}
	vms, err := api.ListAll[api.Instance](ctx(cmd), c, "/instances", url.Values{"q": {ref}})
	if err != nil {
		return "", err
	}
	var ids []string
	for _, vm := range vms {
		if vm.Name == ref {
			ids = append(ids, vm.ID)
		}
	}
	switch len(ids) {
	case 0:
		return "", fmt.Errorf("no VM named %q\nSee: pantech vm list", ref)
	case 1:
		return ids[0], nil
	default:
		return "", fmt.Errorf("%d VMs are named %q: use its id\n(%s)", len(ids), ref, strings.Join(ids, ", "))
	}
}
