package cli

import (
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
)

const (
	namespaceBody = `{"name":"acme","registry_host":"registry.pantechdynamics.com","create_on_push":true,"repository_count":1,"storage_bytes":1048576000,
"storage_limit_bytes":53687091200,"repository_limit":100,"created_at":"2026-11-10T09:00:00Z","updated_at":"2026-11-10T09:00:00Z","version":2}`
	repoBody = `{"id":"crr_1","namespace":"acme","name":"api/server","full_name":"registry.pantechdynamics.com/acme/api/server","description":"",
"visibility":"private","immutable_tags":false,"retention":{"keep_last":50,"untagged_after_days":7,"keep_pattern":"v*"},"tag_count":31,
"storage_bytes":1048576000,"last_pushed_at":"2026-11-10T10:00:00Z","status":"active","suspended_reason":null,"created_at":"2026-11-01T09:00:00Z",
"updated_at":"2026-11-10T10:00:00Z","version":4}`
	tagBody = `{"name":"v1.4.2","digest":"sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08","media_type":"application/vnd.oci.image.index.v1+json",
"size_bytes":48211234,"platforms":["linux/amd64","linux/arm64"],"pushed_at":"2026-11-10T10:00:00Z","last_pulled_at":null,"immutable":false}`
	registryTokenBody = `{"id":"prt_1","name":"github-actions","permission":"push","repositories":["api/server"],"expires_at":null,"last_used_at":null,
"created_at":"2026-11-10T09:00:00Z","revoked_at":null,"secret":"prt_1_s3cretvalue"}`
)

// reposByName answers the lookup a repository named by name makes.
func reposByName(f *fakeAPI) {
	f.on("GET /registry/repositories", 200, `{"data":[`+repoBody+`,{"id":"crr_2","namespace":"acme","name":"web/server","status":"active"},
{"id":"crr_3","namespace":"acme","name":"api/server","status":"deleting"}],"next_cursor":null}`)
}

func TestRegistryNamespaceShowsUsage(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /registry", 200, namespaceBody)
	for _, args := range [][]string{{"registry", "namespace"}, {"registry", "namespace", "get"}} {
		r := run(t, srv, "", args...)
		if r.err != nil || !strings.Contains(r.stdout, "registry.pantechdynamics.com/acme") || !strings.Contains(r.stdout, "1 of 100") || !strings.Contains(r.stdout, "50.0 GiB") {
			t.Fatalf("%v: stdout %q err %v", args, r.stdout, r.err)
		}
	}
}

func TestRegistryNamespaceNotSetSaysWhatToDo(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /registry", 404, `{"status":404,"code":"REGISTRY_NAMESPACE_NOT_SET","detail":"Claim a namespace first."}`)
	r := run(t, srv, "", "registry", "namespace")
	if !api.IsCode(r.err, "REGISTRY_NAMESPACE_NOT_SET") || !strings.Contains(r.err.Error(), "pantech registry namespace set") {
		t.Fatalf("err %v", r.err)
	}
}

func TestRegistryNamespaceSet(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("PUT /registry", 200, namespaceBody)
	if r := run(t, srv, "", "registry", "namespace", "set", "acme", "--create-on-push=false"); r.err != nil {
		t.Fatal(r.err)
	}
	if got, want := f.sentJSON("PUT", "/registry"), map[string]any{"name": "acme", "create_on_push": false}; !reflect.DeepEqual(got, want) {
		t.Fatalf("body %v", got)
	}
}

func TestRegistryReposByName(t *testing.T) {
	f, srv := newFakeAPI(t)
	reposByName(f)
	f.on("GET /registry/repositories/crr_1", 200, repoBody)
	for _, ref := range []string{"api/server", "acme/api/server", "registry.pantechdynamics.com/acme/api/server:v1"} {
		r := run(t, srv, "", "registry", "repos", "get", ref)
		if r.err != nil || !strings.Contains(r.stdout, "keep the newest 50 tags") {
			t.Fatalf("%s: stdout %q err %v", ref, r.stdout, r.err)
		}
	}
	if q := f.sent("GET", "/registry/repositories")[0].Query; q != "q=server" {
		t.Fatalf("lookup query %q", q)
	}
	if r := run(t, srv, "", "registry", "repos", "get", "nope"); r.err == nil || !strings.Contains(r.err.Error(), `no repository named "nope"`) {
		t.Fatalf("err %v", r.err)
	}
}

func TestRegistryReposList(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /registry/repositories", 200, `{"data":[`+repoBody+`],"next_cursor":null}`)
	r := run(t, srv, "", "registry", "repos", "list")
	if r.err != nil || !strings.Contains(r.stdout, "api/server") || !strings.Contains(r.stdout, "1000.0 MiB") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	r = run(t, srv, "", "registry", "repos", "list", "--json")
	var items []map[string]any
	if r.err != nil || json.Unmarshal([]byte(r.stdout), &items) != nil || len(items) != 1 {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
}

func TestRegistryReposCreate(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /registry/repositories", 201, repoBody)
	r := run(t, srv, "", "registry", "repos", "create", "api/server", "--keep-last", "50", "--keep-pattern", "v*", "--immutable-tags")
	if r.err != nil {
		t.Fatal(r.err)
	}
	want := map[string]any{"name": "api/server", "immutable_tags": true, "retention": map[string]any{"keep_last": float64(50), "untagged_after_days": nil, "keep_pattern": "v*"}}
	if got := f.sentJSON("POST", "/registry/repositories"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body %v", got)
	}
}

func TestRegistryReposPublicAsksFirst(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /registry/repositories", 201, repoBody)
	r := run(t, srv, "", "registry", "repos", "create", "api/server", "--visibility", "public")
	if r.err == nil || !strings.Contains(r.err.Error(), "Anyone on the internet") || len(f.sent("POST", "/registry/repositories")) != 0 {
		t.Fatalf("err %v", r.err)
	}
	if r := run(t, srv, "", "registry", "repos", "create", "api/server", "--visibility", "nope", "--yes"); !IsUsage(r.err) {
		t.Fatalf("err %v", r.err)
	}
}

func TestRegistryReposUpdateKeepsTheOtherRetentionRules(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /registry/repositories/crr_1", 200, repoBody)
	f.on("PATCH /registry/repositories/crr_1", 200, repoBody)
	if r := run(t, srv, "", "registry", "repos", "update", "crr_1", "--keep-last", "20", "--keep-pattern", ""); r.err != nil {
		t.Fatal(r.err)
	}
	want := map[string]any{"retention": map[string]any{"keep_last": float64(20), "untagged_after_days": float64(7), "keep_pattern": nil}}
	if got := f.sentJSON("PATCH", "/registry/repositories/crr_1"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body %v", got)
	}
}

func TestRegistryReposUpdateNoRetentionAndNothing(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("PATCH /registry/repositories/crr_1", 200, repoBody)
	if r := run(t, srv, "", "registry", "repos", "update", "crr_1", "--no-retention", "--description", "API"); r.err != nil {
		t.Fatal(r.err)
	}
	want := map[string]any{"description": "API", "retention": map[string]any{"keep_last": nil, "untagged_after_days": nil, "keep_pattern": nil}}
	if got := f.sentJSON("PATCH", "/registry/repositories/crr_1"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body %v", got)
	}
	if r := run(t, srv, "", "registry", "repos", "update", "crr_1"); !IsUsage(r.err) {
		t.Fatalf("err %v", r.err)
	}
}

func TestRegistryReposDeleteAsksFirst(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("DELETE /registry/repositories/crr_1", 202, strings.Replace(repoBody, `"active"`, `"deleting"`, 1))
	r := run(t, srv, "", "registry", "repos", "delete", "crr_1")
	if r.err == nil || !strings.Contains(r.err.Error(), "--yes") || len(f.sent("DELETE", "/registry/repositories/crr_1")) != 0 {
		t.Fatalf("err %v", r.err)
	}
	if r := run(t, srv, "", "registry", "repos", "delete", "crr_1", "--yes"); r.err != nil || len(f.sent("DELETE", "/registry/repositories/crr_1")) != 1 {
		t.Fatalf("err %v", r.err)
	}
}

func TestRegistryTagsListGetDelete(t *testing.T) {
	f, srv := newFakeAPI(t)
	reposByName(f)
	f.on("GET /registry/repositories/crr_1/tags", 200, `{"data":[`+tagBody+`],"next_cursor":null}`)
	f.on("GET /registry/repositories/crr_1/tags/v1.4.2", 200, tagBody)
	f.on("DELETE /registry/repositories/crr_1/tags/v1.4.2", 202, ``)

	r := run(t, srv, "", "registry", "tags", "list", "api/server")
	if r.err != nil || !strings.Contains(r.stdout, "v1.4.2") || !strings.Contains(r.stdout, "linux/amd64, linux/arm64") || !strings.Contains(r.stdout, "sha256:9f86d081884c ") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	if r := run(t, srv, "", "registry", "tags", "get", "api/server", "v1.4.2", "-q"); r.err != nil || !strings.HasPrefix(r.stdout, "sha256:9f86") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	if r := run(t, srv, "", "registry", "tags", "delete", "api/server", "v1.4.2"); r.err == nil || len(f.sent("DELETE", "/registry/repositories/crr_1/tags/v1.4.2")) != 0 {
		t.Fatalf("err %v", r.err)
	}
	if r := run(t, srv, "", "registry", "tags", "delete", "api/server", "v1.4.2", "--yes"); r.err != nil || len(f.sent("DELETE", "/registry/repositories/crr_1/tags/v1.4.2")) != 1 {
		t.Fatalf("err %v", r.err)
	}
}

func TestRegistryTagImmutableSaysWhatToDo(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("DELETE /registry/repositories/crr_1/tags/v1", 409, `{"status":409,"code":"TAG_IMMUTABLE","detail":"This repository's tags cannot be deleted."}`)
	r := run(t, srv, "", "registry", "tags", "delete", "crr_1", "v1", "--yes")
	if !api.IsCode(r.err, "TAG_IMMUTABLE") || !strings.Contains(r.err.Error(), "--immutable-tags=false") {
		t.Fatalf("err %v", r.err)
	}
}

func TestRegistryTokensCreatePrintsTheSecretOnce(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /registry/tokens", 201, registryTokenBody)
	r := run(t, srv, "", "registry", "tokens", "create", "--name", "github-actions", "--permission", "push", "--repo", "api/server")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.stdout != "prt_1_s3cretvalue\n" || strings.Contains(r.stderr, "s3cretvalue") || !strings.Contains(r.stderr, "shown once") {
		t.Fatalf("stdout %q stderr %q", r.stdout, r.stderr)
	}
	if got, want := f.sentJSON("POST", "/registry/tokens"), map[string]any{"name": "github-actions", "permission": "push", "repositories": []any{"api/server"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("body %v", got)
	}

	r = run(t, srv, "", "registry", "tokens", "create", "--name", "x", "--permission", "pull", "-q")
	if r.err != nil || r.stdout != "prt_1_s3cretvalue\nprt_1\n" {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	r = run(t, srv, "", "registry", "tokens", "create", "--name", "x", "--permission", "pull", "--json")
	if r.err != nil || strings.Count(r.stdout, "s3cretvalue") != 1 || r.stderr != "" {
		t.Fatalf("stdout %q stderr %q err %v", r.stdout, r.stderr, r.err)
	}
	if r := run(t, srv, "", "registry", "tokens", "create", "--name", "x", "--permission", "admin"); !IsUsage(r.err) {
		t.Fatalf("err %v", r.err)
	}
}

func TestRegistryTokenExpiry(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 30, 0, 0, time.UTC)
	for in, want := range map[string]string{"90d": "2027-02-08T09:30:00Z", "2027-11-10": "2027-11-10T00:00:00Z", "2027-11-10T12:00:00+01:00": "2027-11-10T11:00:00Z"} {
		got, err := parseExpiry(in, now)
		if err != nil || got.Format(time.RFC3339) != want {
			t.Fatalf("%s: %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"0d", "soon", "-5d"} {
		if _, err := parseExpiry(bad, now); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
}

func TestRegistryTokensListAndRevoke(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /registry/tokens", 200, `{"data":[`+strings.Replace(registryTokenBody, `,"secret":"prt_1_s3cretvalue"`, "", 1)+`],"next_cursor":null}`)
	f.on("DELETE /registry/tokens/prt_1", 204, ``)
	r := run(t, srv, "", "registry", "tokens", "list")
	if r.err != nil || !strings.Contains(r.stdout, "github-actions") || !strings.Contains(r.stdout, "never") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	if r := run(t, srv, "", "registry", "tokens", "revoke", "prt_1"); r.err == nil || len(f.sent("DELETE", "/registry/tokens/prt_1")) != 0 {
		t.Fatalf("err %v", r.err)
	}
	if r := run(t, srv, "", "registry", "tokens", "revoke", "prt_1", "--yes"); r.err != nil || len(f.sent("DELETE", "/registry/tokens/prt_1")) != 1 {
		t.Fatalf("err %v", r.err)
	}
}

// fakeEngine stands in for docker and podman: it records the arguments and
// what was written to its stdin.
type engineRun struct {
	path  string
	args  []string
	stdin string
}

func fakeEngine(t *testing.T, installed []string, fail error) *[]engineRun {
	t.Helper()
	oldLook, oldRun := lookPath, runEngine
	t.Cleanup(func() { lookPath, runEngine = oldLook, oldRun })
	lookPath = func(name string) (string, error) {
		for _, n := range installed {
			if n == name {
				return "/fake/bin/" + name, nil
			}
		}
		return "", exec.ErrNotFound
	}
	runs := &[]engineRun{}
	runEngine = func(_ context.Context, path string, args []string, stdin io.Reader, stdout, _ io.Writer) error {
		in, _ := io.ReadAll(stdin)
		*runs = append(*runs, engineRun{path, args, string(in)})
		_, _ = io.WriteString(stdout, "Login Succeeded\n")
		return fail
	}
	return runs
}

func TestRegistryLoginFeedsTheKeyOnStdin(t *testing.T) {
	_, srv := newFakeAPI(t)
	runs := fakeEngine(t, []string{"docker"}, nil)
	r := run(t, srv, "", "registry", "login")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if len(*runs) != 1 {
		t.Fatalf("runs %+v", *runs)
	}
	got := (*runs)[0]
	if got.path != "/fake/bin/docker" || !reflect.DeepEqual(got.args, []string{"login", "registry.pantechdynamics.com", "-u", "pantech", "--password-stdin"}) {
		t.Fatalf("ran %s %v", got.path, got.args)
	}
	if got.stdin != "PAN_test\n" {
		t.Fatalf("stdin %q", got.stdin)
	}
	for _, a := range got.args {
		if strings.Contains(a, "PAN_test") {
			t.Fatal("the key is on argv")
		}
	}
	if r.stdout != "" || !strings.Contains(r.stderr, "Login Succeeded") || strings.Contains(r.stderr, "PAN_test") {
		t.Fatalf("stdout %q stderr %q", r.stdout, r.stderr)
	}
}

func TestRegistryLoginWithATokenPodmanAndAnotherRegistry(t *testing.T) {
	_, srv := newFakeAPI(t)
	runs := fakeEngine(t, []string{"podman"}, nil)
	r := run(t, srv, "prt_1_s3cretvalue\n", "registry", "login", "--token-stdin", "--engine", "podman", "--registry", "registry-dev.pantechdynamics.com")
	if r.err != nil {
		t.Fatal(r.err)
	}
	got := (*runs)[0]
	if got.path != "/fake/bin/podman" || got.args[1] != "registry-dev.pantechdynamics.com" || got.stdin != "prt_1_s3cretvalue\n" {
		t.Fatalf("ran %+v", got)
	}

	r = run(t, srv, "", "registry", "login", "--token", "prt_2_x", "--engine", "podman")
	if r.err != nil || (*runs)[1].stdin != "prt_2_x\n" || strings.Contains(strings.Join((*runs)[1].args, " "), "prt_2_x") {
		t.Fatalf("err %v runs %+v", r.err, *runs)
	}
}

func TestRegistryLoginWithoutDocker(t *testing.T) {
	_, srv := newFakeAPI(t)
	runs := fakeEngine(t, nil, nil)
	r := run(t, srv, "", "registry", "login")
	if r.err == nil || !strings.Contains(r.err.Error(), "docker was not found on your PATH") || !strings.Contains(r.err.Error(), "--engine podman") || len(*runs) != 0 {
		t.Fatalf("err %v", r.err)
	}
}

func TestRegistryLoginRefusesBadInput(t *testing.T) {
	_, srv := newFakeAPI(t)
	runs := fakeEngine(t, []string{"docker"}, nil)
	for _, args := range [][]string{
		{"registry", "login", "--registry", "--password=x"},
		{"registry", "login", "--registry", "evil.example.com/path"},
		{"registry", "login", "--engine", "nerdctl"},
		{"registry", "login", "--token", "x", "--token-stdin"},
	} {
		if r := run(t, srv, "", args...); r.err == nil {
			t.Fatalf("%v accepted", args)
		}
	}
	if len(*runs) != 0 {
		t.Fatalf("ran %+v", *runs)
	}
}

func TestRegistryLoginFailureDoesNotEchoTheKey(t *testing.T) {
	_, srv := newFakeAPI(t)
	fakeEngine(t, []string{"docker"}, exec.Command("false").Run())
	r := run(t, srv, "", "registry", "login")
	if r.err == nil || strings.Contains(r.err.Error(), "PAN_test") || !strings.Contains(r.err.Error(), "docker login registry.pantechdynamics.com failed") {
		t.Fatalf("err %v", r.err)
	}
}
