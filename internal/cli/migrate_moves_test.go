package cli

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
)

const (
	connGCP = `{"id":"ccn_2","provider":"gcp","name":"Production GCP","status":"awaiting_role","failure":null,"role_arn":null,"aws_account_id":null,"external_account_id":null,"agent":null,"verified_at":null,"created_at":"2026-11-25T10:00:00Z","updated_at":"2026-11-25T10:00:00Z",
"setup":{"access":"federated","issuer_url":"https://id.pantechdynamics.com","audience":"pantech-migrate","subject":"ccn_2","quick_create_url":null,"template":"gcloud iam workload-identity-pools create pantech-migrate","template_format":"shell","files":[{"name":"main.tf","content":"terraform {}"}],"permissions":["compute.instances.list"],"agent":null}}`
	connAgent = `{"id":"ccn_3","provider":"hetzner","name":"Hetzner","status":"awaiting_agent","failure":null,"role_arn":null,"aws_account_id":null,"external_account_id":null,"agent":null,"verified_at":null,"created_at":"2026-11-25T10:00:00Z","updated_at":"2026-11-25T10:00:00Z",
"setup":{"access":"agent","issuer_url":null,"audience":null,"subject":"ccn_3","quick_create_url":null,"template":"Make a read-only project token.","template_format":"agent","files":[],"permissions":["read"],
"agent":{"image":"agent:1","download_url":null,"api_url":"https://api.pantechdynamics.com","token_variables":["HCLOUD_TOKEN"],"token_how_to":"Security > API tokens > Read","enroll_command":"pantech-migrate-agent enroll <ENROLLMENT_CODE>","run_command":"pantech-migrate-agent run","container_command":"run agent:1"}}}`
	bucketImportBody = `{"id":"bim_1","connection_id":"ccn_1","migration_id":null,"source":{"provider":"aws","bucket":"logs","region":"us-east-1"},"destination":{"bucket_id":"bkt_1","bucket_name":"acme-logs","public_read":false},
"status":"%s","failure":null,"cancel_requested":false,"pass":1,"progress":{"objects_copied":10,"objects_skipped":0,"objects_failed":1,"bytes_copied":2048,"bytes_skipped":0},"total_bytes_copied":2048,"mirror_deletes":false,"objects_deleted":0,
"created_at":"2026-11-25T10:00:00Z","finished_at":null,"failures":[{"key":"big.bin","reason":"object_too_large","message":"Too large to copy.","at":"2026-11-25T10:01:00Z"}],"events":[]}`
	serverCopyBody = `{"id":"sco_1","migration_id":"mig_1","item_key":"instance:us-east-1:i-0abc","connection_id":"ccn_1","source":"agent","status":"%s","failure":null,"os_family":"linux","boot_mode":null,
"instance":{"name":"web-1","plan_slug":"standard-2","region":"ng-lagos-1","subnet_id":null,"security_group_id":"sg_web","ssh_key_id":null,"id":null,"order_id":null},
"disks":[{"index":0,"role":"root","source":"vol-1","size_bytes":32212254720,"snapshot_id":null,"volume_id":null}],"flags":[],"pass":{"number":1,"kind":"full","started_at":null},"pending_pass":null,
"progress":{"chunks_total":10,"chunks_done":10,"bytes_total":32212254720,"bytes_uploaded":32212254720,"wire_bytes":100,"eta_seconds":null},"synced_at":null,"expires_at":"2026-12-25T10:00:00Z","completed_at":null,"created_at":"2026-11-25T10:00:00Z","updated_at":"2026-11-25T10:00:00Z"}`
	kubeMoveBody = `{"id":"kmv_1","migration_id":"mig_1","item_key":"kubernetes_cluster:us-east-1:prod","connection_id":"ccn_1","cluster_id":"k8s_1","source_name":"prod","status":"%s","failure":null,
"options":{"include_secrets":false,"copy_volumes":true,"publish_load_balancers":true,"namespaces":[]},"summary":{"objects":12,"refused":1,"skipped":0,"flagged":0,"volumes":1,"warnings":[],"addons":[]},
"allow_privileged":[],"previewed_at":null,"preview_ok":false,"applied_at":null,"completed_at":null,"sync_generation":0,"created_at":"2026-11-25T10:00:00Z","updated_at":"2026-11-25T10:00:00Z",
"resources":[{"group":"apps","kind":"Deployment","namespace":"web","name":"agent","api_version":"apps/v1","tier":6,"action":"create","flags":[],"policy_refused":true,"preview":null,"apply":null}],"volumes":[],"images":[],"holds":[]}`
)

func body(tmpl, status string) string { return strings.Replace(tmpl, "%s", status, 1) }

func TestMigrateConnectGCPSavesTheSetupAndSaysWhatNext(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /cloud-connections", 201, connGCP)
	t.Chdir(t.TempDir())

	r := run(t, srv, "", "migrate", "connect", "--provider", "gcp", "--name", "Production GCP")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got, want := f.sentJSON("POST", "/cloud-connections"), map[string]any{"provider": "gcp", "name": "Production GCP"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %v, want %v", got, want)
	}
	for file, want := range map[string]string{"pantech-migrate-ccn_2.sh": "gcloud iam", "pantech-migrate-ccn_2-main.tf": "terraform {}"} {
		data, err := os.ReadFile(file)
		if err != nil || !strings.Contains(string(data), want) {
			t.Fatalf("%s = %q, %v", file, data, err)
		}
	}
	if !strings.Contains(r.stderr, "pantech migrate set-federation ccn_2 --gcp-project-number") || !strings.Contains(r.stderr, "compute.instances.list") {
		t.Fatalf("stderr %q", r.stderr)
	}
}

func TestMigrateSetFederationSendsOneCloud(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("PATCH /cloud-connections/ccn_2", 200, strings.Replace(connGCP, "awaiting_role", "verifying", 1))
	r := run(t, srv, "", "migrate", "set-federation", "ccn_2", "--gcp-project-number", "123456789012", "--gcp-pool-id", "pantech-migrate",
		"--gcp-provider-id", "ccn-2", "--gcp-service-account", "sa@web.iam.gserviceaccount.com", "--gcp-project", "web", "--gcp-project", "api")
	if r.err != nil {
		t.Fatal(r.err)
	}
	want := map[string]any{"gcp": map[string]any{"project_number": "123456789012", "pool_id": "pantech-migrate", "provider_id": "ccn-2",
		"service_account_email": "sa@web.iam.gserviceaccount.com", "project_ids": []any{"web", "api"}}}
	if got := f.sentJSON("PATCH", "/cloud-connections/ccn_2"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %v, want %v", got, want)
	}

	r = run(t, srv, "", "migrate", "set-federation", "ccn_2", "--gcp-pool-id", "p", "--azure-tenant-id", "t")
	if r.err == nil || !IsUsage(r.err) {
		t.Fatalf("two clouds were accepted: %v", r.err)
	}
	r = run(t, srv, "", "migrate", "set-federation", "ccn_2", "--huawei-idp-id", "idp", "--huawei-project", "af-south-1")
	if r.err == nil || !strings.Contains(r.err.Error(), "REGION=PROJECT_ID") {
		t.Fatalf("a bad --huawei-project was accepted: %v", r.err)
	}
}

func TestMigrateAgentEnrollPrintsTheCommandAloneOnce(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /cloud-connections", 201, connAgent).
		on("POST /cloud-connections/ccn_3/agent-enrollments", 503, `{"status":503,"code":"SERVICE_UNAVAILABLE"}`).
		on("POST /cloud-connections/ccn_3/agent-enrollments", 201, `{"code":"pme_secret","expires_at":"2026-11-25T11:00:00Z","enroll_command":"pantech-migrate-agent enroll pme_secret"}`)
	t.Chdir(t.TempDir())

	r := run(t, srv, "", "migrate", "connect", "--provider", "hetzner", "--name", "Hetzner")
	if r.err != nil || !strings.Contains(r.stderr, "pantech migrate agent enroll ccn_3") {
		t.Fatalf("connect: %v %q", r.err, r.stderr)
	}
	if data, err := os.ReadFile("pantech-migrate-ccn_3.md"); err != nil || !strings.Contains(string(data), "read-only project token") {
		t.Fatalf("setup = %q, %v", data, err)
	}

	// Each call issues a new code, so a failed one is not retried.
	if r = run(t, srv, "", "migrate", "agent", "enroll", "ccn_3"); r.err == nil {
		t.Fatal("a 503 was retried into a code")
	}
	if n := len(f.sent("POST", "/cloud-connections/ccn_3/agent-enrollments")); n != 1 {
		t.Fatalf("enrollment sent %d times, want 1", n)
	}
	r = run(t, srv, "", "migrate", "agent", "enroll", "ccn_3")
	if r.err != nil || r.stdout != "pantech-migrate-agent enroll pme_secret\n" || strings.Contains(r.stderr, "pme_secret") {
		t.Fatalf("stdout %q stderr %q err %v", r.stdout, r.stderr, r.err)
	}
}

func TestMigrateAgentRevokeAndInviteAskFirst(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("DELETE /cloud-connections/ccn_3/agent", 200, connAgent).
		on("POST /migrations/mig_1/invites", 200, `{"results":[{"key":"user:global:ada","email":"ada@example.com","role":"member","outcome":"invited","reason":null,"invite_id":"inv_1"},
{"key":"user:global:bob","email":null,"role":null,"outcome":"skipped","reason":"email_missing","invite_id":null}],"migration":{"id":"mig_1"}}`)
	if r := run(t, srv, "", "migrate", "agent", "revoke", "ccn_3"); r.err == nil || len(f.sent("DELETE", "/cloud-connections/ccn_3/agent")) != 0 {
		t.Fatalf("revoked without asking: %v", r.err)
	}
	if r := run(t, srv, "", "migrate", "agent", "revoke", "ccn_3", "--yes"); r.err != nil || !strings.Contains(r.stderr, "Revoked the agent of ccn_3") {
		t.Fatalf("revoke: %v %q", r.err, r.stderr)
	}
	r := run(t, srv, "", "migrate", "invite", "mig_1", "--key", "user:global:ada", "--key", "user:global:bob", "--yes")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.sentJSON("POST", "/migrations/mig_1/invites"); !reflect.DeepEqual(got, map[string]any{"keys": []any{"user:global:ada", "user:global:bob"}}) {
		t.Fatalf("body = %v", got)
	}
	if !strings.Contains(r.stdout, "invited") || !strings.Contains(r.stdout, "skipped (email_missing)") {
		t.Fatalf("stdout %q", r.stdout)
	}
}

func TestMigrateBucketsAccessStartAndResync(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("PUT /cloud-connections/ccn_1/copy-access", 200, `{"connection_id":"ccn_1","status":"awaiting_role","failure":null,"role_arn":null,"buckets":[{"name":"logs","region":"us-east-1","versioning":"off","ready":false,"failure":null}],"verified_at":null,
"setup":{"issuer_url":"https://id.pantechdynamics.com","audience":"pantech-migrate","subject":"ccn_1:copy","template":"{\"Resources\":{}}"}}`).
		on("POST /bucket-imports", 202, body(bucketImportBody, "queued")).
		on("GET /bucket-imports/bim_1", 200, body(bucketImportBody, "running")).
		on("GET /bucket-imports/bim_1", 200, body(bucketImportBody, "succeeded")).
		on("POST /bucket-imports/bim_1/resync", 202, body(bucketImportBody, "queued"))
	t.Chdir(t.TempDir())

	r := run(t, srv, "", "migrate", "buckets", "access", "ccn_1", "--bucket", "logs")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.sentJSON("PUT", "/cloud-connections/ccn_1/copy-access"); !reflect.DeepEqual(got, map[string]any{"buckets": []any{"logs"}}) {
		t.Fatalf("body = %v", got)
	}
	if data, err := os.ReadFile("pantech-migrate-ccn_1-copy.json"); err != nil || string(data) != "{\"Resources\":{}}\n" {
		t.Fatalf("template = %q, %v", data, err)
	}

	if r = run(t, srv, "", "migrate", "buckets", "start", "--connection", "ccn_1", "--source", "logs", "--to-new", "acme-logs"); r.err == nil {
		t.Fatal("a new bucket was made without asking")
	}
	r = run(t, srv, "", "migrate", "buckets", "start", "--connection", "ccn_1", "--source", "logs", "--to-new", "acme-logs", "--yes")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got, want := f.sentJSON("POST", "/bucket-imports"), map[string]any{"connection_id": "ccn_1", "source_bucket": "logs", "destination_bucket_name": "acme-logs"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %v, want %v", got, want)
	}
	r = run(t, srv, "", "migrate", "buckets", "show", "bim_1", "--wait")
	if r.err != nil || len(f.sent("GET", "/bucket-imports/bim_1")) != 2 || !strings.Contains(r.stdout, "big.bin") {
		t.Fatalf("show --wait: %v %q", r.err, r.stdout)
	}
	if r = run(t, srv, "", "migrate", "buckets", "resync", "bim_1", "--mirror-deletes"); r.err == nil || len(f.sent("POST", "/bucket-imports/bim_1/resync")) != 0 {
		t.Fatalf("mirror deletes without asking: %v", r.err)
	}
	if r = run(t, srv, "", "migrate", "buckets", "resync", "bim_1", "--mirror-deletes", "--yes"); r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.sentJSON("POST", "/bucket-imports/bim_1/resync"); !reflect.DeepEqual(got, map[string]any{"mirror_deletes": true}) {
		t.Fatalf("body = %v", got)
	}
}

func TestMigrateServersStartAndCutover(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /migrations/mig_1/server-copies", 201, body(serverCopyBody, "waiting")).
		on("POST /server-copies/sco_1/cutover", 202, body(serverCopyBody, "importing")).
		on("GET /server-copies/sco_1", 200, body(serverCopyBody, "copying")).
		on("GET /server-copies/sco_1", 200, strings.Replace(body(serverCopyBody, "failed"), `"failure":null`, `"failure":{"code":"agent_offline","message":"Start the agent."}`, 1))

	r := run(t, srv, "", "migrate", "servers", "start", "mig_1", "--key", "instance:us-east-1:i-0abc", "--security-group", "sg_web",
		"--source", "aws_snapshot", "--copy-role-arn", "arn:aws:iam::123456789012:role/Copy", "--snapshot", "vol-1=snap-1")
	if r.err != nil {
		t.Fatal(r.err)
	}
	want := map[string]any{"item_key": "instance:us-east-1:i-0abc", "security_group_id": "sg_web", "source": "aws_snapshot",
		"aws": map[string]any{"copy_role_arn": "arn:aws:iam::123456789012:role/Copy", "snapshots": map[string]any{"vol-1": "snap-1"}}}
	if got := f.sentJSON("POST", "/migrations/mig_1/server-copies"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %v, want %v", got, want)
	}
	if r = run(t, srv, "", "migrate", "servers", "start", "mig_1", "--key", "k", "--create-snapshots"); r.err == nil || !IsUsage(r.err) {
		t.Fatalf("--create-snapshots without aws_snapshot: %v", r.err)
	}
	if r = run(t, srv, "", "migrate", "servers", "cutover", "sco_1"); r.err == nil || len(f.sent("POST", "/server-copies/sco_1/cutover")) != 0 {
		t.Fatalf("cut over without asking: %v", r.err)
	}
	if r = run(t, srv, "", "migrate", "servers", "cutover", "sco_1", "--yes"); r.err != nil || !strings.Contains(r.stdout, "importing") {
		t.Fatalf("cutover: %v %q", r.err, r.stdout)
	}
	r = run(t, srv, "", "migrate", "servers", "show", "sco_1", "--wait")
	var failed *api.MigrateFailed
	if !errors.As(r.err, &failed) || failed.Failure.Code != "agent_offline" {
		t.Fatalf("a failed copy: %v", r.err)
	}
}

func TestMigrateDatabasesAndKubernetes(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /migrations/mig_1/database-imports", 201, `{"id":"dbi_1","migration_id":"mig_1","item_key":"database:us-east-1:orders","destination_database_id":"db_1","target_database":"orders","mode":"replicate","source_ref":"prod-db","engine":"postgres","status":"awaiting_agent","failure":null,"agent_connected":false,"lag_bytes":null,"readiness":null,"tables":null,"created_at":"2026-11-25T10:00:00Z","finished_at":null}`).
		on("POST /database-imports/dbi_1/revoke-access", 200, `{"id":"dbi_1","status":"replicating"}`).
		on("POST /migrations/mig_1/kubernetes-moves", 202, body(kubeMoveBody, "exporting")).
		on("POST /kubernetes-moves/kmv_1/preview", 202, body(kubeMoveBody, "previewing")).
		on("POST /kubernetes-moves/kmv_1/cutover", 202, body(kubeMoveBody, "cutting_over"))

	r := run(t, srv, "", "migrate", "databases", "start", "mig_1", "--key", "database:us-east-1:orders", "--into", "db_1", "--source-ref", "prod-db", "--mode", "replicate")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got, want := f.sentJSON("POST", "/migrations/mig_1/database-imports"), map[string]any{"item_key": "database:us-east-1:orders", "destination_database_id": "db_1", "source_ref": "prod-db", "mode": "replicate"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %v, want %v", got, want)
	}
	if r = run(t, srv, "", "migrate", "databases", "revoke-access", "dbi_1"); r.err != nil || len(f.sent("POST", "/database-imports/dbi_1/revoke-access")) != 1 {
		t.Fatalf("revoke-access: %v", r.err)
	}

	r = run(t, srv, "", "migrate", "k8s", "start", "mig_1", "--key", "kubernetes_cluster:us-east-1:prod", "--cluster", "k8s_1", "--copy-volumes", "--namespace", "web")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got, want := f.sentJSON("POST", "/migrations/mig_1/kubernetes-moves"), map[string]any{"item_key": "kubernetes_cluster:us-east-1:prod", "cluster_id": "k8s_1",
		"include_secrets": false, "copy_volumes": true, "publish_load_balancers": true, "namespaces": []any{"web"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %v, want %v", got, want)
	}
	if !strings.Contains(r.stdout, "web/agent") || !strings.Contains(r.stdout, "refused by the cluster policy") {
		t.Fatalf("stdout %q", r.stdout)
	}
	if r = run(t, srv, "", "migrate", "k8s", "preview", "kmv_1", "--allow", "apps/Deployment/web/agent"); r.err != nil {
		t.Fatal(r.err)
	}
	wantAllow := map[string]any{"allow_privileged": []any{map[string]any{"group": "apps", "kind": "Deployment", "namespace": "web", "name": "agent"}}}
	if got := f.sentJSON("POST", "/kubernetes-moves/kmv_1/preview"); !reflect.DeepEqual(got, wantAllow) {
		t.Fatalf("body = %v", got)
	}
	if r = run(t, srv, "", "migrate", "k8s", "cutover", "kmv_1", "--yes"); r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.sentJSON("POST", "/kubernetes-moves/kmv_1/cutover"); !reflect.DeepEqual(got, map[string]any{"source_writes_stopped": true}) {
		t.Fatalf("body = %v", got)
	}
}
