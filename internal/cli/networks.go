package cli

import (
	"fmt"
	"net/http"
	"net/url"

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

// resolveSubnet takes only ids: subnets are listed per network, so a name
// alone does not say where to look.
func resolveSubnet(_ *cobra.Command, _ *api.Client, ref string) (string, error) {
	return ref, nil
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
		Short:   "Allocate and release public IPs for VPC VMs",
	}
	cmd.AddCommand(
		newPublicIPsListCmd(a),
		newPublicIPsGetCmd(a),
		newPublicIPsCreateCmd(a),
		deleteCmd(a, publicIPKind, "The address is released and may not come back.", resolvePublicIP),
	)
	return cmd
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
				rows[i] = []string{output.Or(p.Address), a.out.State(p.ObservedState), p.Purpose, output.Or(firstSet(p.NetworkName, &p.NetworkID)), output.Or(firstSet(p.InstanceName, p.InstanceID)), a.out.Dim(p.ID)}
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
			a.out.Print(output.Detail{
				Title:    output.Or(p.Address),
				State:    state(a.out, p.ObservedState, p.DesiredState),
				Subtitle: p.ID,
				Sections: [][]output.Pair{
					{{"Purpose", p.Purpose}, {"Network", output.Or(firstSet(p.NetworkName, &p.NetworkID))}, {"VM", output.Or(firstSet(p.InstanceName, p.InstanceID))}, {"Location", output.Or(p.Zone)}},
					{{"Created", output.When(p.CreatedAt)}},
				},
			})
			return nil
		},
	}
}

func newPublicIPsCreateCmd(a *app) *cobra.Command {
	var network, purpose, vm string
	var noWait bool
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Allocate a public IP in a VPC network",
		Long: `Allocate a public IP in a VPC network. A static_nat address (the default)
points at one VM, given with --vm; a port_forwarding address takes port
forwarding rules instead.`,
		Example: `  pantech public-ips create --network prod --vm web-1`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			if err := a.confirm("Allocate a public IP? It is billed until released."); err != nil {
				return err
			}
			return a.runCreate(cmd, c, api.Request{Method: http.MethodPost, Path: "/public-ips", Body: body}, noWait, "public IP")
		},
	}
	cmd.Flags().StringVar(&network, "network", "", "the VPC network, by id or name (required)")
	cmd.Flags().StringVar(&purpose, "purpose", "static_nat", "static_nat or port_forwarding")
	cmd.Flags().StringVar(&vm, "vm", "", "the VM a static_nat address points at, by id or name")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the id without waiting")
	_ = cmd.MarkFlagRequired("network")
	return cmd
}
