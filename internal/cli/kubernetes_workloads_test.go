package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestKubernetesWorkloadsHidesSystemNamespaces(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /kubernetes-clusters/k8s_1/workloads", 200, `{"cluster_id":"k8s_1","public_ip":"203.0.113.10","api_port":6443,`+
		`"services":[{"name":"web","namespace":"default","type":"NodePort","cluster_ip":"10.43.0.80","external_ip":null,"ports":[{"name":"http","protocol":"TCP","port":80,"target_port":8080,"node_port":30088}],"public_port":8088,"external_url":"http://203.0.113.10:8088","created_at":null},`+
		`{"name":"kube-dns","namespace":"kube-system","type":"ClusterIP","cluster_ip":"10.43.0.10","external_ip":null,"ports":[],"public_port":null,"external_url":null,"created_at":null}],`+
		`"pods":[{"name":"web-7d9c","namespace":"default","status":"Running","ready":"1/1","restarts":0,"node_name":"k8s-a-worker-01","pod_ip":"10.42.1.5","age":"3h","created_at":null}]}`)
	r := run(t, srv, "", "k8s", "workloads", "k8s_1")
	if r.err != nil {
		t.Fatal(r.err)
	}
	for _, want := range []string{"web", "80:30088/TCP", "http://203.0.113.10:8088", "web-7d9c", "1/1", "3h"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "kube-dns") {
		t.Fatalf("a system service is shown without -A:\n%s", r.stdout)
	}
	if r := run(t, srv, "", "k8s", "workloads", "k8s_1", "-A"); !strings.Contains(r.stdout, "kube-dns") {
		t.Fatalf("-A hides kube-dns:\n%s", r.stdout)
	}
}

func TestKubernetesApplySendsTheFile(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /kubernetes-clusters/k8s_1/apply-manifest", 200, `{"applied":true,"cluster_id":"k8s_1","output":"deployment.apps/web created","resources":[]}`)
	file := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(file, []byte("apiVersion: apps/v1\nkind: Deployment\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := run(t, srv, "", "k8s", "apply", "k8s_1", "-f", file, "-n", "shop")
	if r.err != nil || r.stdout != "deployment.apps/web created\n" {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	want := map[string]any{"manifest": "apiVersion: apps/v1\nkind: Deployment\n", "namespace": "shop"}
	if got := f.sentJSON("POST", "/kubernetes-clusters/k8s_1/apply-manifest"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %#v", got)
	}
	if r := run(t, srv, "", "k8s", "apply", "k8s_1"); !IsUsage(r.err) {
		t.Fatalf("no -f: %v", r.err)
	}
}

func TestKubernetesExposeAndSyncRouting(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /kubernetes-clusters/k8s_1/expose-service", 200, `{"success":true,"cluster_id":"k8s_1","service_name":"web","namespace":"default","public_ip":"203.0.113.10","public_port":80,"node_port":30088,"protocol":"TCP","external_url":"http://203.0.113.10:80","status":"active"}`).
		on("POST /kubernetes-clusters/k8s_1/sync-routing", 200, `{"success":true,"cluster_id":"k8s_1","public_ip":"203.0.113.10","api_port":6443,"synced_ports":[{"purpose":"kubernetes-api","public_port":6443,"private_port":6443,"protocol":"tcp","target":"control node","status":"active"}],"message":"1 route(s)"}`)
	r := run(t, srv, "", "k8s", "expose", "k8s_1", "web", "--port", "80")
	if r.err != nil || !strings.Contains(r.stderr+r.stdout, "http://203.0.113.10:80") {
		t.Fatalf("expose: %v\n%s%s", r.err, r.stdout, r.stderr)
	}
	if got := f.sentJSON("POST", "/kubernetes-clusters/k8s_1/expose-service"); !reflect.DeepEqual(got, map[string]any{"service_name": "web", "public_port": float64(80), "protocol": "TCP"}) {
		t.Fatalf("expose body = %#v", got)
	}
	for _, args := range [][]string{{"--port", "0"}, {"--port", "80", "--node-port", "80"}, {"--port", "80", "--protocol", "sctp"}} {
		if r := run(t, srv, "", append([]string{"k8s", "expose", "k8s_1", "web"}, args...)...); !IsUsage(r.err) {
			t.Fatalf("%v: %v", args, r.err)
		}
	}
	r = run(t, srv, "", "k8s", "sync-routing", "k8s_1")
	if r.err != nil || !strings.Contains(r.stdout, "203.0.113.10:6443") || !strings.Contains(r.stdout, "kubernetes-api") {
		t.Fatalf("sync: %v\n%s", r.err, r.stdout)
	}
}
