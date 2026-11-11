package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
)

const (
	teamBody     = `{"id":"team_1","name":"Platform","description":"","member_count":1,"policy_ids":["pol_1"],"created_at":"2026-11-10T09:00:00Z","updated_at":"2026-11-10T09:00:00Z","version":1}`
	teamsList    = `{"data":[` + teamBody + `,{"id":"team_2","name":"Finance","member_count":0,"policy_ids":[]}],"next_cursor":null}`
	membersList  = `{"data":[{"user_id":"usr_ada","email":"Ada@Example.com","name":"Ada Obi","role":"member","added_at":"2026-11-10T09:00:00Z"}],"next_cursor":null}`
	policyBody   = `{"id":"pol_1","name":"staging-operators","description":"Restart staging","managed":false,"statements":[{"sid":"RestartStaging","effect":"allow","actions":["instances:Read","instances:Reboot"],"resources":["name:staging-*"],"conditions":{"source_ip":["102.89.0.0/16"]}}],"attachment_count":3,"created_at":"2026-11-10T09:00:00Z","updated_at":"2026-11-10T10:00:00Z","version":5}`
	policiesList = `{"data":[` + policyBody + `,{"id":"pol_pantech_read_only","name":"read-only","managed":true,"statements":[],"attachment_count":0,"version":1}],"next_cursor":null}`
)

// iamDirectory answers the lookups names and emails make.
func iamDirectory(f *fakeAPI) {
	f.on("GET /iam/teams", 200, teamsList)
	f.on("GET /iam/teams/team_1/members", 200, membersList)
	f.on("GET /iam/members", 200, `{"data":[{"user_id":"usr_ada","email":"Ada@Example.com","name":"Ada Obi","role":"member","team_ids":["team_1"],"policy_count":1},
{"user_id":"usr_adam","email":"adam@example.com","name":"Adam","role":"admin","team_ids":[],"policy_count":0}],"next_cursor":null}`)
	f.on("GET /iam/policies", 200, policiesList)
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestIAMActionsFilterByService(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /iam/actions", 200, `{"data":[{"action":"instances:Stop","service":"instances","description":"Stop a virtual machine.","resource_types":["instance"],"read":false},
{"action":"dns:Read","service":"dns","description":"Read zones.","resource_types":["dns_zone"],"read":true}]}`)
	r := run(t, srv, "", "iam", "actions", "--service", "instances")
	if r.err != nil || !strings.Contains(r.stdout, "instances:Stop") || strings.Contains(r.stdout, "dns:Read") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	r = run(t, srv, "", "iam", "actions", "--json", "--service", "dns")
	var items []map[string]any
	if r.err != nil || json.Unmarshal([]byte(r.stdout), &items) != nil || len(items) != 1 || items[0]["action"] != "dns:Read" {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
}

func TestIAMSettingsBaselineNoneAsksFirst(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /iam/settings", 200, `{"member_baseline":"read","version":1,"updated_at":"2026-11-10T09:00:00Z"}`)
	f.on("PUT /iam/settings", 200, `{"member_baseline":"none","version":2,"updated_at":"2026-11-10T09:00:00Z"}`)
	if r := run(t, srv, "", "iam", "settings"); r.err != nil || !strings.Contains(r.stdout, "members may read everything") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	r := run(t, srv, "", "iam", "settings", "set", "--member-baseline", "none")
	if r.err == nil || !strings.Contains(r.err.Error(), "loses read access") || len(f.sent("PUT", "/iam/settings")) != 0 {
		t.Fatalf("err %v", r.err)
	}
	if r := run(t, srv, "", "iam", "settings", "set", "--member-baseline", "none", "--yes"); r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.sentJSON("PUT", "/iam/settings"); got["member_baseline"] != "none" {
		t.Fatalf("body %v", got)
	}
	// read needs no confirmation; anything else is a usage error.
	f, srv = newFakeAPI(t)
	f.on("PUT /iam/settings", 200, `{"member_baseline":"read","version":3}`)
	if r := run(t, srv, "", "iam", "settings", "set", "--member-baseline", "read"); r.err != nil {
		t.Fatal(r.err)
	}
	if r := run(t, srv, "", "iam", "settings", "set", "--member-baseline", "all"); !IsUsage(r.err) {
		t.Fatalf("err %v", r.err)
	}
}

func TestIAMTeamsCreateGetByNameDelete(t *testing.T) {
	f, srv := newFakeAPI(t)
	iamDirectory(f)
	f.on("POST /iam/teams", 201, teamBody)
	f.on("GET /iam/teams/team_1", 200, teamBody)
	f.on("DELETE /iam/teams/team_1", 204, ``)
	if r := run(t, srv, "", "iam", "teams", "create", "Platform", "--description", "Runs the clusters"); r.err != nil {
		t.Fatal(r.err)
	}
	if got, want := f.sentJSON("POST", "/iam/teams"), map[string]any{"name": "Platform", "description": "Runs the clusters"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("body %v", got)
	}
	if r := run(t, srv, "", "iam", "teams", "get", "Platform"); r.err != nil || !strings.Contains(r.stdout, "pol_1") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	if r := run(t, srv, "", "iam", "teams", "get", "Nope"); r.err == nil || !strings.Contains(r.err.Error(), `no team named "Nope"`) {
		t.Fatalf("err %v", r.err)
	}
	if r := run(t, srv, "", "iam", "teams", "delete", "Platform"); r.err == nil || len(f.sent("DELETE", "/iam/teams/team_1")) != 0 {
		t.Fatalf("err %v", r.err)
	}
	if r := run(t, srv, "", "iam", "teams", "delete", "Platform", "--yes"); r.err != nil || len(f.sent("DELETE", "/iam/teams/team_1")) != 1 {
		t.Fatalf("err %v", r.err)
	}
}

func TestIAMTeamsUpdate(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("PATCH /iam/teams/team_1", 200, teamBody)
	if r := run(t, srv, "", "iam", "teams", "update", "team_1", "--description", ""); r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.sentJSON("PATCH", "/iam/teams/team_1"); !reflect.DeepEqual(got, map[string]any{"description": ""}) {
		t.Fatalf("body %v", got)
	}
	if r := run(t, srv, "", "iam", "teams", "update", "team_1"); !IsUsage(r.err) {
		t.Fatalf("err %v", r.err)
	}
}

func TestIAMTeamMembersListAddRemoveByEmail(t *testing.T) {
	f, srv := newFakeAPI(t)
	iamDirectory(f)
	f.on("PUT /iam/teams/team_2/members/usr_ada", 200, `{"user_id":"usr_ada","email":"Ada@Example.com","name":"Ada Obi","role":"member","added_at":"2026-11-10T09:00:00Z"}`)
	f.on("DELETE /iam/teams/team_1/members/usr_ada", 204, ``)

	if r := run(t, srv, "", "iam", "teams", "members", "Platform"); r.err != nil || !strings.Contains(r.stdout, "Ada Obi") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	if r := run(t, srv, "", "iam", "teams", "members", "Finance", "add", "ada@example.com"); r.err == nil || !strings.Contains(r.err.Error(), "--yes") || len(f.sent("PUT", "/iam/teams/team_2/members/usr_ada")) != 0 {
		t.Fatalf("err %v", r.err)
	}
	if r := run(t, srv, "", "iam", "teams", "members", "Finance", "add", "ada@example.com", "--yes"); r.err != nil {
		t.Fatal(r.err)
	}
	if q := f.sent("GET", "/iam/members")[0].Query; q != "q=ada%40example.com" {
		t.Fatalf("lookup query %q", q)
	}
	if len(f.sent("PUT", "/iam/teams/team_2/members/usr_ada")) != 1 {
		t.Fatalf("calls %+v", f.calls)
	}
	if r := run(t, srv, "", "iam", "teams", "members", "team_1", "remove", "usr_ada"); r.err == nil || len(f.sent("DELETE", "/iam/teams/team_1/members/usr_ada")) != 0 {
		t.Fatalf("err %v", r.err)
	}
	if r := run(t, srv, "", "iam", "teams", "members", "team_1", "remove", "usr_ada", "--yes"); r.err != nil || len(f.sent("DELETE", "/iam/teams/team_1/members/usr_ada")) != 1 {
		t.Fatalf("err %v", r.err)
	}
	if r := run(t, srv, "", "iam", "teams", "members", "team_1", "kick", "usr_ada"); r.err == nil {
		t.Fatal("kick accepted")
	}
}

func TestIAMUnknownOrAmbiguousEmail(t *testing.T) {
	f, srv := newFakeAPI(t)
	iamDirectory(f)
	r := run(t, srv, "", "iam", "access", "bob@example.com")
	if r.err == nil || !strings.Contains(r.err.Error(), "no member has the email bob@example.com") {
		t.Fatalf("err %v", r.err)
	}
	// Two members whose emails differ only in case: ambiguous.
	f, srv = newFakeAPI(t)
	f.on("GET /iam/members", 200, `{"data":[{"user_id":"usr_1","email":"x@example.com"},{"user_id":"usr_2","email":"X@example.com"}],"next_cursor":null}`)
	if r := run(t, srv, "", "iam", "access", "x@example.com"); r.err == nil || !strings.Contains(r.err.Error(), "use their id") {
		t.Fatalf("err %v", r.err)
	}
	if r := run(t, srv, "", "iam", "access", "bob"); r.err == nil || !strings.Contains(r.err.Error(), "neither a member id") {
		t.Fatalf("err %v", r.err)
	}
}

func TestIAMPoliciesListAndGet(t *testing.T) {
	f, srv := newFakeAPI(t)
	iamDirectory(f)
	f.on("GET /iam/policies/pol_1", 200, policyBody)
	r := run(t, srv, "", "iam", "policies", "list", "--custom")
	if r.err != nil || !strings.Contains(r.stdout, "staging-operators") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	if q := f.sent("GET", "/iam/policies")[0].Query; q != "managed=false" {
		t.Fatalf("query %q", q)
	}
	r = run(t, srv, "", "iam", "policies", "get", "staging-operators")
	for _, want := range []string{"RestartStaging", "instances:Read, instances:Reboot", "name:staging-*", "from 102.89.0.0/16"} {
		if r.err != nil || !strings.Contains(r.stdout, want) {
			t.Fatalf("stdout %q lacks %q (err %v)", r.stdout, want, r.err)
		}
	}
}

func TestIAMPoliciesCreateFromFileAndStdin(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /iam/policies", 201, policyBody)
	path := writeFile(t, "policy.json", `{"name":"staging-operators","statements":[{"effect":"allow","actions":["instances:Read"],"resources":["name:staging-*"]}]}`)
	r := run(t, srv, "", "iam", "policies", "create", "--file", path)
	if r.err != nil {
		t.Fatal(r.err)
	}
	want := map[string]any{"name": "staging-operators", "statements": []any{map[string]any{"effect": "allow", "actions": []any{"instances:Read"}, "resources": []any{"name:staging-*"}}}}
	if got := f.sentJSON("POST", "/iam/policies"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body %v", got)
	}
	if !strings.Contains(r.stderr, "applies to nobody until attached") {
		t.Fatalf("stderr %q", r.stderr)
	}

	// A bare array of statements from stdin, with --name.
	f, srv = newFakeAPI(t)
	f.on("POST /iam/policies", 201, policyBody)
	if r := run(t, srv, `[{"effect":"deny","actions":["*:Delete"],"resources":["*"]}]`, "iam", "policies", "create", "--file", "-", "--name", "no-deletes"); r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.sentJSON("POST", "/iam/policies"); got["name"] != "no-deletes" {
		t.Fatalf("body %v", got)
	}
}

func TestIAMPoliciesCreateRefusesBadFiles(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /iam/policies", 201, policyBody)
	for _, content := range []string{
		`{"statements":[{"effect":"allow","actions":["*"],"resources":["*"]}]}`, // no name
		`{"name":"x","statements":[]}`,
		`{"name":"x","statments":[{}]}`, // a typo is refused, not ignored
		`not json`,
	} {
		path := writeFile(t, "p.json", content)
		if r := run(t, srv, "", "iam", "policies", "create", "--file", path); r.err == nil {
			t.Fatalf("%s accepted", content)
		}
	}
	if len(f.sent("POST", "/iam/policies")) != 0 {
		t.Fatal("a bad file was sent")
	}
}

func TestIAMInvalidPolicyFieldError(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /iam/policies", 422, `{"status":422,"code":"INVALID_POLICY","detail":"statements[0].actions[0]: unknown action.","errors":[{"field":"statements[0].actions[0]","code":"unknown_action","message":"unknown action"}]}`)
	path := writeFile(t, "p.json", `{"name":"x","statements":[{"effect":"allow","actions":["instances:Explode"],"resources":["*"]}]}`)
	r := run(t, srv, "", "iam", "policies", "create", "--file", path)
	if !api.IsCode(r.err, "INVALID_POLICY") || !strings.Contains(r.err.Error(), "statements[0].actions[0]") || !strings.Contains(r.err.Error(), "pantech iam actions") {
		t.Fatalf("err %v", r.err)
	}
}

func TestIAMPoliciesUpdateKeepsNameAndSendsIfMatch(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /iam/policies/pol_1", 200, policyBody)
	f.on("PUT /iam/policies/pol_1", 200, policyBody)
	path := writeFile(t, "p.json", `[{"effect":"allow","actions":["instances:Read"],"resources":["*"]}]`)

	r := run(t, srv, "", "iam", "policies", "update", "pol_1", "--file", path)
	if r.err == nil || !strings.Contains(r.err.Error(), "3 principals") || len(f.sent("PUT", "/iam/policies/pol_1")) != 0 {
		t.Fatalf("err %v", r.err)
	}
	if r := run(t, srv, "", "iam", "policies", "update", "pol_1", "--file", path, "--yes"); r.err != nil {
		t.Fatal(r.err)
	}
	calls := f.sent("PUT", "/iam/policies/pol_1")
	if len(calls) != 1 || calls[0].Header.Get("If-Match") != "5" {
		t.Fatalf("calls %+v", calls)
	}
	got := f.sentJSON("PUT", "/iam/policies/pol_1")
	if got["name"] != "staging-operators" || got["description"] != "Restart staging" {
		t.Fatalf("body %v", got)
	}

	f, srv = newFakeAPI(t)
	f.on("GET /iam/policies/pol_1", 200, policyBody)
	f.on("PUT /iam/policies/pol_1", 412, `{"status":412,"code":"VERSION_CONFLICT","detail":"The policy changed."}`)
	r = run(t, srv, "", "iam", "policies", "update", "pol_1", "--file", path, "--if-match", "4", "--yes")
	if !api.IsCode(r.err, "VERSION_CONFLICT") || f.sent("PUT", "/iam/policies/pol_1")[0].Header.Get("If-Match") != "4" {
		t.Fatalf("err %v", r.err)
	}
}

func TestIAMManagedPolicyIsNotUpdated(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /iam/policies/pol_pantech_read_only", 200, `{"id":"pol_pantech_read_only","name":"read-only","managed":true,"statements":[],"version":1}`)
	path := writeFile(t, "p.json", `[{"effect":"allow","actions":["*"],"resources":["*"]}]`)
	r := run(t, srv, "", "iam", "policies", "update", "pol_pantech_read_only", "--file", path, "--yes")
	if r.err == nil || !strings.Contains(r.err.Error(), "cannot be changed") || len(f.sent("PUT", "/iam/policies/pol_pantech_read_only")) != 0 {
		t.Fatalf("err %v", r.err)
	}
}

func TestIAMPoliciesDeleteAsksFirst(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("DELETE /iam/policies/pol_1", 204, ``)
	if r := run(t, srv, "", "iam", "policies", "delete", "pol_1"); r.err == nil || len(f.sent("DELETE", "/iam/policies/pol_1")) != 0 {
		t.Fatalf("err %v", r.err)
	}
	if r := run(t, srv, "", "iam", "policies", "delete", "pol_1", "--yes"); r.err != nil || len(f.sent("DELETE", "/iam/policies/pol_1")) != 1 {
		t.Fatalf("err %v", r.err)
	}
}

func TestIAMAttachAndDetach(t *testing.T) {
	f, srv := newFakeAPI(t)
	iamDirectory(f)
	f.on("POST /iam/policies/pol_1/attachments", 201, `{"policy_id":"pol_1","principal_type":"team","principal_id":"team_2","attached_at":"2026-11-10T09:00:00Z"}`)
	f.on("DELETE /iam/policies/pol_1/attachments/member/usr_ada", 204, ``)

	r := run(t, srv, "", "iam", "policies", "attach", "staging-operators", "--team", "Finance")
	if r.err == nil || !strings.Contains(r.err.Error(), "--yes") || len(f.sent("POST", "/iam/policies/pol_1/attachments")) != 0 {
		t.Fatalf("err %v", r.err)
	}
	if r := run(t, srv, "", "iam", "policies", "attach", "staging-operators", "--team", "Finance", "--yes"); r.err != nil {
		t.Fatal(r.err)
	}
	if got, want := f.sentJSON("POST", "/iam/policies/pol_1/attachments"), map[string]any{"principal_type": "team", "principal_id": "team_2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("body %v", got)
	}

	if r := run(t, srv, "", "iam", "policies", "detach", "pol_1", "--member", "ada@example.com"); r.err == nil || !strings.Contains(r.err.Error(), "any deny in it stops applying") {
		t.Fatalf("err %v", r.err)
	}
	if r := run(t, srv, "", "iam", "policies", "detach", "pol_1", "--member", "ada@example.com", "--yes"); r.err != nil {
		t.Fatal(r.err)
	}
	if len(f.sent("DELETE", "/iam/policies/pol_1/attachments/member/usr_ada")) != 1 {
		t.Fatalf("calls %+v", f.calls)
	}

	for _, args := range [][]string{{"iam", "policies", "attach", "pol_1"}, {"iam", "policies", "attach", "pol_1", "--team", "x", "--member", "usr_1"}} {
		if r := run(t, srv, "", args...); !IsUsage(r.err) {
			t.Fatalf("%v: err %v", args, r.err)
		}
	}
}

func TestIAMAdminTargetSaysOwnersOnly(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /iam/policies/pol_1/attachments", 403, `{"status":403,"code":"IAM_ADMIN_TARGET","detail":"Only owners can attach policies to admins."}`)
	r := run(t, srv, "", "iam", "policies", "attach", "pol_1", "--member", "usr_admin", "--yes")
	if !api.IsCode(r.err, "IAM_ADMIN_TARGET") || !strings.Contains(r.err.Error(), "Only an owner") {
		t.Fatalf("err %v", r.err)
	}
}

func TestIAMAccess(t *testing.T) {
	f, srv := newFakeAPI(t)
	iamDirectory(f)
	f.on("GET /iam/members/usr_ada/effective-access", 200, `{"user_id":"usr_ada","role":"member","teams":["team_1"],
"policies":[{"policy_id":"pol_1","team_id":"team_1"},{"policy_id":"pol_pantech_deny_delete","team_id":null}],"member_baseline":"read"}`)
	r := run(t, srv, "", "iam", "access", "ada@example.com")
	for _, want := range []string{"pol_1 (through team_1)", "pol_pantech_deny_delete (attached directly)", "plus every Read action"} {
		if r.err != nil || !strings.Contains(r.stdout, want) {
			t.Fatalf("stdout %q lacks %q (err %v)", r.stdout, want, r.err)
		}
	}
}

func TestIAMSimulate(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /iam/simulate", 200, `{"allowed":false,"decision":"explicit_deny","policy_id":"pol_1","sid":"NoProdDeletes"}`)
	r := run(t, srv, "", "iam", "simulate", "--user", "usr_ada", "--action", "instances:Delete", "--resource-name", "prod-api", "--ip", "102.89.1.2", "--mfa")
	if r.err != nil {
		t.Fatal(r.err)
	}
	want := map[string]any{"user_id": "usr_ada", "action": "instances:Delete", "resource_name": "prod-api", "source_ip": "102.89.1.2", "mfa": true}
	if got := f.sentJSON("POST", "/iam/simulate"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body %v", got)
	}
	for _, want := range []string{"Denied: instances:Delete", "a policy denies it", "pol_1", "NoProdDeletes"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("stdout %q lacks %q", r.stdout, want)
		}
	}
	if r := run(t, srv, "", "iam", "simulate", "--user", "usr_ada", "--action", "instances:Delete", "-q"); r.err != nil || r.stdout != "explicit_deny\n" {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
}

func TestIAMMembersList(t *testing.T) {
	f, srv := newFakeAPI(t)
	iamDirectory(f)
	r := run(t, srv, "", "iam", "members", "--q", "ada")
	if r.err != nil || !strings.Contains(r.stdout, "Ada@Example.com") || !strings.Contains(r.stdout, "team_1") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	if q := f.sent("GET", "/iam/members")[0].Query; q != "q=ada" {
		t.Fatalf("query %q", q)
	}
	if r := run(t, srv, "", "iam", "members", "-q"); r.err != nil || r.stdout != "usr_ada\nusr_adam\n" {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
}
