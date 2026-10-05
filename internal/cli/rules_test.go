package cli

import (
	"reflect"
	"strings"
	"testing"
)

const sgWebBody = `{"id":"sg_web","name":"web","rules":[` +
	`{"direction":"ingress","protocol":"tcp","port_range":"443","cidr":"0.0.0.0/0"},` +
	`{"direction":"ingress","protocol":"tcp","port_range":"22","cidr":"0.0.0.0/0"}]}`

func rule(direction, protocol, ports, cidr string) map[string]any {
	return map[string]any{"direction": direction, "protocol": protocol, "port_range": ports, "cidr": cidr}
}

func TestSecurityGroupRulesAddKeepsTheOthers(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /security-groups/sg_web", 200, sgWebBody).
		on("PUT /security-groups/sg_web/rules", 202, `{"operation_id":"op_1","resource_id":"sg_web","status":"submitting"}`).
		on("GET /operations/op_1", 200, opSucceeded)
	r := run(t, srv, "", "security-groups", "rules", "add", "sg_web", "--rule", "ingress:tcp:22:203.0.113.4/32", "--rule", "ingress:tcp:443:0.0.0.0/0")
	if r.err != nil {
		t.Fatal(r.err)
	}
	want := []any{rule("ingress", "tcp", "443", "0.0.0.0/0"), rule("ingress", "tcp", "22", "0.0.0.0/0"), rule("ingress", "tcp", "22", "203.0.113.4/32")}
	if got := f.sentJSON("PUT", "/security-groups/sg_web/rules")["rules"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("rules = %v\nwant    %v", got, want)
	}
}

func TestSecurityGroupRulesAddNothingNew(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /security-groups/sg_web", 200, sgWebBody)
	if r := run(t, srv, "", "security-groups", "rules", "add", "sg_web", "--rule", "ingress:tcp:443:0.0.0.0/0"); r.err != nil {
		t.Fatal(r.err)
	}
	if len(f.sent("PUT", "/security-groups/sg_web/rules")) != 0 {
		t.Fatal("wrote rules that were already there")
	}
}

func TestSecurityGroupRulesRemoveKeepsTheOthers(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /security-groups/sg_web", 200, sgWebBody).
		on("PUT /security-groups/sg_web/rules", 202, `{"operation_id":"op_1","resource_id":"sg_web","status":"submitting"}`).
		on("GET /operations/op_1", 200, opSucceeded)
	r := run(t, srv, "", "security-groups", "rules", "remove", "sg_web", "--rule", "ingress:tcp:22:0.0.0.0/0", "--yes")
	if r.err != nil {
		t.Fatal(r.err)
	}
	want := []any{rule("ingress", "tcp", "443", "0.0.0.0/0")}
	if got := f.sentJSON("PUT", "/security-groups/sg_web/rules")["rules"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("rules = %v, want %v", got, want)
	}
}

func TestSecurityGroupRulesRemoveTheLastSendsAnEmptyList(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /security-groups/sg_1", 200, `{"id":"sg_1","name":"one","rules":[{"direction":"ingress","protocol":"icmp","port_range":"","cidr":"0.0.0.0/0"}]}`).
		on("PUT /security-groups/sg_1/rules", 202, `{"operation_id":"op_1","resource_id":"sg_1","status":"submitting"}`).
		on("GET /operations/op_1", 200, opSucceeded)
	if r := run(t, srv, "", "security-groups", "rules", "remove", "sg_1", "--rule", "ingress:icmp::0.0.0.0/0", "--yes"); r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.sentJSON("PUT", "/security-groups/sg_1/rules")["rules"]; !reflect.DeepEqual(got, []any{}) {
		t.Fatalf("rules = %#v, want []", got)
	}
}

func TestSecurityGroupRulesRemoveUnknownRule(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /security-groups/sg_web", 200, sgWebBody)
	r := run(t, srv, "", "security-groups", "rules", "remove", "sg_web", "--rule", "ingress:tcp:80:0.0.0.0/0", "--yes")
	if r.err == nil || !strings.Contains(r.err.Error(), "has no rule ingress tcp 80 from 0.0.0.0/0") {
		t.Fatalf("err = %v", r.err)
	}
	if len(f.sent("PUT", "/security-groups/sg_web/rules")) != 0 {
		t.Fatal("wrote rules after a mistake")
	}
}

func TestDBAccessRulesAddAndRemove(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want []any
	}{
		{"add keeps the others", []string{"add", "db_1", "10.250.0.9/32"}, []any{map[string]any{"cidr": "203.0.113.4/32"}, map[string]any{"cidr": "10.250.0.9/32"}}},
		{"add compares ranges as the API stores them", []string{"add", "db_1", "203.0.113.4/32", "10.0.1.7/24"}, []any{map[string]any{"cidr": "203.0.113.4/32"}, map[string]any{"cidr": "10.0.1.0/24"}}},
		{"remove the last", []string{"remove", "db_1", "203.0.113.4/32", "--yes"}, []any{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newFakeAPI(t)
			f.on("GET /databases/db_1", 200, dbBody).
				on("PUT /databases/db_1/access-rules", 202, `{"operation_id":"op_5","resource_id":"db_1","status":"submitting"}`).
				on("GET /operations/op_5", 200, opSucceeded)
			r := run(t, srv, "", append([]string{"db", "access-rules"}, c.args...)...)
			if r.err != nil {
				t.Fatal(r.err)
			}
			if got := f.sentJSON("PUT", "/databases/db_1/access-rules")["rules"]; !reflect.DeepEqual(got, c.want) {
				t.Fatalf("rules = %v, want %v", got, c.want)
			}
		})
	}
}

func TestDBAccessRulesRemoveUnknown(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /databases/db_1", 200, dbBody)
	r := run(t, srv, "", "db", "access-rules", "remove", "db_1", "198.51.100.0/24", "--yes")
	if r.err == nil || !strings.Contains(r.err.Error(), "no access rule for 198.51.100.0/24") {
		t.Fatalf("err = %v", r.err)
	}
	if len(f.sent("PUT", "/databases/db_1/access-rules")) != 0 {
		t.Fatal("wrote the list after a mistake")
	}
}

func TestResolveSubnetByName(t *testing.T) {
	nets := `{"data":[{"id":"net_1","name":"prod"},{"id":"net_2","name":"staging"}],"next_cursor":null}`
	for _, c := range []struct {
		name, ref, want, wantErr string
		net2                     string
	}{
		{name: "in another network", ref: "web", want: "snet_2", net2: `{"data":[{"id":"snet_2","name":"web"}],"next_cursor":null}`},
		{name: "an id is taken as it is", ref: "snet_9", want: "snet_9"},
		{name: "none", ref: "nope", wantErr: `no subnet named "nope"`, net2: `{"data":[],"next_cursor":null}`},
		{name: "two", ref: "db", wantErr: "2 subnets are named", net2: `{"data":[{"id":"snet_3","name":"db"}],"next_cursor":null}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newFakeAPI(t)
			f.on("GET /networks", 200, nets).
				on("GET /networks/net_1/subnets", 200, `{"data":[{"id":"snet_1","name":"db"}],"next_cursor":null}`).
				on("GET /networks/net_2/subnets", 200, c.net2)
			a, cmd := testApp(t, srv)
			got, err := resolveSubnet(cmd, mustClient(t, a), c.ref)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("got %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

func TestVMCreateTakesASubnetByName(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /networks", 200, `{"data":[{"id":"net_1","name":"prod"}],"next_cursor":null}`).
		on("GET /networks/net_1/subnets", 200, `{"data":[{"id":"snet_1","name":"web"}],"next_cursor":null}`).
		on("GET /plans", 200, plansBody).
		on("POST /instances", 202, orderAccepted)
	r := run(t, srv, "", "vm", "create", "--name", "web-1", "--plan", "starter", "--image", "ubuntu-24-04", "--subnet", "web", "--no-wait", "--yes")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.sentJSON("POST", "/instances")["subnet_id"]; got != "snet_1" {
		t.Fatalf("subnet_id = %v", got)
	}
}
