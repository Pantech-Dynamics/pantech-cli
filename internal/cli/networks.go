package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

var (
	networkKind  = kind{prefix: "net_", path: "/networks", noun: "network", listCmd: "pantech networks list"}
	subnetKind   = kind{prefix: "snet_", path: "/subnets", noun: "subnet", listCmd: "pantech networks subnets list <network>"}
	publicIPKind = kind{prefix: "pip_", path: "/public-ips", noun: "public IP", listCmd: "pantech public-ips list", noSearch: true}
)

func resolveNetwork(cmd *cobra.Command, c *api.Client, ref string) (string, error) {
	return resolve(cmd, c, networkKind, ref, func(n api.Network) (string, string) { return n.ID, n.Name })
}

// resolveSubnet takes an id (snet_…) or a name. Subnets are listed only by
// network, so a name is looked for in every network's.
func resolveSubnet(cmd *cobra.Command, c *api.Client, ref string) (string, error) {
	if strings.HasPrefix(ref, subnetKind.prefix) {
		return ref, nil
	}
	nets, err := api.ListAll[api.Network](ctx(cmd), c, "/networks", nil)
	if err != nil {
		return "", err
	}
	var ids []string
	for _, n := range nets {
		subnets, err := api.ListAll[api.Subnet](ctx(cmd), c, "/networks/"+url.PathEscape(n.ID)+"/subnets", nil)
		if err != nil {
			return "", err
		}
		for _, s := range subnets {
			if s.Name == ref {
				ids = append(ids, s.ID)
			}
		}
	}
	switch len(ids) {
	case 0:
		return "", fmt.Errorf("no subnet named %q\nSee: %s", ref, subnetKind.listCmd)
	case 1:
		return ids[0], nil
	default:
		return "", fmt.Errorf("%d subnets are named %q: use its id\n(%s)", len(ids), ref, strings.Join(ids, ", "))
	}
}

// resolvePublicIP takes an id, or the address itself.
func resolvePublicIP(cmd *cobra.Command, c *api.Client, ref string) (string, error) {
	return resolve(cmd, c, publicIPKind, ref, func(p api.PublicIP) (string, string) {
		if p.Address != nil {
			return p.ID, *p.Address
		}
		return p.ID, ""
	})
}

func newNetworksCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "networks",
		Aliases: []string{"network", "net", "vpc"},
		Short:   "Manage VPC networks and their subnets",
	}
	cmd.AddCommand(
		newNetworksListCmd(a),
		newNetworksGetCmd(a),
		newNetworksCreateCmd(a),
		deleteCmd(a, networkKind, "Its subnets must be empty.", resolveNetwork),
		newSubnetsCmd(a),
	)
	return cmd
}

func newNetworksListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List VPC networks",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			nets, raw, err := listItems[api.Network](cmd, c, "/networks", nil)
			if err != nil {
				return err
			}
			if printList(a, nets, raw, func(n api.Network) string { return n.ID }, "No networks yet.", "pantech networks create --name prod --cidr 10.0.0.0/16") {
				return nil
			}
			rows := make([][]string, len(nets))
			for i, n := range nets {
				rows[i] = []string{n.Name, a.out.State(n.ObservedState), n.CIDR, output.Or(n.Zone), a.out.Dim(n.ID)}
			}
			a.out.Table([]string{"name", "state", "cidr", "zone", "id"}, rows)
			a.out.Summary(count(len(nets), "network"))
			return nil
		},
	}
}

func newNetworksGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <network>",
		Aliases: []string{"show"},
		Short:   "Show a VPC network, by id or name",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveNetwork(cmd, c, args[0])
			if err != nil {
				return err
			}
			var n api.Network
			if ok, err := getOne(a, cmd, c, "/networks/"+url.PathEscape(id), &n); !ok {
				return err
			}
			vms := "—"
			if n.InstanceCount != nil {
				vms = fmt.Sprint(*n.InstanceCount)
			}
			a.out.Print(output.Detail{
				Title:    n.Name,
				State:    state(a.out, n.ObservedState, n.DesiredState),
				Subtitle: n.ID,
				Sections: [][]output.Pair{
					{{"CIDR", n.CIDR}, {"Location", output.Or(n.Zone)}, {"VMs", vms}},
					{{"Created", output.When(n.CreatedAt)}},
				},
				Next: [][2]string{{"Its subnets", "pantech networks subnets list " + n.ID}},
			})
			return nil
		},
	}
}

func newNetworksCreateCmd(a *app) *cobra.Command {
	var name, cidr, region string
	var noWait bool
	cmd := &cobra.Command{
		Use:     "create",
		Short:   "Create a VPC network",
		Example: `  pantech networks create --name prod --cidr 10.0.0.0/16`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			body := map[string]any{"name": name, "cidr": cidr}
			if region != "" {
				body["region"] = region
			}
			if err := a.confirm(fmt.Sprintf("Create network %s (%s)? It is billed until deleted.", name, cidr)); err != nil {
				return err
			}
			return a.runCreate(cmd, c, api.Request{Method: http.MethodPost, Path: "/networks", Body: body}, noWait, name)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "its name (required)")
	cmd.Flags().StringVar(&cidr, "cidr", "", "its private range, e.g. 10.0.0.0/16 (required)")
	cmd.Flags().StringVar(&region, "region", "", "region code (default: the platform's region)")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the id without waiting")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("cidr")
	return cmd
}

func newSubnetsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "subnets",
		Aliases: []string{"subnet"},
		Short:   "Manage the subnets of a VPC network",
	}
	list := &cobra.Command{
		Use:     "list <network>",
		Aliases: []string{"ls"},
		Short:   "List a network's subnets",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			netID, err := resolveNetwork(cmd, c, args[0])
			if err != nil {
				return err
			}
			subnets, raw, err := listItems[api.Subnet](cmd, c, "/networks/"+url.PathEscape(netID)+"/subnets", nil)
			if err != nil {
				return err
			}
			if printList(a, subnets, raw, func(s api.Subnet) string { return s.ID }, "No subnets yet.", "pantech networks subnets create "+args[0]+" --name web --cidr 10.0.1.0/24") {
				return nil
			}
			rows := make([][]string, len(subnets))
			for i, s := range subnets {
				rows[i] = []string{s.Name, a.out.State(s.ObservedState), s.CIDR, output.Or(s.Zone), a.out.Dim(s.ID)}
			}
			a.out.Table([]string{"name", "state", "cidr", "zone", "id"}, rows)
			a.out.Summary(count(len(subnets), "subnet"))
			return nil
		},
	}
	var name, cidr string
	var noWait bool
	create := &cobra.Command{
		Use:     "create <network>",
		Short:   "Create a subnet in a network",
		Example: `  pantech networks subnets create prod --name web --cidr 10.0.1.0/24`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			netID, err := resolveNetwork(cmd, c, args[0])
			if err != nil {
				return err
			}
			req := api.Request{Method: http.MethodPost, Path: "/networks/" + url.PathEscape(netID) + "/subnets", Body: map[string]any{"name": name, "cidr": cidr}}
			return a.runCreate(cmd, c, req, noWait, name)
		},
	}
	create.Flags().StringVar(&name, "name", "", "its name (required)")
	create.Flags().StringVar(&cidr, "cidr", "", "its range, inside the network's, e.g. 10.0.1.0/24 (required)")
	create.Flags().BoolVar(&noWait, "no-wait", false, "return the id without waiting")
	_ = create.MarkFlagRequired("name")
	_ = create.MarkFlagRequired("cidr")
	cmd.AddCommand(list, create, deleteCmd(a, subnetKind, "It must have no VMs.", resolveSubnet))
	return cmd
}

func newPublicIPsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "public-ips",
		Aliases: []string{"public-ip", "ips"},
		Short:   "Reserve, attach, detach and release public IPs for VPC VMs",
		Long: `Reserve, attach, detach and release public IPs for VPC VMs.

A static_nat address maps every port to one VM. Detach it and attach it to
another VM to keep the same address across VMs: it stays yours, and is billed
once, until you release it with "pantech public-ips delete". A detached
address is still billed.`,
	}
	cmd.AddCommand(
		newPublicIPsListCmd(a),
		newPublicIPsGetCmd(a),
		newPublicIPsCreateCmd(a),
		newPublicIPsAttachCmd(a),
		newPublicIPsDetachCmd(a),
		deleteCmd(a, publicIPKind, "The address is released and may not come back.", resolvePublicIP),
	)
	return cmd
}

// publicIPVM is what a public IP points at: its VM, "detached" for a
// static_nat address held without one, or a dash for the other purposes.
func publicIPVM(p api.PublicIP) string {
	if p.Detached() {
		return "detached"
	}
	return output.Or(firstSet(p.InstanceName, p.InstanceID))
}

// publicIPState is the address's state, with "applying" while an attach or
// detach has not reached it yet.
func publicIPState(a *app, p api.PublicIP) string {
	s := state(a.out, p.ObservedState, p.DesiredState)
	if api.Applying(p.InSync) {
		s += a.out.Dim(" (applying)")
	}
	return s
}

func newPublicIPsListCmd(a *app) *cobra.Command {
	var network string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List public IPs",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			q := url.Values{}
			if network != "" {
				id, err := resolveNetwork(cmd, c, network)
				if err != nil {
					return err
				}
				q.Set("network_id", id)
			}
			ips, raw, err := listItems[api.PublicIP](cmd, c, "/public-ips", q)
			if err != nil {
				return err
			}
			if printList(a, ips, raw, func(p api.PublicIP) string { return p.ID }, "No public IPs yet.", "pantech public-ips create --network prod --vm web-1") {
				return nil
			}
			rows := make([][]string, len(ips))
			for i, p := range ips {
				rows[i] = []string{output.Or(p.Address), publicIPState(a, p), p.Purpose, output.Or(firstSet(p.NetworkName, &p.NetworkID)), publicIPVM(p), a.out.Dim(p.ID)}
			}
			a.out.Table([]string{"address", "state", "purpose", "network", "vm", "id"}, rows)
			a.out.Summary(count(len(ips), "public IP"))
			return nil
		},
	}
	cmd.Flags().StringVar(&network, "network", "", "only this network's, by id or name")
	return cmd
}

func newPublicIPsGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <public ip>",
		Aliases: []string{"show"},
		Short:   "Show a public IP, by id or address",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolvePublicIP(cmd, c, args[0])
			if err != nil {
				return err
			}
			var p api.PublicIP
			if ok, err := getOne(a, cmd, c, "/public-ips/"+url.PathEscape(id), &p); !ok {
				return err
			}
			sync := "yes"
			if p.InSync == nil {
				sync = "—"
			} else if !*p.InSync {
				sync = "no, an attach or detach is applying"
			}
			d := output.Detail{
				Title:    output.Or(p.Address),
				State:    publicIPState(a, p),
				Subtitle: p.ID,
				Sections: [][]output.Pair{
					{{"Purpose", p.Purpose}, {"Network", output.Or(firstSet(p.NetworkName, &p.NetworkID))}, {"VM", publicIPVM(p)}, {"In sync", sync}, {"Location", output.Or(p.Zone)}},
					{{"Created", output.When(p.CreatedAt)}},
				},
			}
			if p.Detached() {
				d.Next = [][2]string{{"Attach it", "pantech public-ips attach " + p.ID + " --vm <vm>"}, {"Or release it", "pantech public-ips delete " + p.ID}}
			}
			a.out.Print(d)
			return nil
		},
	}
}

func newPublicIPsCreateCmd(a *app) *cobra.Command {
	var network, purpose, vm string
	var noWait bool
	cmd := &cobra.Command{
		Use:     "create",
		Aliases: []string{"reserve"},
		Short:   "Reserve a public IP in a VPC network",
		Long: `Reserve a public IP in a VPC network. A static_nat address (the default)
maps every port to one VM: give it with --vm, or leave --vm out to reserve the
address now and attach it later with "pantech public-ips attach". A
port_forwarding address takes port forwarding rules, and a load_balancer
address carries load balancers; neither takes --vm.

The address is billed from now until it is released, attached or not.`,
		Example: `  pantech public-ips create --network prod --vm web-1
  pantech public-ips create --network prod            # reserved, attach it later
  pantech public-ips create --network prod --purpose load_balancer`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if vm != "" && purpose != "static_nat" {
				return &usageError{fmt.Errorf("--vm is for a static_nat address; a %s address takes none", purpose), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			netID, err := resolveNetwork(cmd, c, network)
			if err != nil {
				return err
			}
			body := map[string]any{"network_id": netID, "purpose": purpose}
			if vm != "" {
				id, err := resolveVM(cmd, c, vm)
				if err != nil {
					return err
				}
				body["instance_id"] = id
			}
			if err := a.confirm("Reserve a public IP? It is billed until released, attached or not."); err != nil {
				return err
			}
			return publicIPHint(a.runCreate(cmd, c, api.Request{Method: http.MethodPost, Path: "/public-ips", Body: body}, noWait, "public IP"))
		},
	}
	cmd.Flags().StringVar(&network, "network", "", "the VPC network, by id or name (required)")
	cmd.Flags().StringVar(&purpose, "purpose", "static_nat", "static_nat, port_forwarding or load_balancer")
	cmd.Flags().StringVar(&vm, "vm", "", "the VM a static_nat address points at, by id or name; leave out to reserve it detached")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the id without waiting")
	_ = cmd.MarkFlagRequired("network")
	return cmd
}

func newPublicIPsAttachCmd(a *app) *cobra.Command {
	var vm string
	var noWait bool
	cmd := &cobra.Command{
		Use:   "attach <public ip> --vm <vm>",
		Short: "Point a static_nat public IP at a VM, keeping the address",
		Long: `Point a static_nat public IP at a VM in the same VPC network, keeping the
address. If it is attached to another VM it moves: you keep the same IP and
pay for it once. The VM must not already have a static_nat address.`,
		Example: `  pantech public-ips attach 102.211.122.90 --vm web-2`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if vm == "" {
				return &usageError{fmt.Errorf("give the VM with --vm"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolvePublicIP(cmd, c, args[0])
			if err != nil {
				return err
			}
			vmID, err := resolveVM(cmd, c, vm)
			if err != nil {
				return err
			}
			req := api.Request{Method: http.MethodPost, Path: "/public-ips/" + url.PathEscape(id) + "/attach", Body: map[string]any{"instance_id": vmID}}
			return publicIPHint(a.runWrite(cmd, c, req, noWait, verb{"Attaching", "Attached"}, args[0]+" to "+vm))
		},
	}
	cmd.Flags().StringVar(&vm, "vm", "", "the VM to point it at, by id or name (required)")
	cmd.Flags().StringVar(&vm, "instance", "", "the same as --vm")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	return cmd
}

func newPublicIPsDetachCmd(a *app) *cobra.Command {
	var noWait bool
	cmd := &cobra.Command{
		Use:   "detach <public ip>",
		Short: "Unmap a static_nat public IP from its VM, keeping the address",
		Long: `Unmap a static_nat public IP from its VM. The address stays yours, and is
still billed, until you attach it to another VM or release it with
"pantech public-ips delete". The VM keeps running, without a public address.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolvePublicIP(cmd, c, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Detach %s? Its VM loses the address; the address stays reserved and billed.", args[0])); err != nil {
				return err
			}
			req := api.Request{Method: http.MethodPost, Path: "/public-ips/" + url.PathEscape(id) + "/detach"}
			return publicIPHint(a.runWrite(cmd, c, req, noWait, verb{"Detaching", "Detached"}, args[0]))
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	return cmd
}

// publicIPHint adds what to do next to the public IP errors that have a CLI answer.
func publicIPHint(err error) error {
	switch {
	case api.IsCode(err, "INSTANCE_ALREADY_HAS_PUBLIC_IP"):
		return &hinted{err: err, hint: "Detach the VM's own address first (pantech public-ips detach <ip>), or release it.\nSee: pantech public-ips list"}
	case api.IsCode(err, "PUBLIC_IP_NOT_STATIC_NAT"):
		return &hinted{err: err, hint: "Only a static_nat address is attached or detached; port forwarding and load balancer addresses use their rules."}
	case api.IsCode(err, "PUBLIC_IP_LIMIT_EXCEEDED"):
		return &hinted{err: err, hint: "Release an address you no longer need (pantech public-ips delete <ip>), or contact support to raise the limit.\nSee: pantech public-ips list"}
	}
	return err
}
