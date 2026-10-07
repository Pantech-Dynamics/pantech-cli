package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

var loadBalancerKind = kind{prefix: "lb_", path: "/load-balancers", noun: "load balancer", listCmd: "pantech load-balancers list", noSearch: true}

func resolveLoadBalancer(cmd *cobra.Command, c *api.Client, ref string) (string, error) {
	return resolve(cmd, c, loadBalancerKind, ref, func(l api.LoadBalancer) (string, string) { return l.ID, l.Name })
}

// resolveVMs resolves VMs given by id or name, in order.
func resolveVMs(cmd *cobra.Command, c *api.Client, refs []string) ([]string, error) {
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		id, err := resolveVM(cmd, c, ref)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func checkAlgorithm(algorithm string) error {
	switch algorithm {
	case "", "roundrobin", "leastconn", "source":
		return nil
	}
	return fmt.Errorf("--algorithm %q: use roundrobin, leastconn or source", algorithm)
}

func newLoadBalancersCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "load-balancers",
		Aliases: []string{"load-balancer", "lb"},
		Short:   "Spread a TCP port of a public IP across VPC VMs",
		Long: `Spread one TCP port of a load_balancer public IP across VMs in one subnet
of that address's VPC network. One public IP can carry several load
balancers, one per public port. Reserve the address first with
"pantech public-ips create --network <network> --purpose load_balancer".`,
	}
	cmd.AddCommand(
		newLoadBalancersListCmd(a),
		newLoadBalancersGetCmd(a),
		newLoadBalancersCreateCmd(a),
		newLoadBalancersUpdateCmd(a),
		deleteCmd(a, loadBalancerKind, "Traffic to its port stops; the public IP stays yours.", resolveLoadBalancer),
	)
	return cmd
}

// lbState is the load balancer's state, with "applying" while a change has
// not reached it yet.
func lbState(a *app, l api.LoadBalancer) string {
	s := state(a.out, l.ObservedState, l.DesiredState)
	if api.Applying(l.InSync) {
		s += a.out.Dim(" (applying)")
	}
	return s
}

func lbListen(l api.LoadBalancer) string {
	return fmt.Sprintf("%s:%d → %d", output.Or(l.PublicIPAddress), l.PublicPort, l.PrivatePort)
}

func newLoadBalancersListCmd(a *app) *cobra.Command {
	var network, publicIP string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List load balancers",
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
			if publicIP != "" {
				id, err := resolvePublicIP(cmd, c, publicIP)
				if err != nil {
					return err
				}
				q.Set("public_ip_id", id)
			}
			lbs, raw, err := listItems[api.LoadBalancer](cmd, c, "/load-balancers", q)
			if err != nil {
				return err
			}
			if printList(a, lbs, raw, func(l api.LoadBalancer) string { return l.ID }, "No load balancers yet.", "pantech load-balancers create --name web --public-ip <ip> --subnet <subnet> --port 443 --target web-1") {
				return nil
			}
			rows := make([][]string, len(lbs))
			for i, l := range lbs {
				rows[i] = []string{l.Name, lbState(a, l), lbListen(l), l.Algorithm, strconv.Itoa(len(l.Members)), a.out.Dim(l.ID)}
			}
			a.out.Table([]string{"name", "state", "listens", "algorithm", "targets", "id"}, rows)
			a.out.Summary(count(len(lbs), "load balancer"))
			return nil
		},
	}
	cmd.Flags().StringVar(&network, "network", "", "only this network's, by id or name")
	cmd.Flags().StringVar(&publicIP, "public-ip", "", "only this public IP's, by id or address")
	return cmd
}

func newLoadBalancersGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <load balancer>",
		Aliases: []string{"show"},
		Short:   "Show a load balancer and its targets, by id or name",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveLoadBalancer(cmd, c, args[0])
			if err != nil {
				return err
			}
			var l api.LoadBalancer
			if ok, err := getOne(a, cmd, c, "/load-balancers/"+url.PathEscape(id), &l); !ok {
				return err
			}
			allowed := "anywhere"
			if len(l.CIDRList) > 0 {
				allowed = strings.Join(l.CIDRList, ", ")
			}
			targets := []output.Pair{}
			for _, m := range l.Members {
				st := a.out.State(m.ObservedState)
				if m.DesiredState == "deleted" {
					st += a.out.Dim(" (being removed)")
				}
				targets = append(targets, output.Pair{output.Or(firstSet(m.InstanceName, &m.InstanceID)), st})
			}
			if len(targets) == 0 {
				targets = append(targets, output.Pair{"Targets", "none yet"})
			}
			a.out.Print(output.Detail{
				Title:    l.Name,
				State:    lbState(a, l),
				Subtitle: l.ID,
				Sections: [][]output.Pair{
					{{"Listens", lbListen(l)}, {"Protocol", l.Protocol}, {"Algorithm", l.Algorithm}, {"Allowed from", allowed}},
					{{"Public IP", l.PublicIPID}, {"Subnet", l.SubnetID}, {"Network", output.Or(l.NetworkID)}},
					targets,
					{{"Created", output.When(l.CreatedAt)}},
				},
			})
			return nil
		},
	}
}

func newLoadBalancersCreateCmd(a *app) *cobra.Command {
	var name, publicIP, subnet, algorithm string
	var port, privatePort int
	var allow, targets []string
	var noWait bool
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a load balancer on a load_balancer public IP",
		Long: `Create a load balancer: one TCP port of a load_balancer public IP spread
across VMs in one subnet of its VPC network. --private-port defaults to
--port and --algorithm to roundrobin; --allow limits which source addresses
may connect (default: anywhere). Targets can be added later with
"pantech load-balancers update --targets". The subnet's firewall rules must
allow the port.`,
		Example: `  pantech load-balancers create --name web --public-ip 102.211.122.91 --subnet snet_1 \
    --port 443 --private-port 8443 --algorithm leastconn --target web-1,web-2`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkAlgorithm(algorithm); err != nil {
				return &usageError{err, cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			pipID, err := resolvePublicIP(cmd, c, publicIP)
			if err != nil {
				return err
			}
			body := map[string]any{"name": name, "public_ip_id": pipID, "subnet_id": subnet, "public_port": port}
			if algorithm != "" {
				body["algorithm"] = algorithm
			}
			if privatePort != 0 {
				body["private_port"] = privatePort
			}
			if len(allow) > 0 {
				body["cidr_list"] = allow
			}
			if len(targets) > 0 {
				ids, err := resolveVMs(cmd, c, targets)
				if err != nil {
					return err
				}
				body["instance_ids"] = ids
			}
			return a.runCreate(cmd, c, api.Request{Method: http.MethodPost, Path: "/load-balancers", Body: body}, noWait, "load balancer "+name)
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "1 to 63 characters (required)")
	f.StringVar(&publicIP, "public-ip", "", "an active load_balancer public IP, by id or address (required)")
	f.StringVar(&subnet, "subnet", "", "a subnet of the public IP's network, by id; every target must be in it (required)")
	f.IntVar(&port, "port", 0, "the TCP port clients connect to, 1 to 65535 (required)")
	f.IntVar(&privatePort, "private-port", 0, "the port on each target (default: --port)")
	f.StringVar(&algorithm, "algorithm", "", "roundrobin (default), leastconn or source")
	f.StringSliceVar(&allow, "allow", nil, "a source CIDR allowed to connect; repeat or comma-separate (default: anywhere)")
	f.StringSliceVar(&targets, "target", nil, "a VM to send traffic to, by id or name; repeat or comma-separate")
	f.BoolVar(&noWait, "no-wait", false, "return the id without waiting")
	for _, req := range []string{"name", "public-ip", "subnet", "port"} {
		_ = cmd.MarkFlagRequired(req)
	}
	return cmd
}

func newLoadBalancersUpdateCmd(a *app) *cobra.Command {
	var name, algorithm string
	var targets []string
	var noTargets, noWait bool
	cmd := &cobra.Command{
		Use:   "update <load balancer>",
		Short: "Rename a load balancer, change its algorithm or set its targets",
		Long: `Rename a load balancer, change its algorithm, or replace its targets.
--targets is the whole new set (each in the load balancer's subnet);
--no-targets removes them all. Flags left out stay as they are.`,
		Example: `  pantech load-balancers update web --algorithm source
  pantech load-balancers update web --targets web-1,web-2,web-3`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fl := cmd.Flags()
			if err := checkAlgorithm(algorithm); err != nil {
				return &usageError{err, cmd}
			}
			if fl.Changed("targets") && noTargets {
				return &usageError{errors.New("--targets and --no-targets do not go together"), cmd}
			}
			if !fl.Changed("name") && !fl.Changed("algorithm") && !fl.Changed("targets") && !noTargets {
				return &usageError{errors.New("give at least one of --name, --algorithm, --targets or --no-targets"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveLoadBalancer(cmd, c, args[0])
			if err != nil {
				return err
			}
			body := map[string]any{}
			if fl.Changed("name") {
				body["name"] = name
			}
			if fl.Changed("algorithm") {
				body["algorithm"] = algorithm
			}
			switch {
			case noTargets:
				if err := a.confirm(fmt.Sprintf("Remove every target from %s? It stops sending traffic anywhere.", args[0])); err != nil {
					return err
				}
				body["instance_ids"] = []string{}
			case fl.Changed("targets"):
				ids, err := resolveVMs(cmd, c, targets)
				if err != nil {
					return err
				}
				body["instance_ids"] = ids
			}
			req := api.Request{Method: http.MethodPatch, Path: "/load-balancers/" + url.PathEscape(id), Body: body}
			return a.runWrite(cmd, c, req, noWait, verb{"Updating", "Updated"}, args[0])
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "a new name, 1 to 63 characters")
	f.StringVar(&algorithm, "algorithm", "", "roundrobin, leastconn or source")
	f.StringSliceVar(&targets, "targets", nil, "the whole new set of target VMs, by id or name, comma-separated or repeated")
	f.BoolVar(&noTargets, "no-targets", false, "remove every target")
	f.BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	return cmd
}
