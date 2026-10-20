package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

// What runs in a cluster, read live from it, and the public routes to it.

type clusterNode struct {
	Name           string `json:"name"`
	Role           string `json:"role"`
	Status         string `json:"status"`
	IPAddress      string `json:"ip_address"`
	VCPU           int    `json:"vcpu"`
	MemoryMB       int    `json:"memory_mb"`
	KubeletVersion string `json:"kubelet_version"`
}

type workloadPort struct {
	Protocol   string `json:"protocol"`
	Port       int    `json:"port"`
	TargetPort int    `json:"target_port"`
	NodePort   int    `json:"node_port"`
}

type workloadService struct {
	Name        string         `json:"name"`
	Namespace   string         `json:"namespace"`
	Type        string         `json:"type"`
	Ports       []workloadPort `json:"ports"`
	PublicPort  *int           `json:"public_port"`
	ExternalURL *string        `json:"external_url"`
}

type workloadPod struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Status    string `json:"status"`
	Ready     string `json:"ready"`
	Restarts  int    `json:"restarts"`
	NodeName  string `json:"node_name"`
	Age       string `json:"age"`
}

type syncedPort struct {
	Purpose     string `json:"purpose"`
	PublicPort  int    `json:"public_port"`
	PrivatePort int    `json:"private_port"`
	Protocol    string `json:"protocol"`
	Target      string `json:"target"`
}

func (p workloadPort) String() string {
	proto := p.Protocol
	if proto == "" {
		proto = "TCP"
	}
	if p.NodePort > 0 {
		return fmt.Sprintf("%d:%d/%s", p.Port, p.NodePort, proto)
	}
	return fmt.Sprintf("%d/%s", p.Port, proto)
}

func newKubernetesNodesCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "nodes <cluster>",
		Short: "List a cluster's nodes, as Kubernetes reports them",
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
			var res struct {
				Nodes []clusterNode `json:"nodes"`
			}
			if ok, err := getOne(a, cmd, c, clusterPath(id)+"/nodes", &res); !ok {
				return err
			}
			if a.out.Quiet {
				for _, n := range res.Nodes {
					a.out.Line("%s", n.Name)
				}
				return nil
			}
			rows := make([][]string, len(res.Nodes))
			for i, n := range res.Nodes {
				rows[i] = []string{n.Name, n.Role, a.out.State(strings.ToLower(n.Status)), n.IPAddress, fmt.Sprintf("%d vCPU · %d MB", n.VCPU, n.MemoryMB), n.KubeletVersion}
			}
			a.out.Table([]string{"name", "role", "status", "address", "size", "version"}, rows)
			return nil
		},
	}
}

func newKubernetesWorkloadsCmd(a *app) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:     "workloads <cluster>",
		Aliases: []string{"ps"},
		Short:   "Show a cluster's services and pods, live",
		Long: `Show the cluster's services and pods as it runs them now. System
namespaces (kube-*) are left out unless --all-namespaces. An exposed
service shows the public address it is reached on.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveCluster(cmd, c, args[0])
			if err != nil {
				return err
			}
			var res struct {
				Services []workloadService `json:"services"`
				Pods     []workloadPod     `json:"pods"`
			}
			if ok, err := getOne(a, cmd, c, clusterPath(id)+"/workloads", &res); !ok {
				return err
			}
			shown := func(ns string) bool { return all || !strings.HasPrefix(ns, "kube-") }
			var svcRows, podRows [][]string
			for _, s := range res.Services {
				if !shown(s.Namespace) {
					continue
				}
				portList := make([]string, len(s.Ports))
				for i, p := range s.Ports {
					portList[i] = p.String()
				}
				svcRows = append(svcRows, []string{s.Namespace, s.Name, s.Type, strings.Join(portList, ","), output.Or(s.ExternalURL)})
			}
			for _, p := range res.Pods {
				if !shown(p.Namespace) {
					continue
				}
				podRows = append(podRows, []string{p.Namespace, p.Name, a.out.State(strings.ToLower(p.Status)), p.Ready, strconv.Itoa(p.Restarts), p.NodeName, p.Age})
			}
			if len(svcRows) == 0 && len(podRows) == 0 {
				a.out.Note("Nothing is deployed yet.")
				a.out.Next("Deploy something", "pantech kubernetes apply "+args[0]+" -f app.yaml")
				return nil
			}
			a.out.Table([]string{"namespace", "service", "type", "ports", "public url"}, svcRows)
			a.out.Line("")
			a.out.Table([]string{"namespace", "pod", "status", "ready", "restarts", "node", "age"}, podRows)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&all, "all-namespaces", "A", false, "include the kube-* system namespaces")
	return cmd
}

// readManifest reads -f: a file, or "-" for stdin.
func readManifest(cmd *cobra.Command, file string) (string, error) {
	var r io.Reader
	if file == "-" {
		r = cmd.InOrStdin()
	} else {
		f, err := os.Open(file)
		if err != nil {
			return "", err
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, 512<<10+1))
	if err != nil {
		return "", err
	}
	if len(b) > 512<<10 {
		return "", errors.New("the manifest is larger than 512 KB")
	}
	return string(b), nil
}

func newKubernetesApplyCmd(a *app) *cobra.Command {
	var file, namespace string
	cmd := &cobra.Command{
		Use:   "apply <cluster> -f FILE",
		Short: "Apply a YAML manifest to a cluster",
		Long: `Apply a YAML manifest to a cluster, as "kubectl apply --server-side"
would: each object is created or updated in order. Applying the same file
again changes nothing. -f - reads the manifest from stdin.`,
		Example: `  pantech kubernetes apply prod -f app.yaml
  cat app.yaml | pantech kubernetes apply prod -f - --namespace shop`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" {
				return &usageError{errors.New("give -f FILE, or -f - for stdin"), cmd}
			}
			manifest, err := readManifest(cmd, file)
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveCluster(cmd, c, args[0])
			if err != nil {
				return err
			}
			body := map[string]any{"manifest": manifest}
			if namespace != "" {
				body["namespace"] = namespace
			}
			var res struct {
				Output string `json:"output"`
			}
			raw, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: clusterPath(id) + "/apply-manifest", Body: body}, &res)
			if err != nil {
				return err
			}
			if a.out.JSON {
				a.out.RawJSON(raw.Body)
				return nil
			}
			a.out.Line("%s", res.Output)
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "filename", "f", "", "the manifest file, or - for stdin (required)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "where objects without a namespace go (default: default)")
	return cmd
}

func newKubernetesManifestsCmd(a *app) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "manifests <cluster>",
		Short: "Export a cluster's workloads as YAML",
		Long: `Export the objects you run in the cluster (outside the kube-* namespaces)
as one YAML stream you can edit and apply back. Secrets are never exported.`,
		Example: `  pantech kubernetes manifests prod > live.yaml
  pantech kubernetes manifests prod -o live.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveCluster(cmd, c, args[0])
			if err != nil {
				return err
			}
			var res struct {
				Manifest      string `json:"manifest"`
				ResourceCount int    `json:"resource_count"`
			}
			if ok, err := getOne(a, cmd, c, clusterPath(id)+"/live-manifests", &res); !ok {
				return err
			}
			if res.ResourceCount == 0 {
				a.out.Note("Nothing is deployed yet.")
				return nil
			}
			if file != "" {
				if err := os.WriteFile(file, []byte(res.Manifest), 0o644); err != nil {
					return err
				}
				a.out.Success("Wrote %d object(s) to %s", res.ResourceCount, file)
				return nil
			}
			_, err = fmt.Fprint(a.out.Out, res.Manifest)
			return err
		},
	}
	cmd.Flags().StringVarP(&file, "output", "o", "", "write to this file instead of stdout")
	return cmd
}

func newKubernetesExposeCmd(a *app) *cobra.Command {
	var namespace, protocol string
	var port, nodePort int
	cmd := &cobra.Command{
		Use:   "expose <cluster> <service> --port PUBLIC_PORT",
		Short: "Route a public port of the cluster's address to a service",
		Long: `Route --port on the cluster's public address to a Service: its port becomes
a NodePort (--node-port, or the one it has, or one the cluster picks) and
the route is created. Needs a cluster in a VPC subnet, and a firewall rule on
the subnet that lets the port in. To take the route away, remove the
service's pantechdynamics.com/public-port annotation with kubectl and run
"pantech kubernetes sync-routing".`,
		Example: `  pantech kubernetes expose prod web --port 80
  pantech kubernetes expose prod api --port 8443 --node-port 30443 --namespace shop`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if port < 1 || port > 65535 {
				return &usageError{errors.New("--port is the public port, 1 to 65535"), cmd}
			}
			if nodePort != 0 && (nodePort < 30000 || nodePort > 32767) {
				return &usageError{errors.New("--node-port is 30000 to 32767"), cmd}
			}
			protocol = strings.ToUpper(protocol)
			if protocol != "TCP" && protocol != "UDP" {
				return &usageError{errors.New("--protocol is tcp or udp"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveCluster(cmd, c, args[0])
			if err != nil {
				return err
			}
			body := map[string]any{"service_name": args[1], "public_port": port, "protocol": protocol}
			if namespace != "" {
				body["namespace"] = namespace
			}
			if nodePort != 0 {
				body["node_port"] = nodePort
			}
			var res struct {
				ExternalURL string `json:"external_url"`
				NodePort    int    `json:"node_port"`
			}
			raw, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: clusterPath(id) + "/expose-service", Body: body}, &res)
			if err != nil {
				return err
			}
			if a.out.JSON {
				a.out.RawJSON(raw.Body)
				return nil
			}
			a.out.Success("Routed %s → %s (node port %d)", res.ExternalURL, args[1], res.NodePort)
			return nil
		},
	}
	f := cmd.Flags()
	f.IntVar(&port, "port", 0, "the public port clients connect to (required)")
	f.IntVar(&nodePort, "node-port", 0, "the service's NodePort, 30000 to 32767 (default: its own, or one the cluster picks)")
	f.StringVarP(&namespace, "namespace", "n", "", "the service's namespace (default: default)")
	f.StringVar(&protocol, "protocol", "tcp", "tcp or udp")
	_ = cmd.MarkFlagRequired("port")
	return cmd
}

func newKubernetesSyncRoutingCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "sync-routing <cluster>",
		Short: "Reconcile a cluster's public routes with what it exposes",
		Long: `Make the port forwards on the cluster's public address match what the
cluster exposes now: its API server, and every service carrying the
pantechdynamics.com/public-port annotation. Routes to services no longer
exposed are removed. Only the cluster's own routes are changed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveCluster(cmd, c, args[0])
			if err != nil {
				return err
			}
			var res struct {
				PublicIP    string       `json:"public_ip"`
				SyncedPorts []syncedPort `json:"synced_ports"`
			}
			raw, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: clusterPath(id) + "/sync-routing"}, &res)
			if err != nil {
				return err
			}
			if a.out.JSON {
				a.out.RawJSON(raw.Body)
				return nil
			}
			rows := make([][]string, len(res.SyncedPorts))
			for i, p := range res.SyncedPorts {
				rows[i] = []string{fmt.Sprintf("%s:%d", res.PublicIP, p.PublicPort), fmt.Sprintf("%s:%d/%s", p.Target, p.PrivatePort, strings.ToUpper(p.Protocol)), p.Purpose}
			}
			a.out.Table([]string{"public", "routes to", "purpose"}, rows)
			return nil
		},
	}
}
