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
	zoneBody = `{"id":"dnsz_1","name":"example.com","description":"","status":"awaiting_delegation","failure_code":null,
"name_servers":["ns-1.example-dns.org","ns-2.example-dns.com","ns-3.example-dns.net","ns-4.example-dns.co.uk"],
"domain_id":null,"record_count":2,"last_checked_at":null,"activated_at":null,"suspended_reason":null,
"created_at":"2026-11-10T09:58:12Z","updated_at":"2026-11-10T10:00:00Z","version":1}`
	recordPending = `{"id":"dnsr_1","zone_id":"dnsz_1","name":"www","fqdn":"www.example.com","type":"A","ttl":300,"values":["203.0.113.10"],
"allow_private":false,"comment":"","managed":false,"status":"pending","failure_code":null,"version":2,"created_at":"2026-11-10T10:01:00Z","updated_at":"2026-11-10T10:01:00Z"}`
	recordActive = `{"id":"dnsr_1","zone_id":"dnsz_1","name":"www","fqdn":"www.example.com","type":"A","ttl":300,"values":["203.0.113.10"],
"allow_private":false,"comment":"","managed":false,"status":"active","failure_code":null,"version":2,"created_at":"2026-11-10T10:01:00Z","updated_at":"2026-11-10T10:01:00Z"}`
	recordFailed = `{"id":"dnsr_1","zone_id":"dnsz_1","name":"www","fqdn":"www.example.com","type":"A","ttl":300,"values":["203.0.113.10"],
"allow_private":false,"comment":"","managed":false,"status":"failed","failure_code":"invalid_change","version":2,"created_at":"2026-11-10T10:01:00Z","updated_at":"2026-11-10T10:01:00Z"}`
)

// zoneList is a zone list with one live zone named example.com.
const zoneList = `{"data":[` + zoneBody + `],"next_cursor":null}`

// zonesByName answers the name lookup a zone named by name makes.
func zonesByName(f *fakeAPI) {
	f.on("GET /dns/zones", 200, `{"data":[`+zoneBody+`,{"id":"dnsz_0","name":"example.com","status":"deleted"},{"id":"dnsz_2","name":"shop.example.com","status":"active"}],"next_cursor":null}`)
}

func TestDNSZonesCreatePrintsTheNameServers(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /dns/zones", 201, zoneBody)
	r := run(t, srv, "", "dns", "zones", "create", "Example.COM.", "--description", "Marketing")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got, want := f.sentJSON("POST", "/dns/zones"), map[string]any{"name": "example.com", "description": "Marketing"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("body %v", got)
	}
	for _, want := range []string{"ns-1.example-dns.org", "ns-4.example-dns.co.uk", "awaiting"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("stdout %q lacks %q", r.stdout, want)
		}
	}
	if !strings.Contains(r.stderr, "Created zone example.com (dnsz_1)") {
		t.Fatalf("stderr %q", r.stderr)
	}
}

func TestDNSZonesListTableQuietAndJSON(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /dns/zones", 200, `{"data":[`+zoneBody+`],"next_cursor":null}`)
	r := run(t, srv, "", "dns", "zones", "list", "--status", "active", "--q", "exam")
	if r.err != nil || !strings.Contains(r.stdout, "dnsz_1") || !strings.Contains(r.stdout, "example.com") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	if q := f.sent("GET", "/dns/zones")[0].Query; !strings.Contains(q, "status=active") || !strings.Contains(q, "q=exam") {
		t.Fatalf("query %q", q)
	}
	if r := run(t, srv, "", "dns", "zones", "list", "-q"); r.err != nil || r.stdout != "dnsz_1\n" {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	r = run(t, srv, "", "dns", "zones", "list", "--json")
	var items []map[string]any
	if r.err != nil || json.Unmarshal([]byte(r.stdout), &items) != nil || len(items) != 1 || items[0]["name_servers"] == nil {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
}

func TestDNSZoneByNameSkipsDeletedAndOtherNames(t *testing.T) {
	f, srv := newFakeAPI(t)
	zonesByName(f)
	f.on("GET /dns/zones/dnsz_1", 200, zoneBody)
	r := run(t, srv, "", "dns", "zones", "get", "example.com")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if len(f.sent("GET", "/dns/zones/dnsz_1")) != 1 {
		t.Fatalf("calls %+v", f.calls)
	}
	if q := f.sent("GET", "/dns/zones")[0].Query; q != "q=example.com" {
		t.Fatalf("lookup query %q", q)
	}
}

func TestDNSZoneByNameNoneOrMany(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /dns/zones", 200, `{"data":[],"next_cursor":null}`)
	if r := run(t, srv, "", "dns", "zones", "get", "nope.com"); r.err == nil || !strings.Contains(r.err.Error(), `no zone named "nope.com"`) {
		t.Fatalf("err %v", r.err)
	}

	f, srv = newFakeAPI(t)
	f.on("GET /dns/zones", 200, `{"data":[{"id":"dnsz_1","name":"example.com","status":"active"},{"id":"dnsz_9","name":"example.com","status":"failed"}],"next_cursor":null}`)
	if r := run(t, srv, "", "dns", "zones", "get", "example.com"); r.err == nil || !strings.Contains(r.err.Error(), "use its id") {
		t.Fatalf("err %v", r.err)
	}
}

func TestDNSZonesDeleteAsksFirst(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("DELETE /dns/zones/dnsz_1", 202, strings.Replace(zoneBody, "awaiting_delegation", "deleting", 1))
	r := run(t, srv, "", "dns", "zones", "delete", "dnsz_1")
	if r.err == nil || !strings.Contains(r.err.Error(), "--yes") || len(f.sent("DELETE", "/dns/zones/dnsz_1")) != 0 {
		t.Fatalf("err %v, calls %+v", r.err, f.calls)
	}
	if r := run(t, srv, "", "dns", "zones", "delete", "dnsz_1", "--yes"); r.err != nil || len(f.sent("DELETE", "/dns/zones/dnsz_1")) != 1 {
		t.Fatalf("err %v", r.err)
	}
}

func TestDNSZoneInUseByDomainSaysWhatToDo(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("DELETE /dns/zones/dnsz_1", 409, `{"status":409,"code":"DNS_ZONE_IN_USE_BY_DOMAIN","detail":"A Pantech domain delegates to this zone."}`)
	r := run(t, srv, "", "dns", "zones", "delete", "dnsz_1", "--yes")
	if !api.IsCode(r.err, "DNS_ZONE_IN_USE_BY_DOMAIN") || !strings.Contains(r.err.Error(), "pantech domains nameservers") {
		t.Fatalf("err %v", r.err)
	}
}

func TestDNSZonesVerify(t *testing.T) {
	f, srv := newFakeAPI(t)
	zonesByName(f)
	f.on("POST /dns/zones/dnsz_1/verify", 202, zoneBody)
	if r := run(t, srv, "", "dns", "zones", "verify", "example.com"); r.err != nil {
		t.Fatal(r.err)
	}
	if len(f.sent("POST", "/dns/zones/dnsz_1/verify")) != 1 {
		t.Fatalf("calls %+v", f.calls)
	}
}

func TestDNSRecordsCreateWaitsUntilLive(t *testing.T) {
	f, srv := newFakeAPI(t)
	zonesByName(f)
	f.on("POST /dns/zones/dnsz_1/records", 202, recordPending)
	f.on("GET /dns/zones/dnsz_1/records/dnsr_1", 200, recordPending)
	f.on("GET /dns/zones/dnsz_1/records/dnsr_1", 200, recordActive)
	r := run(t, srv, "", "dns", "records", "create", "example.com", "--name", "www", "--type", "a", "--value", "203.0.113.10", "--ttl", "60", "--wait")
	if r.err != nil {
		t.Fatal(r.err)
	}
	want := map[string]any{"name": "www", "type": "A", "values": []any{"203.0.113.10"}, "ttl": float64(60)}
	if got := f.sentJSON("POST", "/dns/zones/dnsz_1/records"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body %v", got)
	}
	if len(f.sent("GET", "/dns/zones/dnsz_1/records/dnsr_1")) < 2 || !strings.Contains(r.stdout, "www.example.com") {
		t.Fatalf("stdout %q", r.stdout)
	}
}

func TestDNSRecordsCreateFailedChangeExitsWithFailure(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /dns/zones/dnsz_1/records", 202, recordPending)
	f.on("GET /dns/zones/dnsz_1/records/dnsr_1", 200, recordFailed)
	r := run(t, srv, "", "dns", "records", "create", "dnsz_1", "--name", "www", "--type", "A", "--value", "203.0.113.10", "--wait")
	var failed *api.DNSRecordFailed
	if !errors.As(r.err, &failed) || !strings.Contains(r.err.Error(), "invalid_change") {
		t.Fatalf("err %v", r.err)
	}
}

func TestDNSRecordsCreateNeedsAValue(t *testing.T) {
	_, srv := newFakeAPI(t)
	r := run(t, srv, "", "dns", "records", "create", "dnsz_1", "--name", "www", "--type", "A")
	if r.err == nil || !IsUsage(r.err) {
		t.Fatalf("err %v", r.err)
	}
}

func TestDNSRecordsUpdateSendsOnlyWhatChangedAndIfMatch(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("PATCH /dns/zones/dnsz_1/records/dnsr_1", 202, recordPending)
	r := run(t, srv, "", "dns", "records", "update", "dnsz_1", "dnsr_1", "--value", "203.0.113.20", "--comment", "", "--if-match", "2")
	if r.err != nil {
		t.Fatal(r.err)
	}
	want := map[string]any{"values": []any{"203.0.113.20"}, "comment": ""}
	if got := f.sentJSON("PATCH", "/dns/zones/dnsz_1/records/dnsr_1"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body %v", got)
	}
	if h := f.sent("PATCH", "/dns/zones/dnsz_1/records/dnsr_1")[0].Header.Get("If-Match"); h != "2" {
		t.Fatalf("If-Match %q", h)
	}

	// Without --if-match no header is sent.
	f, srv = newFakeAPI(t)
	f.on("PATCH /dns/zones/dnsz_1/records/dnsr_1", 202, recordPending)
	if r := run(t, srv, "", "dns", "records", "update", "dnsz_1", "dnsr_1", "--ttl", "120"); r.err != nil {
		t.Fatal(r.err)
	}
	if h := f.sent("PATCH", "/dns/zones/dnsz_1/records/dnsr_1")[0].Header.Get("If-Match"); h != "" {
		t.Fatalf("If-Match %q", h)
	}
}

func TestDNSRecordsUpdateNothingIsUsage(t *testing.T) {
	_, srv := newFakeAPI(t)
	if r := run(t, srv, "", "dns", "records", "update", "dnsz_1", "dnsr_1"); !IsUsage(r.err) {
		t.Fatalf("err %v", r.err)
	}
}

func TestDNSVersionConflictSaysWhatToDo(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("PATCH /dns/zones/dnsz_1/records/dnsr_1", 412, `{"status":412,"code":"VERSION_CONFLICT","detail":"The record changed since you read it."}`)
	r := run(t, srv, "", "dns", "records", "update", "dnsz_1", "dnsr_1", "--ttl", "60", "--if-match", "1")
	if !api.IsCode(r.err, "VERSION_CONFLICT") || !strings.Contains(r.err.Error(), "pantech dns records get") {
		t.Fatalf("err %v", r.err)
	}
}

func TestDNSRecordsDeleteAsksFirstAndSendsIfMatch(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /dns/zones/dnsz_1/records/dnsr_1", 200, recordActive)
	f.on("DELETE /dns/zones/dnsz_1/records/dnsr_1", 202, strings.Replace(recordActive, `"active"`, `"deleting"`, 1))
	r := run(t, srv, "", "dns", "records", "delete", "dnsz_1", "dnsr_1")
	if r.err == nil || !strings.Contains(r.err.Error(), "www.example.com A") || len(f.sent("DELETE", "/dns/zones/dnsz_1/records/dnsr_1")) != 0 {
		t.Fatalf("err %v", r.err)
	}
	r = run(t, srv, "", "dns", "records", "delete", "dnsz_1", "dnsr_1", "--if-match", "2", "--yes")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if calls := f.sent("DELETE", "/dns/zones/dnsz_1/records/dnsr_1"); len(calls) != 1 || calls[0].Header.Get("If-Match") != "2" {
		t.Fatalf("calls %+v", calls)
	}
}

func TestDNSRecordsListFilters(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /dns/zones/dnsz_1/records", 200, `{"data":[`+recordActive+`],"next_cursor":null}`)
	r := run(t, srv, "", "dns", "records", "list", "dnsz_1", "--type", "A", "--name", "www")
	if r.err != nil || !strings.Contains(r.stdout, "203.0.113.10") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	q := f.sent("GET", "/dns/zones/dnsz_1/records")[0].Query
	if !strings.Contains(q, "type=A") || !strings.Contains(q, "name=www") {
		t.Fatalf("query %q", q)
	}
}

func TestDNSExportPrintsTheFileAsIs(t *testing.T) {
	f, srv := newFakeAPI(t)
	file := "$ORIGIN example.com.\n$TTL 300\nwww 300 IN A 203.0.113.10\n"
	f.on("GET /dns/zones/dnsz_1/export", 200, file)
	r := run(t, srv, "", "dns", "export", "dnsz_1")
	if r.err != nil || r.stdout != file {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	if accept := f.sent("GET", "/dns/zones/dnsz_1/export")[0].Header.Get("Accept"); accept != "text/dns" {
		t.Fatalf("Accept %q", accept)
	}
}

func TestDNSImportReplaceAsksFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "example.com.zone")
	if err := os.WriteFile(path, []byte("www 300 IN A 203.0.113.10\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, srv := newFakeAPI(t)
	f.on("POST /dns/zones/dnsz_1/import", 202, `{"data":[`+recordPending+`]}`)

	r := run(t, srv, "", "dns", "import", "dnsz_1", "--file", path, "--replace")
	if r.err == nil || !strings.Contains(r.err.Error(), "deleted") || len(f.sent("POST", "/dns/zones/dnsz_1/import")) != 0 {
		t.Fatalf("err %v", r.err)
	}

	r = run(t, srv, "", "dns", "import", "dnsz_1", "--file", path)
	if r.err != nil {
		t.Fatal(r.err)
	}
	want := map[string]any{"zone_file": "www 300 IN A 203.0.113.10\n"}
	if got := f.sentJSON("POST", "/dns/zones/dnsz_1/import"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body %v", got)
	}
	if !strings.Contains(r.stderr, "1 change") {
		t.Fatalf("stderr %q", r.stderr)
	}

	f, srv = newFakeAPI(t)
	f.on("POST /dns/zones/dnsz_1/import", 202, `{"data":[]}`)
	if r := run(t, srv, "", "dns", "import", "dnsz_1", "--file", path, "--replace", "--yes"); r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.sentJSON("POST", "/dns/zones/dnsz_1/import"); got["replace"] != true {
		t.Fatalf("body %v", got)
	}
}

func TestDNSImportFromStdin(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /dns/zones/dnsz_1/import", 202, `{"data":[]}`)
	if r := run(t, srv, "@ 300 IN TXT \"hello\"\n", "dns", "import", "dnsz_1", "--file", "-"); r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.sentJSON("POST", "/dns/zones/dnsz_1/import"); got["zone_file"] != "@ 300 IN TXT \"hello\"\n" {
		t.Fatalf("body %v", got)
	}
}

func TestDNSInvalidZoneFileNamesTheFlag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.zone")
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, srv := newFakeAPI(t)
	f.on("POST /dns/zones/dnsz_1/import", 422, `{"status":422,"code":"VALIDATION_FAILED","detail":"zone_file: line 1 does not parse.","errors":[{"field":"zone_file","code":"invalid","message":"line 1 does not parse"}]}`)
	r := run(t, srv, "", "dns", "import", "dnsz_1", "--file", path)
	if r.err == nil || !strings.Contains(r.err.Error(), "--file") {
		t.Fatalf("err %v", r.err)
	}
}

func TestDNSZonesQuote(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /dns/zones/quote", 200, `{"free_zones":5,"live_zones":7,"billable_zones":2,"currency":"USD","monthly_per_zone_minor":50,"monthly_estimate_minor":100,"nominal_month_hours":730}`)
	r := run(t, srv, "", "dns", "zones", "quote")
	for _, want := range []string{"USD 0.50 a month", "USD 1.00 a month", "7"} {
		if r.err != nil || !strings.Contains(r.stdout, want) {
			t.Fatalf("stdout %q lacks %q (err %v)", r.stdout, want, r.err)
		}
	}
	if r := run(t, srv, "", "dns", "zones", "quote", "-q"); r.err != nil || r.stdout != "100\n" {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
}
