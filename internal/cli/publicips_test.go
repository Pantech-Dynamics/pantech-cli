package cli

import (
	"reflect"
	"strings"
	"testing"
)

const pipListBody = `{"data":[` +
	`{"id":"pip_1","network_id":"net_1","network_name":"prod","purpose":"static_nat","address":"102.211.122.90","instance_id":"vm_1","instance_name":"web-1","observed_state":"active","desired_state":"present","in_sync":true},` +
	`{"id":"pip_2","network_id":"net_1","network_name":"prod","purpose":"static_nat","address":"102.211.122.91","instance_id":null,"observed_state":"active","desired_state":"present","in_sync":false}` +
	`],"next_cursor":null}`

func TestPublicIPCreateWithoutAVMReserves(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /networks", 200, `{"data":[{"id":"net_1","name":"prod"}],"next_cursor":null}`).
		on("POST /public-ips", 202, `{"operation_id":"op_1","resource_id":"pip_2","status":"submitting"}`).
		on("GET /operations/op_1", 200, `{"id":"op_1","status":"succeeded"}`)
	if r := run(t, srv, "", "public-ips", "create", "--network", "prod"); r.err == nil || !strings.Contains(r.err.Error(), "--yes") {
		t.Fatalf("err = %v: reserving costs money, so it asks", r.err)
	}
	r := run(t, srv, "", "public-ips", "reserve", "--network", "prod", "--yes")
	if r.err != nil || r.stdout != "pip_2\n" {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	want := map[string]any{"network_id": "net_1", "purpose": "static_nat"}
	if got := f.sentJSON("POST", "/public-ips"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %v, want no instance_id", got)
	}
}

func TestPublicIPCreateRefusesAVMForOtherPurposes(t *testing.T) {
	_, srv := newFakeAPI(t)
	r := run(t, srv, "", "public-ips", "create", "--network", "net_1", "--purpose", "load_balancer", "--vm", "vm_1", "--yes")
	if !IsUsage(r.err) || !strings.Contains(r.err.Error(), "--vm is for a static_nat address") {
		t.Fatalf("err = %v", r.err)
	}
}

func TestPublicIPListShowsDetachedAndApplying(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /public-ips", 200, pipListBody)
	r := run(t, srv, "", "public-ips", "list")
	if r.err != nil {
		t.Fatal(r.err)
	}
	lines := strings.Split(r.stdout, "\n")
	var attached, detached string
	for _, l := range lines {
		switch {
		case strings.Contains(l, "pip_1"):
			attached = l
		case strings.Contains(l, "pip_2"):
			detached = l
		}
	}
	if !strings.Contains(attached, "web-1") || strings.Contains(attached, "applying") {
		t.Fatalf("attached row %q", attached)
	}
	if !strings.Contains(detached, "detached") || !strings.Contains(detached, "applying") {
		t.Fatalf("detached row %q: want detached and applying", detached)
	}
}

func TestPublicIPGetShowsSyncAndNextSteps(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /public-ips", 200, pipListBody).
		on("GET /public-ips/pip_2", 200, `{"id":"pip_2","network_id":"net_1","purpose":"static_nat","address":"102.211.122.91","instance_id":null,"observed_state":"active","desired_state":"present","in_sync":false}`)
	r := run(t, srv, "", "public-ips", "get", "102.211.122.91")
	if r.err != nil {
		t.Fatal(r.err)
	}
	for _, want := range []string{"detached", "an attach or detach is applying"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}
	if !strings.Contains(r.stderr, "pantech public-ips attach pip_2 --vm <vm>") {
		t.Fatalf("stderr lacks the attach hint:\n%s", r.stderr)
	}
}

func TestPublicIPAttach(t *testing.T) {
	for _, flag := range []string{"--vm", "--instance"} {
		t.Run(flag, func(t *testing.T) {
			f, srv := newFakeAPI(t)
			f.on("GET /public-ips", 200, pipListBody).
				on("GET /instances", 200, `{"data":[{"id":"vm_2","name":"web-2"}],"next_cursor":null}`).
				on("POST /public-ips/pip_2/attach", 202, `{"operation_id":"op_2","resource_id":"pip_2","status":"submitting"}`).
				on("GET /operations/op_2", 200, `{"id":"op_2","status":"running"}`).
				on("GET /operations/op_2", 200, `{"id":"op_2","status":"succeeded"}`)
			r := run(t, srv, "", "public-ips", "attach", "102.211.122.91", flag, "web-2")
			if r.err != nil {
				t.Fatal(r.err)
			}
			if got := f.sentJSON("POST", "/public-ips/pip_2/attach"); !reflect.DeepEqual(got, map[string]any{"instance_id": "vm_2"}) {
				t.Fatalf("body = %v", got)
			}
			if len(f.sent("GET", "/operations/op_2")) < 2 {
				t.Fatal("the attach was not followed to the end")
			}
		})
	}
}

func TestPublicIPAttachNeedsAVM(t *testing.T) {
	_, srv := newFakeAPI(t)
	if r := run(t, srv, "", "public-ips", "attach", "pip_2"); !IsUsage(r.err) {
		t.Fatalf("err = %v, want a usage error", r.err)
	}
}

func TestPublicIPAttachHintsWhenTheVMHasOne(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /public-ips/pip_2/attach", 409, `{"status":409,"code":"INSTANCE_ALREADY_HAS_PUBLIC_IP","detail":"The instance already has a static NAT address."}`)
	r := run(t, srv, "", "public-ips", "attach", "pip_2", "--vm", "vm_1")
	if r.err == nil || !strings.Contains(r.err.Error(), "pantech public-ips detach") {
		t.Fatalf("err = %v", r.err)
	}
}

func TestPublicIPDetach(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /public-ips/pip_1/detach", 202, `{"operation_id":"op_3","resource_id":"pip_1","status":"submitting"}`).
		on("GET /operations/op_3", 200, `{"id":"op_3","status":"succeeded"}`)
	if r := run(t, srv, "", "public-ips", "detach", "pip_1"); r.err == nil || !strings.Contains(r.err.Error(), "--yes") {
		t.Fatalf("err = %v: detaching asks first", r.err)
	}
	if len(f.sent("POST", "/public-ips/pip_1/detach")) != 0 {
		t.Fatal("detached without asking")
	}
	r := run(t, srv, "", "public-ips", "detach", "pip_1", "--yes", "--no-wait")
	if r.err != nil || r.stdout != "op_3\n" {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
}

func TestPublicIPDetachThatChangesNothing(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /public-ips/pip_2/detach", 202, `{"operation_id":"","resource_id":"pip_2","status":"succeeded"}`)
	r := run(t, srv, "", "public-ips", "detach", "pip_2", "--yes")
	if r.err != nil || r.stdout != "" || !strings.Contains(r.stderr, "Nothing to change") {
		t.Fatalf("stdout %q stderr %q err %v", r.stdout, r.stderr, r.err)
	}
	if strings.Contains(r.stderr, "operations wait") {
		t.Fatalf("suggests following an operation that does not exist:\n%s", r.stderr)
	}
}

func TestVMDeleteHintsAtItsPublicIP(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("DELETE /instances/vm_1", 409, `{"status":409,"code":"INSTANCE_HAS_PUBLIC_IP","detail":"The instance has a static NAT public IP."}`)
	r := run(t, srv, "", "vm", "delete", "vm_1", "--yes")
	if r.err == nil || !strings.Contains(r.err.Error(), "pantech public-ips detach") || !strings.Contains(r.err.Error(), "INSTANCE_HAS_PUBLIC_IP") {
		t.Fatalf("err = %v", r.err)
	}
}
