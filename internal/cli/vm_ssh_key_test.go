package cli

import (
	"strings"
	"testing"
)

func TestVMChangeSSHKeyByName(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /instances", 200, `{"data":[{"id":"vm_1","name":"web-1"}],"next_cursor":null}`).
		on("GET /ssh-keys", 200, `{"data":[{"id":"sshk_1","name":"old"},{"id":"sshk_2","name":"laptop"}],"next_cursor":null}`).
		on("POST /instances/vm_1/change-ssh-key", 202, `{"operation_id":"op_7","resource_id":"vm_1","status":"submitting"}`).
		on("GET /operations/op_7", 200, opSucceeded)
	r := run(t, srv, "", "vm", "change-ssh-key", "web-1", "laptop", "--yes")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.sentJSON("POST", "/instances/vm_1/change-ssh-key"); got["ssh_key_id"] != "sshk_2" || len(got) != 1 {
		t.Fatalf("body = %v", got)
	}
	if len(f.sent("GET", "/operations/op_7")) == 0 {
		t.Fatal("the change was not followed")
	}
	if !strings.Contains(r.stderr, "Changed the SSH key of web-1") {
		t.Fatalf("stderr %q", r.stderr)
	}
}

func TestVMChangeSSHKeyAsks(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /instances/vm_1/change-ssh-key", 202, `{"operation_id":"op_7","resource_id":"vm_1","status":"submitting"}`)
	if r := run(t, srv, "", "vm", "change-ssh-key", "vm_1", "sshk_2"); r.err == nil {
		t.Fatal("changed without asking")
	}
	if len(f.sent("POST", "/instances/vm_1/change-ssh-key")) != 0 {
		t.Fatal("sent before confirming")
	}
}

func TestVMChangeSSHKeyNoWait(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /instances/vm_1/change-ssh-key", 202, `{"operation_id":"op_7","resource_id":"vm_1","status":"submitting"}`)
	r := run(t, srv, "", "vm", "change-ssh-key", "vm_1", "sshk_2", "--yes", "--no-wait")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.stdout != "op_7\n" || len(f.sent("GET", "/operations/op_7")) != 0 {
		t.Fatalf("stdout %q", r.stdout)
	}
}

func TestVMChangeSSHKeyStoppedHints(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /instances/vm_1/change-ssh-key", 409, `{"type":"about:blank","title":"Conflict","status":409,"code":"INVALID_RESOURCE_STATE","detail":"The instance is not running.","request_id":"req_5"}`)
	r := run(t, srv, "", "vm", "change-ssh-key", "vm_1", "sshk_2", "--yes")
	if r.err == nil || !strings.Contains(r.err.Error(), "pantech vm start vm_1") {
		t.Fatalf("err = %v", r.err)
	}
}

func TestVMChangeSSHKeyUnknownKey(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /ssh-keys", 200, `{"data":[{"id":"sshk_1","name":"old"}],"next_cursor":null}`)
	r := run(t, srv, "", "vm", "change-ssh-key", "vm_1", "laptop", "--yes")
	if r.err == nil || !strings.Contains(r.err.Error(), `no SSH key named "laptop"`) {
		t.Fatalf("err = %v", r.err)
	}
	if len(f.sent("POST", "/instances/vm_1/change-ssh-key")) != 0 {
		t.Fatal("sent with an unknown key")
	}
}
