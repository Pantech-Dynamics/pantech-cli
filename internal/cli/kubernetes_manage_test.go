package cli

import (
	"reflect"
	"strings"
	"testing"
)

const k8sPoolsBody = `{"data":[{"id":"k8spool_1","cluster_id":"k8s_1","name":"db","node_plan_id":"plan_2","workers":2,"autoscaling_enabled":true,"min_workers":1,"max_workers":4,` +
	`"labels":{"role":"db"},"taints":[{"key":"dedicated","value":"db","effect":"NoSchedule"}]}],"next_cursor":null}`

func TestKubernetesCreateNetworkFlags(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /kubernetes-clusters", 202, k8sAccepted)
	r := run(t, srv, "", "k8s", "create", "--name", "prod", "--zone", "af-abj-1", "--version", "k8sv_1", "--node-plan", "s-2vcpu-4gb", "--workers", "2",
		"--cni", "calico", "--pod-cidr", "10.60.0.0/16", "--service-cidr", "10.61.0.0/16", "--yes", "--no-wait")
	if r.err != nil {
		t.Fatal(r.err)
	}
	got := f.sentJSON("POST", "/kubernetes-clusters")
	for k, want := range map[string]any{"cni_plugin": "calico", "pod_cidr": "10.60.0.0/16", "service_cidr": "10.61.0.0/16"} {
		if got[k] != want {
			t.Fatalf("%s = %v, want %v (body %v)", k, got[k], want, got)
		}
	}
	if r := run(t, srv, "", "k8s", "create", "--name", "prod", "--zone", "af-abj-1", "--version", "k8sv_1", "--node-plan", "p", "--workers", "2", "--cni", "weave", "--yes"); !IsUsage(r.err) {
		t.Fatalf("err = %v, want a usage error", r.err)
	}
}

func TestKubernetesClusterWrites(t *testing.T) {
	for _, c := range []struct {
		name, path string
		args       []string
		want       map[string]any
	}{
		{"resize", "/kubernetes-clusters/k8s_1/resize-plan", []string{"resize", "k8s_1", "--node-plan", "s-4vcpu-8gb", "--yes"}, map[string]any{"node_plan": "s-4vcpu-8gb"}},
		{"network", "/kubernetes-clusters/k8s_1/upgrade-network", []string{"network", "k8s_1", "--cni", "calico", "--yes"}, map[string]any{"cni_plugin": "calico"}},
		{"endpoint access", "/kubernetes-clusters/k8s_1/endpoint-access", []string{"endpoint-access", "k8s_1", "dual", "--api-allow", "203.0.113.0/24"},
			map[string]any{"endpoint_access": "dual", "api_allowed_cidrs": []any{"203.0.113.0/24"}}},
		{"addon on", "/kubernetes-clusters/k8s_1/addons/metrics-server/toggle", []string{"addons", "enable", "k8s_1", "metrics-server"}, map[string]any{"enabled": true}},
		{"addon off", "/kubernetes-clusters/k8s_1/addons/csi-driver/toggle", []string{"addons", "disable", "k8s_1", "csi-driver", "--yes"}, map[string]any{"enabled": false}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newFakeAPI(t)
			f.on("POST "+c.path, 202, k8sAccepted).on("GET /operations/op_1", 200, opDone)
			r := run(t, srv, "", append([]string{"kubernetes"}, c.args...)...)
			if r.err != nil {
				t.Fatal(r.err)
			}
			if got := f.sentJSON("POST", c.path); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("body = %#v\nwant   %#v", got, c.want)
			}
		})
	}
}

func TestKubernetesClusterWritesChecked(t *testing.T) {
	_, srv := newFakeAPI(t)
	for _, args := range [][]string{
		{"network", "k8s_1", "--yes"},
		{"network", "k8s_1", "--cni", "weave", "--yes"},
		{"endpoint-access", "k8s_1", "internal"},
		{"addons", "enable", "k8s_1", "dashboard"},
	} {
		if r := run(t, srv, "", append([]string{"kubernetes"}, args...)...); !IsUsage(r.err) {
			t.Fatalf("%v: err = %v, want a usage error", args, r.err)
		}
	}
}

func TestNodePoolsList(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /kubernetes-clusters/k8s_1/node-pools", 200, k8sPoolsBody)
	r := run(t, srv, "", "k8s", "node-pools", "list", "k8s_1")
	if r.err != nil {
		t.Fatal(r.err)
	}
	for _, want := range []string{"db", "2 (autoscale 1–4)", "role=db", "dedicated=db:NoSchedule", "k8spool_1"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}
}

func TestNodePoolsCreate(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /kubernetes-clusters/k8s_1/node-pools", 202, `{"operation_id":"op_1","resource_id":"k8spool_1","status":"submitting"}`)
	r := run(t, srv, "", "k8s", "node-pools", "create", "k8s_1", "--name", "db", "--node-plan", "s-4vcpu-8gb", "--autoscale", "1:4",
		"--label", "role=db", "--taint", "dedicated=db:NoSchedule", "--yes", "--no-wait")
	if r.err != nil || r.stdout != "k8spool_1\n" {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	want := map[string]any{"name": "db", "plan_id": "s-4vcpu-8gb", "workers": float64(1), "autoscaling_enabled": true, "min_workers": float64(1), "max_workers": float64(4),
		"labels": map[string]any{"role": "db"}, "taints": []any{map[string]any{"key": "dedicated", "value": "db", "effect": "NoSchedule"}}}
	if got := f.sentJSON("POST", "/kubernetes-clusters/k8s_1/node-pools"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %#v\nwant   %#v", got, want)
	}
	for _, args := range [][]string{
		{"--name", "db", "--node-plan", "p"},                                     // no size
		{"--name", "db", "--node-plan", "p", "--workers", "1", "--taint", "x=y"}, // no effect
		{"--name", "db", "--node-plan", "p", "--workers", "1", "--label", "nokey"},
	} {
		if r := run(t, srv, "", append([]string{"k8s", "node-pools", "create", "k8s_1", "--yes"}, args...)...); !IsUsage(r.err) {
			t.Fatalf("%v: err = %v, want a usage error", args, r.err)
		}
	}
}

// The API takes a pool's whole size, so a scale keeps what it was not asked
// to change.
func TestNodePoolsScaleKeepsTheRest(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want map[string]any
	}{
		{"new range", []string{"--autoscale", "2:6"}, map[string]any{"workers": float64(2), "autoscaling_enabled": true, "min_workers": float64(2), "max_workers": float64(6)}},
		{"fixed", []string{"--no-autoscale", "--workers", "3"}, map[string]any{"workers": float64(3), "autoscaling_enabled": false, "min_workers": float64(0), "max_workers": float64(0)}},
		{"count only", []string{"--workers", "3"}, map[string]any{"workers": float64(3), "autoscaling_enabled": true, "min_workers": float64(1), "max_workers": float64(4)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newFakeAPI(t)
			f.on("GET /kubernetes-clusters/k8s_1/node-pools", 200, k8sPoolsBody).
				on("PATCH /kubernetes-clusters/k8s_1/node-pools/k8spool_1", 202, k8sAccepted).
				on("GET /operations/op_1", 200, opDone)
			r := run(t, srv, "", append([]string{"k8s", "node-pools", "scale", "k8s_1", "db", "--yes"}, c.args...)...)
			if r.err != nil {
				t.Fatal(r.err)
			}
			if got := f.sentJSON("PATCH", "/kubernetes-clusters/k8s_1/node-pools/k8spool_1"); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("body = %#v\nwant   %#v", got, c.want)
			}
		})
	}
}

func TestNodePoolsDelete(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /kubernetes-clusters/k8s_1/node-pools", 200, k8sPoolsBody).
		on("DELETE /kubernetes-clusters/k8s_1/node-pools/k8spool_1", 202, k8sAccepted).
		on("GET /operations/op_1", 200, opDone)
	if r := run(t, srv, "", "k8s", "node-pools", "delete", "k8s_1", "db"); r.err == nil || !strings.Contains(r.err.Error(), "--yes") {
		t.Fatalf("err = %v, want a confirmation", r.err)
	}
	if r := run(t, srv, "", "k8s", "node-pools", "delete", "k8s_1", "db", "--yes"); r.err != nil {
		t.Fatal(r.err)
	}
	if len(f.sent("DELETE", "/kubernetes-clusters/k8s_1/node-pools/k8spool_1")) != 1 {
		t.Fatal("no delete was sent")
	}
}
