package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
)

func clusterPath(id string) string { return "/kubernetes-clusters/" + url.PathEscape(id) }

func newKubernetesResizeCmd(a *app) *cobra.Command {
	var nodePlan string
	var noWait bool
	cmd := &cobra.Command{
		Use:   "resize <cluster> --node-plan PLAN",
		Short: "Move every node of a cluster to another plan",
		Long: `Move every node of a cluster, control and worker, to another plan. Nodes are
replaced one at a time, so workloads keep running if they have more than
one replica. Each node is then billed as a VM of the new plan.`,
		Example: `  pantech kubernetes resize prod --node-plan s-4vcpu-8gb`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveCluster(cmd, c, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Move every node of %s to %s? Each is then billed as a %s VM.", args[0], nodePlan, nodePlan)); err != nil {
				return err
			}
			req := api.Request{Method: http.MethodPost, Path: clusterPath(id) + "/resize-plan", Body: map[string]any{"node_plan": nodePlan}}
			return a.runWrite(cmd, c, req, noWait, verb{"Resizing", "Resized"}, args[0])
		},
	}
	cmd.Flags().StringVar(&nodePlan, "node-plan", "", "the plan slug every node moves to, from pantech plans (required)")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	_ = cmd.MarkFlagRequired("node-plan")
	return cmd
}

func newKubernetesNetworkCmd(a *app) *cobra.Command {
	var cni, podCIDR, serviceCIDR string
	var noWait bool
	cmd := &cobra.Command{
		Use:   "network <cluster>",
		Short: "Change a cluster's pod network plugin or address ranges",
		Long: `Change a running cluster's pod network: the CNI plugin (flannel, wireguard
or calico), the pods' address range or the services' address range. Flags
left out stay as they are. Pods restart while the network is rebuilt, so
expect a short interruption.`,
		Example: `  pantech kubernetes network prod --cni calico
  pantech kubernetes network prod --pod-cidr 10.60.0.0/16`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkCNI(cni); err != nil {
				return &usageError{err, cmd}
			}
			body := map[string]any{}
			for k, v := range map[string]string{"cni_plugin": cni, "pod_cidr": podCIDR, "service_cidr": serviceCIDR} {
				if v != "" {
					body[k] = v
				}
			}
			if len(body) == 0 {
				return &usageError{errors.New("give --cni, --pod-cidr or --service-cidr"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveCluster(cmd, c, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Change %s's pod network? Its pods restart.", args[0])); err != nil {
				return err
			}
			req := api.Request{Method: http.MethodPost, Path: clusterPath(id) + "/upgrade-network", Body: body}
			return a.runWrite(cmd, c, req, noWait, verb{"Changing the network of", "Changed the network of"}, args[0])
		},
	}
	f := cmd.Flags()
	f.StringVar(&cni, "cni", "", "flannel, wireguard or calico")
	f.StringVar(&podCIDR, "pod-cidr", "", "the pods' new address range, e.g. 10.42.0.0/16")
	f.StringVar(&serviceCIDR, "service-cidr", "", "the services' new address range, e.g. 10.43.0.0/16")
	f.BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	return cmd
}

func newKubernetesEndpointAccessCmd(a *app) *cobra.Command {
	var apiAllow []string
	var noWait bool
	cmd := &cobra.Command{
		Use:   "endpoint-access <cluster> (public|private|dual)",
		Short: "Choose where a cluster's API server can be reached from",
		Long: `Choose where a cluster's API server can be reached from:

  public   from the internet (limit it with --api-allow)
  private  only from inside the cluster's network
  dual     both

--api-allow, when given, replaces the list of CIDRs allowed to reach the
public endpoint.`,
		Example: `  pantech kubernetes endpoint-access prod private
  pantech kubernetes endpoint-access prod dual --api-allow 203.0.113.0/24`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			access := args[1]
			switch access {
			case "public", "private", "dual":
			default:
				return &usageError{fmt.Errorf("%q: use public, private or dual", access), cmd}
			}
			body := map[string]any{"endpoint_access": access}
			if cmd.Flags().Changed("api-allow") {
				body["api_allowed_cidrs"] = apiAllow
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveCluster(cmd, c, args[0])
			if err != nil {
				return err
			}
			req := api.Request{Method: http.MethodPost, Path: clusterPath(id) + "/endpoint-access", Body: body}
			return a.runWrite(cmd, c, req, noWait, verb{"Updating", "Updated"}, args[0])
		},
	}
	cmd.Flags().StringSliceVar(&apiAllow, "api-allow", nil, "the whole new list of CIDRs allowed to reach the API server; repeat or comma-separate")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	return cmd
}

// --- node pools ---

func nodePoolsPath(clusterID string) string { return clusterPath(clusterID) + "/node-pools" }

// resolveNodePool takes a pool id (k8spool_…) or a pool name in the cluster.
func resolveNodePool(cmd *cobra.Command, c *api.Client, clusterID, ref string) (string, error) {
	return resolve(cmd, c, kind{prefix: "k8spool_", path: nodePoolsPath(clusterID), noun: "node pool", listCmd: "pantech kubernetes node-pools list <cluster>", noSearch: true}, ref,
		func(p api.KubernetesNodePool) (string, string) { return p.ID, p.Name })
}

func poolWorkers(p api.KubernetesNodePool) string {
	if p.AutoscalingEnabled {
		return fmt.Sprintf("%d (autoscale %d–%d)", p.Workers, p.MinWorkers, p.MaxWorkers)
	}
	return strconv.Itoa(p.Workers)
}

func newKubernetesNodePoolsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "node-pools",
		Aliases: []string{"node-pool", "pools"},
		Short:   "Add groups of workers with their own plan, labels and taints",
		Long: `A node pool is a group of workers, beside the cluster's own, with its own
plan, size, Kubernetes labels and taints: for example bigger nodes for a
database, or nodes only some workloads may use. Each worker is billed as a
VM of its pool's plan once it has joined the cluster. Pool workers carry the
label pantech.io/node-pool=<name> beside the pool's own; give a workload a
nodeSelector (and tolerations for the pool's taints) to run it there. With
--autoscale the platform adds a worker when a pod the pool could run is
waiting for room, and removes the newest one after 10 idle minutes.`,
	}
	cmd.AddCommand(newNodePoolsListCmd(a), newNodePoolsCreateCmd(a), newNodePoolsScaleCmd(a), newNodePoolsDeleteCmd(a))
	return cmd
}

func newNodePoolsListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list <cluster>",
		Aliases: []string{"ls"},
		Short:   "List a cluster's node pools",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveCluster(cmd, c, args[0])
			if err != nil {
				return err
			}
			pools, raw, err := listItems[api.KubernetesNodePool](cmd, c, nodePoolsPath(id), nil)
			if err != nil {
				return err
			}
			if printList(a, pools, raw, func(p api.KubernetesNodePool) string { return p.ID }, "No node pools yet.", "pantech kubernetes node-pools create "+args[0]+" --name big --node-plan <plan> --workers 2") {
				return nil
			}
			rows := make([][]string, len(pools))
			for i, p := range pools {
				rows[i] = []string{p.Name, a.out.State(p.Status), fmt.Sprintf("%d of %s", p.ObservedWorkers, poolWorkers(p)), p.NodePlanID, poolLabels(p), poolTaints(p), a.out.Dim(p.ID)}
			}
			a.out.Table([]string{"name", "state", "joined", "plan", "labels", "taints", "id"}, rows)
			a.out.Summary(count(len(pools), "node pool"))
			return nil
		},
	}
}

func poolLabels(p api.KubernetesNodePool) string {
	parts := make([]string, 0, len(p.Labels))
	for k, v := range p.Labels {
		parts = append(parts, k+"="+v)
	}
	if len(parts) == 0 {
		return "-"
	}
	slices.Sort(parts)
	return strings.Join(parts, ",")
}

func poolTaints(p api.KubernetesNodePool) string {
	parts := make([]string, 0, len(p.Taints))
	for _, t := range p.Taints {
		parts = append(parts, t.Key+"="+t.Value+":"+t.Effect)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}

// parseLabels reads KEY=VALUE pairs.
func parseLabels(in []string) (map[string]string, error) {
	out := map[string]string{}
	for _, s := range in {
		k, v, ok := strings.Cut(s, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--label %q: give KEY=VALUE", s)
		}
		out[k] = v
	}
	return out, nil
}

// parseTaints reads KEY=VALUE:EFFECT.
func parseTaints(in []string) ([]api.KubernetesNodePoolTaint, error) {
	out := make([]api.KubernetesNodePoolTaint, 0, len(in))
	for _, s := range in {
		kv, effect, ok := strings.Cut(s, ":")
		k, v, ok2 := strings.Cut(kv, "=")
		if !ok || !ok2 || k == "" {
			return nil, fmt.Errorf("--taint %q: give KEY=VALUE:EFFECT, e.g. dedicated=db:NoSchedule", s)
		}
		switch effect {
		case "NoSchedule", "PreferNoSchedule", "NoExecute":
		default:
			return nil, fmt.Errorf("--taint %q: the effect is NoSchedule, PreferNoSchedule or NoExecute", s)
		}
		out = append(out, api.KubernetesNodePoolTaint{Key: k, Value: v, Effect: effect})
	}
	return out, nil
}

func newNodePoolsCreateCmd(a *app) *cobra.Command {
	var name, nodePlan, autoscale string
	var workers int
	var labels, taints []string
	var noWait bool
	cmd := &cobra.Command{
		Use:   "create <cluster>",
		Short: "Add a node pool to a cluster",
		Long: `Add a node pool to a running cluster. Give a fixed --workers count, or
--autoscale MIN:MAX to let the autoscaler move it (it starts at MIN, or at
--workers when both are given). --label and --taint are set on every node
of the pool.`,
		Example: `  pantech kubernetes node-pools create prod --name db --node-plan s-4vcpu-8gb --workers 2 \
    --label role=db --taint dedicated=db:NoSchedule
  pantech kubernetes node-pools create prod --name burst --node-plan s-2vcpu-4gb --autoscale 1:5`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fl := cmd.Flags()
			if !fl.Changed("workers") && autoscale == "" {
				return &usageError{errors.New("give --workers N or --autoscale MIN:MAX"), cmd}
			}
			body := map[string]any{"name": name, "plan_id": nodePlan}
			if fl.Changed("workers") {
				body["workers"] = workers
			}
			if autoscale != "" {
				as, err := parseAutoscale(autoscale)
				if err != nil {
					return &usageError{err, cmd}
				}
				body["autoscaling_enabled"], body["min_workers"], body["max_workers"] = true, as.MinWorkers, as.MaxWorkers
				if !fl.Changed("workers") {
					body["workers"] = as.MinWorkers
				}
			}
			if len(labels) > 0 {
				l, err := parseLabels(labels)
				if err != nil {
					return &usageError{err, cmd}
				}
				body["labels"] = l
			}
			if len(taints) > 0 {
				t, err := parseTaints(taints)
				if err != nil {
					return &usageError{err, cmd}
				}
				body["taints"] = t
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveCluster(cmd, c, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Add node pool %s to %s? Each of its workers is billed as a %s VM.", name, args[0], nodePlan)); err != nil {
				return err
			}
			return a.runCreate(cmd, c, api.Request{Method: http.MethodPost, Path: nodePoolsPath(id), Body: body}, noWait, "node pool "+name)
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "1 to 63 characters, unique in the cluster (required)")
	f.StringVar(&nodePlan, "node-plan", "", "the plan slug every node of the pool uses, from pantech plans (required)")
	f.IntVar(&workers, "workers", 0, "a fixed number of workers, 1 to 10")
	f.StringVar(&autoscale, "autoscale", "", "let the autoscaler keep MIN:MAX workers, e.g. 1:5")
	f.StringSliceVar(&labels, "label", nil, "a Kubernetes label KEY=VALUE for the pool's nodes; repeat or comma-separate")
	f.StringArrayVar(&taints, "taint", nil, "a taint KEY=VALUE:EFFECT (NoSchedule, PreferNoSchedule or NoExecute); repeat")
	f.BoolVar(&noWait, "no-wait", false, "return the id without waiting")
	for _, req := range []string{"name", "node-plan"} {
		_ = cmd.MarkFlagRequired(req)
	}
	return cmd
}

func newNodePoolsScaleCmd(a *app) *cobra.Command {
	var workers int
	var autoscale string
	var noAutoscale, noWait bool
	cmd := &cobra.Command{
		Use:   "scale <cluster> <pool>",
		Short: "Change a node pool's workers or its autoscaler",
		Long: `Change a node pool: a fixed number of workers (--workers), the autoscaler
on with a range (--autoscale MIN:MAX) or off (--no-autoscale, with
--workers for the count it lands on). Flags left out stay as they are.`,
		Example: `  pantech kubernetes node-pools scale prod db --workers 3
  pantech kubernetes node-pools scale prod burst --autoscale 2:8`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			fl := cmd.Flags()
			switch {
			case autoscale != "" && noAutoscale:
				return &usageError{errors.New("--autoscale and --no-autoscale do not go together"), cmd}
			case !fl.Changed("workers") && autoscale == "" && !noAutoscale:
				return &usageError{errors.New("give --workers, --autoscale or --no-autoscale"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			clusterID, err := resolveCluster(cmd, c, args[0])
			if err != nil {
				return err
			}
			poolID, err := resolveNodePool(cmd, c, clusterID, args[1])
			if err != nil {
				return err
			}
			// The API takes the pool's whole size each time, so start from what
			// it has now and change only what was asked.
			pools, err := api.ListAll[api.KubernetesNodePool](ctx(cmd), c, nodePoolsPath(clusterID), nil)
			if err != nil {
				return err
			}
			var cur *api.KubernetesNodePool
			for i := range pools {
				if pools[i].ID == poolID {
					cur = &pools[i]
				}
			}
			if cur == nil {
				return fmt.Errorf("no node pool %s in %s\nSee: pantech kubernetes node-pools list %s", args[1], args[0], args[0])
			}
			body := map[string]any{"workers": cur.Workers, "autoscaling_enabled": cur.AutoscalingEnabled, "min_workers": cur.MinWorkers, "max_workers": cur.MaxWorkers}
			if autoscale != "" {
				as, err := parseAutoscale(autoscale)
				if err != nil {
					return &usageError{err, cmd}
				}
				body["autoscaling_enabled"], body["min_workers"], body["max_workers"] = true, as.MinWorkers, as.MaxWorkers
				if w := cur.Workers; !fl.Changed("workers") && (w < as.MinWorkers || w > as.MaxWorkers) {
					body["workers"] = min(max(w, as.MinWorkers), as.MaxWorkers)
				}
			}
			if noAutoscale {
				body["autoscaling_enabled"], body["min_workers"], body["max_workers"] = false, 0, 0
			}
			if fl.Changed("workers") {
				body["workers"] = workers
			}
			if err := a.confirm(fmt.Sprintf("Change node pool %s's workers? Each is billed as a VM of its plan.", args[1])); err != nil {
				return err
			}
			req := api.Request{Method: http.MethodPatch, Path: nodePoolsPath(clusterID) + "/" + url.PathEscape(poolID), Body: body}
			return a.runWrite(cmd, c, req, noWait, verb{"Scaling", "Scaled"}, "node pool "+args[1])
		},
	}
	f := cmd.Flags()
	f.IntVar(&workers, "workers", 0, "a fixed number of workers, 1 to 10")
	f.StringVar(&autoscale, "autoscale", "", "turn the autoscaler on with MIN:MAX workers, e.g. 2:5")
	f.BoolVar(&noAutoscale, "no-autoscale", false, "turn the autoscaler off")
	f.BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	return cmd
}

func newNodePoolsDeleteCmd(a *app) *cobra.Command {
	var noWait bool
	cmd := &cobra.Command{
		Use:     "delete <cluster> <pool>",
		Aliases: []string{"rm"},
		Short:   "Delete a node pool, by id or name",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			clusterID, err := resolveCluster(cmd, c, args[0])
			if err != nil {
				return err
			}
			poolID, err := resolveNodePool(cmd, c, clusterID, args[1])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Delete node pool %s of %s? Its nodes are drained and removed; pods move to other nodes if they fit.", args[1], args[0])); err != nil {
				return err
			}
			req := api.Request{Method: http.MethodDelete, Path: nodePoolsPath(clusterID) + "/" + url.PathEscape(poolID)}
			return a.runWrite(cmd, c, req, noWait, verb{"Deleting", "Deleted"}, "node pool "+args[1])
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	return cmd
}

// --- add-ons ---

func newKubernetesAddonsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "addons",
		Aliases: []string{"addon"},
		Short:   "Turn a cluster's managed add-ons on or off",
		Long: `Managed add-ons the platform installs and keeps up to date in a cluster:

  metrics-server      CPU and memory metrics, for kubectl top and autoscaling
  csi-driver          persistent volumes on the node's own disk (StorageClass
                      local-path; the data stays on that node)
  ingress-controller  ingress-nginx, the default IngressClass; in a VPC zone
                      served on 80 and 443 of the cluster's public address

A cluster starts with metrics-server and csi-driver on. The platform keeps
the choice applied; "status" shows when a change has taken effect.`,
	}
	list := &cobra.Command{
		Use:     "list <cluster>",
		Aliases: []string{"ls"},
		Short:   "List a cluster's add-ons",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveCluster(cmd, c, args[0])
			if err != nil {
				return err
			}
			addons, raw, err := listItems[api.KubernetesAddon](cmd, c, clusterPath(id)+"/addons", nil)
			if err != nil {
				return err
			}
			if printList(a, addons, raw, func(x api.KubernetesAddon) string { return x.Name }, "No add-ons are installed.", "pantech kubernetes addons enable "+args[0]+" metrics-server") {
				return nil
			}
			rows := make([][]string, len(addons))
			for i, x := range addons {
				on := "off"
				if x.Enabled {
					on = "on"
				}
				rows[i] = []string{x.Name, on, a.out.State(x.Status), x.Version}
			}
			a.out.Table([]string{"name", "enabled", "status", "version"}, rows)
			return nil
		},
	}
	cmd.AddCommand(list, newAddonToggleCmd(a, true), newAddonToggleCmd(a, false))
	return cmd
}

func checkAddon(name string) error {
	switch name {
	case "metrics-server", "csi-driver", "ingress-controller":
		return nil
	}
	return fmt.Errorf("%q: the add-ons are metrics-server, csi-driver and ingress-controller", name)
}

func newAddonToggleCmd(a *app, enable bool) *cobra.Command {
	var noWait bool
	action, v := "enable", verb{"Enabling", "Enabled"}
	if !enable {
		action, v = "disable", verb{"Disabling", "Disabled"}
	}
	cmd := &cobra.Command{
		Use:     action + " <cluster> <addon>",
		Short:   strings.ToUpper(action[:1]) + action[1:] + " an add-on",
		Example: "  pantech kubernetes addons " + action + " prod metrics-server",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkAddon(args[1]); err != nil {
				return &usageError{err, cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveCluster(cmd, c, args[0])
			if err != nil {
				return err
			}
			if !enable {
				if err := a.confirm(fmt.Sprintf("Disable %s in %s? Workloads that rely on it stop working.", args[1], args[0])); err != nil {
					return err
				}
			}
			req := api.Request{Method: http.MethodPost, Path: clusterPath(id) + "/addons/" + url.PathEscape(args[1]) + "/toggle", Body: map[string]any{"enabled": enable}}
			return a.runWrite(cmd, c, req, noWait, v, args[1]+" in "+args[0])
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	return cmd
}
