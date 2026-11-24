package cli

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
)

const (
	dynamicIPActive    = `{"instance_id":"vm_1","schedule":"weekly","connections":"keep","on_demand":true,"state":"active","current_address":"198.51.100.10","next_rotation_at":"2026-12-01T00:00:00Z","last_rotated_at":"2026-11-24T00:00:00Z","surcharge_percent":135,"on_demand_used":2,"on_demand_limit":10,"failure_reason":null,"events":[{"kind":"rotated","from_address":"198.51.100.9","to_address":"198.51.100.10","degraded":false,"occurred_at":"2026-11-24T00:00:00Z"}]}`
	dynamicIPEnabling  = `{"instance_id":"vm_1","schedule":"weekly","connections":"keep","on_demand":false,"state":"enabling","current_address":null,"next_rotation_at":null,"last_rotated_at":null,"surcharge_percent":40,"on_demand_used":0,"on_demand_limit":10,"failure_reason":null,"events":[]}`
	dynamicIPDegraded  = `{"instance_id":"vm_1","schedule":"weekly","connections":"keep","on_demand":false,"state":"degraded","current_address":"198.51.100.11","next_rotation_at":"2026-12-01T00:00:00Z","last_rotated_at":"2026-11-24T00:00:00Z","surcharge_percent":40,"on_demand_used":0,"on_demand_limit":10,"failure_reason":null,"events":[{"kind":"rotated","from_address":"198.51.100.10","to_address":"198.51.100.11","degraded":true,"occurred_at":"2026-11-24T00:00:00Z"}]}`
	dynamicIPFailed    = `{"instance_id":"vm_1","schedule":"weekly","connections":"keep","on_demand":false,"state":"failed","current_address":null,"next_rotation_at":null,"last_rotated_at":null,"surcharge_percent":40,"on_demand_used":0,"on_demand_limit":10,"failure_reason":"agent did not acknowledge route","events":[]}`
	dynamicIPDisabling = `{"instance_id":"vm_1","schedule":"weekly","connections":"keep","on_demand":false,"state":"disabling","current_address":"198.51.100.10","next_rotation_at":null,"last_rotated_at":"2026-11-24T00:00:00Z","surcharge_percent":40,"on_demand_used":0,"on_demand_limit":10,"failure_reason":null,"events":[]}`
	dynamicIPOff       = `{"instance_id":"vm_1","schedule":"weekly","connections":"keep","on_demand":false,"state":"off","current_address":null,"next_rotation_at":null,"last_rotated_at":"2026-11-24T00:00:00Z","surcharge_percent":0,"on_demand_used":0,"on_demand_limit":10,"failure_reason":null,"events":[]}`
	dynamicIPNotFound  = `{"status":404,"code":"ADDRESS_ROTATION_NOT_FOUND","detail":"Dynamic IP is not configured on this instance.","request_id":"req_dip_404"}`
)

func vmListForDynamicIP(f *fakeAPI) {
	f.on("GET /instances", 200, `{"data":[{"id":"vm_1","name":"web-1","observed_state":"running"}],"next_cursor":null}`)
}

func TestVMDynamicIPGet(t *testing.T) {
	for _, c := range []struct {
		name       string
		status     int
		body       string
		args       []string
		wantStdout []string
		wantStderr []string
		wantErr    string
		wantJSON   bool
	}{
		{
			name:       "active with recent events table, by VM name",
			status:     200,
			body:       dynamicIPActive,
			args:       []string{"vm", "dynamic-ip", "get", "web-1"},
			wantStdout: []string{"web-1", "active", "198.51.100.10", "weekly", "keep", "on (2/10 used in 24h)", "+135%", "rotated", "198.51.100.9"},
			wantStderr: []string{"pantech vm dynamic-ip rotate web-1", "pantech vm dynamic-ip off web-1"},
		},
		{
			name:     "json returns raw API body",
			status:   200,
			body:     dynamicIPActive,
			args:     []string{"vm", "dip", "show", "vm_1", "--json"},
			wantJSON: true,
		},
		{
			name:       "quiet prints current address when present",
			status:     200,
			body:       dynamicIPActive,
			args:       []string{"vm", "dynamic-ip", "get", "vm_1", "--quiet"},
			wantStdout: []string{"198.51.100.10\n"},
		},
		{
			name:       "quiet prints state when current address is nil",
			status:     200,
			body:       dynamicIPEnabling,
			args:       []string{"vm", "dynamic-ip", "get", "vm_1", "-q"},
			wantStdout: []string{"enabling\n"},
		},
		{
			name:       "404 ADDRESS_ROTATION_NOT_FOUND prints friendly note and Next hint",
			status:     404,
			body:       dynamicIPNotFound,
			args:       []string{"vm", "dynamic-ip", "get", "web-1"},
			wantStderr: []string{"Dynamic IP is off on web-1", "pantech vm dynamic-ip set web-1 --schedule weekly"},
		},
		{
			name:       "404 ADDRESS_ROTATION_NOT_FOUND with quiet prints off",
			status:     404,
			body:       dynamicIPNotFound,
			args:       []string{"vm", "dynamic-ip", "get", "vm_1", "--quiet"},
			wantStdout: []string{"off\n"},
		},
		{
			name:    "404 ADDRESS_ROTATION_NOT_FOUND with json returns the 404 error",
			status:  404,
			body:    dynamicIPNotFound,
			args:    []string{"vm", "dynamic-ip", "get", "vm_1", "--json"},
			wantErr: "ADDRESS_ROTATION_NOT_FOUND",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newFakeAPI(t)
			vmListForDynamicIP(f)
			f.on("GET /instances/vm_1/address-rotation", c.status, c.body)
			r := run(t, srv, "", c.args...)
			if c.wantErr != "" {
				if r.err == nil || !strings.Contains(r.err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", r.err, c.wantErr)
				}
				return
			}
			if r.err != nil {
				t.Fatal(r.err)
			}
			if c.wantJSON {
				var got, want any
				if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
					t.Fatalf("stdout not JSON: %v (%q)", err, r.stdout)
				}
				_ = json.Unmarshal([]byte(c.body), &want)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("json = %v, want %v", got, want)
				}
				return
			}
			for _, w := range c.wantStdout {
				if !strings.Contains(r.stdout, w) {
					t.Fatalf("stdout %q lacks %q", r.stdout, w)
				}
			}
			for _, w := range c.wantStderr {
				if !strings.Contains(r.stderr, w) {
					t.Fatalf("stderr %q lacks %q", r.stderr, w)
				}
			}
		})
	}
}

func TestVMDynamicIPSet(t *testing.T) {
	t.Run("asks for confirmation naming the surcharge for each schedule and on-demand", func(t *testing.T) {
		for _, c := range []struct {
			schedule string
			onDemand bool
			wantPct  []string
		}{
			{"every_12_hours", false, []string{"+60%"}},
			{"daily", false, []string{"+50%"}},
			{"weekly", false, []string{"+40%"}},
			{"monthly", false, []string{"+30%"}},
			{"weekly", true, []string{"+40%", "+95%"}},
		} {
			f, srv := newFakeAPI(t)
			args := []string{"vm", "dynamic-ip", "set", "vm_1", "--schedule", c.schedule}
			if c.onDemand {
				args = append(args, "--on-demand")
			}
			r := run(t, srv, "", args...)
			if r.err == nil {
				t.Fatalf("%s: proceeded without --yes", c.schedule)
			}
			for _, w := range c.wantPct {
				if !strings.Contains(r.err.Error(), w) {
					t.Fatalf("%s: confirm err %q lacks %q", c.schedule, r.err.Error(), w)
				}
			}
			if len(f.sent("PUT", "/instances/vm_1/address-rotation")) != 0 {
				t.Fatalf("%s: sent PUT without confirmation", c.schedule)
			}
		}
	})

	t.Run("invalid schedule or connections is a usage error before calling the API", func(t *testing.T) {
		for _, args := range [][]string{
			{"vm", "dynamic-ip", "set", "vm_1", "--schedule", "hourly", "--yes"},
			{"vm", "dynamic-ip", "set", "vm_1", "--schedule", "weekly", "--connections", "drop", "--yes"},
		} {
			f, srv := newFakeAPI(t)
			r := run(t, srv, "", args...)
			if !IsUsage(r.err) {
				t.Fatalf("%v: err = %v, want usage error", args, r.err)
			}
			if len(f.calls) != 0 {
				t.Fatalf("%v: called API %+v", args, f.calls)
			}
		}
	})

	t.Run("enables and polls until active, carrying Idempotency-Key", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("PUT /instances/vm_1/address-rotation", 201, dynamicIPEnabling).
			on("GET /instances/vm_1/address-rotation", 200, dynamicIPEnabling).
			on("GET /instances/vm_1/address-rotation", 200, dynamicIPActive)
		r := run(t, srv, "", "vm", "dynamic-ip", "enable", "vm_1", "--schedule", "weekly", "--connections", "move", "--on-demand", "--yes")
		if r.err != nil {
			t.Fatal(r.err)
		}
		calls := f.sent("PUT", "/instances/vm_1/address-rotation")
		if len(calls) != 1 || calls[0].IdempotencyKey == "" {
			t.Fatalf("PUT calls = %+v, want 1 with Idempotency-Key", calls)
		}
		wantBody := map[string]any{"schedule": "weekly", "connections": "move", "on_demand": true}
		if got := f.sentJSON("PUT", "/instances/vm_1/address-rotation"); !reflect.DeepEqual(got, wantBody) {
			t.Fatalf("body = %v, want %v", got, wantBody)
		}
		if len(f.sent("GET", "/instances/vm_1/address-rotation")) < 2 {
			t.Fatalf("expected polling GETs, got %d", len(f.sent("GET", "/instances/vm_1/address-rotation")))
		}
		if !strings.Contains(r.stdout, "198.51.100.10") {
			t.Fatalf("stdout = %q", r.stdout)
		}
	})

	t.Run("omits on_demand when flag not passed and does not poll on 200 active update", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("PUT /instances/vm_1/address-rotation", 200, dynamicIPActive)
		r := run(t, srv, "", "vm", "dynamic-ip", "update", "vm_1", "--schedule", "daily", "--yes")
		if r.err != nil {
			t.Fatal(r.err)
		}
		wantBody := map[string]any{"schedule": "daily", "connections": "keep"}
		if got := f.sentJSON("PUT", "/instances/vm_1/address-rotation"); !reflect.DeepEqual(got, wantBody) {
			t.Fatalf("body = %v, want %v", got, wantBody)
		}
		if len(f.sent("GET", "/instances/vm_1/address-rotation")) != 0 {
			t.Fatal("should not poll when already active")
		}
	})

	t.Run("polls until degraded and warns", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("PUT /instances/vm_1/address-rotation", 201, dynamicIPEnabling).
			on("GET /instances/vm_1/address-rotation", 200, dynamicIPDegraded)
		r := run(t, srv, "", "vm", "dynamic-ip", "set", "vm_1", "--schedule", "weekly", "--yes")
		if r.err != nil {
			t.Fatal(r.err)
		}
		if !strings.Contains(r.stderr, "degraded") || !strings.Contains(r.stdout, "198.51.100.11") {
			t.Fatalf("stdout=%q stderr=%q", r.stdout, r.stderr)
		}
	})

	t.Run("polls until failed and returns DynamicIPFailed (exit code 5)", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("PUT /instances/vm_1/address-rotation", 201, dynamicIPEnabling).
			on("GET /instances/vm_1/address-rotation", 200, dynamicIPFailed)
		r := run(t, srv, "", "vm", "dynamic-ip", "set", "vm_1", "--schedule", "weekly", "--yes")
		var failed *api.DynamicIPFailed
		if !errors.As(r.err, &failed) || !strings.Contains(r.err.Error(), "agent did not acknowledge route") {
			t.Fatalf("err = %v, want *api.DynamicIPFailed with failure_reason", r.err)
		}
	})

	t.Run("no-wait returns without polling and supports json and quiet", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("PUT /instances/vm_1/address-rotation", 201, dynamicIPEnabling)
		r := run(t, srv, "", "vm", "dynamic-ip", "set", "vm_1", "--schedule", "weekly", "--no-wait", "--yes")
		if r.err != nil || len(f.sent("GET", "/instances/vm_1/address-rotation")) != 0 {
			t.Fatalf("err=%v gets=%d", r.err, len(f.sent("GET", "/instances/vm_1/address-rotation")))
		}
		if !strings.Contains(r.stderr, "pantech vm dynamic-ip get vm_1") {
			t.Fatalf("stderr = %q", r.stderr)
		}

		r2 := run(t, srv, "", "vm", "dynamic-ip", "set", "vm_1", "--schedule", "weekly", "--no-wait", "--yes", "--quiet")
		if r2.err != nil || r2.stdout != "enabling\n" {
			t.Fatalf("quiet stdout=%q err=%v", r2.stdout, r2.err)
		}

		r3 := run(t, srv, "", "vm", "dynamic-ip", "set", "vm_1", "--schedule", "weekly", "--no-wait", "--yes", "--json")
		if r3.err != nil || !strings.Contains(r3.stdout, `"state": "enabling"`) {
			t.Fatalf("json stdout=%q err=%v", r3.stdout, r3.err)
		}
	})
}

func TestVMDynamicIPRotate(t *testing.T) {
	t.Run("rotates and prints new IP and 24h quota, with Idempotency-Key", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		rotated := strings.Replace(dynamicIPActive, `"198.51.100.10"`, `"198.51.100.42"`, 1)
		rotated = strings.Replace(rotated, `"on_demand_used":2`, `"on_demand_used":3`, 1)
		f.on("POST /instances/vm_1/address-rotation/rotate", 200, rotated)

		r := run(t, srv, "", "vm", "dynamic-ip", "rotate", "vm_1")
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.stdout != "198.51.100.42\n" {
			t.Fatalf("stdout = %q, want new IP alone", r.stdout)
		}
		if !strings.Contains(r.stderr, "3/10 in 24h") {
			t.Fatalf("stderr = %q, want 3/10 in 24h", r.stderr)
		}
		calls := f.sent("POST", "/instances/vm_1/address-rotation/rotate")
		if len(calls) != 1 || calls[0].IdempotencyKey == "" {
			t.Fatalf("calls = %+v, want 1 with Idempotency-Key", calls)
		}
	})

	t.Run("json and quiet output on alias now", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("POST /instances/vm_1/address-rotation/rotate", 200, dynamicIPActive)
		rq := run(t, srv, "", "vm", "dynamic-ip", "now", "vm_1", "-q")
		if rq.err != nil || rq.stdout != "198.51.100.10\n" || rq.stderr != "" {
			t.Fatalf("quiet stdout=%q stderr=%q err=%v", rq.stdout, rq.stderr, rq.err)
		}
		rj := run(t, srv, "", "vm", "dynamic-ip", "rotate", "vm_1", "--json")
		if rj.err != nil || !strings.Contains(rj.stdout, `"current_address": "198.51.100.10"`) {
			t.Fatalf("json stdout=%q err=%v", rj.stdout, rj.err)
		}
	})

	t.Run("rotate is non-replaying: never retried on 500 and still carries Idempotency-Key", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("POST /instances/vm_1/address-rotation/rotate", 500, `{"status":500,"code":"INTERNAL","detail":"Temporary failure."}`)
		r := run(t, srv, "", "vm", "dynamic-ip", "rotate", "vm_1")
		if !api.IsCode(r.err, "INTERNAL") {
			t.Fatalf("err = %v, want INTERNAL", r.err)
		}
		calls := f.sent("POST", "/instances/vm_1/address-rotation/rotate")
		if len(calls) != 1 {
			t.Fatalf("rotate sent %d times, want 1 (must not retry)", len(calls))
		}
		if calls[0].IdempotencyKey == "" {
			t.Fatal("rotate missing Idempotency-Key")
		}
	})
}

func TestVMDynamicIPOff(t *testing.T) {
	t.Run("asks for confirmation before deleting", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		r := run(t, srv, "", "vm", "dynamic-ip", "off", "vm_1")
		if r.err == nil || len(f.sent("DELETE", "/instances/vm_1/address-rotation")) != 0 {
			t.Fatalf("deleted without asking: err=%v", r.err)
		}
	})

	t.Run("polls until off", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("DELETE /instances/vm_1/address-rotation", 200, dynamicIPDisabling).
			on("GET /instances/vm_1/address-rotation", 200, dynamicIPDisabling).
			on("GET /instances/vm_1/address-rotation", 200, dynamicIPOff)
		r := run(t, srv, "", "vm", "dynamic-ip", "disable", "vm_1", "--yes")
		if r.err != nil {
			t.Fatal(r.err)
		}
		calls := f.sent("DELETE", "/instances/vm_1/address-rotation")
		if len(calls) != 1 || calls[0].IdempotencyKey == "" {
			t.Fatalf("DELETE calls = %+v, want 1 with Idempotency-Key", calls)
		}
		if len(f.sent("GET", "/instances/vm_1/address-rotation")) < 2 {
			t.Fatalf("expected polling GETs, got %d", len(f.sent("GET", "/instances/vm_1/address-rotation")))
		}
	})

	t.Run("polls until 404 and succeeds", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("DELETE /instances/vm_1/address-rotation", 200, dynamicIPDisabling).
			on("GET /instances/vm_1/address-rotation", 404, dynamicIPNotFound)
		r := run(t, srv, "", "vm", "dynamic-ip", "off", "vm_1", "--yes", "-q")
		if r.err != nil || r.stdout != "off\n" {
			t.Fatalf("stdout=%q err=%v", r.stdout, r.err)
		}
	})

	t.Run("immediate off or no-wait skips polling", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("DELETE /instances/vm_1/address-rotation", 200, dynamicIPOff)
		r := run(t, srv, "", "vm", "dynamic-ip", "off", "vm_1", "--yes")
		if r.err != nil || len(f.sent("GET", "/instances/vm_1/address-rotation")) != 0 {
			t.Fatalf("err=%v gets=%d", r.err, len(f.sent("GET", "/instances/vm_1/address-rotation")))
		}

		f2, srv2 := newFakeAPI(t)
		f2.on("DELETE /instances/vm_1/address-rotation", 200, dynamicIPDisabling)
		r2 := run(t, srv2, "", "vm", "dynamic-ip", "off", "vm_1", "--yes", "--no-wait", "--json")
		if r2.err != nil || len(f2.sent("GET", "/instances/vm_1/address-rotation")) != 0 || !strings.Contains(r2.stdout, `"state": "disabling"`) {
			t.Fatalf("stdout=%q err=%v gets=%d", r2.stdout, r2.err, len(f2.sent("GET", "/instances/vm_1/address-rotation")))
		}
	})
}

func TestVMDynamicIPErrorHints(t *testing.T) {
	for _, c := range []struct {
		name     string
		route    string
		status   int
		code     string
		args     []string
		wantHint string
	}{
		{
			name:     "needs private network suggests attach",
			route:    "PUT /instances/vm_1/address-rotation",
			status:   409,
			code:     "ADDRESS_ROTATION_NEEDS_PRIVATE_NETWORK",
			args:     []string{"vm", "dynamic-ip", "set", "vm_1", "--schedule", "weekly", "--yes"},
			wantHint: "pantech vm private-network attach vm_1",
		},
		{
			name:     "needs agent mentions Pantech agent 0.5.0+",
			route:    "PUT /instances/vm_1/address-rotation",
			status:   409,
			code:     "ADDRESS_ROTATION_NEEDS_AGENT",
			args:     []string{"vm", "dynamic-ip", "set", "vm_1", "--schedule", "weekly", "--yes"},
			wantHint: "0.5.0",
		},
		{
			name:     "not enabled mentions closed beta",
			route:    "PUT /instances/vm_1/address-rotation",
			status:   403,
			code:     "ADDRESS_ROTATION_NOT_ENABLED",
			args:     []string{"vm", "dynamic-ip", "set", "vm_1", "--schedule", "weekly", "--yes"},
			wantHint: "closed beta",
		},
		{
			name:     "on-demand off suggests enabling on-demand",
			route:    "POST /instances/vm_1/address-rotation/rotate",
			status:   409,
			code:     "ADDRESS_ROTATION_ON_DEMAND_OFF",
			args:     []string{"vm", "dynamic-ip", "rotate", "vm_1"},
			wantHint: "--on-demand",
		},
		{
			name:     "limit reached explains 24h limit",
			route:    "POST /instances/vm_1/address-rotation/rotate",
			status:   429,
			code:     "ADDRESS_ROTATION_LIMIT_REACHED",
			args:     []string{"vm", "dynamic-ip", "rotate", "vm_1"},
			wantHint: "24-hour",
		},
		{
			name:     "not found on rotate hints turning it on",
			route:    "POST /instances/vm_1/address-rotation/rotate",
			status:   404,
			code:     "ADDRESS_ROTATION_NOT_FOUND",
			args:     []string{"vm", "dynamic-ip", "rotate", "vm_1"},
			wantHint: "pantech vm dynamic-ip set vm_1 --schedule weekly",
		},
		{
			name:     "needs security group points to security-groups",
			route:    "PUT /instances/vm_1/address-rotation",
			status:   409,
			code:     "ADDRESS_ROTATION_NEEDS_SECURITY_GROUP",
			args:     []string{"vm", "dynamic-ip", "set", "vm_1", "--schedule", "weekly", "--yes"},
			wantHint: "pantech security-groups list",
		},
		{
			name:     "security group allows private network reuses narrowing hint",
			route:    "PUT /instances/vm_1/address-rotation",
			status:   409,
			code:     "SECURITY_GROUP_ALLOWS_PRIVATE_NETWORK",
			args:     []string{"vm", "dynamic-ip", "set", "vm_1", "--schedule", "weekly", "--yes"},
			wantHint: "pantech security-groups rules remove sg_locked",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newFakeAPI(t)
			f.on("GET /instances/vm_1", 200, `{"id":"vm_1","name":"web-1","zone":"af-abj-1","security_group_id":"sg_locked"}`).
				on("GET /regions", 200, `{"data":[{"code":"af-abj","placements":[{"zone":"af-abj-1","private_network_cidr":"10.250.0.0/20"}]}],"next_cursor":null}`).
				on(c.route, c.status, `{"status":`+itoa(c.status)+`,"code":"`+c.code+`","detail":"API detail."}`)
			r := run(t, srv, "", c.args...)
			if !api.IsCode(r.err, c.code) {
				t.Fatalf("err = %v, want code %s", r.err, c.code)
			}
			if !strings.Contains(r.err.Error(), c.wantHint) {
				t.Fatalf("err = %q, want hint %q", r.err.Error(), c.wantHint)
			}
		})
	}
}

func TestVMCreateWithDynamicIP(t *testing.T) {
	base := []string{"vm", "create", "--ssh-key", "sshk_test1", "--name", "edge-1", "--plan", "starter", "--image", "ubuntu-24-04"}

	t.Run("local validation refuses bad flag combinations before any API call", func(t *testing.T) {
		for _, c := range []struct {
			name      string
			extra     []string
			wantErr   string
			wantUsage bool
		}{
			{
				name:      "connections without dynamic-ip is a usage error",
				extra:     []string{"--dynamic-ip-connections", "move", "--yes"},
				wantErr:   "--dynamic-ip-connections requires --dynamic-ip",
				wantUsage: true,
			},
			{
				name:      "invalid schedule is a usage error",
				extra:     []string{"--security-group", "sg_1", "--dynamic-ip", "hourly", "--yes"},
				wantErr:   "--dynamic-ip must be one of",
				wantUsage: true,
			},
			{
				name:      "invalid connections is a usage error",
				extra:     []string{"--security-group", "sg_1", "--dynamic-ip", "weekly", "--dynamic-ip-connections", "drop", "--yes"},
				wantErr:   "--dynamic-ip-connections must be keep or move",
				wantUsage: true,
			},
			{
				name:    "dynamic-ip with subnet is refused",
				extra:   []string{"--subnet", "snet_1", "--dynamic-ip", "weekly", "--yes"},
				wantErr: "cannot be used with --subnet",
			},
			{
				name:    "dynamic-ip without security-group is refused",
				extra:   []string{"--dynamic-ip", "weekly", "--yes"},
				wantErr: "--dynamic-ip requires --security-group",
			},
		} {
			t.Run(c.name, func(t *testing.T) {
				f, srv := newFakeAPI(t)
				r := run(t, srv, "", append(append([]string{}, base...), c.extra...)...)
				if r.err == nil || !strings.Contains(r.err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", r.err, c.wantErr)
				}
				if c.wantUsage && !IsUsage(r.err) {
					t.Fatalf("err = %v, want usage error", r.err)
				}
				if len(f.calls) != 0 {
					t.Fatalf("called API %+v", f.calls)
				}
			})
		}
	})

	t.Run("confirmation mentions the surcharge percent", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("GET /plans", 200, plansBody)
		args := append(append([]string{}, base...), "--security-group", "sg_locked", "--dynamic-ip", "daily")
		r := run(t, srv, "", args...)
		if r.err == nil || !strings.Contains(r.err.Error(), "+50%") {
			t.Fatalf("err = %v, want confirmation mentioning +50%%", r.err)
		}
		if len(f.sent("POST", "/instances")) != 0 {
			t.Fatal("ordered without confirmation")
		}
	})

	t.Run("sends address_rotation and prints dynamic-ip Next hint after provisioning", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("GET /plans", 200, plansBody).
			on("POST /instances", 202, orderAccepted).
			on("GET /instance-orders/ord_1", 200, `{"id":"ord_1","instance_id":"vm_1","status":"provisioned"}`).
			on("GET /instances/vm_1", 200, `{"id":"vm_1","name":"edge-1","observed_state":"running","private_ipv4":"203.0.113.5"}`)
		args := append(append([]string{}, base...), "--security-group", "sg_locked", "--dynamic-ip", "weekly", "--dynamic-ip-connections", "move", "--yes")
		r := run(t, srv, "", args...)
		if r.err != nil {
			t.Fatal(r.err)
		}
		body := f.sentJSON("POST", "/instances")
		wantRot := map[string]any{"schedule": "weekly", "connections": "move"}
		if !reflect.DeepEqual(body["address_rotation"], wantRot) {
			t.Fatalf("address_rotation = %v, want %v", body["address_rotation"], wantRot)
		}
		if !strings.Contains(r.stderr, "pantech vm dynamic-ip get edge-1") {
			t.Fatalf("stderr = %q, want dynamic-ip Next hint", r.stderr)
		}
	})

	t.Run("maps address_rotation field errors to --dynamic-ip flags", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("GET /plans", 200, plansBody).
			on("POST /instances", 422, `{"status":422,"code":"VALIDATION_FAILED","title":"Validation failed","detail":"address_rotation: not allowed.","errors":[{"field":"address_rotation","code":"INVALID","message":"not allowed"}]}`)
		args := append(append([]string{}, base...), "--security-group", "sg_locked", "--dynamic-ip", "weekly", "--yes", "--no-wait")
		r := run(t, srv, "", args...)
		if r.err == nil || !strings.Contains(r.err.Error(), "--dynamic-ip: not allowed") {
			t.Fatalf("err = %v, want --dynamic-ip flag name", r.err)
		}
	})
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
