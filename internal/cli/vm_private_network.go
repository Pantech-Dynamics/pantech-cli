package cli

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

// A standard VM's second interface, on its zone's private database network,
// through which it reaches managed databases by their private address.

// privateNetwork is the interface's state, with its address once it has one.
func privateNetwork(out *output.Printer, vm *api.Instance) string {
	s := vm.PrivateNetworkState
	if vm.PrivateNetworkIP != nil && *vm.PrivateNetworkIP != "" {
		s = *vm.PrivateNetworkIP + out.Dim(" ("+s+")")
	}
	return s
}

func newVMPrivateNetworkCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "private-network",
		Aliases: []string{"pn"},
		Short:   "Connect a VM to the private network its zone's databases are on",
	}
	var attachNoWait bool
	attach := &cobra.Command{
		Use:   "attach <vm>",
		Short: "Add an interface on the private database network to a VM",
		Long: `Add a second network interface to a standard VM, on its zone's private
database network, so it can reach managed databases by their private address.
The VM must be running or stopped with no other change in progress; a running
VM gets the interface without a restart. Once attached, allow the interface's
address on each database as a /32 access rule.

The VM's security group must allow nothing from the private network's range
(its zone's "private network" in "pantech regions", e.g. 10.250.0.0/20),
including 0.0.0.0/0 and ICMP-only rules: a group
applies to every interface, so such a rule would open the VM to the whole
network. A group that does is refused; narrow its rules first with
"pantech security-groups rules set".

In the guest the interface stays down until configured. On Ubuntu add it to
netplan with DHCP only (dhcp4: true, with dhcp4-overrides use-routes: false
and use-dns: false), then run netplan apply.`,
		Example: `  pantech vm private-network attach web-1`,
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
			req := api.Request{Method: http.MethodPost, Path: "/instances/" + url.PathEscape(id) + "/private-network"}
			if err := a.runWrite(cmd, c, req, attachNoWait, verb{"Attaching the private network to", "Attached the private network to"}, args[0]); err != nil {
				return privateNetworkHint(cmd, c, id, err)
			}
			if attachNoWait || a.out.JSON {
				return nil
			}
			var vm api.Instance
			if _, err := api.Get(ctx(cmd), c, "/instances/"+url.PathEscape(id), &vm); err != nil {
				return err
			}
			if vm.PrivateNetworkIP == nil || *vm.PrivateNetworkIP == "" {
				a.out.Next("Its address, once assigned", "pantech vm get "+args[0])
				return nil
			}
			a.out.Line("%s", *vm.PrivateNetworkIP)
			a.out.Next("Allow it on a database (with the database's other rules)", "pantech db access-rules set <database> "+*vm.PrivateNetworkIP+"/32")
			return nil
		},
	}
	attach.Flags().BoolVar(&attachNoWait, "no-wait", false, "return the operation id without waiting")

	var detachNoWait bool
	detach := &cobra.Command{
		Use:   "detach <vm>",
		Short: "Remove a VM's interface on the private database network",
		Long: `Remove the interface "attach" added. The VM must be running or stopped with
no other change in progress. Afterwards remove any database access rule for
its address, and the interface from the guest's network configuration.`,
		Example: `  pantech vm private-network detach web-1`,
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
			if err := a.confirm(fmt.Sprintf("Detach %s from the private network? Its connections to databases over it are dropped.", args[0])); err != nil {
				return err
			}
			req := api.Request{Method: http.MethodDelete, Path: "/instances/" + url.PathEscape(id) + "/private-network"}
			return a.runWrite(cmd, c, req, detachNoWait, verb{"Detaching the private network from", "Detached the private network from"}, args[0])
		},
	}
	detach.Flags().BoolVar(&detachNoWait, "no-wait", false, "return the operation id without waiting")
	cmd.AddCommand(attach, detach)
	return cmd
}

// privateNetworkHint keeps the API's own message for a security group that
// would open the VM to the private network, and adds how to narrow it.
func privateNetworkHint(cmd *cobra.Command, c *api.Client, vmID string, err error) error {
	if !api.IsCode(err, "SECURITY_GROUP_ALLOWS_PRIVATE_NETWORK") {
		return err
	}
	group, rng := "<security group>", "the private network's range"
	var vm api.Instance
	if _, getErr := api.Get(ctx(cmd), c, "/instances/"+url.PathEscape(vmID), &vm); getErr == nil {
		if vm.SecurityGroupID != nil && *vm.SecurityGroupID != "" {
			group = *vm.SecurityGroupID
		}
		if cidr := privateNetworkCIDR(cmd, c, vm.Zone); cidr != "" {
			rng += " (" + cidr + ")"
		}
	}
	return &hinted{err: err, hint: "Narrow the group's ingress rules so none covers " + rng + ", then attach again.\n" +
		"See: pantech security-groups rules set " + group + " --rule ingress:tcp:22:<your address>/32"}
}

// privateNetworkCIDR is the private database network range of zone, from
// the regions list; "" when not known.
func privateNetworkCIDR(cmd *cobra.Command, c *api.Client, zone *string) string {
	if zone == nil || *zone == "" {
		return ""
	}
	regions, err := api.ListAll[api.Region](ctx(cmd), c, "/regions", nil)
	if err != nil {
		return ""
	}
	for _, r := range regions {
		for _, p := range r.Placements {
			if p.Zone == *zone && p.PrivateNetworkCIDR != nil && *p.PrivateNetworkCIDR != "" {
				return *p.PrivateNetworkCIDR
			}
		}
	}
	return ""
}

// hinted is an error with a line of advice after it; errors.As still finds
// what it wraps, so the exit code is the wrapped error's.
type hinted struct {
	err  error
	hint string
}

func (h *hinted) Error() string { return h.err.Error() + "\n" + h.hint }
func (h *hinted) Unwrap() error { return h.err }
