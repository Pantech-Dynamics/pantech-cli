package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
)

const (
	opAccepted  = `{"operation_id":"op_7","resource_id":"db_1","status":"submitting"}`
	opSucceeded = `{"id":"op_7","status":"succeeded"}`
	dbSnapBody  = `{"id":"snap_1","name":"before-upgrade","database_id":"db_1","trigger":"manual","size_bytes":2147483648,"desired_state":"present","observed_state":"active","created_at":"2026-10-05T10:00:00Z"}`
)

func dbWithStorage(size, pending string) string {
	return strings.Replace(dbBody, `"port":5432,`, `"port":5432,"data_volume_size_gb":`+size+`,"pending_data_volume_size_gb":`+pending+`,`, 1)
}

func TestDBCreateWithStorage(t *testing.T) {
	f, create := dbCreateAPI(t, strings.Replace(strings.Replace(dbAccepted, "%s", "false", 1), "%s", "null", 1))
	if r := create("", "--storage-gb", "50", "--no-wait"); r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.sentJSON("POST", "/databases")["storage_gb"]; got != float64(50) {
		t.Fatalf("storage_gb = %v", got)
	}
}

func TestDBStorageResize(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /databases/db_1", 200, dbWithStorage("20", "null")).
		on("POST /databases/db_1/resize-storage", 202, opAccepted).
		on("GET /operations/op_7", 200, `{"id":"op_7","status":"running"}`).
		on("GET /operations/op_7", 200, opSucceeded)
	r := run(t, srv, "", "db", "storage", "resize", "db_1", "--storage-gb", "40", "--yes")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.sentJSON("POST", "/databases/db_1/resize-storage"); got["storage_gb"] != float64(40) || len(got) != 1 {
		t.Fatalf("body = %v", got)
	}
	if len(f.sent("GET", "/operations/op_7")) < 2 {
		t.Fatal("the resize was not followed to the end")
	}
}

func TestDBStorageResizeNoWait(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /databases/db_1", 200, dbWithStorage("20", "null")).on("POST /databases/db_1/resize-storage", 202, opAccepted)
	r := run(t, srv, "", "db", "storage", "resize", "db_1", "--storage-gb", "40", "--yes", "--no-wait")
	if r.err != nil || r.stdout != "op_7\n" || !strings.Contains(r.stderr, "growing to 40 GB") {
		t.Fatalf("stdout %q stderr %q err %v", r.stdout, r.stderr, r.err)
	}
	if len(f.sent("GET", "/operations/op_7")) != 0 {
		t.Fatal("--no-wait waited")
	}
}

func TestDBStorageResizeRefusedBeforeSending(t *testing.T) {
	for name, c := range map[string]struct{ db, size, want string }{
		"smaller":     {dbWithStorage("20", "null"), "10", "storage only grows: db_1 has 20 GB"},
		"the same":    {dbWithStorage("20", "null"), "20", "storage only grows"},
		"in progress": {dbWithStorage("20", "40"), "60", "already growing to 40 GB"},
	} {
		t.Run(name, func(t *testing.T) {
			f, srv := newFakeAPI(t)
			f.on("GET /databases/db_1", 200, c.db)
			r := run(t, srv, "", "db", "storage", "resize", "db_1", "--storage-gb", c.size, "--yes")
			if r.err == nil || !strings.Contains(r.err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", r.err, c.want)
			}
			if len(f.sent("POST", "/databases/db_1/resize-storage")) != 0 {
				t.Fatal("sent anyway")
			}
		})
	}
}

func TestDBStorageResizeShowsTheAPIsFieldError(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /databases/db_1", 200, dbWithStorage("20", "null")).
		on("POST /databases/db_1/resize-storage", 422, `{"type":"about:blank","title":"Validation failed","status":422,"code":"VALIDATION_FAILED","detail":"The request has invalid fields.","request_id":"req_9","errors":[{"field":"storage_gb","code":"INVALID_DATABASE_STORAGE","message":"Must be a multiple of 10 GB."}]}`)
	r := run(t, srv, "", "db", "storage", "resize", "db_1", "--storage-gb", "45", "--yes")
	var p *api.Problem
	if !errors.As(r.err, &p) || p.Status != 422 || !strings.Contains(r.err.Error(), "storage_gb: Must be a multiple of 10 GB.") {
		t.Fatalf("err = %v", r.err)
	}
}

func TestDBGetShowsPendingStorage(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /databases/db_1", 200, dbWithStorage("20", "40"))
	r := run(t, srv, "", "db", "get", "db_1")
	if r.err != nil || !strings.Contains(r.stdout, "20 GB (growing to 40 GB)") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
}

func TestDBSnapshots(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /databases", 200, `{"data":[`+dbBody+`],"next_cursor":null}`).
		on("GET /databases/db_1/snapshots", 200, `{"data":[`+dbSnapBody+`],"next_cursor":null}`).
		on("GET /databases/db_1/snapshots/snap_1", 200, dbSnapBody).
		on("POST /databases/db_1/snapshots", 202, `{"operation_id":"op_7","resource_id":"snap_2","status":"submitting"}`).
		on("DELETE /databases/db_1/snapshots/snap_1", 202, `{"operation_id":"op_7","resource_id":"snap_1","status":"submitting"}`).
		on("GET /operations/op_7", 200, opSucceeded)

	r := run(t, srv, "", "db", "snapshots", "list", "app")
	if r.err != nil || !strings.Contains(r.stdout, "before-upgrade") || !strings.Contains(r.stdout, "2.0 GB") {
		t.Fatalf("list: %q %v", r.stdout, r.err)
	}
	r = run(t, srv, "", "db", "snapshots", "get", "db_1", "before-upgrade")
	if r.err != nil || !strings.Contains(r.stdout, "database db_1") {
		t.Fatalf("get by name: %q %v", r.stdout, r.err)
	}
	if calls := f.sent("GET", "/databases/db_1/snapshots"); len(calls) != 2 || calls[1].Query != "q=before-upgrade" {
		t.Fatalf("the name was not looked up in the database's snapshots: %+v", calls)
	}
	r = run(t, srv, "", "db", "snapshots", "create", "db_1", "--name", "nightly", "--yes")
	if r.err != nil || r.stdout != "snap_2\n" {
		t.Fatalf("create: %q %v", r.stdout, r.err)
	}
	if got := f.sentJSON("POST", "/databases/db_1/snapshots"); got["name"] != "nightly" {
		t.Fatalf("create body = %v", got)
	}
	if r := run(t, srv, "", "db", "snapshots", "delete", "db_1", "snap_1"); r.err == nil || len(f.sent("DELETE", "/databases/db_1/snapshots/snap_1")) != 0 {
		t.Fatalf("deleted without asking: %v", r.err)
	}
	if r := run(t, srv, "", "db", "snapshots", "delete", "db_1", "snap_1", "--yes"); r.err != nil {
		t.Fatal(r.err)
	}
	if len(f.sent("DELETE", "/databases/db_1/snapshots/snap_1")) != 1 {
		t.Fatal("not deleted")
	}
}

const vmPrivateBody = `{"id":"vm_1","name":"web-1","plan_id":"p","image_id":"i","zone":"af-abj-1","security_group_id":"sg_web","private_ipv4":"102.211.122.77","desired_state":"running","observed_state":"running","private_network_state":"attached","private_network_ip":"10.250.0.9","tags":{}}`

func TestVMPrivateNetworkAttach(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /instances/vm_1/private-network", 202, `{"operation_id":"op_7","resource_id":"vm_1","status":"submitting"}`).
		on("GET /operations/op_7", 200, opSucceeded).
		on("GET /instances/vm_1", 200, vmPrivateBody)
	r := run(t, srv, "", "vm", "private-network", "attach", "vm_1")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.stdout != "10.250.0.9\n" || !strings.Contains(r.stderr, "pantech db access-rules set <database> 10.250.0.9/32") {
		t.Fatalf("stdout %q stderr %q", r.stdout, r.stderr)
	}
	if len(f.sent("GET", "/operations/op_7")) == 0 {
		t.Fatal("the attach was not followed")
	}
}

func TestVMPrivateNetworkAttachRefusedBySecurityGroup(t *testing.T) {
	f, srv := newFakeAPI(t)
	msg := "Security group web allows 10.250.0.0/20 through these rules: ingress tcp 22 from 0.0.0.0/0. Narrow them first."
	f.on("POST /instances/vm_1/private-network", 409, `{"type":"about:blank","title":"Conflict","status":409,"code":"SECURITY_GROUP_ALLOWS_PRIVATE_NETWORK","detail":"`+msg+`","request_id":"req_5"}`).
		on("GET /instances/vm_1", 200, vmPrivateBody)
	r := run(t, srv, "", "vm", "private-network", "attach", "vm_1")
	var p *api.Problem
	if !errors.As(r.err, &p) || p.Code != "SECURITY_GROUP_ALLOWS_PRIVATE_NETWORK" {
		t.Fatalf("err = %v, want the API's problem", r.err)
	}
	for _, want := range []string{msg, "(SECURITY_GROUP_ALLOWS_PRIVATE_NETWORK, request req_5)", "See: pantech security-groups rules set sg_web"} {
		if !strings.Contains(r.err.Error(), want) {
			t.Fatalf("%q lacks %q", r.err.Error(), want)
		}
	}
}

func TestVMPrivateNetworkDetachAsks(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("DELETE /instances/vm_1/private-network", 202, `{"operation_id":"op_7","resource_id":"vm_1","status":"submitting"}`).
		on("GET /operations/op_7", 200, opSucceeded)
	if r := run(t, srv, "", "vm", "private-network", "detach", "vm_1"); r.err == nil {
		t.Fatal("detached without asking")
	}
	if r := run(t, srv, "", "vm", "private-network", "detach", "vm_1", "--yes"); r.err != nil {
		t.Fatal(r.err)
	}
	if len(f.sent("DELETE", "/instances/vm_1/private-network")) != 1 {
		t.Fatal("not detached")
	}
}

func TestVMGetShowsThePrivateNetwork(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /instances/vm_1", 200, vmPrivateBody)
	r := run(t, srv, "", "vm", "get", "vm_1")
	if r.err != nil || !strings.Contains(r.stdout, "Private network") || !strings.Contains(r.stdout, "10.250.0.9 (attached)") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
}
