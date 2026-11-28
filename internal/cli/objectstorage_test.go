package cli

import (
	"reflect"
	"strings"
	"testing"
	"time"

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

func TestObjectStorageStatusBlockPublicAccessAndSettings(t *testing.T) {
	t.Run("status shows block public access", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("GET /object-storage", 200, `{"enabled":true,"endpoint":"https://s3.af-abj.pantechdynamics.com","beta":true,"direct_upload_enabled":true,"block_public_access":true}`)
		r := run(t, srv, "", "object-storage", "status")
		if r.err != nil || !strings.Contains(r.stdout, "Block public access") || !strings.Contains(r.stdout, "on") {
			t.Fatalf("stdout=%q err=%v", r.stdout, r.err)
		}
	})

	t.Run("settings requires flag and confirms when turning on", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("PATCH /object-storage/settings", 200, `{"block_public_access":true}`).
			on("PATCH /object-storage/settings", 200, `{"block_public_access":false}`)

		if r := run(t, srv, "", "object-storage", "settings"); !IsUsage(r.err) {
			t.Fatalf("missing flag err=%v, want usage error", r.err)
		}
		if r := run(t, srv, "", "object-storage", "settings", "--block-public-access=true"); r.err == nil || len(f.sent("PATCH", "/object-storage/settings")) != 0 {
			t.Fatalf("enabled block without confirmation: err=%v", r.err)
		}
		ron := run(t, srv, "", "object-storage", "settings", "--block-public-access=true", "--yes")
		if ron.err != nil || !strings.Contains(ron.stderr, "on") {
			t.Fatalf("on stderr=%q err=%v", ron.stderr, ron.err)
		}
		wantOn := map[string]any{"block_public_access": true}
		if got := f.sentJSON("PATCH", "/object-storage/settings"); !reflect.DeepEqual(got, wantOn) {
			t.Fatalf("on body=%v want=%v", got, wantOn)
		}

		// Turning off does not require confirmation.
		roff := run(t, srv, "", "object-storage", "settings", "--block-public-access=false", "-q")
		if roff.err != nil || strings.TrimSpace(roff.stdout) != "off" {
			t.Fatalf("off stdout=%q err=%v", roff.stdout, roff.err)
		}
		calls := f.sent("PATCH", "/object-storage/settings")
		if len(calls) != 2 || calls[0].IdempotencyKey == "" || calls[1].IdempotencyKey == "" {
			t.Fatalf("PATCH calls=%+v", calls)
		}
	})

	t.Run("public access blocked error hints settings command", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("PUT /buckets/bkt_1/objects/visibility", 403, `{"status":403,"code":"PUBLIC_ACCESS_BLOCKED","detail":"Public access is blocked for this organization."}`)
		r := run(t, srv, "", "buckets", "objects", "visibility", "bkt_1", "logo.png", "--public")
		if !api.IsCode(r.err, "PUBLIC_ACCESS_BLOCKED") || !strings.Contains(r.err.Error(), "object-storage settings --block-public-access=false") {
			t.Fatalf("err=%v", r.err)
		}
	})
}

func TestObjectStorageKeysScopedPermissionsPrefixesAndExpiry(t *testing.T) {
	fixedNow := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	oldNow := nowFunc
	nowFunc = func() time.Time { return fixedNow }
	t.Cleanup(func() { nowFunc = oldNow })

	t.Run("create with permissions, prefixes, expires-in and Idempotency-Key", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("POST /object-storage/keys", 201, `{"id":"osk_2","name":"uploader","region":"af-abj","access_key_id":"AKIAUP","read_only":false,"permissions":"write_only","buckets":["uploads"],"prefixes":["incoming/","raw/"],"created_at":"2026-11-28T12:00:00Z","expires_at":"2026-12-28T12:00:00Z","secret_access_key":"topsecret","endpoint":"https://s3.af-abj.pantechdynamics.com"}`)

		r := run(t, srv, "", "object-storage", "keys", "create",
			"--name", "uploader",
			"--permissions", "write_only",
			"--bucket", "uploads",
			"--prefix", "incoming/",
			"--prefix", "raw/",
			"--expires-in", "30d",
		)
		if r.err != nil || !strings.Contains(r.stdout, "topsecret") {
			t.Fatalf("stdout=%q err=%v", r.stdout, r.err)
		}
		calls := f.sent("POST", "/object-storage/keys")
		if len(calls) != 1 || calls[0].IdempotencyKey == "" {
			t.Fatalf("calls=%+v", calls)
		}
		want := map[string]any{
			"name":        "uploader",
			"permissions": "write_only",
			"buckets":     []any{"uploads"},
			"prefixes":    []any{"incoming/", "raw/"},
			"expires_at":  "2026-12-28T12:00:00Z",
		}
		if got := f.sentJSON("POST", "/object-storage/keys"); !reflect.DeepEqual(got, want) {
			t.Fatalf("body=%v want=%v", got, want)
		}
	})

	t.Run("create with expires-at and validation errors", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("POST /object-storage/keys", 201, `{"id":"osk_3","name":"ci","region":"af-abj","access_key_id":"AKIACI","read_only":false,"permissions":"read_write","buckets":[],"prefixes":[],"created_at":"2026-11-28T12:00:00Z","expires_at":"2027-01-01T00:00:00Z","secret_access_key":"sec","endpoint":"https://s3.af-abj.pantechdynamics.com"}`)

		for _, tc := range []struct {
			name string
			args []string
		}{
			{"invalid permissions", []string{"object-storage", "keys", "create", "--name", "k", "--permissions", "admin"}},
			{"conflicting read-only and permissions", []string{"object-storage", "keys", "create", "--name", "k", "--read-only", "--permissions", "write_only"}},
			{"both expires-at and expires-in", []string{"object-storage", "keys", "create", "--name", "k", "--expires-at", "2027-01-01T00:00:00Z", "--expires-in", "7d"}},
			{"invalid expires-at", []string{"object-storage", "keys", "create", "--name", "k", "--expires-at", "not-a-time"}},
			{"invalid expires-in", []string{"object-storage", "keys", "create", "--name", "k", "--expires-in", "0d"}},
		} {
			if r := run(t, srv, "", tc.args...); !IsUsage(r.err) {
				t.Fatalf("%s: err=%v, want usage error", tc.name, r.err)
			}
		}

		r := run(t, srv, "", "object-storage", "keys", "create", "--name", "ci", "--expires-at", "2027-01-01T00:00:00Z", "-q")
		if r.err != nil || r.stdout != "AKIACI\nsec\n" {
			t.Fatalf("stdout=%q err=%v", r.stdout, r.err)
		}
		got := f.sentJSON("POST", "/object-storage/keys")
		if got["expires_at"] != "2027-01-01T00:00:00Z" {
			t.Fatalf("body=%v", got)
		}
	})

	t.Run("list shows permissions, prefixes and expires", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("GET /object-storage/keys", 200, `{"data":[{"id":"osk_2","name":"uploader","region":"af-abj","access_key_id":"AKIAUP","read_only":false,"permissions":"write_only","buckets":["uploads"],"prefixes":["incoming/"],"created_at":"2026-11-28T12:00:00Z","expires_at":"2027-01-01T00:00:00Z"}]}`)
		r := run(t, srv, "", "object-storage", "keys", "list")
		if r.err != nil || !strings.Contains(r.stdout, "write only") || !strings.Contains(r.stdout, "incoming/") || !strings.Contains(r.stdout, "2027") {
			t.Fatalf("stdout=%q err=%v", r.stdout, r.err)
		}
	})

	t.Run("bucket flag help clarifies bucket names not bkt_ ids", func(t *testing.T) {
		fl := newObjectStorageKeysCreateCmd(&app{}).Flags().Lookup("bucket")
		if fl == nil || !strings.Contains(fl.Usage, "bucket name") || !strings.Contains(fl.Usage, "bkt_") {
			t.Fatalf("bucket flag=%+v", fl)
		}
	})
}

func TestBucketsEncryptionCreateUpdateGetList(t *testing.T) {
	encryptedBucket := strings.Replace(bucketBody, `"object_lock":false`, `"object_lock":false,"encryption":"sse_s3"`, 1)

	t.Run("create and update with encryption flag and validation", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("POST /buckets", 201, encryptedBucket).
			on("PATCH /buckets/bkt_1", 200, encryptedBucket).
			on("GET /buckets/bkt_1", 200, encryptedBucket).
			on("GET /buckets", 200, `{"data":[`+encryptedBucket+`]}`)

		if r := run(t, srv, "", "buckets", "create", "uploads", "--encryption", "aes", "--yes"); !IsUsage(r.err) {
			t.Fatalf("bad create encryption err=%v", r.err)
		}
		if r := run(t, srv, "", "buckets", "update", "bkt_1", "--encryption", "kms"); !IsUsage(r.err) {
			t.Fatalf("bad update encryption err=%v", r.err)
		}

		rc := run(t, srv, "", "buckets", "create", "uploads", "--encryption", "sse_s3", "--yes")
		if rc.err != nil || strings.TrimSpace(rc.stdout) != "bkt_1" {
			t.Fatalf("create stdout=%q err=%v", rc.stdout, rc.err)
		}
		if calls := f.sent("POST", "/buckets"); len(calls) != 1 || calls[0].IdempotencyKey == "" {
			t.Fatalf("create calls=%+v", calls)
		}
		if got := f.sentJSON("POST", "/buckets"); got["encryption"] != "sse_s3" {
			t.Fatalf("create body=%v", got)
		}

		ru := run(t, srv, "", "buckets", "update", "bkt_1", "--encryption", "sse_s3")
		if ru.err != nil || !strings.Contains(ru.stdout, "Encryption") || !strings.Contains(ru.stdout, "sse_s3") {
			t.Fatalf("update stdout=%q err=%v", ru.stdout, ru.err)
		}
		if calls := f.sent("PATCH", "/buckets/bkt_1"); len(calls) != 1 || calls[0].IdempotencyKey == "" {
			t.Fatalf("update calls=%+v", calls)
		}
		if got := f.sentJSON("PATCH", "/buckets/bkt_1"); !reflect.DeepEqual(got, map[string]any{"encryption": "sse_s3"}) {
			t.Fatalf("update body=%v", got)
		}

		rg := run(t, srv, "", "buckets", "get", "bkt_1")
		if rg.err != nil || !strings.Contains(rg.stdout, "Encryption") || !strings.Contains(rg.stdout, "sse_s3") {
			t.Fatalf("get stdout=%q err=%v", rg.stdout, rg.err)
		}

		rl := run(t, srv, "", "buckets", "list")
		if rl.err != nil || !strings.Contains(rl.stdout, "ENCRYPTION") || !strings.Contains(rl.stdout, "sse_s3") {
			t.Fatalf("list stdout=%q err=%v", rl.stdout, rl.err)
		}
	})
}
