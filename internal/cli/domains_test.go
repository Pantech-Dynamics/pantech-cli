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
	tldsBody            = `{"data":[{"tld":"com","currency":"USD","register_minor":1500,"renew_minor":1700,"transfer_minor":1400,"featured":true},{"tld":"uk","currency":"USD","register_minor":900,"renew_minor":900,"transfer_minor":900,"featured":false},{"tld":"co.uk","currency":"USD","register_minor":800,"renew_minor":800,"transfer_minor":800,"featured":false}]}`
	domainActive        = `{"id":"dom_1","name":"example.com","tld":"com","status":"active","failure_code":null,"auto_renew":true,"transfer_lock":true,"privacy":true,"name_server_mode":"pantech","name_servers":["ns-1.example-dns.org"],"zone_id":"dnsz_1","registered_at":"2026-11-10T10:05:00Z","expires_at":"2027-11-10T10:05:00Z","contact_verified":false,"settings_pending":false,"auth_code_pending":false,"suspended_reason":null,"created_at":"2026-11-10T10:04:41Z","updated_at":"2026-11-10T10:05:00Z","version":4}`
	orderPending        = `{"id":"dor_1","domain_id":"dom_1","domain_name":"example.com","kind":"register","years":2,"amount_minor":3200,"currency":"USD","status":"pending","failure_code":null,"refunded":false,"created_at":"2026-11-10T10:04:41Z","completed_at":null}`
	orderDone           = `{"id":"dor_1","domain_id":"dom_1","domain_name":"example.com","kind":"register","years":2,"amount_minor":3200,"currency":"USD","status":"succeeded","failure_code":null,"refunded":false,"created_at":"2026-11-10T10:04:41Z","completed_at":"2026-11-10T10:05:00Z"}`
	orderFailed         = `{"id":"dor_1","domain_id":"dom_1","domain_name":"example.com","kind":"register","years":2,"amount_minor":3200,"currency":"USD","status":"failed","failure_code":"domain_unavailable","refunded":true,"created_at":"2026-11-10T10:04:41Z","completed_at":"2026-11-10T10:05:00Z"}`
	contactJSON         = `{"type":"person","first_name":"Ada","last_name":"Obi","email":"ada@example.com","phone":"+234.8012345678","address_line1":"1 Marina","city":"Lagos","country_code":"NG"}`
	domainOrderAccepted = `{"domain":` + domainActive + `,"order":` + orderPending + `}`
)

func contactFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "contact.json")
	if err := os.WriteFile(path, []byte(contactJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDomainsRegisterNeedsTheTerms(t *testing.T) {
	_, srv := newFakeAPI(t)
	r := run(t, srv, "", "domains", "register", "example.com", "--contact-file", contactFile(t))
	if !IsUsage(r.err) || !strings.Contains(r.err.Error(), "--accept-terms") {
		t.Fatalf("err = %v", r.err)
	}
}

func TestDomainsRegisterShowsThePriceAndAsks(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /domains/tlds", 200, tldsBody)
	f.on("POST /domains", 202, domainOrderAccepted)
	r := run(t, srv, "", "domains", "register", "example.com", "--years", "2", "--contact-file", contactFile(t), "--accept-terms")
	// 1500 + 1700: the first year at the register price, the second at renew.
	if r.err == nil || !strings.Contains(r.err.Error(), "USD 32.00") || !strings.Contains(r.err.Error(), "credit") {
		t.Fatalf("err = %v", r.err)
	}
	if len(f.sent("POST", "/domains")) != 0 {
		t.Fatal("ordered without asking")
	}
}

func TestDomainsRegisterAndWait(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /domains/tlds", 200, tldsBody)
	f.on("POST /domains", 202, domainOrderAccepted)
	f.on("GET /domain-orders/dor_1", 200, orderPending).on("GET /domain-orders/dor_1", 200, orderDone)
	f.on("GET /domains/dom_1", 200, domainActive)
	r := run(t, srv, "", "domains", "register", "Example.com", "--years", "2", "--contact-file", contactFile(t), "--accept-terms", "--no-privacy", "--auto-renew=false", "--wait", "--yes")
	if r.err != nil {
		t.Fatal(r.err)
	}
	var contact map[string]any
	_ = json.Unmarshal([]byte(contactJSON), &contact)
	want := map[string]any{"name": "example.com", "years": float64(2), "contact": contact, "accept_terms": true, "privacy": false, "auto_renew": false}
	if got := f.sentJSON("POST", "/domains"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %v, want %v", got, want)
	}
	if n := len(f.sent("GET", "/domain-orders/dor_1")); n != 2 {
		t.Fatalf("polled %d times", n)
	}
	if !strings.Contains(r.stdout, "example.com") || !strings.Contains(r.stdout, "not verified") {
		t.Fatalf("stdout %q", r.stdout)
	}
	// The registrant's details are never printed back.
	if strings.Contains(r.stdout+r.stderr, "ada@example.com") {
		t.Fatal("printed the contact")
	}
}

func TestDomainsRegisterDefaultsLeaveOptionsToTheAPI(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /domains/tlds", 200, tldsBody)
	f.on("POST /domains", 202, domainOrderAccepted)
	r := run(t, srv, "", "domains", "register", "example.com", "--contact-file", contactFile(t), "--accept-terms", "--yes")
	if r.err != nil {
		t.Fatal(r.err)
	}
	got := f.sentJSON("POST", "/domains")
	if _, ok := got["auto_renew"]; ok {
		t.Fatalf("sent auto_renew: %v", got)
	}
	if _, ok := got["privacy"]; ok {
		t.Fatalf("sent privacy: %v", got)
	}
	if !strings.Contains(r.stderr, "USD 32.00 plus VAT") || !strings.Contains(r.stderr, "pantech domains orders dom_1") {
		t.Fatalf("stderr %q", r.stderr)
	}
}

func TestDomainsRegisterOrderFailureExitsWithAFailure(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /domains/tlds", 200, tldsBody)
	f.on("POST /domains", 202, domainOrderAccepted)
	f.on("GET /domain-orders/dor_1", 200, orderFailed)
	r := run(t, srv, "", "domains", "register", "example.com", "--contact-file", contactFile(t), "--accept-terms", "--wait", "--yes")
	if r.err == nil || !strings.Contains(r.err.Error(), "domain_unavailable") || !strings.Contains(r.err.Error(), "returned to your credit") {
		t.Fatalf("err = %v", r.err)
	}
}

func TestDomainsRegisterAnUnsoldTLD(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /domains/tlds", 200, tldsBody)
	r := run(t, srv, "", "domains", "register", "example.xyz", "--contact-file", contactFile(t), "--accept-terms", "--yes")
	if r.err == nil || !strings.Contains(r.err.Error(), "pantech domains tlds") {
		t.Fatalf("err = %v", r.err)
	}
}

func TestDomainsTransferReadsTheAuthCodeFromStdin(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /domains/tlds", 200, tldsBody)
	f.on("POST /domains/transfers", 202, domainOrderAccepted)
	r := run(t, srv, "s3cr3t-code\n", "domains", "transfer", "shop.co.uk", "--auth-code-stdin", "--contact-file", contactFile(t), "--accept-terms", "--yes")
	if r.err != nil {
		t.Fatal(r.err)
	}
	got := f.sentJSON("POST", "/domains/transfers")
	if got["auth_code"] != "s3cr3t-code" || got["name"] != "shop.co.uk" {
		t.Fatalf("body = %v", got)
	}
	if strings.Contains(r.stdout+r.stderr, "s3cr3t-code") {
		t.Fatal("printed the auth code")
	}
	// The longest TLD wins: co.uk's 8.00, not uk's 9.00.
	r = run(t, srv, "s3cr3t-code\n", "domains", "transfer", "shop.co.uk", "--auth-code-stdin", "--contact-file", contactFile(t), "--accept-terms")
	if r.err == nil || !strings.Contains(r.err.Error(), "USD 8.00") {
		t.Fatalf("err = %v", r.err)
	}
}

func TestDomainsRenewPrice(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /domains", 200, `{"data":[`+domainActive+`],"next_cursor":null}`)
	f.on("GET /domains/dom_1", 200, domainActive)
	f.on("GET /domains/tlds", 200, tldsBody)
	f.on("POST /domains/dom_1/renew", 202, domainOrderAccepted)
	r := run(t, srv, "", "domains", "renew", "example.com", "--years", "3")
	if r.err == nil || !strings.Contains(r.err.Error(), "USD 51.00") {
		t.Fatalf("err = %v", r.err)
	}
	if r := run(t, srv, "", "domains", "renew", "example.com", "--years", "3", "--yes"); r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.sentJSON("POST", "/domains/dom_1/renew"); !reflect.DeepEqual(got, map[string]any{"years": float64(3)}) {
		t.Fatalf("body = %v", got)
	}
}

func TestDomainsSet(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("PATCH /domains/dom_1", 200, domainActive)
	r := run(t, srv, "", "domains", "set", "dom_1", "--lock=off", "--auto-renew=on")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got, want := f.sentJSON("PATCH", "/domains/dom_1"), map[string]any{"transfer_lock": false, "auto_renew": true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %v, want %v", got, want)
	}
	if r := run(t, srv, "", "domains", "set", "dom_1", "--lock=maybe"); !IsUsage(r.err) {
		t.Fatalf("err = %v", r.err)
	}
	if r := run(t, srv, "", "domains", "set", "dom_1"); !IsUsage(r.err) {
		t.Fatalf("err = %v", r.err)
	}
}

func TestDomainsNameServers(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("PUT /domains/dom_1/name-servers", 202, domainActive)
	if r := run(t, srv, "", "domains", "nameservers", "dom_1", "--ns", "ns1.example.net", "--ns", "ns2.example.net"); r.err != nil {
		t.Fatal(r.err)
	}
	want := map[string]any{"use_pantech_dns": false, "name_servers": []any{"ns1.example.net", "ns2.example.net"}}
	if got := f.sentJSON("PUT", "/domains/dom_1/name-servers"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %v, want %v", got, want)
	}
	if r := run(t, srv, "", "domains", "nameservers", "dom_1", "--pantech", "--ns", "a.example.net"); !IsUsage(r.err) {
		t.Fatalf("err = %v", r.err)
	}
	if r := run(t, srv, "", "domains", "nameservers", "dom_1"); !IsUsage(r.err) {
		t.Fatalf("err = %v", r.err)
	}
}

func TestDomainsNameServersPantechWithAZone(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("PUT /domains/dom_1/name-servers", 202, domainActive)
	f.on("GET /dns/zones", 200, zoneList)
	if r := run(t, srv, "", "domains", "nameservers", "dom_1", "--pantech", "--zone", "example.com"); r.err != nil {
		t.Fatal(r.err)
	}
	if got, want := f.sentJSON("PUT", "/domains/dom_1/name-servers"), map[string]any{"use_pantech_dns": true, "zone_id": "dnsz_1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %v, want %v", got, want)
	}
}

func TestDomainsContactGetAndSet(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /domains/dom_1/contact", 200, contactJSON)
	f.on("PUT /domains/dom_1/contact", 202, domainActive)
	r := run(t, srv, "", "domains", "contact", "dom_1")
	if r.err != nil || !strings.Contains(r.stdout, "Ada Obi") || !strings.Contains(r.stdout, "ada@example.com") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	if r := run(t, srv, "", "domains", "contact", "dom_1", "--set", contactFile(t)); r.err != nil {
		t.Fatal(r.err)
	}
	got := f.sentJSON("PUT", "/domains/dom_1/contact")
	if c, ok := got["contact"].(map[string]any); !ok || c["email"] != "ada@example.com" {
		t.Fatalf("body = %v", got)
	}
}

func TestDomainsContactFileMustBeAnObject(t *testing.T) {
	_, srv := newFakeAPI(t)
	path := filepath.Join(t.TempDir(), "c.json")
	_ = os.WriteFile(path, []byte(`["not","an","object"]`), 0o600)
	r := run(t, srv, "", "domains", "contact", "dom_1", "--set", path)
	if r.err == nil || !strings.Contains(r.err.Error(), "JSON object") {
		t.Fatalf("err = %v", r.err)
	}
}

func TestDomainsAuthCodeAsksWaitsAndPrintsOnce(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /domains/dom_1/auth-code", 202, domainActive)
	f.on("GET /domains/dom_1/auth-code", 202, `{"status":"pending"}`).on("GET /domains/dom_1/auth-code", 200, `{"auth_code":"Xy7#q9!","expires_at":"2026-11-10T11:00:00Z"}`)
	if r := run(t, srv, "", "domains", "auth-code", "dom_1"); r.err == nil || !strings.Contains(r.err.Error(), "move the domain away") {
		t.Fatalf("err = %v", r.err)
	}
	if len(f.sent("POST", "/domains/dom_1/auth-code")) != 0 {
		t.Fatal("asked for the code without confirming")
	}
	r := run(t, srv, "", "domains", "auth-code", "dom_1", "--yes")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.stdout != "Xy7#q9!\n" {
		t.Fatalf("stdout %q", r.stdout)
	}
	if !strings.Contains(r.stderr, "leave Pantech") {
		t.Fatalf("stderr %q", r.stderr)
	}
	if n := len(f.sent("GET", "/domains/dom_1/auth-code")); n != 2 {
		t.Fatalf("polled %d times", n)
	}
}

func TestDomainsAuthCodeLockedSaysWhatToDo(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /domains/dom_1/auth-code", 409, `{"status":409,"code":"DOMAIN_LOCKED","detail":"Turn the transfer lock off first."}`)
	r := run(t, srv, "", "domains", "auth-code", "dom_1", "--yes")
	if r.err == nil || !strings.Contains(r.err.Error(), "pantech domains set dom_1 --lock=off") {
		t.Fatalf("err = %v", r.err)
	}
}

func TestDomainsSearchAndTLDs(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /domains/search", 200, `{"data":[{"name":"example.com","tld":"com","status":"unavailable","price":null},{"name":"example.dev","tld":"dev","status":"available","price":{"tld":"dev","currency":"USD","register_minor":1200,"renew_minor":1300,"transfer_minor":1200,"featured":false}}]}`)
	f.on("GET /domains/tlds", 200, tldsBody)
	r := run(t, srv, "", "domains", "search", "example")
	if r.err != nil || !strings.Contains(r.stdout, "USD 12.00") || !strings.Contains(r.stderr, "pantech domains register example.dev") {
		t.Fatalf("stdout %q stderr %q err %v", r.stdout, r.stderr, r.err)
	}
	if q := f.sent("GET", "/domains/search")[0].Query; q != "query=example" {
		t.Fatalf("query %q", q)
	}
	r = run(t, srv, "", "domains", "search", "example", "-q")
	if r.stdout != "example.dev\n" {
		t.Fatalf("quiet stdout %q", r.stdout)
	}
	r = run(t, srv, "", "domains", "tlds")
	if r.err != nil || !strings.Contains(r.stdout, ".co.uk") || !strings.Contains(r.stdout, "USD 17.00") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
}

func TestDomainsTransferCheck(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /domains/transfer-check", 200, `{"name":"example.com","transferable":true,"reason":null,"price":{"tld":"com","currency":"USD","register_minor":1500,"renew_minor":1700,"transfer_minor":1400,"featured":true}}`)
	r := run(t, srv, "", "domains", "transfer-check", "Example.com")
	if r.err != nil || !strings.Contains(r.stdout, "USD 14.00") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	if q := f.sent("GET", "/domains/transfer-check")[0].Query; q != "name=example.com" {
		t.Fatalf("query %q", q)
	}
}

func TestDomainsOrdersAndByName(t *testing.T) {
	f, srv := newFakeAPI(t)
	failedDup := strings.Replace(strings.Replace(domainActive, `"dom_1"`, `"dom_0"`, 1), `"active"`, `"failed"`, 1)
	f.on("GET /domains", 200, `{"data":[`+failedDup+`,`+domainActive+`],"next_cursor":null}`)
	f.on("GET /domains/dom_1/orders", 200, `{"data":[`+orderFailed+`],"next_cursor":null}`)
	r := run(t, srv, "", "domains", "orders", "example.com")
	if r.err != nil || !strings.Contains(r.stdout, "domain_unavailable (refunded)") || !strings.Contains(r.stdout, "USD 32.00") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
}

const (
	orderAwaitingPayment = `{"id":"dor_1","domain_id":"dom_1","domain_name":"example.com","kind":"register","years":2,"amount_minor":3200,"currency":"USD","status":"awaiting_payment","failure_code":null,"refunded":false,"payment_url":"https://pay.example.test/o/dor_1","payment_expires_at":"2026-11-10T11:04:41Z","created_at":"2026-11-10T10:04:41Z","completed_at":null}`
	orderPaid            = `{"id":"dor_1","domain_id":"dom_1","domain_name":"example.com","kind":"register","years":2,"amount_minor":3200,"currency":"USD","status":"paid","failure_code":null,"refunded":false,"payment_url":null,"payment_expires_at":null,"created_at":"2026-11-10T10:04:41Z","completed_at":null}`
	ngTLDs               = `{"data":[{"tld":"com.ng","currency":"NGN","register_minor":550000,"renew_minor":550000,"transfer_minor":550000,"featured":true,"privacy_always_on":true}]}`
)

func TestDomainsRegisterPrintsThePaymentLink(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /domains/tlds", 200, tldsBody)
	f.on("POST /domains", 202, domainOrderAccepted)
	f.on("GET /domain-orders/dor_1", 200, orderPending).on("GET /domain-orders/dor_1", 200, orderAwaitingPayment)
	r := run(t, srv, "", "domains", "register", "example.com", "--contact-file", contactFile(t), "--accept-terms", "--yes")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !strings.Contains(r.stderr, "https://pay.example.test/o/dor_1") || !strings.Contains(r.stderr, "confirm the card payment") {
		t.Fatalf("stderr %q", r.stderr)
	}
	// --quiet: the domain id on stdout, the link alone on stderr.
	f, srv = newFakeAPI(t)
	f.on("GET /domains/tlds", 200, tldsBody)
	f.on("POST /domains", 202, domainOrderAccepted)
	f.on("GET /domain-orders/dor_1", 200, orderAwaitingPayment)
	r = run(t, srv, "", "domains", "register", "example.com", "--contact-file", contactFile(t), "--accept-terms", "--yes", "-q")
	if r.err != nil || r.stdout != "dom_1\n" || strings.TrimSpace(r.stderr) != "https://pay.example.test/o/dor_1" {
		t.Fatalf("stdout %q stderr %q err %v", r.stdout, r.stderr, r.err)
	}
}

func TestDomainsRegisterWaitsThroughThePayment(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /domains/tlds", 200, tldsBody)
	f.on("POST /domains", 202, domainOrderAccepted)
	f.on("GET /domain-orders/dor_1", 200, orderPending).on("GET /domain-orders/dor_1", 200, orderAwaitingPayment).
		on("GET /domain-orders/dor_1", 200, orderAwaitingPayment).on("GET /domain-orders/dor_1", 200, orderPaid).on("GET /domain-orders/dor_1", 200, orderDone)
	f.on("GET /domains/dom_1", 200, domainActive)
	r := run(t, srv, "", "domains", "register", "example.com", "--contact-file", contactFile(t), "--accept-terms", "--yes", "--wait")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if strings.Count(r.stderr, "https://pay.example.test/o/dor_1") != 1 || !strings.Contains(r.stderr, "Payment received") {
		t.Fatalf("stderr %q", r.stderr)
	}
	if !strings.Contains(r.stdout, "example.com") {
		t.Fatalf("stdout %q", r.stdout)
	}

	// The window ends unpaid: insufficient_credit, nothing charged, exit 5.
	f, srv = newFakeAPI(t)
	f.on("GET /domains/tlds", 200, tldsBody)
	f.on("POST /domains", 202, domainOrderAccepted)
	f.on("GET /domain-orders/dor_1", 200, orderAwaitingPayment).
		on("GET /domain-orders/dor_1", 200, strings.Replace(strings.Replace(orderAwaitingPayment, `"awaiting_payment"`, `"failed"`, 1), `"failure_code":null`, `"failure_code":"insufficient_credit"`, 1))
	r = run(t, srv, "", "domains", "register", "example.com", "--contact-file", contactFile(t), "--accept-terms", "--yes", "--wait")
	var failed *api.DomainOrderFailed
	if !errors.As(r.err, &failed) || !strings.Contains(r.err.Error(), "nothing was charged") {
		t.Fatalf("err %v", r.err)
	}
}

func TestDomainsNGPrivacyIsAlwaysOn(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /domains/tlds", 200, ngTLDs)
	f.on("POST /domains", 202, domainOrderAccepted)
	r := run(t, srv, "", "domains", "register", "shop.com.ng", "--contact-file", contactFile(t), "--accept-terms", "--no-privacy", "--yes")
	if !IsUsage(r.err) || !strings.Contains(r.err.Error(), "The .ng registry hides every registrant's contact details") || len(f.sent("POST", "/domains")) != 0 {
		t.Fatalf("err %v", r.err)
	}
	if r := run(t, srv, "", "domains", "tlds"); r.err != nil || !strings.Contains(r.stdout, "always on") || !strings.Contains(r.stderr, "before VAT") {
		t.Fatalf("stdout %q stderr %q err %v", r.stdout, r.stderr, r.err)
	}

	f.on("GET /domains/dom_ng", 200, strings.Replace(strings.Replace(domainActive, `"example.com"`, `"shop.com.ng"`, 1), `"dom_1"`, `"dom_ng"`, 1))
	f.on("PATCH /domains/dom_ng", 200, domainActive)
	r = run(t, srv, "", "domains", "set", "dom_ng", "--privacy=off")
	if !IsUsage(r.err) || !strings.Contains(r.err.Error(), "cannot be turned off") || len(f.sent("PATCH", "/domains/dom_ng")) != 0 {
		t.Fatalf("err %v", r.err)
	}
}

func TestDomainsPrivacyAlwaysOnFieldError(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /domains/tlds", 200, tldsBody)
	f.on("POST /domains", 422, `{"status":422,"code":"VALIDATION_FAILED","detail":"privacy: always on.","errors":[{"field":"privacy","code":"PRIVACY_ALWAYS_ON","message":"always on"}]}`)
	r := run(t, srv, "", "domains", "register", "example.com", "--contact-file", contactFile(t), "--accept-terms", "--no-privacy", "--yes")
	if r.err == nil || !strings.Contains(r.err.Error(), "privacy stays on") {
		t.Fatalf("err %v", r.err)
	}
}

func TestDomainsRetryPayment(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /domain-orders/dor_1", 200, orderAwaitingPayment)
	f.on("POST /domain-orders/dor_1/retry-payment", 202, orderAwaitingPayment)
	// It asks first, naming the amount and that the saved card is charged.
	r := run(t, srv, "", "domains", "retry-payment", "dor_1")
	if r.err == nil || !strings.Contains(r.err.Error(), "USD 32.00") || !strings.Contains(r.err.Error(), "saved card") {
		t.Fatalf("err = %v", r.err)
	}
	if len(f.sent("POST", "/domain-orders/dor_1/retry-payment")) != 0 {
		t.Fatal("retried without asking")
	}
	r = run(t, srv, "", "domains", "retry-payment", "dor_1", "--yes")
	if r.err != nil {
		t.Fatal(r.err)
	}
	calls := f.sent("POST", "/domain-orders/dor_1/retry-payment")
	if len(calls) != 1 || len(calls[0].Body) != 0 {
		t.Fatalf("calls %+v", calls)
	}
	if !strings.Contains(r.stdout, "dor_1") || !strings.Contains(r.stdout, "awaiting_payment") {
		t.Fatalf("stdout %q", r.stdout)
	}
	if !strings.Contains(r.stderr, "https://pay.example.test/o/dor_1") || !strings.Contains(r.stderr, "pantech domains orders dom_1") {
		t.Fatalf("stderr %q", r.stderr)
	}
}

func TestDomainsRetryPaymentNairaSaysAddCredit(t *testing.T) {
	ngn := strings.Replace(strings.Replace(orderAwaitingPayment, `"USD"`, `"NGN"`, 1), `"payment_url":"https://pay.example.test/o/dor_1"`, `"payment_url":null`, 1)
	f, srv := newFakeAPI(t)
	f.on("GET /domain-orders/dor_1", 200, ngn)
	f.on("POST /domain-orders/dor_1/retry-payment", 202, ngn)
	r := run(t, srv, "", "domains", "retry-payment", "dor_1", "--yes")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !strings.Contains(r.stderr, "Add credit, then retry: pantech domains retry-payment dor_1") {
		t.Fatalf("stderr %q", r.stderr)
	}
	// -q: the order id alone; --json: the API's body.
	r = run(t, srv, "", "domains", "retry-payment", "dor_1", "--yes", "-q")
	if r.err != nil || r.stdout != "dor_1\n" {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	r = run(t, srv, "", "domains", "retry-payment", "dor_1", "--yes", "--json")
	if r.err != nil || !strings.Contains(r.stdout, `"currency": "NGN"`) || strings.Contains(r.stderr, "Add credit") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
}

func TestDomainsRetryPaymentAndWait(t *testing.T) {
	f, srv := newFakeAPI(t)
	// The read before asking, then the order as the retry leaves it.
	f.on("GET /domain-orders/dor_1", 200, orderAwaitingPayment).on("GET /domain-orders/dor_1", 200, orderAwaitingPayment).
		on("GET /domain-orders/dor_1", 200, orderPaid).on("GET /domain-orders/dor_1", 200, orderDone)
	f.on("POST /domain-orders/dor_1/retry-payment", 202, orderAwaitingPayment)
	f.on("GET /domains/dom_1", 200, domainActive)
	r := run(t, srv, "", "domains", "retry-payment", "dor_1", "--yes", "--wait")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !strings.Contains(r.stderr, "Payment received") || !strings.Contains(r.stdout, "example.com") {
		t.Fatalf("stdout %q stderr %q", r.stdout, r.stderr)
	}
}

func TestDomainsRetryPaymentNotAwaiting(t *testing.T) {
	// Already paid: refused before asking or calling the API.
	f, srv := newFakeAPI(t)
	f.on("GET /domain-orders/dor_1", 200, orderDone)
	r := run(t, srv, "", "domains", "retry-payment", "dor_1", "--yes")
	if r.err == nil || !strings.Contains(r.err.Error(), "not waiting for a payment") || !strings.Contains(r.err.Error(), "pantech domains orders dom_1") {
		t.Fatalf("err = %v", r.err)
	}
	if len(f.sent("POST", "/domain-orders/dor_1/retry-payment")) != 0 {
		t.Fatal("retried an order that is not awaiting payment")
	}
	// The hour ended between the read and the retry: the API's 409.
	f, srv = newFakeAPI(t)
	f.on("GET /domain-orders/dor_1", 200, orderAwaitingPayment)
	f.on("POST /domain-orders/dor_1/retry-payment", 409, `{"status":409,"code":"DOMAIN_ORDER_NOT_AWAITING_PAYMENT","detail":"The order is not waiting for a payment."}`)
	r = run(t, srv, "", "domains", "retry-payment", "dor_1", "--yes")
	if !api.IsCode(r.err, "DOMAIN_ORDER_NOT_AWAITING_PAYMENT") || !strings.Contains(r.err.Error(), "place the order again") {
		t.Fatalf("err = %v", r.err)
	}
	// An unknown order.
	f, srv = newFakeAPI(t)
	f.on("GET /domain-orders/dor_9", 404, `{"status":404,"code":"DOMAIN_ORDER_NOT_FOUND","detail":"No such order."}`)
	r = run(t, srv, "", "domains", "retry-payment", "dor_9", "--yes")
	if !api.IsCode(r.err, "DOMAIN_ORDER_NOT_FOUND") || !strings.Contains(r.err.Error(), "pantech domains orders") {
		t.Fatalf("err = %v", r.err)
	}
}

func TestDomainsOrdersSayWhatToDoWhileAwaitingPayment(t *testing.T) {
	saved := strings.Replace(orderAwaitingPayment, `"payment_url":"https://pay.example.test/o/dor_1"`, `"payment_url":null`, 1)
	f, srv := newFakeAPI(t)
	f.on("GET /domains", 200, `{"data":[`+domainActive+`],"next_cursor":null}`)
	f.on("GET /domains/dom_1/orders", 200, `{"data":[`+saved+`],"next_cursor":null}`)
	r := run(t, srv, "", "domains", "orders", "example.com")
	if r.err != nil || !strings.Contains(r.stdout, "awaiting_payment") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	if !strings.Contains(r.stderr, "saved card is being charged") || !strings.Contains(r.stderr, "pantech domains retry-payment dor_1") {
		t.Fatalf("stderr %q", r.stderr)
	}
}
