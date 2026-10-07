package cli

import (
	"reflect"
	"strings"
	"testing"
)

const lbBody = `{"id":"lb_1","name":"web","public_ip_id":"pip_9","public_ip_address":"102.211.122.99","network_id":"net_1","subnet_id":"snet_1","protocol":"tcp","algorithm":"roundrobin","public_port":443,"private_port":8443,"cidr_list":["203.0.113.0/24"],` +
	`"members":[{"instance_id":"vm_1","instance_name":"web-1","desired_state":"present","observed_state":"active"},{"instance_id":"vm_2","instance_name":"web-2","desired_state":"deleted","observed_state":"deleting"}],` +
	`"desired_state":"present","observed_state":"active","in_sync":false}`

func TestLoadBalancersList(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /networks", 200, `{"data":[{"id":"net_1","name":"prod"}],"next_cursor":null}`).
		on("GET /load-balancers", 200, `{"data":[`+lbBody+`],"next_cursor":null}`)
	r := run(t, srv, "", "lb", "list", "--network", "prod")
	if r.err != nil {
		t.Fatal(r.err)
	}
	for _, want := range []string{"web", "102.211.122.99:443 → 8443", "roundrobin", "applying", "lb_1"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}
	if q := f.sent("GET", "/load-balancers")[0].Query; q != "network_id=net_1" {
		t.Fatalf("query %q", q)
	}
}

func TestLoadBalancersGetByName(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /load-balancers", 200, `{"data":[`+lbBody+`],"next_cursor":null}`).
		on("GET /load-balancers/lb_1", 200, lbBody)
	r := run(t, srv, "", "load-balancers", "get", "web")
	if r.err != nil {
		t.Fatal(r.err)
	}
	for _, want := range []string{"203.0.113.0/24", "web-1", "web-2", "being removed", "snet_1"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}
}

func TestLoadBalancersCreate(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /public-ips", 200, `{"data":[{"id":"pip_9","network_id":"net_1","purpose":"load_balancer","address":"102.211.122.99"}],"next_cursor":null}`).
		on("GET /instances", 200, `{"data":[{"id":"vm_1","name":"web-1"},{"id":"vm_2","name":"web-2"}],"next_cursor":null}`).
		on("POST /load-balancers", 202, `{"operation_id":"op_1","resource_id":"lb_1","status":"submitting"}`).
		on("GET /operations/op_1", 200, `{"id":"op_1","status":"succeeded"}`)
	r := run(t, srv, "", "load-balancers", "create", "--name", "web", "--public-ip", "102.211.122.99", "--subnet", "snet_1",
		"--port", "443", "--private-port", "8443", "--algorithm", "leastconn", "--allow", "203.0.113.0/24,198.51.100.7/32", "--target", "vm_1", "--target", "vm_2")
	if r.err != nil || r.stdout != "lb_1\n" {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	want := map[string]any{"name": "web", "public_ip_id": "pip_9", "subnet_id": "snet_1", "public_port": float64(443), "private_port": float64(8443),
		"algorithm": "leastconn", "cidr_list": []any{"203.0.113.0/24", "198.51.100.7/32"}, "instance_ids": []any{"vm_1", "vm_2"}}
	if got := f.sentJSON("POST", "/load-balancers"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %v\nwant   %v", got, want)
	}
}

func TestLoadBalancersCreateMinimalAndChecked(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /load-balancers", 202, `{"operation_id":"op_1","resource_id":"lb_1","status":"submitting"}`)
	r := run(t, srv, "", "lb", "create", "--name", "web", "--public-ip", "pip_9", "--subnet", "snet_1", "--port", "80", "--no-wait")
	if r.err != nil {
		t.Fatal(r.err)
	}
	want := map[string]any{"name": "web", "public_ip_id": "pip_9", "subnet_id": "snet_1", "public_port": float64(80)}
	if got := f.sentJSON("POST", "/load-balancers"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %v", got)
	}
	if r := run(t, srv, "", "lb", "create", "--name", "web", "--public-ip", "pip_9", "--subnet", "snet_1"); r.err == nil || !strings.Contains(r.err.Error(), `"port"`) {
		t.Fatalf("err = %v, want --port required", r.err)
	}
	if r := run(t, srv, "", "lb", "create", "--name", "web", "--public-ip", "pip_9", "--subnet", "snet_1", "--port", "80", "--algorithm", "random"); !IsUsage(r.err) {
		t.Fatalf("err = %v, want a usage error", r.err)
	}
	if n := len(f.sent("POST", "/load-balancers")); n != 1 {
		t.Fatalf("%d creates, want only the valid one", n)
	}
}

func TestLoadBalancersUpdate(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want map[string]any
	}{
		{"rename and algorithm", []string{"--name", "web2", "--algorithm", "source"}, map[string]any{"name": "web2", "algorithm": "source"}},
		{"set targets", []string{"--targets", "web-1,vm_3"}, map[string]any{"instance_ids": []any{"vm_1", "vm_3"}}},
		{"no targets", []string{"--no-targets", "--yes"}, map[string]any{"instance_ids": []any{}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newFakeAPI(t)
			f.on("GET /instances", 200, `{"data":[{"id":"vm_1","name":"web-1"}],"next_cursor":null}`).
				on("PATCH /load-balancers/lb_1", 202, `{"operation_id":"op_2","resource_id":"lb_1","status":"submitting"}`).
				on("GET /operations/op_2", 200, `{"id":"op_2","status":"succeeded"}`)
			r := run(t, srv, "", append([]string{"lb", "update", "lb_1"}, c.args...)...)
			if r.err != nil {
				t.Fatal(r.err)
			}
			if got := f.sentJSON("PATCH", "/load-balancers/lb_1"); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("body = %#v\nwant   %#v", got, c.want)
			}
		})
	}
}

func TestLoadBalancersUpdateNeedsAChange(t *testing.T) {
	_, srv := newFakeAPI(t)
	for _, args := range [][]string{{}, {"--targets", "vm_1", "--no-targets"}} {
		if r := run(t, srv, "", append([]string{"lb", "update", "lb_1"}, args...)...); !IsUsage(r.err) {
			t.Fatalf("%v: err = %v, want a usage error", args, r.err)
		}
	}
}

func TestLoadBalancersDelete(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("DELETE /load-balancers/lb_1", 202, `{"operation_id":"op_3","resource_id":"lb_1","status":"submitting"}`).
		on("GET /operations/op_3", 200, `{"id":"op_3","status":"succeeded"}`)
	if r := run(t, srv, "", "lb", "delete", "lb_1"); r.err == nil || !strings.Contains(r.err.Error(), "--yes") {
		t.Fatalf("err = %v", r.err)
	}
	if r := run(t, srv, "", "lb", "delete", "lb_1", "--yes"); r.err != nil {
		t.Fatal(r.err)
	}
	if len(f.sent("GET", "/operations/op_3")) == 0 {
		t.Fatal("the delete was not followed")
	}
}
