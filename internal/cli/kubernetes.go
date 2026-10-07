package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

var clusterKind = kind{prefix: "k8s_", path: "/kubernetes-clusters", noun: "cluster", listCmd: "pantech kubernetes list", noSearch: true}

func resolveCluster(cmd *cobra.Command, c *api.Client, ref string) (string, error) {
	return resolve(cmd, c, clusterKind, ref, func(k api.KubernetesCluster) (string, string) { return k.ID, k.Name })
}

// kubernetesVersions is the versions list, in one zone when zone is set.
func kubernetesVersions(cmd *cobra.Command, c *api.Client, zone string) (*api.KubernetesVersionList, []byte, error) {
	path := "/kubernetes-versions"
	if zone != "" {
		path += "?" + url.Values{"zone_id": {zone}}.Encode()
	}
	var list api.KubernetesVersionList
	body, err := api.Get(ctx(cmd), c, path, &list)
	if err != nil {
		return nil, nil, err
	}
	return &list, body, nil
}

// resolveKubernetesVersion takes a version id (k8sv_…) or a version such as
// 1.31.2, offered in zone.
func resolveKubernetesVersion(cmd *cobra.Command, c *api.Client, zone, ref string) (string, error) {
	if strings.HasPrefix(ref, "k8sv_") {
		return ref, nil
	}
	list, _, err := kubernetesVersions(cmd, c, zone)
	if err != nil {
		return "", err
	}
	var offered []string
	for _, v := range list.Data {
		if zone != "" && v.ZoneID != zone {
			continue
		}
		if v.Status != "available" {
			continue
		}
		if v.Version == ref {
			return v.ID, nil
		}
		offered = append(offered, v.Version)
	}
	if len(offered) == 0 {
		return "", fmt.Errorf("no Kubernetes versions are offered in %s\nSee: pantech kubernetes versions", zone)
	}
	return "", fmt.Errorf("version %s is not offered in %s: use one of %s\nSee: pantech kubernetes versions --zone %s", ref, zone, strings.Join(offered, ", "), zone)
}

// parseAutoscale reads MIN:MAX.
func parseAutoscale(s string) (api.KubernetesAutoscaling, error) {
	los, his, ok := strings.Cut(s, ":")
	lo, err1 := strconv.Atoi(los)
	hi, err2 := strconv.Atoi(his)
	if !ok || err1 != nil || err2 != nil || lo < 1 || hi > 10 || lo > hi {
		return api.KubernetesAutoscaling{}, fmt.Errorf("--autoscale %q: give MIN:MAX workers, 1 to 10, e.g. 2:5", s)
	}
	return api.KubernetesAutoscaling{Enabled: true, MinWorkers: lo, MaxWorkers: hi}, nil
}

func newKubernetesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "kubernetes",
		Aliases: []string{"k8s"},
		Short:   "Create and run managed Kubernetes clusters",
		Long: `Create and run managed Kubernetes clusters. Every node, control and worker,
is a VM of the node plan in your account and is billed as one; there is no
separate control-plane fee. Download a kubeconfig for kubectl once the
cluster is running.`,
	}
	cmd.AddCommand(
		newKubernetesVersionsCmd(a),
		newKubernetesListCmd(a),
		newKubernetesGetCmd(a),
		newKubernetesCreateCmd(a),
		newKubernetesConfigureCmd(a),
		newKubernetesUpgradeCmd(a),
		newKubernetesPowerCmd(a, "start", "Start a stopped cluster", "Start %s? Its nodes are billed for compute again.", verb{"Starting", "Started"}),
		newKubernetesPowerCmd(a, "stop", "Stop a running cluster", "Stop %s? Its workloads stop; its disks stay billed.", verb{"Stopping", "Stopped"}),
		deleteCmd(a, clusterKind, "Every node, its disks and the workloads' volumes are erased and cannot be recovered.", resolveCluster),
		newKubernetesKubeconfigCmd(a),
	)
	return cmd
}

func newKubernetesVersionsCmd(a *app) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "versions",
		Short: "List the Kubernetes versions clusters can be created with",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			list, body, err := kubernetesVersions(cmd, c, zone)
			if err != nil {
				return err
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(body)
				return nil
			case a.out.Quiet:
				for _, v := range list.Data {
					a.out.Line("%s", v.ID)
				}
				return nil
			case len(list.Data) == 0:
				a.out.Note("No Kubernetes versions are offered here yet.")
				return nil
			}
			rows := make([][]string, len(list.Data))
			for i, v := range list.Data {
				rows[i] = []string{v.Version, v.ZoneID, a.out.State(v.Status), fmt.Sprintf("%d vCPU, %d MB", v.MinCPU, v.MinMemoryMB), a.out.Dim(v.ID)}
			}
			a.out.Table([]string{"version", "zone", "status", "node minimum", "id"}, rows)
			if len(list.HAZoneIDs) > 0 {
				a.out.Summary("3 control nodes in " + strings.Join(list.HAZoneIDs, ", "))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "only versions in this zone")
	return cmd
}

// clusterState is the cluster's state, with "applying" while a scale or
// upgrade has not finished.
func clusterState(a *app, k api.KubernetesCluster) string {
	s := state(a.out, k.ObservedState, k.DesiredState)
	if api.Applying(k.InSync) && k.ObservedState != "updating" {
		s += a.out.Dim(" (applying)")
	}
	return s
}

func clusterWorkers(k api.KubernetesCluster) string {
	if k.Autoscaling.Enabled {
		return fmt.Sprintf("%d (autoscale %d–%d)", k.Workers, k.Autoscaling.MinWorkers, k.Autoscaling.MaxWorkers)
	}
	return strconv.Itoa(k.Workers)
}

func newKubernetesListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List Kubernetes clusters",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			ks, raw, err := listItems[api.KubernetesCluster](cmd, c, "/kubernetes-clusters", nil)
			if err != nil {
				return err
			}
			if printList(a, ks, raw, func(k api.KubernetesCluster) string { return k.ID }, "No Kubernetes clusters yet.", "pantech kubernetes create --name prod --zone <zone> --version <version> --node-plan <plan> --workers 2") {
				return nil
			}
			rows := make([][]string, len(ks))
			for i, k := range ks {
				rows[i] = []string{k.Name, clusterState(a, k), k.KubernetesVersion, strconv.Itoa(k.ControlNodes), clusterWorkers(k), k.ZoneID, a.out.Dim(k.ID)}
			}
			a.out.Table([]string{"name", "state", "version", "control", "workers", "zone", "id"}, rows)
			a.out.Summary(count(len(ks), "cluster"))
			return nil
		},
	}
}

func newKubernetesGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <cluster>",
		Aliases: []string{"show"},
		Short:   "Show a Kubernetes cluster, by id or name",
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
			var k api.KubernetesCluster
			if ok, err := getOne(a, cmd, c, "/kubernetes-clusters/"+url.PathEscape(id), &k); !ok {
				return err
			}
			allowed := "any address"
			if len(k.APIAllowedCIDRs) > 0 {
				allowed = strings.Join(k.APIAllowedCIDRs, ", ")
			}
			upgrades := make([]string, len(k.AvailableUpgrades))
			for i, v := range k.AvailableUpgrades {
				upgrades[i] = v.Version
			}
			upgradeTo := "none"
			if len(upgrades) > 0 {
				upgradeTo = strings.Join(upgrades, ", ")
			}
			failure := output.Or(k.FailureCode)
			if k.FailureReason != nil && *k.FailureReason != "" {
				failure += ": " + *k.FailureReason
			}
			d := output.Detail{
				Title:    k.Name,
				State:    clusterState(a, k),
				Subtitle: k.ID,
				Sections: [][]output.Pair{
					{{"Version", k.KubernetesVersion}, {"Upgrades", upgradeTo}, {"Endpoint", output.Or(k.Endpoint)}, {"API allowed from", allowed}},
					{{"Control nodes", strconv.Itoa(k.ControlNodes)}, {"Workers", clusterWorkers(k)}, {"Node", fmt.Sprintf("%s · %d vCPU · %d MB · %d GB", k.NodePlanID, k.Node.VCPU, k.Node.MemoryMB, k.Node.DiskGB)}, {"Nodes billed", strconv.Itoa(k.Nodes)}, {"Volume storage", fmt.Sprintf("%d GB", k.VolumeStorageGB)}},
					{{"Zone", k.ZoneID}, {"Network", output.Or(k.NetworkID)}, {"Subnet", output.Or(k.SubnetID)}},
					{{"Created", output.When(k.CreatedAt)}},
				},
			}
			if k.FailureCode != nil {
				d.Sections = append(d.Sections, []output.Pair{{"Failure", failure}})
			}
			if k.ObservedState == "running" {
				d.Next = [][2]string{{"Get a kubeconfig", "pantech kubernetes kubeconfig " + k.Name + " -o " + k.Name + ".kubeconfig"}}
			}
			a.out.Print(d)
			return nil
		},
	}
}

func newKubernetesCreateCmd(a *app) *cobra.Command {
	var name, zone, version, subnet, nodePlan, autoscale string
	var controlNodes, workers int
	var apiAllow []string
	var noWait bool
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a managed Kubernetes cluster",
		Long: `Create a managed Kubernetes cluster. Every node is a VM of --node-plan,
billed as one. In a VPC zone the nodes go in --subnet; in a standard zone
leave it out. --control-nodes is 1, or 3 where the zone offers a highly
available control plane (see "pantech kubernetes versions"). Give a fixed
--workers count, or --autoscale MIN:MAX to let the cluster autoscaler add and
remove workers. --api-allow limits which addresses reach the API server (VPC
zone only; default: any address).

--version is a version such as 1.31.2, or its id, offered in --zone.`,
		Example: `  pantech kubernetes create --name prod --zone af-abj-2 --version 1.31.2 \
    --subnet snet_1 --node-plan s-2vcpu-4gb --workers 2
  pantech kubernetes create --name prod --zone af-abj-2 --version 1.31.2 \
    --subnet snet_1 --node-plan s-2vcpu-4gb --control-nodes 3 --autoscale 2:5 \
    --api-allow 203.0.113.0/24`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fl := cmd.Flags()
			body := map[string]any{"name": name, "zone_id": zone, "node_plan": nodePlan}
			switch {
			case fl.Changed("workers") && autoscale != "":
				// Both is allowed: workers is the starting count, within the range.
			case !fl.Changed("workers") && autoscale == "":
				return &usageError{errors.New("give --workers N or --autoscale MIN:MAX"), cmd}
			}
			if fl.Changed("workers") {
				body["workers"] = workers
			}
			if autoscale != "" {
				as, err := parseAutoscale(autoscale)
				if err != nil {
					return &usageError{err, cmd}
				}
				body["autoscaling"] = as
			}
			if fl.Changed("control-nodes") {
				if controlNodes != 1 && controlNodes != 3 {
					return &usageError{errors.New("--control-nodes is 1, or 3 for a highly available control plane"), cmd}
				}
				body["control_nodes"] = controlNodes
			}
			if subnet != "" {
				body["subnet_id"] = subnet
			}
			if len(apiAllow) > 0 {
				body["api_allowed_cidrs"] = apiAllow
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			versionID, err := resolveKubernetesVersion(cmd, c, zone, version)
			if err != nil {
				return err
			}
			body["kubernetes_version_id"] = versionID
			if err := a.confirm(fmt.Sprintf("Create cluster %s? Every node is billed as a %s VM.", name, nodePlan)); err != nil {
				return err
			}
			return a.runCreate(cmd, c, api.Request{Method: http.MethodPost, Path: "/kubernetes-clusters", Body: body}, noWait, "cluster "+name)
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "1 to 40 lowercase letters, digits and hyphens, starting with a letter (required)")
	f.StringVar(&zone, "zone", "", "the zone, from pantech kubernetes versions (required)")
	f.StringVar(&version, "version", "", "a Kubernetes version such as 1.31.2, or its id (required)")
	f.StringVar(&subnet, "subnet", "", "the subnet the nodes go in, by id; required in a VPC zone")
	f.StringVar(&nodePlan, "node-plan", "", "the plan slug every node uses, from pantech plans (required)")
	f.IntVar(&controlNodes, "control-nodes", 1, "1, or 3 for a highly available control plane")
	f.IntVar(&workers, "workers", 0, "a fixed number of workers, 1 to 10")
	f.StringVar(&autoscale, "autoscale", "", "let the autoscaler keep MIN:MAX workers, e.g. 2:5")
	f.StringSliceVar(&apiAllow, "api-allow", nil, "a CIDR allowed to reach the API server; repeat or comma-separate (VPC zone only)")
	f.BoolVar(&noWait, "no-wait", false, "return the id without waiting")
	for _, req := range []string{"name", "zone", "version", "node-plan"} {
		_ = cmd.MarkFlagRequired(req)
	}
	return cmd
}

func newKubernetesConfigureCmd(a *app) *cobra.Command {
	var workers int
	var autoscale string
	var noAutoscale, apiAllowAny, noWait bool
	var apiAllow []string
	cmd := &cobra.Command{
		Use:     "configure <cluster>",
		Aliases: []string{"scale"},
		Short:   "Scale a cluster's workers, set its autoscaler or its API allow-list",
		Long: `Change a running cluster: a fixed number of workers (--workers), the
autoscaler on with a range (--autoscale MIN:MAX) or off (--no-autoscale),
or the whole list of addresses allowed to reach the API server (--api-allow,
or --api-allow-any to allow any address; VPC zone only). Flags left out stay
as they are. Added workers are billed as VMs of the node plan.`,
		Example: `  pantech kubernetes configure prod --workers 4
  pantech kubernetes configure prod --autoscale 2:6
  pantech kubernetes configure prod --no-autoscale --workers 3
  pantech kubernetes configure prod --api-allow 203.0.113.0/24,198.51.100.7/32`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fl := cmd.Flags()
			body := map[string]any{}
			if autoscale != "" && noAutoscale {
				return &usageError{errors.New("--autoscale and --no-autoscale do not go together"), cmd}
			}
			if autoscale != "" && fl.Changed("workers") {
				return &usageError{errors.New("--workers is a fixed count: with --autoscale the autoscaler sets it"), cmd}
			}
			if fl.Changed("api-allow") && apiAllowAny {
				return &usageError{errors.New("--api-allow and --api-allow-any do not go together"), cmd}
			}
			if fl.Changed("workers") {
				body["workers"] = workers
			}
			if autoscale != "" {
				as, err := parseAutoscale(autoscale)
				if err != nil {
					return &usageError{err, cmd}
				}
				body["autoscaling"] = as
			}
			if noAutoscale {
				body["autoscaling"] = api.KubernetesAutoscaling{}
			}
			switch {
			case apiAllowAny:
				body["api_allowed_cidrs"] = []string{}
			case fl.Changed("api-allow"):
				body["api_allowed_cidrs"] = apiAllow
			}
			if len(body) == 0 {
				return &usageError{errors.New("give --workers, --autoscale, --no-autoscale, --api-allow or --api-allow-any"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveCluster(cmd, c, args[0])
			if err != nil {
				return err
			}
			if body["workers"] != nil || autoscale != "" {
				if err := a.confirm(fmt.Sprintf("Change %s's workers? Each worker is billed as a VM of its node plan.", args[0])); err != nil {
					return err
				}
			}
			req := api.Request{Method: http.MethodPatch, Path: "/kubernetes-clusters/" + url.PathEscape(id), Body: body}
			return a.runWrite(cmd, c, req, noWait, verb{"Updating", "Updated"}, args[0])
		},
	}
	f := cmd.Flags()
	f.IntVar(&workers, "workers", 0, "a fixed number of workers, 1 to 10")
	f.StringVar(&autoscale, "autoscale", "", "turn the autoscaler on with MIN:MAX workers, e.g. 2:5")
	f.BoolVar(&noAutoscale, "no-autoscale", false, "turn the autoscaler off")
	f.StringSliceVar(&apiAllow, "api-allow", nil, "the whole new list of CIDRs allowed to reach the API server; repeat or comma-separate")
	f.BoolVar(&apiAllowAny, "api-allow-any", false, "allow any address to reach the API server")
	f.BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	return cmd
}

func newKubernetesUpgradeCmd(a *app) *cobra.Command {
	var version string
	var noWait bool
	cmd := &cobra.Command{
		Use:   "upgrade <cluster> --version VERSION",
		Short: "Upgrade a cluster to a newer Kubernetes version",
		Long: `Upgrade a running cluster to one of its available upgrades (the next patch,
or the next minor version), shown by "pantech kubernetes get". Nodes are
replaced one at a time. Upgrades never go down.`,
		Example: `  pantech kubernetes upgrade prod --version 1.32.0`,
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
			versionID := version
			if !strings.HasPrefix(version, "k8sv_") {
				var k api.KubernetesCluster
				if _, err := api.Get(ctx(cmd), c, "/kubernetes-clusters/"+url.PathEscape(id), &k); err != nil {
					return err
				}
				versionID = ""
				var offered []string
				for _, v := range k.AvailableUpgrades {
					if v.Version == version {
						versionID = v.ID
					}
					offered = append(offered, v.Version)
				}
				if versionID == "" {
					if len(offered) == 0 {
						return fmt.Errorf("%s has no upgrade available now (it is on %s)", args[0], k.KubernetesVersion)
					}
					return fmt.Errorf("%s cannot be upgraded to %s: use one of %s", args[0], version, strings.Join(offered, ", "))
				}
			}
			if err := a.confirm(fmt.Sprintf("Upgrade %s to %s? Its nodes are replaced one at a time.", args[0], version)); err != nil {
				return err
			}
			req := api.Request{Method: http.MethodPost, Path: "/kubernetes-clusters/" + url.PathEscape(id) + "/upgrade", Body: map[string]any{"kubernetes_version_id": versionID}}
			return a.runWrite(cmd, c, req, noWait, verb{"Upgrading", "Upgraded"}, args[0])
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "the version to upgrade to, such as 1.32.0, or its id (required)")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	_ = cmd.MarkFlagRequired("version")
	return cmd
}

func newKubernetesPowerCmd(a *app, action, short, question string, v verb) *cobra.Command {
	var noWait bool
	cmd := &cobra.Command{
		Use:   action + " <cluster>",
		Short: short + ", by id or name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveCluster(cmd, c, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf(question, args[0])); err != nil {
				return err
			}
			req := api.Request{Method: http.MethodPost, Path: "/kubernetes-clusters/" + url.PathEscape(id) + "/" + action}
			return a.runWrite(cmd, c, req, noWait, v, args[0])
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	return cmd
}

func newKubernetesKubeconfigCmd(a *app) *cobra.Command {
	var file string
	var toStdout bool
	cmd := &cobra.Command{
		Use:   "kubeconfig <cluster> (-o FILE | --stdout)",
		Short: "Download a cluster's admin kubeconfig",
		Long: `Download a running cluster's kubeconfig, for kubectl. It holds a long-lived
cluster-admin credential: store it like a password. -o writes it to a file
readable only by you (mode 0600, replacing the file if it exists); --stdout
prints it instead. One of them is required, so it is never printed by
accident. It needs a key with the write scope.`,
		Example: `  pantech kubernetes kubeconfig prod -o ~/.kube/prod.yaml
  KUBECONFIG=~/.kube/prod.yaml kubectl get nodes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case file == "" && !toStdout:
				return &usageError{errors.New("give -o FILE to write the kubeconfig to a file, or --stdout to print it"), cmd}
			case file != "" && toStdout:
				return &usageError{errors.New("-o and --stdout do not go together"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveCluster(cmd, c, args[0])
			if err != nil {
				return err
			}
			var kc struct {
				Kubeconfig string `json:"kubeconfig"`
			}
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: "/kubernetes-clusters/" + url.PathEscape(id) + "/kubeconfig"}, &kc)
			if err != nil {
				return err
			}
			warning := fmt.Sprintf("This kubeconfig is a cluster-admin credential for %s: keep it like a password.", args[0])
			if a.out.Quiet || a.out.JSON {
				a.out.Note("%s", warning) // said even then: it is a secret
			} else {
				a.out.Warn("%s", warning)
			}
			if toStdout {
				if a.out.JSON {
					a.out.RawJSON(res.Body)
					return nil
				}
				_, err := fmt.Fprint(a.out.Out, kc.Kubeconfig)
				return err
			}
			if err := writePrivateFile(file, []byte(kc.Kubeconfig)); err != nil {
				return err
			}
			a.out.Success("Wrote %s (readable only by you)", file)
			a.out.Next("Use it", "KUBECONFIG="+file+" kubectl get nodes")
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "output", "o", "", "write the kubeconfig to this file, mode 0600")
	cmd.Flags().BoolVar(&toStdout, "stdout", false, "print the kubeconfig on stdout")
	return cmd
}

// writePrivateFile writes data to path readable only by its owner, also
// when the file already exists with wider permissions.
func writePrivateFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
