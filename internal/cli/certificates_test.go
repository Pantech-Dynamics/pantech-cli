package cli

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
)

const certPending = `{"id":"cert_1","kind":"public","names":["example.com","*.example.com","api.example.org"],"common_name":"example.com","description":"",
"key_type":"ecdsa-p256","exportable":true,"status":"pending_validation","pending_reason":"awaiting_cname","failure_code":null,
"validations":[{"name":"example.com","method":"pantech_dns","zone_id":"dnsz_1","cname_name":null,"cname_target":null,"status":"ready"},
{"name":"*.example.com","method":"pantech_dns","zone_id":"dnsz_1","cname_name":null,"cname_target":null,"status":"ready"},
{"name":"api.example.org","method":"cname","zone_id":null,"cname_name":"_acme-challenge.api.example.org","cname_target":"k3x9tq.acme.pantechdynamics.com","status":"pending"}],
"validation_deadline_at":"2026-11-13T10:00:00Z","issuer":null,"serial_number":null,"fingerprint_sha256":null,"not_before":null,"not_after":null,
"issued_at":null,"renew_after":null,"renewal_status":null,"in_use_by":[],"revocation_pending":false,"revoked_at":null,
"created_at":"2026-11-10T10:00:00Z","updated_at":"2026-11-10T10:00:00Z","version":1}`

var (
	certIssued = strings.NewReplacer(`"status":"pending_validation","pending_reason":"awaiting_cname"`, `"status":"issued","pending_reason":null`,
		`"not_after":null`, `"not_after":"2027-02-08T10:00:00Z"`, `"issuer":null`, `"issuer":"R11"`).Replace(certPending)
	certFailedBody = strings.Replace(certPending, `"status":"pending_validation","pending_reason":"awaiting_cname","failure_code":null`,
		`"status":"failed","pending_reason":null,"failure_code":"validation_timeout"`, 1)
)

const exportBody = `{"certificate_pem":"-----BEGIN CERTIFICATE-----\nLEAF\n-----END CERTIFICATE-----","chain_pem":"-----BEGIN CERTIFICATE-----\nINTER\n-----END CERTIFICATE-----\n",
"private_key_pem":"-----BEGIN PRIVATE KEY-----\nSECRETKEYMATERIAL\n-----END PRIVATE KEY-----\n","not_after":"2027-02-08T10:00:00Z"}`

func TestCertificatesRequestShowsTheCNAMEs(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /certificates", 202, certPending)
	r := run(t, srv, "", "certificates", "request", "Example.com", "*.example.com", "api.example.org", "--exportable")
	if r.err != nil {
		t.Fatal(r.err)
	}
	want := map[string]any{"names": []any{"example.com", "*.example.com", "api.example.org"}, "exportable": true}
	if got := f.sentJSON("POST", "/certificates"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body %v", got)
	}
	if !strings.Contains(r.stderr, "_acme-challenge.api.example.org  CNAME  k3x9tq.acme.pantechdynamics.com") || strings.Count(r.stderr, "CNAME  ") != 1 {
		t.Fatalf("stderr %q", r.stderr)
	}
	if strings.Contains(r.stdout, "CNAME  ") || !strings.Contains(r.stdout, "Pantech DNS (automatic)") {
		t.Fatalf("stdout %q", r.stdout)
	}
	// --json: the API's body only, no instructions mixed in.
	r = run(t, srv, "", "certificates", "request", "api.example.org", "--key-type", "rsa-2048", "--json")
	if r.err != nil || !strings.HasPrefix(strings.TrimSpace(r.stdout), "{") || r.stderr != "" {
		t.Fatalf("stdout %q stderr %q err %v", r.stdout, r.stderr, r.err)
	}
}

func TestCertificatesRequestWait(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /certificates", 202, certPending)
	f.on("GET /certificates/cert_1", 200, certPending)
	f.on("GET /certificates/cert_1", 200, certIssued)
	r := run(t, srv, "", "certificates", "request", "example.com", "--wait")
	if r.err != nil || !strings.Contains(r.stdout, "R11") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}

	f, srv = newFakeAPI(t)
	f.on("POST /certificates", 202, certPending)
	f.on("GET /certificates/cert_1", 200, certFailedBody)
	r = run(t, srv, "", "certificates", "request", "example.com", "--wait")
	var failed *api.CertificateFailed
	if !errors.As(r.err, &failed) || !strings.Contains(r.err.Error(), "validation_timeout") || !strings.Contains(r.err.Error(), "72 hours") {
		t.Fatalf("err %v", r.err)
	}
}

func TestCertificatesList(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /certificates", 200, `{"data":[`+certIssued+`],"next_cursor":null}`)
	r := run(t, srv, "", "certificates", "list", "--status", "issued", "--kind", "public")
	if r.err != nil || !strings.Contains(r.stdout, "cert_1") || !strings.Contains(r.stdout, "example.com, *.example.com, api.example.org") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	if q := f.sent("GET", "/certificates")[0].Query; !strings.Contains(q, "status=issued") || !strings.Contains(q, "kind=public") {
		t.Fatalf("query %q", q)
	}
}

func TestCertificatesImportReadsThePEMFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cert := write("cert.pem", "-----BEGIN CERTIFICATE-----\nLEAF\n-----END CERTIFICATE-----\n")
	key := write("key.pem", "-----BEGIN PRIVATE KEY-----\nSECRETKEYMATERIAL\n-----END PRIVATE KEY-----\n")
	notPEM := write("bad.pem", "hello")
	f, srv := newFakeAPI(t)
	f.on("POST /certificates/import", 201, certIssued)
	r := run(t, srv, "", "certificates", "import", "--cert", cert, "--key", key)
	if r.err != nil {
		t.Fatal(r.err)
	}
	got := f.sentJSON("POST", "/certificates/import")
	if got["certificate_pem"] == nil || got["private_key_pem"] == nil || got["chain_pem"] != nil {
		t.Fatalf("body keys %v", reflect.ValueOf(got).MapKeys())
	}
	if strings.Contains(r.stdout+r.stderr, "SECRETKEYMATERIAL") {
		t.Fatal("printed the key")
	}
	if r := run(t, srv, "", "certificates", "import", "--cert", cert); !IsUsage(r.err) {
		t.Fatalf("err %v", r.err)
	}
	if r := run(t, srv, "", "certificates", "import", "--cert", notPEM, "--key", key); r.err == nil || !strings.Contains(r.err.Error(), "not a PEM file") {
		t.Fatalf("err %v", r.err)
	}
}

func TestCertificatesExportWritesPrivateFiles(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /certificates/cert_1/export", 200, exportBody)
	dir := filepath.Join(t.TempDir(), "out")

	r := run(t, srv, "", "certificates", "export", "cert_1", "--out-dir", dir)
	if r.err == nil || !strings.Contains(r.err.Error(), "--yes") || len(f.sent("POST", "/certificates/cert_1/export")) != 0 {
		t.Fatalf("err %v", r.err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("made the directory before asking")
	}

	r = run(t, srv, "", "certificates", "export", "cert_1", "--out-dir", dir, "--yes")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if strings.Contains(r.stdout+r.stderr, "SECRETKEYMATERIAL") {
		t.Fatal("printed the key")
	}
	for name, want := range map[string]string{
		"cert.pem":      "-----BEGIN CERTIFICATE-----\nLEAF\n-----END CERTIFICATE-----\n",
		"chain.pem":     "-----BEGIN CERTIFICATE-----\nINTER\n-----END CERTIFICATE-----\n",
		"fullchain.pem": "-----BEGIN CERTIFICATE-----\nLEAF\n-----END CERTIFICATE-----\n-----BEGIN CERTIFICATE-----\nINTER\n-----END CERTIFICATE-----\n",
		"privkey.pem":   "-----BEGIN PRIVATE KEY-----\nSECRETKEYMATERIAL\n-----END PRIVATE KEY-----\n",
	} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("%s: %q %v", name, data, err)
		}
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", name, info.Mode().Perm())
		}
	}

	// A second export does not overwrite without --force, and asks the API nothing.
	before := len(f.sent("POST", "/certificates/cert_1/export"))
	if r := run(t, srv, "", "certificates", "export", "cert_1", "--out-dir", dir, "--yes"); r.err == nil || !strings.Contains(r.err.Error(), "--force") {
		t.Fatalf("err %v", r.err)
	}
	if len(f.sent("POST", "/certificates/cert_1/export")) != before {
		t.Fatal("exported although nothing could be written")
	}
	if r := run(t, srv, "", "certificates", "export", "cert_1", "--out-dir", dir, "--yes", "--force"); r.err != nil {
		t.Fatal(r.err)
	}
}

func TestCertificatesExportRefusesSymlinks(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /certificates/cert_1/export", 200, exportBody)
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Symlink(target, filepath.Join(dir, "privkey.pem")); err != nil {
		t.Skip(err)
	}
	r := run(t, srv, "", "certificates", "export", "cert_1", "--out-dir", dir, "--yes", "--force")
	if r.err == nil || !strings.Contains(r.err.Error(), "symbolic link") {
		t.Fatalf("err %v", r.err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("wrote through the link")
	}
}

func TestCertificatesExportToStdout(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /certificates/cert_1/export", 200, exportBody)
	r := run(t, srv, "", "certificates", "export", "cert_1", "--stdout", "--yes")
	if r.err != nil || !strings.HasPrefix(r.stdout, "-----BEGIN CERTIFICATE-----\nLEAF") || !strings.HasSuffix(r.stdout, "SECRETKEYMATERIAL\n-----END PRIVATE KEY-----\n") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	if r := run(t, srv, "", "certificates", "export", "cert_1", "--stdout", "--out-dir", "x", "--yes"); !IsUsage(r.err) {
		t.Fatalf("err %v", r.err)
	}
}

func TestCertificatesNotExportableSaysWhy(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /certificates/cert_1/export", 409, `{"status":409,"code":"CERTIFICATE_NOT_EXPORTABLE","detail":"This certificate was created without exportable."}`)
	r := run(t, srv, "", "certificates", "export", "cert_1", "--out-dir", t.TempDir(), "--yes")
	if !api.IsCode(r.err, "CERTIFICATE_NOT_EXPORTABLE") || !strings.Contains(r.err.Error(), "--exportable") {
		t.Fatalf("err %v", r.err)
	}
}

func TestCertificatesRevokeAndDeleteAskFirst(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /certificates/cert_1/revoke", 202, strings.Replace(certIssued, `"revocation_pending":false`, `"revocation_pending":true`, 1))
	f.on("DELETE /certificates/cert_1", 204, ``)
	for _, args := range [][]string{{"certificates", "revoke", "cert_1"}, {"certificates", "delete", "cert_1"}} {
		if r := run(t, srv, "", args...); r.err == nil || !strings.Contains(r.err.Error(), "--yes") {
			t.Fatalf("%v: err %v", args, r.err)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("calls %+v", f.calls)
	}
	if r := run(t, srv, "", "certificates", "revoke", "cert_1", "--reason", "key_compromise", "--yes"); r.err != nil || !strings.Contains(r.stdout, "pending at the CA") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	if got := f.sentJSON("POST", "/certificates/cert_1/revoke"); got["reason"] != "key_compromise" {
		t.Fatalf("body %v", got)
	}
	if r := run(t, srv, "", "certificates", "delete", "cert_1", "--yes"); r.err != nil || len(f.sent("DELETE", "/certificates/cert_1")) != 1 {
		t.Fatalf("err %v", r.err)
	}
	if r := run(t, srv, "", "certificates", "revoke", "cert_1", "--reason", "boredom", "--yes"); !IsUsage(r.err) {
		t.Fatalf("err %v", r.err)
	}
}

func TestCertificatesInUseSaysWhatToDo(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("DELETE /certificates/cert_1", 409, `{"status":409,"code":"CERTIFICATE_IN_USE","detail":"A load balancer uses this certificate."}`)
	r := run(t, srv, "", "certificates", "delete", "cert_1", "--yes")
	if !api.IsCode(r.err, "CERTIFICATE_IN_USE") || !strings.Contains(r.err.Error(), "Detach it") {
		t.Fatalf("err %v", r.err)
	}
}

func TestCertificatesVerifyUpdateReimport(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "c.pem")
	key := filepath.Join(dir, "k.pem")
	_ = os.WriteFile(cert, []byte("-----BEGIN CERTIFICATE-----\nX\n-----END CERTIFICATE-----\n"), 0o600)
	_ = os.WriteFile(key, []byte("-----BEGIN PRIVATE KEY-----\nY\n-----END PRIVATE KEY-----\n"), 0o600)
	f, srv := newFakeAPI(t)
	f.on("POST /certificates/cert_1/verify", 202, certPending)
	f.on("PATCH /certificates/cert_1", 200, certIssued)
	f.on("POST /certificates/cert_1/reimport", 422, `{"status":422,"code":"CERTIFICATE_NAMES_MISMATCH","detail":"The names differ."}`)
	if r := run(t, srv, "", "certificates", "verify", "cert_1"); r.err != nil || !strings.Contains(r.stderr, "_acme-challenge.api.example.org") {
		t.Fatalf("stderr %q err %v", r.stderr, r.err)
	}
	if r := run(t, srv, "", "certificates", "update", "cert_1", "--description", "Site"); r.err != nil {
		t.Fatal(r.err)
	}
	if got := f.sentJSON("PATCH", "/certificates/cert_1"); got["description"] != "Site" {
		t.Fatalf("body %v", got)
	}
	r := run(t, srv, "", "certificates", "reimport", "cert_1", "--cert", cert, "--key", key)
	if !api.IsCode(r.err, "CERTIFICATE_NAMES_MISMATCH") || !strings.Contains(r.err.Error(), "same names") {
		t.Fatalf("err %v", r.err)
	}
}
