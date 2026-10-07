package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	k8sVersionsBody = `{"data":[` +
		`{"id":"k8sv_2","zone_id":"af-abj-2","version":"1.32.0","status":"available","min_cpu":2,"min_memory_mb":2048},` +
		`{"id":"k8sv_1","zone_id":"af-abj-2","version":"1.31.2","status":"available","min_cpu":2,"min_memory_mb":2048},` +
		`{"id":"k8sv_0","zone_id":"af-abj-2","version":"1.30.9","status":"withdrawn","min_cpu":2,"min_memory_mb":2048}` +
		`],"ha_zone_ids":["af-abj-2"]}`
	k8sClusterBody = `{"id":"k8s_1","name":"prod","zone_id":"af-abj-2","kubernetes_version_id":"k8sv_1","kubernetes_version":"1.31.2","network_id":"net_1","subnet_id":"snet_1","node_plan_id":"plan_1",` +
		`"node":{"vcpu":2,"memory_mb":4096,"disk_gb":50},"control_nodes":3,"workers":2,"nodes":5,"desired_state":"running","observed_state":"running","in_sync":true,` +
		`"failure_code":null,"failure_reason":null,"available_upgrades":[{"id":"k8sv_2","zone_id":"af-abj-2","version":"1.32.0","status":"available","min_cpu":2,"min_memory_mb":2048}],` +
		`"autoscaling":{"enabled":true,"min_workers":2,"max_workers":5},"api_allowed_cidrs":["203.0.113.0/24"],"endpoint":"https://203.0.113.10:6443","volume_storage_gb":20}`
	k8sAccepted = `{"operation_id":"op_1","resource_id":"k8s_1","status":"submitting"}`
	opDone      = `{"id":"op_1","status":"succeeded"}`
)

func TestKubernetesVersions(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /kubernetes-versions", 200, k8sVersionsBody)
	r := run(t, srv, "", "k8s", "versions", "--zone", "af-abj-2")
	if r.err != nil {
		t.Fatal(r.err)
	}
	for _, want := range []string{"1.32.0", "withdrawn", "2 vCPU, 2048 MB", "k8sv_1"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}
	if q := f.sent("GET", "/kubernetes-versions")[0].Query; q != "zone_id=af-abj-2" {
		t.Fatalf("query %q", q)
	}
}

func TestKubernetesListAndGet(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /kubernetes-clusters", 200, `{"data":[`+k8sClusterBody+`],"next_cursor":null}`).
		on("GET /kubernetes-clusters/k8s_1", 200, k8sClusterBody)
	r := run(t, srv, "", "kubernetes", "list")
	if r.err != nil {
		t.Fatal(r.err)
	}
	for _, want := range []string{"prod", "1.31.2", "2 (autoscale 2–5)", "k8s_1"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("list lacks %q:\n%s", want, r.stdout)
		}
	}
	r = run(t, srv, "", "kubernetes", "get", "prod")
	if r.err != nil {
		t.Fatal(r.err)
	}
	for _, want := range []string{"1.32.0", "https://203.0.113.10:6443", "203.0.113.0/24", "20 GB"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("get lacks %q:\n%s", want, r.stdout)
		}
	}
	if !strings.Contains(r.stderr, "pantech kubernetes kubeconfig prod -o") {
		t.Fatalf("no kubeconfig hint:\n%s", r.stderr)
	}
}

func TestKubernetesCreate(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want map[string]any
	}{
		{
			name: "fixed workers, version by number",
			args: []string{"--version", "1.31.2", "--workers", "2"},
			want: map[string]any{"name": "prod", "zone_id": "af-abj-2", "node_plan": "s-2vcpu-4gb", "kubernetes_version_id": "k8sv_1", "subnet_id": "snet_1", "workers": float64(2)},
		},
		{
			name: "autoscaled, HA, API allow-list, version by id",
			args: []string{"--version", "k8sv_2", "--autoscale", "2:5", "--control-nodes", "3", "--api-allow", "203.0.113.0/24", "--api-allow", "198.51.100.7/32"},
			want: map[string]any{"name": "prod", "zone_id": "af-abj-2", "node_plan": "s-2vcpu-4gb", "kubernetes_version_id": "k8sv_2", "subnet_id": "snet_1", "control_nodes": float64(3),
				"autoscaling":       map[string]any{"enabled": true, "min_workers": float64(2), "max_workers": float64(5)},
				"api_allowed_cidrs": []any{"203.0.113.0/24", "198.51.100.7/32"}},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newFakeAPI(t)
			f.on("GET /kubernetes-versions", 200, k8sVersionsBody).
				on("POST /kubernetes-clusters", 202, k8sAccepted).
				on("GET /operations/op_1", 200, opDone)
			args := append([]string{"k8s", "create", "--name", "prod", "--zone", "af-abj-2", "--subnet", "snet_1", "--node-plan", "s-2vcpu-4gb", "--yes"}, c.args...)
			r := run(t, srv, "", args...)
			if r.err != nil || r.stdout != "k8s_1\n" {
				t.Fatalf("stdout %q err %v", r.stdout, r.err)
			}
			if got := f.sentJSON("POST", "/kubernetes-clusters"); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("body = %v\nwant   %v", got, c.want)
			}
		})
	}
}

func TestKubernetesCreateRefusedBeforeSending(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /kubernetes-versions", 200, k8sVersionsBody).on("POST /kubernetes-clusters", 202, k8sAccepted)
	base := []string{"k8s", "create", "--name", "prod", "--zone", "af-abj-2", "--node-plan", "s-2vcpu-4gb", "--yes"}
	for _, c := range []struct {
		args    []string
		usage   bool
		wantErr string
	}{
		{[]string{"--version", "1.31.2"}, true, "--workers N or --autoscale"},
		{[]string{"--version", "1.31.2", "--autoscale", "5:2"}, true, "MIN:MAX"},
		{[]string{"--version", "1.31.2", "--workers", "2", "--control-nodes", "2"}, true, "--control-nodes"},
		{[]string{"--version", "1.30.9", "--workers", "2"}, false, "use one of 1.32.0, 1.31.2"},
	} {
		r := run(t, srv, "", append(base, c.args...)...)
		if r.err == nil || IsUsage(r.err) != c.usage || !strings.Contains(r.err.Error(), c.wantErr) {
			t.Fatalf("%v: err = %v, want %q (usage %v)", c.args, r.err, c.wantErr, c.usage)
		}
	}
	if len(f.sent("POST", "/kubernetes-clusters")) != 0 {
		t.Fatal("created anyway")
	}
}

func TestKubernetesConfigure(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want map[string]any
	}{
		{"workers", []string{"--workers", "4", "--yes"}, map[string]any{"workers": float64(4)}},
		{"autoscale on", []string{"--autoscale", "2:6", "--yes"}, map[string]any{"autoscaling": map[string]any{"enabled": true, "min_workers": float64(2), "max_workers": float64(6)}}},
		{"autoscale off with a count", []string{"--no-autoscale", "--workers", "3", "--yes"}, map[string]any{"workers": float64(3), "autoscaling": map[string]any{"enabled": false, "min_workers": float64(0), "max_workers": float64(0)}}},
		{"api allow-list", []string{"--api-allow", "203.0.113.0/24,198.51.100.7/32"}, map[string]any{"api_allowed_cidrs": []any{"203.0.113.0/24", "198.51.100.7/32"}}},
		{"api allow any", []string{"--api-allow-any"}, map[string]any{"api_allowed_cidrs": []any{}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newFakeAPI(t)
			f.on("GET /kubernetes-clusters", 200, `{"data":[`+k8sClusterBody+`],"next_cursor":null}`).
				on("PATCH /kubernetes-clusters/k8s_1", 202, k8sAccepted).
				on("GET /operations/op_1", 200, opDone)
			r := run(t, srv, "", append([]string{"k8s", "configure", "prod"}, c.args...)...)
			if r.err != nil {
				t.Fatal(r.err)
			}
			if got := f.sentJSON("PATCH", "/kubernetes-clusters/k8s_1"); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("body = %#v\nwant   %#v", got, c.want)
			}
		})
	}
}

func TestKubernetesConfigureRefusals(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("PATCH /kubernetes-clusters/k8s_1", 202, k8sAccepted)
	for _, args := range [][]string{{}, {"--autoscale", "2:5", "--no-autoscale"}, {"--autoscale", "2:5", "--workers", "3"}, {"--api-allow", "10.0.0.0/8", "--api-allow-any"}} {
		if r := run(t, srv, "", append([]string{"k8s", "scale", "k8s_1", "--yes"}, args...)...); !IsUsage(r.err) {
			t.Fatalf("%v: err = %v, want a usage error", args, r.err)
		}
	}
	// Adding workers costs money: without --yes and no terminal, refused.
	if r := run(t, srv, "", "k8s", "configure", "k8s_1", "--workers", "5"); r.err == nil || !strings.Contains(r.err.Error(), "--yes") {
		t.Fatalf("err = %v", r.err)
	}
	if len(f.sent("PATCH", "/kubernetes-clusters/k8s_1")) != 0 {
		t.Fatal("changed anyway")
	}
}

func TestKubernetesUpgrade(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /kubernetes-clusters/k8s_1", 200, k8sClusterBody).
		on("POST /kubernetes-clusters/k8s_1/upgrade", 202, k8sAccepted).
		on("GET /operations/op_1", 200, opDone)
	r := run(t, srv, "", "k8s", "upgrade", "k8s_1", "--version", "1.33.0", "--yes")
	if r.err == nil || !strings.Contains(r.err.Error(), "use one of 1.32.0") {
		t.Fatalf("err = %v", r.err)
	}
	r = run(t, srv, "", "k8s", "upgrade", "k8s_1", "--version", "1.32.0", "--yes")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.sentJSON("POST", "/kubernetes-clusters/k8s_1/upgrade"); !reflect.DeepEqual(got, map[string]any{"kubernetes_version_id": "k8sv_2"}) {
		t.Fatalf("body = %v", got)
	}
}

func TestKubernetesStartStopDelete(t *testing.T) {
	for _, c := range []struct {
		args         []string
		method, path string
	}{
		{[]string{"start", "k8s_1"}, "POST", "/kubernetes-clusters/k8s_1/start"},
		{[]string{"stop", "k8s_1"}, "POST", "/kubernetes-clusters/k8s_1/stop"},
		{[]string{"delete", "k8s_1"}, "DELETE", "/kubernetes-clusters/k8s_1"},
	} {
		t.Run(c.args[0], func(t *testing.T) {
			f, srv := newFakeAPI(t)
			f.on(c.method+" "+c.path, 202, k8sAccepted).on("GET /operations/op_1", 200, opDone)
			if r := run(t, srv, "", append([]string{"k8s"}, c.args...)...); r.err == nil || !strings.Contains(r.err.Error(), "--yes") {
				t.Fatalf("err = %v: asks first", r.err)
			}
			if r := run(t, srv, "", append([]string{"k8s", "--yes"}, c.args...)...); r.err != nil {
				t.Fatal(r.err)
			}
			if len(f.sent(c.method, c.path)) != 1 || len(f.sent("GET", "/operations/op_1")) == 0 {
				t.Fatal("not sent once and followed")
			}
		})
	}
}

func TestKubernetesKubeconfig(t *testing.T) {
	const kc = "apiVersion: v1\nkind: Config\n"
	f, srv := newFakeAPI(t)
	f.on("POST /kubernetes-clusters/k8s_1/kubeconfig", 200, `{"kubeconfig":"apiVersion: v1\nkind: Config\n"}`)

	// Neither -o nor --stdout: refused before anything is fetched.
	if r := run(t, srv, "", "k8s", "kubeconfig", "k8s_1"); !IsUsage(r.err) {
		t.Fatalf("err = %v, want a usage error", r.err)
	}
	if len(f.sent("POST", "/kubernetes-clusters/k8s_1/kubeconfig")) != 0 {
		t.Fatal("fetched without being told where to put it")
	}

	file := filepath.Join(t.TempDir(), "prod.yaml")
	if err := os.WriteFile(file, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := run(t, srv, "", "k8s", "kubeconfig", "k8s_1", "-o", file)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.stdout != "" {
		t.Fatalf("stdout %q: the credential must only go to the file", r.stdout)
	}
	if !strings.Contains(r.stderr, "cluster-admin credential") {
		t.Fatalf("no warning:\n%s", r.stderr)
	}
	got, err := os.ReadFile(file)
	if err != nil || string(got) != kc {
		t.Fatalf("file %q err %v", got, err)
	}
	if fi, _ := os.Stat(file); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
	}

	r = run(t, srv, "", "k8s", "kubeconfig", "k8s_1", "--stdout", "-q")
	if r.err != nil || r.stdout != kc || !strings.Contains(r.stderr, "cluster-admin credential") {
		t.Fatalf("stdout %q stderr %q err %v", r.stdout, r.stderr, r.err)
	}
	if calls := f.sent("POST", "/kubernetes-clusters/k8s_1/kubeconfig"); calls[0].IdempotencyKey == "" {
		t.Fatal("a POST without an Idempotency-Key")
	}
}
