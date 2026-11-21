package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
)

const (
	bucketBody = `{"id":"bkt_1","name":"uploads","region":"af-abj","endpoint":"https://s3.af-abj.pantechdynamics.com","quota_bytes":10737418240,
"versioning":true,"object_lock":false,"public_prefixes":[],"state":"active","created_at":"2026-11-20T09:00:00Z"}`
	osKeyBody = `{"id":"osk_1","name":"app","region":"af-abj","access_key_id":"AKIAEXAMPLE","read_only":true,"buckets":["uploads"],
"created_at":"2026-11-20T09:00:00Z"}`
)

func bucketsByName(f *fakeAPI) {
	f.on("GET /buckets", 200, `{"data":[`+bucketBody+`,{"id":"bkt_2","name":"uploads","state":"deleting"}]}`)
}

func TestObjectStorageStatus(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /object-storage", 200, `{"enabled":true,"endpoint":"https://s3.af-abj.pantechdynamics.com","beta":true}`)
	r := run(t, srv, "", "object-storage")
	if r.err != nil || !strings.Contains(r.stdout, "https://s3.af-abj.pantechdynamics.com") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
}

func TestObjectStorageStatusNotInBeta(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /object-storage", 200, `{"enabled":false,"endpoint":"https://s3.af-abj.pantechdynamics.com","beta":true}`)
	r := run(t, srv, "", "s3", "status")
	if r.err != nil || !strings.Contains(r.stderr, "closed beta") {
		t.Fatalf("stderr %q err %v", r.stderr, r.err)
	}
}

func TestBucketsList(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /buckets", 200, `{"data":[`+bucketBody+`]}`)
	r := run(t, srv, "", "buckets", "list")
	if r.err != nil || !strings.Contains(r.stdout, "uploads") || !strings.Contains(r.stdout, "10.0 GiB") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
}

func TestBucketsCreateAsksThenWaits(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /buckets", 201, strings.Replace(bucketBody, `"state":"active"`, `"state":"provisioning","operation_id":"op_7"`, 1)).
		on("GET /operations/op_7", 200, opSucceeded)

	if r := run(t, srv, "", "buckets", "create", "uploads"); r.err == nil || len(f.sent("POST", "/buckets")) != 0 {
		t.Fatalf("created without asking: err %v", r.err)
	}
	r := run(t, srv, "", "buckets", "create", "uploads", "--quota", "10", "--versioning", "--yes")
	if r.err != nil || strings.TrimSpace(r.stdout) != "bkt_1" {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	want := map[string]any{"name": "uploads", "versioning": true, "object_lock": false, "quota_bytes": float64(10 << 30)}
	if got := f.sentJSON("POST", "/buckets"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body %v", got)
	}
}

func TestBucketsDeleteByNameWithForce(t *testing.T) {
	f, srv := newFakeAPI(t)
	bucketsByName(f)
	f.on("DELETE /buckets/bkt_1", 202, `{"operation_id":"op_7","resource_id":"bkt_1","status":"submitted"}`).
		on("GET /operations/op_7", 200, opSucceeded)
	r := run(t, srv, "", "buckets", "delete", "uploads", "--force", "--yes")
	if r.err != nil {
		t.Fatal(r.err)
	}
	calls := f.sent("DELETE", "/buckets/bkt_1")
	if len(calls) != 1 || calls[0].Query != "force=true" {
		t.Fatalf("calls %+v", calls)
	}
}

func TestBucketsDeleteNotEmptySaysForce(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("DELETE /buckets/bkt_1", 409, `{"status":409,"code":"BUCKET_NOT_EMPTY","detail":"The bucket is not empty."}`)
	r := run(t, srv, "", "buckets", "delete", "bkt_1", "--yes")
	if !api.IsCode(r.err, "BUCKET_NOT_EMPTY") || !strings.Contains(r.err.Error(), "--force") {
		t.Fatalf("err %v", r.err)
	}
}

func TestObjectStorageKeysCreatePrintsSecretOnce(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /object-storage/keys", 201, strings.TrimSuffix(osKeyBody, "}")+`,"secret_access_key":"s3cr3t","endpoint":"https://s3.af-abj.pantechdynamics.com"}`)
	r := run(t, srv, "", "object-storage", "keys", "create", "--name", "app", "--read-only", "--bucket", "uploads")
	if r.err != nil || !strings.Contains(r.stdout, "s3cr3t") || !strings.Contains(r.stdout, "AKIAEXAMPLE") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	want := map[string]any{"name": "app", "read_only": true, "buckets": []any{"uploads"}}
	if got := f.sentJSON("POST", "/object-storage/keys"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body %v", got)
	}
}

func TestObjectStorageKeysListAndRevoke(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /object-storage/keys", 200, `{"data":[`+osKeyBody+`]}`).
		on("DELETE /object-storage/keys/osk_1", 204, ``)
	r := run(t, srv, "", "object-storage", "keys", "list")
	if r.err != nil || !strings.Contains(r.stdout, "read only") || strings.Contains(r.stdout, "secret") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	if r := run(t, srv, "", "object-storage", "keys", "revoke", "osk_1"); r.err == nil {
		t.Fatal("revoked without asking")
	}
	if r := run(t, srv, "", "object-storage", "keys", "revoke", "osk_1", "--yes"); r.err != nil || len(f.sent("DELETE", "/object-storage/keys/osk_1")) != 1 {
		t.Fatalf("err %v", r.err)
	}
}

func TestObjectStorageNotEnabledSaysBeta(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /buckets", 403, `{"status":403,"code":"OBJECT_STORAGE_NOT_ENABLED","detail":"Object storage is not enabled."}`)
	r := run(t, srv, "", "buckets", "list")
	if !api.IsCode(r.err, "OBJECT_STORAGE_NOT_ENABLED") || !strings.Contains(r.err.Error(), "closed beta") {
		t.Fatalf("err %v", r.err)
	}
}
