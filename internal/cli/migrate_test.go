package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
)

const (
	connTemplate = `{\"AWSTemplateFormatVersion\":\"2010-09-09\",\"Resources\":{}}`
	connSetup    = `"setup":{"issuer_url":"https://id.pantechdynamics.com","audience":"sts.amazonaws.com","subject":"ccn_1","quick_create_url":"https://console.aws.amazon.com/cloudformation/home#/stacks/quickcreate?stackName=pantech","template":"` + connTemplate + `"}`
	connAwaiting = `{"id":"ccn_1","provider":"aws","name":"Production AWS","status":"awaiting_role","failure":null,"role_arn":null,"aws_account_id":null,"verified_at":null,"created_at":"2026-10-20T10:00:00Z","updated_at":"2026-10-20T10:00:00Z",` + connSetup + `}`
	connVerify   = `{"id":"ccn_1","provider":"aws","name":"Production AWS","status":"verifying","failure":null,"role_arn":"arn:aws:iam::123456789012:role/PantechRead","aws_account_id":null,"verified_at":null,"created_at":"2026-10-20T10:00:00Z","updated_at":"2026-10-20T10:01:00Z",` + connSetup + `}`
	connOK       = `{"id":"ccn_1","provider":"aws","name":"Production AWS","status":"connected","failure":null,"role_arn":"arn:aws:iam::123456789012:role/PantechRead","aws_account_id":"123456789012","verified_at":"2026-10-20T10:02:00Z","created_at":"2026-10-20T10:00:00Z","updated_at":"2026-10-20T10:02:00Z",` + connSetup + `}`

	scanQueued  = `{"id":"csn_1","connection_id":"ccn_1","status":"queued","failure":null,"created_at":"2026-10-20T10:00:00Z","started_at":null,"finished_at":null,"aws_account_id":"123456789012","regions":[],"counts":null,"cost":null,"warnings":[],"truncated":[],"inventory":null}`
	scanRunning = `{"id":"csn_1","connection_id":"ccn_1","status":"running","failure":null,"created_at":"2026-10-20T10:00:00Z","started_at":"2026-10-20T10:00:05Z","finished_at":null,"aws_account_id":"123456789012","regions":[],"counts":null,"cost":null,"warnings":[],"truncated":[],"inventory":null}`
	scanDone    = `{"id":"csn_1","connection_id":"ccn_1","status":"succeeded","failure":null,"created_at":"2026-10-20T10:00:00Z","started_at":"2026-10-20T10:00:05Z","finished_at":"2026-10-20T10:03:00Z","aws_account_id":"123456789012","regions":["us-east-1","eu-west-1"],"counts":{"instances":3,"volumes":4,"networks":1,"subnets":2,"security_groups":2,"public_ips":1,"load_balancers":0,"databases":1,"buckets":5,"users":2,"ssh_keys":1},"cost":{"currency":"USD","average_monthly_minor":123456,"months":[{"month":"2026-08","amount_minor":120000},{"month":"2026-09","amount_minor":126912}]},"warnings":["region_denied:eu-south-1"],"truncated":[],"inventory":null}`
	scanFailed  = `{"id":"csn_1","connection_id":"ccn_1","status":"failed","failure":{"code":"permission_missing","message":"The role cannot read EC2. Update the stack from a new template."},"created_at":"2026-10-20T10:00:00Z","started_at":"2026-10-20T10:00:05Z","finished_at":"2026-10-20T10:01:00Z","aws_account_id":"123456789012","regions":[],"counts":null,"cost":null,"warnings":[],"truncated":[],"inventory":null}`

	migrationBody = `{"id":"mig_1","name":"Move to Pantech","region":"ng-lagos-1","connection_id":"ccn_1","scan_id":"csn_1","stage":"planning","version":1,
"summary":{"planned":3,"excluded":1,"unsupported":1,"by_kind":{"instance":2,"network":1}},
"estimate":{"currency":"USD","pantech_priced":true,"pantech_monthly_minor":45000,"covers":["servers","volumes","networks"],"not_covered":["databases"],"source_known":true,"source_monthly_minor":123456,"source_currency":"USD","source_months":3,"source_scope":"whole_account"},
"items":[
{"key":"instance:us-east-1:i-0abc","kind":"instance","source":{"id":"i-0abc","name":"web-1","region":"us-east-1","summary":"t3.medium","vcpu":2,"memory_mib":4096,"disk_gb":30},"status":"planned","flags":[],"target":{"plan_slug":"standard-2","disk_gb":30,"cidr":null,"network_key":"network:us-east-1:vpc-1","subnet_key":null,"instance_key":null,"rules":[],"engine":null,"engine_version":null,"storage_gb":null,"role":null,"email":null,"public_key":null,"listeners":[]}},
{"key":"instance:us-east-1:i-0def","kind":"instance","source":{"id":"i-0def","name":null,"region":"us-east-1","summary":"p4d.24xlarge","vcpu":96,"memory_mib":1179648,"disk_gb":100},"status":"unsupported","flags":[{"code":"gpu_not_offered","message":"GPU servers are not offered yet.","blocking":true}],"target":{"plan_slug":null,"disk_gb":null,"cidr":null,"network_key":null,"subnet_key":null,"instance_key":null,"rules":[],"engine":null,"engine_version":null,"storage_gb":null,"role":null,"email":null,"public_key":null,"listeners":[]}},
{"key":"network:us-east-1:vpc-1","kind":"network","source":{"id":"vpc-1","name":"main","region":"us-east-1","summary":"10.0.0.0/16","vcpu":null,"memory_mib":null,"disk_gb":null},"status":"planned","flags":[],"target":{"plan_slug":null,"disk_gb":null,"cidr":"10.0.0.0/16","network_key":null,"subnet_key":null,"instance_key":null,"rules":[],"engine":null,"engine_version":null,"storage_gb":null,"role":null,"email":null,"public_key":null,"listeners":[]}},
{"key":"volume:us-east-1:vol-9","kind":"volume","source":{"id":"vol-9","name":null,"region":"us-east-1","summary":"gp3, 500 GB","vcpu":null,"memory_mib":null,"disk_gb":500},"status":"excluded","flags":[],"target":{"plan_slug":null,"disk_gb":null,"cidr":null,"network_key":null,"subnet_key":null,"instance_key":null,"rules":[],"engine":null,"engine_version":null,"storage_gb":500,"role":null,"email":null,"public_key":null,"listeners":[]}}
],"created_at":"2026-10-20T10:00:00Z","updated_at":"2026-10-20T10:00:00Z"}`
)

// stubBrowser makes the CLI think no one can see a browser, and records any open.
func stubBrowser(t *testing.T, can bool) *[]string {
	t.Helper()
	var opened []string
	oldOpen, oldCan := openBrowser, canOpenBrowser
	openBrowser = func(u string) error { opened = append(opened, u); return nil }
	canOpenBrowser = func() bool { return can }
	t.Cleanup(func() { openBrowser, canOpenBrowser = oldOpen, oldCan })
	return &opened
}

func TestMigrateConnectWritesTheTemplate(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /cloud-connections", 201, connAwaiting)
	opened := stubBrowser(t, true)
	t.Chdir(t.TempDir())

	r := run(t, srv, "", "migrate", "connect", "--name", "Production AWS")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got, want := f.sentJSON("POST", "/cloud-connections"), map[string]any{"provider": "aws", "name": "Production AWS"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %v, want %v", got, want)
	}
	data, err := os.ReadFile("pantech-migrate-ccn_1.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"AWSTemplateFormatVersion":"2010-09-09","Resources":{}}`+"\n" {
		t.Fatalf("template = %q", data)
	}
	for _, want := range []string{"Created connection ccn_1", "pantech-migrate-ccn_1.json", "quickcreate", "pantech migrate set-role ccn_1 --role-arn"} {
		if !strings.Contains(r.stderr, want) {
			t.Fatalf("stderr %q lacks %q", r.stderr, want)
		}
	}
	if len(*opened) != 1 || !strings.Contains((*opened)[0], "quickcreate") {
		t.Fatalf("opened %v", *opened)
	}
}

func TestMigrateConnectTemplateOutNoBrowserAndJSON(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /cloud-connections", 201, connAwaiting)
	opened := stubBrowser(t, true)
	out := filepath.Join(t.TempDir(), "role.json")

	r := run(t, srv, "", "migrate", "connect", "--name", "Production AWS", "--template-out", out, "--no-browser")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}
	if len(*opened) != 0 {
		t.Fatalf("opened a browser with --no-browser: %v", *opened)
	}
	if !strings.Contains(r.stderr, "https://console.aws.amazon.com/cloudformation") {
		t.Fatalf("stderr %q lacks the link", r.stderr)
	}

	r = run(t, srv, "", "migrate", "connect", "--name", "Production AWS", "--template-out", out, "--json", "--force")
	var got map[string]any
	if r.err != nil || json.Unmarshal([]byte(r.stdout), &got) != nil || got["id"] != "ccn_1" {
		t.Fatalf("stdout %q, err %v", r.stdout, r.err)
	}
	if len(*opened) != 0 {
		t.Fatal("opened a browser with --json")
	}
}

func TestMigrateSetRoleWaitsForTheCheck(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("PATCH /cloud-connections/ccn_1", 200, connVerify)
	f.on("GET /cloud-connections/ccn_1", 200, connVerify).on("GET /cloud-connections/ccn_1", 200, connOK)

	r := run(t, srv, "", "migrate", "set-role", "ccn_1", "--role-arn", "arn:aws:iam::123456789012:role/PantechRead", "--wait")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got, want := f.sentJSON("PATCH", "/cloud-connections/ccn_1"), map[string]any{"role_arn": "arn:aws:iam::123456789012:role/PantechRead"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %v, want %v", got, want)
	}
	if n := len(f.sent("GET", "/cloud-connections/ccn_1")); n != 2 {
		t.Fatalf("polled %d times, want 2", n)
	}
	for _, want := range []string{"connected", "123456789012"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("stdout %q lacks %q", r.stdout, want)
		}
	}
}

func TestMigrateSetRoleWithoutWait(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("PATCH /cloud-connections/ccn_1", 200, connVerify)
	r := run(t, srv, "", "migrate", "set-role", "ccn_1", "--role-arn", "arn:aws:iam::123456789012:role/PantechRead")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if len(f.sent("GET", "/cloud-connections/ccn_1")) != 0 {
		t.Fatal("polled without --wait")
	}
	if !strings.Contains(r.stderr, "pantech migrate connection ccn_1 --watch") {
		t.Fatalf("stderr %q", r.stderr)
	}
}

func TestMigrateRetrySendsRetry(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("PATCH /cloud-connections/ccn_1", 200, connOK)
	r := run(t, srv, "", "migrate", "retry", "ccn_1", "--wait")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.sentJSON("PATCH", "/cloud-connections/ccn_1"); !reflect.DeepEqual(got, map[string]any{"retry": true}) {
		t.Fatalf("body = %v", got)
	}
}

func TestMigrateScanWaitPollsToSuccess(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /cloud-connections/ccn_1/scans", 202, scanQueued)
	f.on("GET /cloud-scans/csn_1", 200, scanQueued).on("GET /cloud-scans/csn_1", 200, scanRunning).on("GET /cloud-scans/csn_1", 200, scanDone)

	r := run(t, srv, "", "migrate", "scan", "ccn_1", "--wait", "-y")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if n := len(f.sent("GET", "/cloud-scans/csn_1")); n != 3 {
		t.Fatalf("polled %d times, want 3", n)
	}
	for _, want := range []string{"succeeded", "us-east-1, eu-west-1", "Servers", "3", "Buckets", "USD 1,234.56", "2026-09", "eu-south-1 could not be read", "pantech migrate plan csn_1"} {
		if !strings.Contains(r.stdout+r.stderr, want) {
			t.Fatalf("output lacks %q:\n%s\n%s", want, r.stdout, r.stderr)
		}
	}
	if strings.Contains(r.stdout, "Load balancers") {
		t.Fatalf("shows a kind it found none of:\n%s", r.stdout)
	}
}

func TestMigrateScanWaitFails(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /cloud-connections/ccn_1/scans", 202, scanQueued)
	f.on("GET /cloud-scans/csn_1", 200, scanFailed)
	r := run(t, srv, "", "migrate", "scan", "ccn_1", "--wait", "-y")
	var failed *api.MigrateFailed
	if !errors.As(r.err, &failed) || !strings.Contains(r.err.Error(), "The role cannot read EC2") {
		t.Fatalf("err = %v", r.err)
	}
}

func TestMigrateScanWithoutWaitPrintsTheID(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /cloud-connections/ccn_1/scans", 202, scanQueued)
	r := run(t, srv, "", "migrate", "scan", "ccn_1", "--yes")
	if r.err != nil || r.stdout != "csn_1\n" {
		t.Fatalf("stdout %q, err %v", r.stdout, r.err)
	}
}

func TestMigrateScanNotConnected(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /cloud-connections/ccn_1/scans", 409, `{"status":409,"code":"CLOUD_CONNECTION_NOT_CONNECTED","detail":"Finish connecting first."}`)
	r := run(t, srv, "", "migrate", "scan", "ccn_1", "-y")
	if r.err == nil || !strings.Contains(r.err.Error(), "pantech migrate set-role ccn_1") {
		t.Fatalf("err = %v", r.err)
	}
}

func TestMigratePlanAndShow(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /migrations", 201, migrationBody)
	f.on("GET /migrations/mig_1", 200, migrationBody)

	r := run(t, srv, "", "migrate", "plan", "csn_1", "--name", "Move to Pantech", "--region", "ng-lagos-1")
	if r.err != nil {
		t.Fatal(r.err)
	}
	want := map[string]any{"scan_id": "csn_1", "name": "Move to Pantech", "region": "ng-lagos-1"}
	if got := f.sentJSON("POST", "/migrations"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %v, want %v", got, want)
	}
	for _, want := range []string{"USD 450.00", "USD 1,234.56", "USD 784.56 less a month", "databases", "1 network, 2 servers"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}
	if !strings.Contains(r.stderr, "Planned mig_1: 3 to move, 1 excluded, 1 unsupported") {
		t.Fatalf("stderr %q", r.stderr)
	}

	r = run(t, srv, "", "migrate", "show", "mig_1")
	if r.err != nil {
		t.Fatal(r.err)
	}
	// Grouped by kind: networks before servers before disks.
	net, srvs, disks := strings.Index(r.stdout, "Networks"), strings.Index(r.stdout, "Servers"), strings.Index(r.stdout, "Disks")
	if net < 0 || srvs < net || disks < srvs {
		t.Fatalf("groups out of order (%d, %d, %d):\n%s", net, srvs, disks, r.stdout)
	}
	for _, want := range []string{"instance:us-east-1:i-0abc", "web-1 (t3.medium)", "standard-2 · 30 GB disk", "unsupported", "!gpu_not_offered", "GPU servers are not offered yet.", "excluded", "10.0.0.0/16"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}

	r = run(t, srv, "", "migrate", "show", "mig_1", "--json")
	var got map[string]any
	if r.err != nil || json.Unmarshal([]byte(r.stdout), &got) != nil || got["id"] != "mig_1" {
		t.Fatalf("stdout %q, err %v", r.stdout, r.err)
	}
}

func TestMigrateItemSendsOnlyWhatChanged(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want map[string]any
	}{
		{"exclude", []string{"--exclude"}, map[string]any{"excluded": true}},
		{"include", []string{"--include"}, map[string]any{"excluded": false}},
		{"plan", []string{"--plan", "standard-4"}, map[string]any{"plan_slug": "standard-4"}},
		{"storage", []string{"--storage-gb", "200"}, map[string]any{"storage_gb": float64(200)}},
		{"person", []string{"--role", "admin", "--email", "a@example.com"}, map[string]any{"role": "admin", "email": "a@example.com"}},
		{"cidr", []string{"--cidr", "10.1.0.0/16"}, map[string]any{"cidr": "10.1.0.0/16"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newFakeAPI(t)
			f.on("PATCH /migrations/mig_1/items", 200, migrationBody)
			r := run(t, srv, "", append([]string{"migrate", "item", "mig_1", "--key", "instance:us-east-1:i-0abc"}, c.args...)...)
			if r.err != nil {
				t.Fatal(r.err)
			}
			c.want["key"] = "instance:us-east-1:i-0abc"
			if got := f.sentJSON("PATCH", "/migrations/mig_1/items"); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("body = %v, want %v", got, c.want)
			}
		})
	}
}

func TestMigrateItemUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"--key", "k"},
		{"--key", "k", "--exclude", "--include"},
		{"--key", "k", "--role", "owner"},
		{"--key", "k", "--storage-gb", "0"},
	} {
		f, srv := newFakeAPI(t)
		r := run(t, srv, "", append([]string{"migrate", "item", "mig_1"}, args...)...)
		if !IsUsage(r.err) {
			t.Fatalf("%v: err = %v, want a usage error", args, r.err)
		}
		if len(f.sent("PATCH", "/migrations/mig_1/items")) != 0 {
			t.Fatalf("%v: sent anyway", args)
		}
	}
}

func TestMigrateTerraformWritesFilesAndRefusesOverwrite(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /migrations/mig_1/terraform", 200, `{"files":[{"name":"main.tf","content":"resource \"pantechdynamics_vm\" \"web_1\" {}\n"},{"name":"variables.tf","content":"variable \"image\" {}\n"}]}`)
	t.Chdir(t.TempDir())

	r := run(t, srv, "", "migrate", "terraform", "mig_1")
	if r.err != nil {
		t.Fatal(r.err)
	}
	main := filepath.Join("pantech-migrate-mig_1", "main.tf")
	if data, err := os.ReadFile(main); err != nil || !strings.Contains(string(data), "pantechdynamics_vm") {
		t.Fatalf("main.tf = %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join("pantech-migrate-mig_1", "variables.tf")); err != nil || string(data) != "variable \"image\" {}\n" {
		t.Fatalf("variables.tf = %q, %v", data, err)
	}

	// A second run refuses to overwrite, and changes nothing.
	if err := os.WriteFile(main, []byte("# edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = run(t, srv, "", "migrate", "terraform", "mig_1")
	if r.err == nil || !strings.Contains(r.err.Error(), "--force") {
		t.Fatalf("err = %v", r.err)
	}
	if data, _ := os.ReadFile(main); string(data) != "# edited\n" {
		t.Fatalf("overwrote main.tf: %q", data)
	}

	r = run(t, srv, "", "migrate", "terraform", "mig_1", "--force")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if data, _ := os.ReadFile(main); !strings.Contains(string(data), "pantechdynamics_vm") {
		t.Fatalf("--force did not overwrite: %q", data)
	}

	out := filepath.Join(t.TempDir(), "infra")
	r = run(t, srv, "", "migrate", "terraform", "mig_1", "--out", out, "-q")
	if r.err != nil || r.stdout != filepath.Join(out, "main.tf")+"\n"+filepath.Join(out, "variables.tf")+"\n" {
		t.Fatalf("stdout %q, err %v", r.stdout, r.err)
	}
}

func TestMigrateTerraformRefusesBadNames(t *testing.T) {
	for _, files := range []string{
		`[{"name":"../evil.tf","content":"x"}]`,
		`[{"name":"a/b.tf","content":"x"}]`,
		`[{"name":".tf","content":"x"}]`,
		`[{"name":"main.tf.sh","content":"x"}]`,
		`[{"name":"main tf.tf","content":"x"}]`,
		`[{"name":"main.tf","content":"x"},{"name":"main.tf","content":"y"}]`,
		`[{"name":"main.tf","content":"x"},{"name":"bad\u001b.tf","content":"y"}]`,
	} {
		f, srv := newFakeAPI(t)
		f.on("GET /migrations/mig_1/terraform", 200, `{"files":`+files+`}`)
		dir := t.TempDir()
		r := run(t, srv, "", "migrate", "terraform", "mig_1", "--out", filepath.Join(dir, "tf"))
		if r.err == nil || !strings.Contains(r.err.Error(), "nothing was written") {
			t.Fatalf("%s: err = %v", files, r.err)
		}
		if strings.Contains(r.err.Error(), "\x1b") {
			t.Fatalf("%s: control character in the error", files)
		}
		if _, err := os.Stat(filepath.Join(dir, "tf")); err == nil {
			t.Fatalf("%s: wrote something", files)
		}
		if _, err := os.Stat(filepath.Join(dir, "evil.tf")); err == nil {
			t.Fatal("wrote outside the directory")
		}
	}
}

func TestMigrateTerraformRefusesSymlinksEvenWithForce(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /migrations/mig_1/terraform", 200, `{"files":[{"name":"main.tf","content":"new"},{"name":"variables.tf","content":"new"}]}`)
	dir := t.TempDir()
	out := filepath.Join(dir, "tf")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(out, "variables.tf")); err != nil {
		t.Fatal(err)
	}
	r := run(t, srv, "", "migrate", "terraform", "mig_1", "--out", out, "--force")
	if r.err == nil || !strings.Contains(r.err.Error(), "symbolic link") {
		t.Fatalf("err = %v", r.err)
	}
	if data, _ := os.ReadFile(victim); string(data) != "keep" {
		t.Fatalf("wrote through the link: %q", data)
	}
	// Checked before writing: main.tf, first in the list, was not written either.
	if _, err := os.Stat(filepath.Join(out, "main.tf")); err == nil {
		t.Fatal("wrote main.tf before refusing")
	}
}

func TestMigrateTerraformDefaultDirNeedsAMigrationID(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /migrations/..", 200, `{"files":[{"name":"main.tf","content":"x"}]}`)
	f.on("GET /migrations/mig_1/terraform", 200, `{"files":[{"name":"main.tf","content":"x"}]}`)
	t.Chdir(t.TempDir())
	r := run(t, srv, "", "migrate", "terraform", "../x")
	if r.err == nil {
		t.Fatal("accepted a path as a migration id")
	}
	entries, _ := os.ReadDir(".")
	if len(entries) != 0 {
		t.Fatalf("wrote %v", entries)
	}
}

func TestMigrateDeleteAsksFirst(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /migrations/mig_1", 200, migrationBody)
	f.on("DELETE /migrations/mig_1", 204, "")

	r := run(t, srv, "", "migrate", "delete", "mig_1")
	if r.err == nil || !strings.Contains(r.err.Error(), "--yes") {
		t.Fatalf("err = %v", r.err)
	}
	if len(f.sent("DELETE", "/migrations/mig_1")) != 0 {
		t.Fatal("deleted without asking")
	}

	r = run(t, srv, "", "migrate", "delete", "mig_1", "-y")
	if r.err != nil || len(f.sent("DELETE", "/migrations/mig_1")) != 1 {
		t.Fatalf("err %v", r.err)
	}
}

func TestMigrateDisconnect(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /cloud-connections/ccn_1", 200, connOK)
	f.on("DELETE /cloud-connections/ccn_1", 204, "")

	r := run(t, srv, "", "migrate", "disconnect", "ccn_1")
	if r.err == nil || len(f.sent("DELETE", "/cloud-connections/ccn_1")) != 0 {
		t.Fatalf("disconnected without asking: %v", r.err)
	}
	r = run(t, srv, "", "migrate", "disconnect", "ccn_1", "--yes")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !strings.Contains(r.stderr, "delete the CloudFormation stack") {
		t.Fatalf("stderr %q", r.stderr)
	}
}

func TestMigrateListsAndScanInventory(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /cloud-connections", 200, `{"data":[`+connOK+`],"next_cursor":null}`)
	f.on("GET /cloud-connections/ccn_1/scans", 200, `{"data":[`+scanDone+`],"next_cursor":null}`)
	f.on("GET /migrations", 200, `{"data":[`+strings.Replace(migrationBody, `"items":[`, `"items_":[`, 1)+`],"next_cursor":null}`)
	inv := strings.Replace(scanDone, `"inventory":null`, `"inventory":{"instances":[{"id":"i-0abc","name":"web-1","region":"us-east-1","instance_type":"t3.medium","state":"running"}],"buckets":[{"name":"assets","region":"us-east-1","created_at":null}]}`, 1)
	f.on("GET /cloud-scans/csn_1", 200, inv)

	r := run(t, srv, "", "migrate", "connections")
	if r.err != nil || !strings.Contains(r.stdout, "Production AWS") || !strings.Contains(r.stdout, "connected") {
		t.Fatalf("stdout %q, err %v", r.stdout, r.err)
	}
	r = run(t, srv, "", "migrate", "scans", "ccn_1")
	if r.err != nil || !strings.Contains(r.stdout, "USD 1,234.56/month") {
		t.Fatalf("stdout %q, err %v", r.stdout, r.err)
	}
	r = run(t, srv, "", "migrate", "plans", "-q")
	if r.err != nil || r.stdout != "mig_1\n" {
		t.Fatalf("stdout %q, err %v", r.stdout, r.err)
	}
	r = run(t, srv, "", "migrate", "scan-show", "csn_1", "--inventory")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if calls := f.sent("GET", "/cloud-scans/csn_1"); len(calls) != 1 || calls[0].Query != "include=inventory" {
		t.Fatalf("calls %+v", calls)
	}
	for _, want := range []string{"t3.medium · running", "assets"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}
}

func TestMigrateScanAsksFirst(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /cloud-connections/ccn_1/scans", 202, scanQueued)
	r := run(t, srv, "", "migrate", "scan", "ccn_1")
	if r.err == nil || !strings.Contains(r.err.Error(), "USD 0.01") || !strings.Contains(r.err.Error(), "--yes") {
		t.Fatalf("err = %v", r.err)
	}
	if len(f.sent("POST", "/cloud-connections/ccn_1/scans")) != 0 {
		t.Fatal("scanned without asking")
	}
}

func TestMigrateRenameConnection(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("PATCH /cloud-connections/ccn_1", 200, strings.Replace(connOK, `"name":"Production AWS"`, `"name":"Prod"`, 1))
	r := run(t, srv, "", "migrate", "rename-connection", "ccn_1", "--name", "Prod")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.sentJSON("PATCH", "/cloud-connections/ccn_1"); !reflect.DeepEqual(got, map[string]any{"name": "Prod"}) {
		t.Fatalf("body = %v", got)
	}
	if !strings.Contains(r.stderr, "Renamed ccn_1 to Prod") {
		t.Fatalf("stderr %q", r.stderr)
	}
	r = run(t, srv, "", "migrate", "rename-connection", "ccn_1")
	if r.err == nil || len(f.sent("PATCH", "/cloud-connections/ccn_1")) != 1 {
		t.Fatalf("without --name: err = %v", r.err)
	}
}

func TestMigrateConnectQuickCreateURLMustBeTheAWSConsole(t *testing.T) {
	for _, c := range []struct {
		url  string
		safe bool
	}{
		{"https://console.aws.amazon.com/cloudformation/home#/stacks/quickcreate", true},
		{"https://us-east-1.console.aws.amazon.com/cloudformation/home?region=us-east-1#/stacks/quickcreate", true},
		{"http://console.aws.amazon.com/cloudformation", false},
		{"https://evil.example.com/cloudformation", false},
		{"https://console.aws.amazon.com.evil.example/x", false},
		{"https://evilconsole.aws.amazon.com/x", false},
		{"https://user:pw@console.aws.amazon.com/x", false},
		{"https://console.aws.amazon.com:8443/x", false},
		{"-a Calculator https://console.aws.amazon.com/", false},
		{"javascript:alert(1)", false},
		{`https://console.aws.amazon.com/\u001b]8;;https://evil/\u0007x`, false},
	} {
		t.Run(c.url, func(t *testing.T) {
			f, srv := newFakeAPI(t)
			body := strings.Replace(connAwaiting, `"https://console.aws.amazon.com/cloudformation/home#/stacks/quickcreate?stackName=pantech"`, `"`+c.url+`"`, 1)
			f.on("POST /cloud-connections", 201, body)
			opened := stubBrowser(t, true)
			r := run(t, srv, "", "migrate", "connect", "--name", "x", "--template-out", filepath.Join(t.TempDir(), "t.json"))
			if r.err != nil {
				t.Fatal(r.err)
			}
			shown := strings.Contains(r.stderr, "one click") || strings.Contains(r.stderr, "Opened AWS")
			if c.safe != (len(*opened) == 1) || c.safe != shown {
				t.Fatalf("safe=%v, opened %v, stderr %q", c.safe, *opened, r.stderr)
			}
			if !c.safe && !strings.Contains(r.stderr, "in the CloudFormation console of your AWS account") {
				t.Fatalf("no fallback: %q", r.stderr)
			}
		})
	}
}

func TestMigrateConnectTemplateFileSafety(t *testing.T) {
	t.Run("refuses an existing --template-out before creating anything", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("POST /cloud-connections", 201, connAwaiting)
		stubBrowser(t, false)
		out := filepath.Join(t.TempDir(), "role.json")
		if err := os.WriteFile(out, []byte("mine"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := run(t, srv, "", "migrate", "connect", "--name", "x", "--template-out", out)
		if r.err == nil || !strings.Contains(r.err.Error(), "--force") {
			t.Fatalf("err = %v", r.err)
		}
		if len(f.sent("POST", "/cloud-connections")) != 0 {
			t.Fatal("created a connection anyway")
		}
		if data, _ := os.ReadFile(out); string(data) != "mine" {
			t.Fatalf("overwrote: %q", data)
		}
		r = run(t, srv, "", "migrate", "connect", "--name", "x", "--template-out", out, "--force")
		if r.err != nil {
			t.Fatal(r.err)
		}
		if data, _ := os.ReadFile(out); !strings.Contains(string(data), "AWSTemplateFormatVersion") {
			t.Fatalf("--force did not overwrite: %q", data)
		}
		entries, _ := os.ReadDir(filepath.Dir(out))
		if len(entries) != 1 {
			t.Fatalf("left a temporary file: %v", entries)
		}
	})
	t.Run("never writes through a symlink, even with --force", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("POST /cloud-connections", 201, connAwaiting)
		stubBrowser(t, false)
		dir := t.TempDir()
		victim := filepath.Join(dir, "victim")
		if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "role.json")
		if err := os.Symlink(victim, link); err != nil {
			t.Fatal(err)
		}
		r := run(t, srv, "", "migrate", "connect", "--name", "x", "--template-out", link, "--force")
		if r.err == nil || !strings.Contains(r.err.Error(), "symbolic link") {
			t.Fatalf("err = %v", r.err)
		}
		if data, _ := os.ReadFile(victim); string(data) != "keep" {
			t.Fatalf("wrote through the link: %q", data)
		}
	})
	t.Run("the default file exists", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("POST /cloud-connections", 201, connAwaiting)
		stubBrowser(t, false)
		t.Chdir(t.TempDir())
		if err := os.WriteFile("pantech-migrate-ccn_1.json", []byte("mine"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := run(t, srv, "", "migrate", "connect", "--name", "x")
		if r.err == nil || !strings.Contains(r.err.Error(), "was created, but its template was not saved") {
			t.Fatalf("err = %v", r.err)
		}
		if data, _ := os.ReadFile("pantech-migrate-ccn_1.json"); string(data) != "mine" {
			t.Fatalf("overwrote: %q", data)
		}
	})
	t.Run("an unexpected id never names a file", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("POST /cloud-connections", 201, strings.Replace(connAwaiting, `"id":"ccn_1"`, `"id":"../../etc/x"`, 1))
		stubBrowser(t, false)
		dir := t.TempDir()
		t.Chdir(dir)
		r := run(t, srv, "", "migrate", "connect", "--name", "x")
		if r.err == nil || !strings.Contains(r.err.Error(), "unexpected connection id") {
			t.Fatalf("err = %v", r.err)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatalf("wrote %v", entries)
		}
	})
}

func TestMigrateOutputStripsControlCharacters(t *testing.T) {
	f, srv := newFakeAPI(t)
	evil := strings.NewReplacer(`"name":"web-1"`, `"name":"web\u001b[2J\u001b]0;pwned\u0007-1\u009b"`,
		`"GPU servers are not offered yet."`, `"GPU\r\u001b[1Aservers"`).Replace(migrationBody)
	f.on("GET /migrations/mig_1", 200, evil)
	f.on("GET /cloud-connections/ccn_1", 200, strings.Replace(connOK, `"name":"Production AWS"`, `"name":"Prod\u001b[31m\u0007"`, 1))

	r := run(t, srv, "", "migrate", "show", "mig_1")
	if r.err != nil {
		t.Fatal(r.err)
	}
	for _, bad := range []string{"\x1b", "\x07", "\r", "\u009b"} {
		if strings.Contains(r.stdout+r.stderr, bad) {
			t.Fatalf("output has %q:\n%q", bad, r.stdout)
		}
	}
	if !strings.Contains(r.stdout, "web[2J]0;pwned-1") || !strings.Contains(r.stdout, "GPU[1Aservers") {
		t.Fatalf("stdout %q", r.stdout)
	}
	r = run(t, srv, "", "migrate", "connection", "ccn_1")
	if r.err != nil || strings.ContainsAny(r.stdout, "\x1b\x07") || !strings.Contains(r.stdout, "Prod[31m") {
		t.Fatalf("stdout %q, err %v", r.stdout, r.err)
	}

	// --json stays the API's body, escapes and all.
	r = run(t, srv, "", "migrate", "show", "mig_1", "--json")
	if r.err != nil || !strings.Contains(r.stdout, `\u001b[2J`) {
		t.Fatalf("stdout %q, err %v", r.stdout, r.err)
	}
}
