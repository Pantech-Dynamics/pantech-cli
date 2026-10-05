package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/config"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
	"github.com/Pantech-Dynamics/pantech-cli/internal/update"
)

const (
	plansBody     = `{"data":[{"slug":"starter","price":{"currency":"NGN","monthly_estimate_minor":1500000}}],"next_cursor":null}`
	orderAccepted = `{"order_id":"ord_1","instance_id":"vm_1","status":"awaiting_payment","operation_id":null,"amount_minor":1500000,"currency":"NGN"}`
)

func TestVMCreate(t *testing.T) {
	for _, c := range []struct {
		name          string
		args          []string
		wantPlacement string
		wantBody      map[string]any
		wantStdout    []string
		wantErr       string
	}{
		{
			name:          "standard, with a security group by name and tags",
			args:          []string{"--security-group", "web", "--tags", "env=prod,team=web"},
			wantPlacement: "placement=standard",
			wantBody:      map[string]any{"name": "web-1", "plan_slug": "starter", "image_slug": "ubuntu-24-04", "security_group_id": "sg_web", "tags": map[string]any{"env": "prod", "team": "web"}},
			wantStdout:    []string{"order     ord_1", "instance  vm_1"},
		},
		{
			name:          "in a VPC subnet: priced without a public IP",
			args:          []string{"--subnet", "snet_1"},
			wantPlacement: "placement=vpc",
			wantBody:      map[string]any{"name": "web-1", "plan_slug": "starter", "image_slug": "ubuntu-24-04", "subnet_id": "snet_1"},
			wantStdout:    []string{"order     ord_1", "instance  vm_1"},
		},
		{
			name:    "a security group in a subnet is refused before ordering",
			args:    []string{"--subnet", "snet_1", "--security-group", "web"},
			wantErr: "--security-group is for a standard VM",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newFakeAPI(t)
			f.on("GET /plans", 200, plansBody).
				on("GET /security-groups", 200, `{"data":[{"id":"sg_web","name":"web"},{"id":"sg_webby","name":"webby"}],"next_cursor":null}`).
				on("POST /instances", 202, orderAccepted)
			args := append([]string{"vm", "create", "--name", "web-1", "--plan", "starter", "--image", "ubuntu-24-04", "--yes", "--no-wait"}, c.args...)
			r := run(t, srv, "", args...)
			if c.wantErr != "" {
				if r.err == nil || !strings.Contains(r.err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", r.err, c.wantErr)
				}
				if len(f.sent("POST", "/instances")) != 0 {
					t.Fatal("ordered anyway")
				}
				return
			}
			if r.err != nil {
				t.Fatal(r.err)
			}
			if plans := f.sent("GET", "/plans"); len(plans) != 1 || plans[0].Query != c.wantPlacement {
				t.Fatalf("plans asked with %+v, want %s", plans, c.wantPlacement)
			}
			if got := f.sentJSON("POST", "/instances"); !reflect.DeepEqual(got, c.wantBody) {
				t.Fatalf("body = %v\nwant   %v", got, c.wantBody)
			}
			for _, want := range c.wantStdout {
				if !strings.Contains(r.stdout, want) {
					t.Fatalf("stdout %q lacks %q", r.stdout, want)
				}
			}
		})
	}
}

func TestVMCreateNoWaitQuietPrintsTheInstanceID(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /plans", 200, plansBody).on("POST /instances", 202, orderAccepted)
	r := run(t, srv, "", "vm", "create", "--name", "web-1", "--plan", "starter", "--image", "ubuntu-24-04", "--yes", "--no-wait", "-q")
	if r.err != nil || r.stdout != "vm_1\n" {
		t.Fatalf("stdout %q, err %v", r.stdout, r.err)
	}
}

func TestVMCreateFailedOrderIsTyped(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /plans", 200, plansBody).
		on("POST /instances", 202, orderAccepted).
		on("GET /instance-orders/ord_1", 200, `{"id":"ord_1","instance_id":"vm_1","status":"awaiting_payment"}`).
		on("GET /instance-orders/ord_1", 200, `{"id":"ord_1","instance_id":"vm_1","status":"payment_failed","failure_code":"card_declined"}`)
	r := run(t, srv, "", "vm", "create", "--name", "web-1", "--plan", "starter", "--image", "ubuntu-24-04", "--yes")
	var failed *api.OrderFailed
	if !errors.As(r.err, &failed) || !strings.Contains(r.err.Error(), "card_declined") {
		t.Fatalf("err = %v, want an *api.OrderFailed naming card_declined", r.err)
	}
}

func TestVMOrders(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /instance-orders", 200, `{"data":[{"id":"ord_1","instance_id":"vm_1","status":"provisioned","amount_minor":1500000,"currency":"NGN"}],"next_cursor":"c2"}`).
		on("GET /instance-orders", 200, `{"data":[{"id":"ord_2","instance_id":"vm_2","status":"failed","failure_code":"provisioning_handoff_failed","amount_minor":1500000,"currency":"NGN"}],"next_cursor":null}`)
	r := run(t, srv, "", "vm", "orders", "list", "-q")
	if r.err != nil || r.stdout != "ord_1\nord_2\n" {
		t.Fatalf("stdout %q, err %v: every page must be read", r.stdout, r.err)
	}
	if calls := f.sent("GET", "/instance-orders"); calls[1].Query != "cursor=c2" {
		t.Fatalf("second page asked with %q", calls[1].Query)
	}
}

func TestOpenSSHAccess(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /instances/vm_1/ssh-access", 202, `{"operation_id":"op_1","resource_id":"sshg_1","status":"submitting"}`).
		on("GET /operations/op_1", 200, `{"id":"op_1","status":"submitted"}`).
		on("GET /operations/op_1", 200, `{"id":"op_1","status":"succeeded"}`).
		on("GET /instances/vm_1/ssh-access/sshg_1", 200, `{"id":"sshg_1","instance_id":"vm_1","status":"active","host":"203.0.113.76","port":2222,"expires_at":"2026-10-05T10:15:00Z"}`).
		on("DELETE /instances/vm_1/ssh-access/sshg_1", 202, `{"operation_id":"op_2","resource_id":"sshg_1","status":"submitting"}`).
		on("GET /operations/op_2", 200, `{"id":"op_2","status":"succeeded"}`)
	a, cmd := testApp(t, srv)
	grant, err := a.openSSHAccess(cmd, mustClient(t, a), &api.Instance{ID: "vm_1", Name: "web-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := sshArgv("ubuntu", grant, []string{"-A"}); !reflect.DeepEqual(got, []string{"ssh", "-p", "2222", "ubuntu@203.0.113.76", "-A"}) {
		t.Fatalf("argv = %v", got)
	}
	if err := a.revokeSSHAccess(cmd, mustClient(t, a), "vm_1", grant.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.sent("DELETE", "/instances/vm_1/ssh-access/sshg_1")) != 1 {
		t.Fatal("the grant was not revoked")
	}
}

func TestOpenSSHAccessFallsBackToTheVMAddress(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /instances/vm_1/ssh-access", 202, `{"operation_id":"op_1","resource_id":"sshg_1","status":"submitting"}`).
		on("GET /operations/op_1", 200, `{"id":"op_1","status":"succeeded"}`).
		on("GET /instances/vm_1/ssh-access/sshg_1", 200, `{"id":"sshg_1","instance_id":"vm_1","status":"active","host":null,"port":null}`)
	a, cmd := testApp(t, srv)
	addr := "203.0.113.76"
	grant, err := a.openSSHAccess(cmd, mustClient(t, a), &api.Instance{ID: "vm_1", Name: "web-1", PrivateIPv4: &addr})
	if err != nil {
		t.Fatal(err)
	}
	if got := sshArgv("root", grant, nil); !reflect.DeepEqual(got, []string{"ssh", "root@203.0.113.76"}) {
		t.Fatalf("argv = %v", got)
	}
}

func TestOpenSSHAccessFailedOperation(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /instances/vm_1/ssh-access", 202, `{"operation_id":"op_1","resource_id":"sshg_1","status":"submitting"}`).
		on("GET /operations/op_1", 200, `{"id":"op_1","kind":"open_ssh_access","status":"failed","failure":{"code":"X","reason":"no"}}`)
	a, cmd := testApp(t, srv)
	_, err := a.openSSHAccess(cmd, mustClient(t, a), &api.Instance{ID: "vm_1", Name: "web-1"})
	var failed *api.OperationFailed
	if !errors.As(err, &failed) {
		t.Fatalf("err = %v, want *api.OperationFailed", err)
	}
}

func TestSSHTargetFallsBackWhenAccessIsRefused(t *testing.T) {
	for _, c := range []struct {
		name, status, body string
		code               int
	}{
		{"the VM shares its security group", "POST", `{"status":409,"code":"SECURITY_GROUP_SHARED","detail":"Another VM uses this security group."}`, 409},
		{"the key cannot write", "POST", `{"status":403,"code":"INSUFFICIENT_SCOPE","detail":"This key lacks the write scope."}`, 403},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newFakeAPI(t)
			f.on("POST /instances/vm_1/ssh-access", c.code, c.body)
			a, cmd := testApp(t, srv)
			addr := "203.0.113.76"
			grant, opened, err := a.sshTarget(cmd, mustClient(t, a), &api.Instance{ID: "vm_1", Name: "web-1", PrivateIPv4: &addr})
			if err != nil || opened {
				t.Fatalf("opened %v, err %v: want the VM's address", opened, err)
			}
			if got := sshArgv("ubuntu", grant, nil); !reflect.DeepEqual(got, []string{"ssh", "ubuntu@203.0.113.76"}) {
				t.Fatalf("argv = %v", got)
			}
		})
	}
}

func TestSSHTargetDoesNotFallBack(t *testing.T) {
	addr := "203.0.113.76"
	for _, c := range []struct {
		name string
		vm   api.Instance
		code int
		body string
	}{
		{"the key was refused", api.Instance{ID: "vm_1", Name: "web-1", PrivateIPv4: &addr}, 401, `{"status":401,"code":"UNAUTHORIZED","detail":"Invalid key."}`},
		{"no address to fall back to", api.Instance{ID: "vm_1", Name: "web-1"}, 409, `{"status":409,"code":"SECURITY_GROUP_SHARED","detail":"Shared."}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newFakeAPI(t)
			f.on("POST /instances/vm_1/ssh-access", c.code, c.body)
			a, cmd := testApp(t, srv)
			if _, _, err := a.sshTarget(cmd, mustClient(t, a), &c.vm); err == nil {
				t.Fatal("want the error, not a direct connection")
			}
		})
	}
}

// Ctrl+C during a --revoke session cancels the command's context; the grant
// must be revoked all the same.
func TestRevokeSSHAccessAfterInterrupt(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("DELETE /instances/vm_1/ssh-access/sshg_1", 202, `{"operation_id":"op_2","resource_id":"sshg_1","status":"submitting"}`).
		on("GET /operations/op_2", 200, `{"id":"op_2","status":"succeeded"}`)
	a, cmd := testApp(t, srv)
	interrupted, cancel := context.WithCancel(context.Background())
	cancel()
	cmd.SetContext(interrupted)
	if err := a.revokeSSHAccess(cmd, mustClient(t, a), "vm_1", "sshg_1"); err != nil {
		t.Fatal(err)
	}
	if len(f.sent("DELETE", "/instances/vm_1/ssh-access/sshg_1")) != 1 || len(f.sent("GET", "/operations/op_2")) == 0 {
		t.Fatal("the grant was not revoked and waited for")
	}
}

const (
	dbAccepted = `{"order_id":"ord_9","database_id":"db_1","resource_id":"db_1","status":"awaiting_payment","operation_id":null,"failure_code":null,"amount_minor":1200000,"currency":"NGN","password_returned":%s,"password":%s,"admin_username":"dbadmin"}`
	dbBody     = `{"id":"db_1","name":"app","engine":"postgresql","version":"18","port":5432,"zone_id":"af-abj-1","hostname":"db-1.af-abj-1.db.pantechdynamics.com","admin_username":"dbadmin","desired_state":"running","observed_state":"running","generation":1,"observed_generation":1,"access_rules":[{"cidr":"203.0.113.4/32","protocol":"tcp","port":5432}]}`
)

// dbCreateAPI answers a whole database create: order, payment, operation, read.
func dbCreateAPI(t *testing.T, accepted string) (*fakeAPI, func(stdin string, args ...string) result) {
	f, srv := newFakeAPI(t)
	f.on("GET /plans", 200, plansBody).
		on("POST /databases", 202, accepted).
		on("GET /database-orders/ord_9", 200, `{"id":"ord_9","database_id":"db_1","status":"awaiting_payment"}`).
		on("GET /database-orders/ord_9", 200, `{"id":"ord_9","database_id":"db_1","status":"provisioned","operation_id":"op_9"}`).
		on("GET /operations/op_9", 200, `{"id":"op_9","status":"succeeded"}`).
		on("GET /databases/db_1", 200, dbBody)
	base := []string{"db", "create", "--name", "app", "--engine", "postgresql", "--version", "18", "--plan", "starter", "--zone", "af-abj-1", "--yes"}
	return f, func(stdin string, args ...string) result { return run(t, srv, stdin, append(base, args...)...) }
}

const generated = "Gen3ratedPassw0rdGen3ratedPassw0rd"

func TestDBCreateWithAGeneratedPassword(t *testing.T) {
	f, create := dbCreateAPI(t, strings.Replace(strings.Replace(dbAccepted, "%s", "true", 1), "%s", `"`+generated+`"`, 1))
	r := create("", "--access-rule", "203.0.113.4/32")
	if r.err != nil {
		t.Fatal(r.err)
	}
	body := f.sentJSON("POST", "/databases")
	want := map[string]any{"name": "app", "engine": "postgresql", "version": "18", "plan_slug": "starter", "zone_id": "af-abj-1", "access_rules": []any{"203.0.113.4/32"}}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("body = %v\nwant   %v", body, want)
	}
	if strings.Count(r.stdout, generated) != 1 || !strings.HasPrefix(r.stdout, generated+"\n") {
		t.Fatalf("the password must be on stdout once, alone, first: %q", r.stdout)
	}
	if strings.Contains(r.stderr, generated) {
		t.Fatal("the password must never reach stderr")
	}
	if !strings.Contains(r.stderr, "shown once") {
		t.Fatalf("stderr does not say the password is shown once: %q", r.stderr)
	}
	if plans := f.sent("GET", "/plans"); len(plans) != 1 || plans[0].Query != "placement=vpc" {
		t.Fatalf("a database is priced as a VM with no public address: %+v", plans)
	}
	if len(f.sent("GET", "/operations/op_9")) == 0 {
		t.Fatal("the provisioning operation was not followed")
	}
}

func TestDBCreateJSONCarriesThePasswordOnce(t *testing.T) {
	_, create := dbCreateAPI(t, strings.Replace(strings.Replace(dbAccepted, "%s", "true", 1), "%s", `"`+generated+`"`, 1))
	r := create("", "--json")
	if r.err != nil {
		t.Fatal(r.err)
	}
	var out struct {
		Order    api.DatabaseOrderAccepted `json:"order"`
		Database api.Database              `json:"database"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, r.stdout)
	}
	if out.Order.Password == nil || *out.Order.Password != generated || out.Database.ID != "db_1" {
		t.Fatalf("got %+v", out)
	}
	if strings.Count(r.stdout, generated) != 1 {
		t.Fatal("the password must appear once")
	}
}

func TestDBCreateWithAChosenPassword(t *testing.T) {
	chosen := "My-Own-Passw0rd-1234"
	f, create := dbCreateAPI(t, strings.Replace(strings.Replace(dbAccepted, "%s", "false", 1), "%s", "null", 1))
	r := create(chosen+"\n", "--password-stdin", "--admin-username", "app_admin", "--subnet", "snet_1")
	if r.err != nil {
		t.Fatal(r.err)
	}
	body := f.sentJSON("POST", "/databases")
	if body["password"] != chosen || body["admin_username"] != "app_admin" || body["subnet_id"] != "snet_1" {
		t.Fatalf("body = %v", body)
	}
	if strings.Contains(r.stdout+r.stderr, chosen) {
		t.Fatal("a chosen password must never be printed")
	}
	if strings.Contains(r.stderr, "cannot be shown") {
		t.Fatalf("no warning is due for a chosen password: %q", r.stderr)
	}
}

func TestDBCreateRefusesABadPasswordBeforeOrdering(t *testing.T) {
	for name, pw := range map[string]string{
		"too short":   "short",
		"too long":    strings.Repeat("a", 129),
		"a space":     "has a space in it here",
		"a quote":     `has'aquote0123456789`,
		"a backslash": `has\abackslash012345`,
		"non-ASCII":   "pässwordpässword1234",
	} {
		t.Run(name, func(t *testing.T) {
			f, create := dbCreateAPI(t, "{}")
			r := create(pw+"\n", "--password-stdin")
			if r.err == nil {
				t.Fatal("accepted")
			}
			if strings.Contains(r.err.Error(), pw) {
				t.Fatal("the error repeats the password")
			}
			if len(f.calls) != 0 {
				t.Fatalf("called the API: %+v", f.calls)
			}
		})
	}
}

func TestDBCreateReplayWarns(t *testing.T) {
	_, create := dbCreateAPI(t, strings.Replace(strings.Replace(dbAccepted, "%s", "false", 1), "%s", "null", 1))
	r := create("")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !strings.Contains(r.stderr, "pantech db password reset db_1") {
		t.Fatalf("a replayed create must say how to get a password: %q", r.stderr)
	}
}

func TestDBCreateNoWait(t *testing.T) {
	f, create := dbCreateAPI(t, strings.Replace(strings.Replace(dbAccepted, "%s", "true", 1), "%s", `"`+generated+`"`, 1))
	r := create("", "--no-wait")
	if r.err != nil || r.stdout != generated+"\n" || !strings.Contains(r.stderr, "ord_9") {
		t.Fatalf("stdout %q stderr %q err %v", r.stdout, r.stderr, r.err)
	}
	if len(f.sent("GET", "/database-orders/ord_9")) != 0 {
		t.Fatal("--no-wait waited")
	}
}

func TestDBCreateFailedOrder(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /plans", 200, plansBody).
		on("POST /databases", 202, strings.Replace(strings.Replace(dbAccepted, "%s", "false", 1), "%s", "null", 1)).
		on("GET /database-orders/ord_9", 200, `{"id":"ord_9","database_id":"db_1","status":"failed","failure_code":"database_name_taken"}`)
	r := run(t, srv, "", "db", "create", "--name", "app", "--engine", "postgresql", "--version", "18", "--plan", "starter", "--zone", "af-abj-1", "--yes")
	var failed *api.OrderFailed
	if !errors.As(r.err, &failed) || !strings.Contains(r.err.Error(), "database_name_taken") {
		t.Fatalf("err = %v", r.err)
	}
}

func TestDBPasswordReset(t *testing.T) {
	for _, c := range []struct {
		name, body string
		wantErr    bool
	}{
		{"shown once", `{"operation_id":"op_3","resource_id":"db_1","status":"submitting","password_returned":true,"password":"` + generated + `"}`, false},
		{"replayed: nothing to show", `{"operation_id":"op_3","resource_id":"db_1","status":"submitting","password_returned":false,"password":null}`, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newFakeAPI(t)
			f.on("POST /databases/db_1/reset-password", 202, c.body).on("GET /operations/op_3", 200, `{"id":"op_3","status":"succeeded"}`)
			r := run(t, srv, "", "db", "password", "reset", "db_1", "--yes")
			if c.wantErr {
				if r.err == nil || !strings.Contains(r.err.Error(), "again") {
					t.Fatalf("err = %v", r.err)
				}
				return
			}
			if r.err != nil || r.stdout != generated+"\n" || strings.Contains(r.stderr, generated) {
				t.Fatalf("stdout %q stderr %q err %v", r.stdout, r.stderr, r.err)
			}
		})
	}
}

func TestDBPasswordResetWithNoOperation(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /databases/db_1/reset-password", 202, `{"operation_id":"","resource_id":"db_1","status":"succeeded","password_returned":true,"password":"`+generated+`"}`)
	for _, args := range [][]string{nil, {"--no-wait"}} {
		r := run(t, srv, "", append([]string{"db", "password", "reset", "db_1", "--yes"}, args...)...)
		if r.err != nil || r.stdout != generated+"\n" {
			t.Fatalf("%v: stdout %q err %v", args, r.stdout, r.err)
		}
		if strings.Contains(r.stderr, "operations wait") {
			t.Fatalf("%v: suggests following an operation that has no id: %q", args, r.stderr)
		}
	}
}

func TestDBPasswordSet(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("PUT /databases/db_1/password", 202, `{"operation_id":"op_4","resource_id":"db_1","status":"submitting"}`).on("GET /operations/op_4", 200, `{"id":"op_4","status":"succeeded"}`)
	if r := run(t, srv, "", "db", "password", "set", "db_1"); !IsUsage(r.err) {
		t.Fatalf("without --password-stdin: err = %v, want a usage error", r.err)
	}
	r := run(t, srv, "Another-Passw0rd-123\r\n", "db", "password", "set", "db_1", "--password-stdin")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.sentJSON("PUT", "/databases/db_1/password"); got["password"] != "Another-Passw0rd-123" {
		t.Fatalf("body = %v", got)
	}
}

func TestDBAccessRulesSet(t *testing.T) {
	for _, c := range []struct {
		name  string
		args  []string
		want  any
		usage bool
	}{
		{"two CIDRs", []string{"203.0.113.4/32", "10.0.1.0/24"}, []any{map[string]any{"cidr": "203.0.113.4/32"}, map[string]any{"cidr": "10.0.1.0/24"}}, false},
		{"--none clears", []string{"--none"}, []any{}, false},
		{"nothing given", nil, nil, true},
		{"--none with CIDRs", []string{"--none", "10.0.0.0/8"}, nil, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newFakeAPI(t)
			f.on("GET /databases", 200, `{"data":[{"id":"db_1","name":"app"}],"next_cursor":null}`).
				on("PUT /databases/db_1/access-rules", 202, `{"operation_id":"op_5","resource_id":"db_1","status":"submitting"}`).
				on("GET /operations/op_5", 200, `{"id":"op_5","status":"succeeded"}`)
			r := run(t, srv, "", append([]string{"db", "access-rules", "set", "app", "--yes"}, c.args...)...)
			if c.usage {
				if !IsUsage(r.err) {
					t.Fatalf("err = %v, want a usage error", r.err)
				}
				return
			}
			if r.err != nil {
				t.Fatal(r.err)
			}
			if got := f.sentJSON("PUT", "/databases/db_1/access-rules")["rules"]; !reflect.DeepEqual(got, c.want) {
				t.Fatalf("rules = %#v, want %#v", got, c.want)
			}
			if q := f.sent("GET", "/databases")[0].Query; q != "q=app" {
				t.Fatalf("name looked up with %q", q)
			}
		})
	}
}

func TestDBGetAndList(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /databases/db_1", 200, dbBody).on("GET /databases", 200, `{"data":[`+dbBody+`],"next_cursor":null}`).
		on("GET /database-engines", 200, `{"data":[{"engine":"postgresql","display_name":"PostgreSQL","port":5432,"versions":[{"version":"18","eol_date":"2030-11-14","zones":["af-abj-1","af-abj-2"]}]}],"next_cursor":null}`)
	r := run(t, srv, "", "db", "get", "db_1")
	for _, want := range []string{"app", "db-1.af-abj-1.db.pantechdynamics.com:5432", "dbadmin", "203.0.113.4/32"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("db get lacks %q:\n%s", want, r.stdout)
		}
	}
	if !strings.Contains(r.stderr, `psql "host=db-1.af-abj-1.db.pantechdynamics.com port=5432 user=dbadmin`) {
		t.Fatalf("no connect hint: %s", r.stderr)
	}
	r = run(t, srv, "", "db", "list", "--json")
	var list []map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &list); err != nil || len(list) != 1 || list[0]["observed_generation"] == nil {
		t.Fatalf("--json must print the API's items as sent: %v %s", err, r.stdout)
	}
	r = run(t, srv, "", "db", "engines")
	if r.err != nil || !strings.Contains(r.stdout, "postgresql") || !strings.Contains(r.stdout, "2030-11-14") {
		t.Fatalf("engines: %q %v", r.stdout, r.err)
	}
}

func TestDBDeleteAsksAndFollows(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("DELETE /databases/db_1", 202, `{"operation_id":"op_6","resource_id":"db_1","status":"submitting"}`).on("GET /operations/op_6", 200, `{"id":"op_6","status":"succeeded"}`)
	// No terminal to ask in and no --yes: refused.
	if r := run(t, srv, "", "db", "delete", "db_1"); r.err == nil || !strings.Contains(r.err.Error(), "--yes") {
		t.Fatalf("err = %v", r.err)
	}
	if len(f.sent("DELETE", "/databases/db_1")) != 0 {
		t.Fatal("deleted without asking")
	}
	if r := run(t, srv, "", "db", "delete", "db_1", "--yes"); r.err != nil {
		t.Fatal(r.err)
	}
	if len(f.sent("GET", "/operations/op_6")) == 0 {
		t.Fatal("the delete was not followed")
	}
}

func TestParseRules(t *testing.T) {
	for _, c := range []struct {
		spec    string
		want    api.SecurityGroupRule
		wantErr bool
	}{
		{"ingress:tcp:443:0.0.0.0/0", api.SecurityGroupRule{Direction: "ingress", Protocol: "tcp", PortRange: "443", CIDR: "0.0.0.0/0"}, false},
		{"egress:udp:8000-8100:10.0.0.0/8", api.SecurityGroupRule{Direction: "egress", Protocol: "udp", PortRange: "8000-8100", CIDR: "10.0.0.0/8"}, false},
		{"ingress:icmp::0.0.0.0/0", api.SecurityGroupRule{Direction: "ingress", Protocol: "icmp", CIDR: "0.0.0.0/0"}, false},
		{"ingress:tcp::0.0.0.0/0", api.SecurityGroupRule{}, true},
		{"ingress:icmp:22:0.0.0.0/0", api.SecurityGroupRule{}, true},
		{"sideways:tcp:22:0.0.0.0/0", api.SecurityGroupRule{}, true},
		{"ingress:sctp:22:0.0.0.0/0", api.SecurityGroupRule{}, true},
		{"ingress:tcp:22", api.SecurityGroupRule{}, true},
	} {
		got, err := parseRules([]string{c.spec})
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v", c.spec, err)
			continue
		}
		if !c.wantErr && got[0] != c.want {
			t.Errorf("%s: %+v, want %+v", c.spec, got[0], c.want)
		}
	}
}

func TestSecurityGroupsCreateAndRules(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /security-groups", 202, `{"operation_id":"op_7","resource_id":"sg_new","status":"submitting"}`).
		on("GET /operations/op_7", 200, `{"id":"op_7","status":"succeeded"}`).
		on("PUT /security-groups/sg_new/rules", 202, `{"operation_id":"op_7","resource_id":"sg_new","status":"submitting"}`)
	r := run(t, srv, "", "security-groups", "create", "--name", "web", "--rule", "ingress:tcp:443:0.0.0.0/0", "--rule", "ingress:icmp::0.0.0.0/0")
	if r.err != nil || r.stdout != "sg_new\n" {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	body := f.sentJSON("POST", "/security-groups")
	rules := body["rules"].([]any)
	if len(rules) != 2 || rules[1].(map[string]any)["port_range"] != "" {
		t.Fatalf("rules = %v: port_range is sent even when empty", rules)
	}
	r = run(t, srv, "", "security-groups", "rules", "set", "sg_new", "--none", "--yes")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.sentJSON("PUT", "/security-groups/sg_new/rules")["rules"]; !reflect.DeepEqual(got, []any{}) {
		t.Fatalf("--none sent %#v", got)
	}
}

func TestResourceLists(t *testing.T) {
	for _, c := range []struct {
		args       []string
		route      string
		body       string
		wantStdout []string
	}{
		{[]string{"volumes", "list"}, "GET /volumes", `{"data":[{"id":"vol_1","name":"data","size_gb":5,"disk_offering_slug":"small-5gb","attached_instance_name":"web-1","observed_state":"active"}],"next_cursor":null}`, []string{"data", "5 GB", "small-5gb", "web-1", "vol_1"}},
		{[]string{"snapshots", "list"}, "GET /snapshots", `{"data":[{"id":"snap_1","name":"nightly","volume_id":"vol_1","volume_name":"data","size_bytes":2040109465,"observed_state":"active"}],"next_cursor":null}`, []string{"nightly", "volume data", "1.9 GB", "snap_1"}},
		{[]string{"networks", "list"}, "GET /networks", `{"data":[{"id":"net_1","name":"prod","cidr":"10.0.0.0/16","zone":"af-abj-2","observed_state":"active"}],"next_cursor":null}`, []string{"prod", "10.0.0.0/16", "af-abj-2", "net_1"}},
		{[]string{"public-ips", "list"}, "GET /public-ips", `{"data":[{"id":"pip_1","network_id":"net_1","network_name":"prod","purpose":"static_nat","address":"203.0.113.90","instance_name":"web-1","observed_state":"active"}],"next_cursor":null}`, []string{"203.0.113.90", "static_nat", "prod", "web-1", "pip_1"}},
		{[]string{"security-groups", "list"}, "GET /security-groups", `{"data":[{"id":"sg_1","name":"default","rules":[{"direction":"ingress","protocol":"icmp","port_range":"","cidr":"0.0.0.0/0"}],"observed_state":"active"}],"next_cursor":null}`, []string{"default", "sg_1"}},
		{[]string{"db", "orders", "list"}, "GET /database-orders", `{"data":[{"id":"ord_9","database_id":"db_1","status":"provisioned","amount_minor":1200000,"currency":"NGN"}],"next_cursor":null}`, []string{"ord_9", "db_1", "NGN 12,000.00"}},
	} {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			f, srv := newFakeAPI(t)
			f.on(c.route, 200, c.body)
			r := run(t, srv, "", c.args...)
			if r.err != nil {
				t.Fatal(r.err)
			}
			for _, want := range c.wantStdout {
				if !strings.Contains(r.stdout, want) {
					t.Fatalf("stdout lacks %q:\n%s", want, r.stdout)
				}
			}
		})
	}
}

func TestResourceGetByName(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /volumes", 200, `{"data":[{"id":"vol_1","name":"data"},{"id":"vol_2","name":"data-old"}],"next_cursor":null}`).
		on("GET /volumes/vol_1", 200, `{"id":"vol_1","name":"data","size_gb":5,"disk_offering_slug":"small-5gb","observed_state":"active","desired_state":"present"}`)
	r := run(t, srv, "", "volumes", "get", "data")
	if r.err != nil || !strings.Contains(r.stdout, "vol_1") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	r = run(t, srv, "", "volumes", "get", "nope")
	if r.err == nil || !strings.Contains(r.err.Error(), `no volume named "nope"`) {
		t.Fatalf("err = %v", r.err)
	}
}

func TestSnapshotsCreateNeedsOneSource(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /volumes/vol_1/snapshots", 202, `{"operation_id":"op_8","resource_id":"snap_new","status":"submitting"}`).
		on("GET /operations/op_8", 200, `{"id":"op_8","status":"succeeded"}`)
	for _, args := range [][]string{{}, {"--vm", "vm_1", "--volume", "vol_1"}} {
		if r := run(t, srv, "", append([]string{"snapshots", "create", "--name", "n", "--yes"}, args...)...); !IsUsage(r.err) {
			t.Fatalf("%v: err = %v, want a usage error", args, r.err)
		}
	}
	r := run(t, srv, "", "snapshots", "create", "--name", "n", "--volume", "vol_1", "--yes")
	if r.err != nil || r.stdout != "snap_new\n" {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
}

func TestPublicIPCreate(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /networks", 200, `{"data":[{"id":"net_1","name":"prod"}],"next_cursor":null}`).
		on("GET /instances", 200, `{"data":[{"id":"vm_1","name":"web-1"}],"next_cursor":null}`).
		on("POST /public-ips", 202, `{"operation_id":"op_9","resource_id":"pip_1","status":"submitting"}`)
	r := run(t, srv, "", "public-ips", "create", "--network", "prod", "--vm", "web-1", "--yes", "--no-wait")
	if r.err != nil || r.stdout != "pip_1\n" {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	want := map[string]any{"network_id": "net_1", "purpose": "static_nat", "instance_id": "vm_1"}
	if got := f.sentJSON("POST", "/public-ips"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %v", got)
	}
}

// testApp is an app pointed at srv, and a command carrying a context, for
// calling the helpers commands share.
func testApp(t *testing.T, srv *httptest.Server) (*app, *cobra.Command) {
	t.Helper()
	t.Setenv("PANTECH_API_KEY", "PAN_test")
	poll := api.PollInterval
	api.PollInterval = time.Millisecond
	t.Cleanup(func() { api.PollInterval = poll })
	var out, errOut bytes.Buffer
	a := &app{apiURL: srv.URL, cfg: &config.Config{Profiles: map[string]config.Profile{}}, out: &output.Printer{Out: &out, Err: &errOut}}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return a, cmd
}

func mustClient(t *testing.T, a *app) *api.Client {
	t.Helper()
	c, err := a.client()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDBSecurityGroupsSet(t *testing.T) {
	for _, c := range []struct {
		name  string
		args  []string
		want  any
		usage bool
	}{
		{"by name and id", []string{"office", "sg_vpn"}, []any{"sg_office", "sg_vpn"}, false},
		{"--none detaches", []string{"--none"}, []any{}, false},
		{"nothing given", nil, nil, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newFakeAPI(t)
			f.on("GET /security-groups", 200, `{"data":[{"id":"sg_office","name":"office"}],"next_cursor":null}`).
				on("PUT /databases/db_1/security-groups", 202, `{"operation_id":"op_10","resource_id":"db_1","status":"submitting"}`).
				on("GET /operations/op_10", 200, `{"id":"op_10","status":"succeeded"}`)
			r := run(t, srv, "", append([]string{"db", "security-groups", "set", "db_1"}, c.args...)...)
			if c.usage {
				if !IsUsage(r.err) {
					t.Fatalf("err = %v, want a usage error", r.err)
				}
				return
			}
			if r.err != nil {
				t.Fatal(r.err)
			}
			if got := f.sentJSON("PUT", "/databases/db_1/security-groups")["security_group_ids"]; !reflect.DeepEqual(got, c.want) {
				t.Fatalf("security_group_ids = %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestDBGetShowsWhatSecurityGroupsAdd(t *testing.T) {
	f, srv := newFakeAPI(t)
	body := strings.TrimSuffix(dbBody, "}") + `,"security_group_ids":["sg_office"],` +
		`"effective_access_rules":[{"cidr":"203.0.113.4/32","protocol":"tcp","port":5432,"source":"access_rule"},{"cidr":"198.51.100.0/24","protocol":"tcp","port":5432,"source":"security_group:sg_office"}],` +
		`"ignored_security_group_rules":[{"security_group_id":"sg_office","direction":"ingress","protocol":"tcp","port_range":"22","cidr":"0.0.0.0/0","reason":"port_not_covered"}]}`
	f.on("GET /databases/db_1", 200, body)
	r := run(t, srv, "", "db", "get", "db_1")
	for _, want := range []string{"sg_office", "198.51.100.0/24 (security_group:sg_office)", "tcp 22 from 0.0.0.0/0 (sg_office: port_not_covered)"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("db get lacks %q:\n%s", want, r.stdout)
		}
	}
}

func TestUpgradeCheck(t *testing.T) {
	dl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/latest.txt" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("v0.1.5\n"))
	}))
	t.Cleanup(dl.Close)
	oldURL, oldVersion := update.BaseURL, Version
	update.BaseURL = dl.URL
	t.Cleanup(func() { update.BaseURL, Version = oldURL, oldVersion })
	_, srv := newFakeAPI(t)

	Version = "v0.1.4"
	r := run(t, srv, "", "upgrade", "--check")
	if r.err != nil || r.stdout != "v0.1.5\n" || !strings.Contains(r.stderr, "pantech upgrade") {
		t.Fatalf("stdout %q stderr %q err %v", r.stdout, r.stderr, r.err)
	}

	Version = "v0.1.5"
	if r := run(t, srv, "", "upgrade"); r.err != nil || !strings.Contains(r.stderr, "is the latest version") {
		t.Fatalf("stderr %q err %v", r.stderr, r.err)
	}

	Version = "dev"
	if r := run(t, srv, "", "upgrade"); r.err == nil || !strings.Contains(r.err.Error(), "pantech upgrade v0.1.5") {
		t.Fatalf("a dev build: err %v", r.err)
	}
}

func TestUpdateNotice(t *testing.T) {
	oldVersion := Version
	t.Cleanup(func() { Version = oldVersion })
	Version = "v0.1.4"
	var errOut bytes.Buffer
	a := &app{out: &output.Printer{Out: &bytes.Buffer{}, Err: &errOut}}
	found := make(chan string, 1)
	found <- "v0.1.5"
	a.updateNotice(found)
	if !strings.Contains(errOut.String(), "pantech v0.1.5 is out (you have v0.1.4)") {
		t.Fatalf("stderr %q", errOut.String())
	}
	errOut.Reset()
	found <- "v0.1.4"
	a.updateNotice(found)
	if errOut.Len() != 0 {
		t.Fatalf("a notice with nothing newer: %q", errOut.String())
	}
}
