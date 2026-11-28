package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestObjectStorageStatusWithLimitsAndUsage(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /object-storage", 200, `{"enabled":true,"endpoint":"https://s3.af-abj.pantechdynamics.com","beta":true,"direct_upload_enabled":true,"limits":{"max_buckets":25,"max_storage_bytes":107374182400,"max_objects":1000000}}`).
		on("GET /object-storage/usage", 200, `{"range":"7d","total_bytes_sent":2097152,"total_bytes_received":1048576,"total_read_ops":42,"total_write_ops":7,"hours":[{"hour":"2026-11-24T10:00:00Z","bytes_sent":2097152,"bytes_received":1048576,"read_ops":42,"write_ops":7}]}`)

	r := run(t, srv, "", "object-storage")
	if r.err != nil || !strings.Contains(r.stdout, "Direct uploads") || !strings.Contains(r.stdout, "25") || !strings.Contains(r.stdout, "100.0 GiB") {
		t.Fatalf("status stdout=%q err=%v", r.stdout, r.err)
	}

	u := run(t, srv, "", "object-storage", "usage", "--range", "7d")
	if u.err != nil || !strings.Contains(u.stdout, "2.0 MiB") || !strings.Contains(u.stdout, "42") {
		t.Fatalf("usage stdout=%q err=%v", u.stdout, u.err)
	}
	if calls := f.sent("GET", "/object-storage/usage"); len(calls) != 1 || calls[0].Query != "range=7d" {
		t.Fatalf("usage calls=%+v", calls)
	}

	uq := run(t, srv, "", "object-storage", "usage", "--range", "7d", "-q")
	if uq.err != nil || strings.TrimSpace(uq.stdout) != "2097152 1048576 42 7" {
		t.Fatalf("quiet stdout=%q err=%v", uq.stdout, uq.err)
	}

	uj := run(t, srv, "", "object-storage", "usage", "--json")
	if uj.err != nil || !strings.Contains(uj.stdout, `"total_read_ops": 42`) {
		t.Fatalf("json stdout=%q err=%v", uj.stdout, uj.err)
	}

	bad := run(t, srv, "", "object-storage", "usage", "--range", "90d")
	if !IsUsage(bad.err) {
		t.Fatalf("want usage error for --range 90d, got %v", bad.err)
	}
}

func TestBucketsUpdateStatsAndEmpty(t *testing.T) {
	t.Run("update sends only changed fields and waits on operation", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		bucketsByName(f)
		f.on("PATCH /buckets/bkt_1", 200, strings.Replace(bucketBody, `"state":"active"`, `"state":"updating","operation_id":"op_8"`, 1)).
			on("GET /operations/op_8", 200, opSucceeded)

		r := run(t, srv, "", "buckets", "update", "uploads", "--quota-gb", "50", "--no-versioning", "--public-prefix", "public/", "--public-prefix", "assets/")
		if r.err != nil {
			t.Fatal(r.err)
		}
		calls := f.sent("PATCH", "/buckets/bkt_1")
		if len(calls) != 1 || calls[0].IdempotencyKey == "" {
			t.Fatalf("PATCH calls=%+v", calls)
		}
		want := map[string]any{
			"quota_bytes":     float64(50 << 30),
			"versioning":      false,
			"public_prefixes": []any{"public/", "assets/"},
		}
		if got := f.sentJSON("PATCH", "/buckets/bkt_1"); !reflect.DeepEqual(got, want) {
			t.Fatalf("body=%v want=%v", got, want)
		}
	})

	t.Run("update clear flags and validation", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("PATCH /buckets/bkt_1", 200, bucketBody)

		if r := run(t, srv, "", "buckets", "update", "bkt_1"); !IsUsage(r.err) {
			t.Fatalf("empty update err=%v, want usage error", r.err)
		}
		if r := run(t, srv, "", "buckets", "update", "bkt_1", "--quota-gb", "10", "--no-quota"); !IsUsage(r.err) {
			t.Fatalf("conflicting quota err=%v", r.err)
		}
		if r := run(t, srv, "", "buckets", "update", "bkt_1", "--versioning", "--no-versioning"); !IsUsage(r.err) {
			t.Fatalf("conflicting versioning err=%v", r.err)
		}

		r := run(t, srv, "", "buckets", "update", "bkt_1", "--no-quota", "--clear-public-prefixes", "--no-wait", "-q")
		if r.err != nil || strings.TrimSpace(r.stdout) != "bkt_1" {
			t.Fatalf("stdout=%q err=%v", r.stdout, r.err)
		}
		want := map[string]any{"quota_bytes": float64(0), "public_prefixes": []any{}}
		if got := f.sentJSON("PATCH", "/buckets/bkt_1"); !reflect.DeepEqual(got, want) {
			t.Fatalf("body=%v want=%v", got, want)
		}
	})

	t.Run("stats", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("GET /buckets/bkt_1/stats", 200, `{"size_bytes":5242880,"object_count":12}`)
		r := run(t, srv, "", "buckets", "stats", "bkt_1")
		if r.err != nil || !strings.Contains(r.stdout, "5.0 MiB") || !strings.Contains(r.stdout, "12") {
			t.Fatalf("stdout=%q err=%v", r.stdout, r.err)
		}
		rq := run(t, srv, "", "buckets", "stats", "bkt_1", "-q")
		if rq.err != nil || strings.TrimSpace(rq.stdout) != "5242880 12" {
			t.Fatalf("quiet=%q err=%v", rq.stdout, rq.err)
		}
	})

	t.Run("empty requires confirm and reports locked objects", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("POST /buckets/bkt_1/empty", 200, `{"deleted_count":15,"aborted_uploads":2,"locked_count":1,"sample_locked_keys":["audit/2026.log"]}`)

		if r := run(t, srv, "", "buckets", "empty", "bkt_1"); r.err == nil || len(f.sent("POST", "/buckets/bkt_1/empty")) != 0 {
			t.Fatalf("emptied without confirmation: err=%v", r.err)
		}
		r := run(t, srv, "", "buckets", "empty", "bkt_1", "--yes")
		if r.err != nil || !strings.Contains(r.stderr, "15 objects") || !strings.Contains(r.stderr, "audit/2026.log") {
			t.Fatalf("stderr=%q err=%v", r.stderr, r.err)
		}
		calls := f.sent("POST", "/buckets/bkt_1/empty")
		if len(calls) != 1 || calls[0].IdempotencyKey == "" {
			t.Fatalf("calls=%+v", calls)
		}
	})
}

func TestBucketRulesCORSLifecycleTagsAndObjectLock(t *testing.T) {
	t.Run("cors get and set from file and stdin", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		corsResp := `{"rules":[{"id":"web","allowed_origins":["https://app.example.com"],"allowed_methods":["GET","PUT"],"allowed_headers":["*"],"expose_headers":["ETag"],"max_age_seconds":3600}]}`
		f.on("GET /buckets/bkt_1/cors", 200, corsResp).
			on("PUT /buckets/bkt_1/cors", 200, corsResp)

		rg := run(t, srv, "", "buckets", "cors", "get", "bkt_1")
		if rg.err != nil || !strings.Contains(rg.stdout, "https://app.example.com") {
			t.Fatalf("get stdout=%q err=%v", rg.stdout, rg.err)
		}

		// Bare JSON array via stdin
		rs := run(t, srv, `[{"id":"web","allowed_origins":["https://app.example.com"],"allowed_methods":["GET","PUT"],"allowed_headers":["*"],"expose_headers":["ETag"],"max_age_seconds":3600}]`,
			"buckets", "cors", "set", "bkt_1", "--file", "-")
		if rs.err != nil || !strings.Contains(rs.stderr, "1 CORS rule") {
			t.Fatalf("set stderr=%q err=%v", rs.stderr, rs.err)
		}
		if calls := f.sent("PUT", "/buckets/bkt_1/cors"); len(calls) != 1 || calls[0].IdempotencyKey == "" {
			t.Fatalf("calls=%+v", calls)
		}
	})

	t.Run("lifecycle get and set from file", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		lcResp := `{"rules":[{"id":"expire-logs","prefix":"logs/","enabled":true,"expiration_days":30,"noncurrent_expiration_days":7,"abort_incomplete_multipart_days":3,"expired_object_delete_marker":false}]}`
		f.on("GET /buckets/bkt_1/lifecycle", 200, lcResp).
			on("PUT /buckets/bkt_1/lifecycle", 200, lcResp)

		rg := run(t, srv, "", "buckets", "lifecycle", "get", "bkt_1")
		if rg.err != nil || !strings.Contains(rg.stdout, "expire-logs") || !strings.Contains(rg.stdout, "30d") {
			t.Fatalf("get stdout=%q err=%v", rg.stdout, rg.err)
		}

		p := filepath.Join(t.TempDir(), "lc.json")
		if err := os.WriteFile(p, []byte(lcResp), 0o600); err != nil {
			t.Fatal(err)
		}
		rs := run(t, srv, "", "buckets", "lifecycle", "set", "bkt_1", "--file", p, "--json")
		if rs.err != nil || !strings.Contains(rs.stdout, `"expire-logs"`) {
			t.Fatalf("set stdout=%q err=%v", rs.stdout, rs.err)
		}
	})

	t.Run("tags get, set and clear", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("GET /buckets/bkt_1/tags", 200, `{"tags":[{"key":"env","value":"prod"}]}`).
			on("PUT /buckets/bkt_1/tags", 200, `{"tags":[{"key":"env","value":"prod"},{"key":"team","value":"web"}]}`).
			on("PUT /buckets/bkt_1/tags", 200, `{"tags":[]}`)

		if r := run(t, srv, "", "buckets", "tags", "get", "bkt_1"); r.err != nil || !strings.Contains(r.stdout, "prod") {
			t.Fatalf("get stdout=%q err=%v", r.stdout, r.err)
		}
		if r := run(t, srv, "", "buckets", "tags", "set", "bkt_1", "--tag", "env=prod", "--tag", "team=web", "-q"); r.err != nil || r.stdout != "env=prod\nteam=web\n" {
			t.Fatalf("set quiet=%q err=%v", r.stdout, r.err)
		}
		if r := run(t, srv, "", "buckets", "tags", "set", "bkt_1", "--clear"); r.err != nil || !strings.Contains(r.stderr, "Cleared") {
			t.Fatalf("clear stderr=%q err=%v", r.stderr, r.err)
		}
		calls := f.sent("PUT", "/buckets/bkt_1/tags")
		if len(calls) != 2 || calls[0].IdempotencyKey == "" {
			t.Fatalf("calls=%+v", calls)
		}
	})

	t.Run("object-lock get, set and clear", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("GET /buckets/bkt_1/object-lock", 200, `{"enabled":true,"mode":"GOVERNANCE","days":30,"years":0}`).
			on("PUT /buckets/bkt_1/object-lock", 200, `{"enabled":true,"mode":"COMPLIANCE","days":0,"years":1}`).
			on("PUT /buckets/bkt_1/object-lock", 200, `{"enabled":false,"mode":"","days":0,"years":0}`)

		if r := run(t, srv, "", "buckets", "object-lock", "get", "bkt_1"); r.err != nil || !strings.Contains(r.stdout, "GOVERNANCE") || !strings.Contains(r.stdout, "30 days") {
			t.Fatalf("get stdout=%q err=%v", r.stdout, r.err)
		}
		if r := run(t, srv, "", "buckets", "object-lock", "set", "bkt_1", "--mode", "bad", "--days", "1"); !IsUsage(r.err) {
			t.Fatalf("bad mode err=%v", r.err)
		}
		if r := run(t, srv, "", "buckets", "object-lock", "set", "bkt_1", "--mode", "compliance", "--years", "1"); r.err != nil || !strings.Contains(r.stderr, "COMPLIANCE") {
			t.Fatalf("set stderr=%q err=%v", r.stderr, r.err)
		}
		if r := run(t, srv, "", "buckets", "object-lock", "set", "bkt_1", "--clear", "-q"); r.err != nil || r.stdout != "off\n" {
			t.Fatalf("clear stdout=%q err=%v", r.stdout, r.err)
		}
		var first map[string]any
		_ = json.Unmarshal(f.sent("PUT", "/buckets/bkt_1/object-lock")[0].Body, &first)
		if first["mode"] != "COMPLIANCE" || first["years"] != float64(1) || first["enabled"] != true {
			t.Fatalf("first PUT body=%v", first)
		}
	})
}

func TestBucketObjectsListMetaUploadDownload(t *testing.T) {
	t.Run("list objects and versions", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("GET /buckets/bkt_1/objects", 200, `{"prefix":"images/","folders":["images/2026/"],"objects":[{"key":"images/logo.png","size_bytes":2048,"etag":"abc","content_type":"image/png","last_modified":"2026-11-24T10:00:00Z","public":true,"object_public":true,"public_source":"object","public_url":"https://api.pantechdynamics.com/storage/public/bkt_1/images/logo.png"}],"next_cursor":"tok2","is_truncated":true}`).
			on("GET /buckets/bkt_1/objects", 200, `{"versions":[{"key":"images/logo.png","version_id":"v2","is_latest":true,"is_delete_marker":false,"size_bytes":2048,"etag":"abc","last_modified":"2026-11-24T10:00:00Z"}],"is_truncated":false}`)

		r := run(t, srv, "", "buckets", "objects", "list", "bkt_1", "--prefix", "images/", "--delimiter", "/", "--search", "logo", "--limit", "50")
		if r.err != nil || !strings.Contains(r.stdout, "images/2026/") || !strings.Contains(r.stdout, "images/logo.png") || !strings.Contains(r.stderr, "--cursor tok2") {
			t.Fatalf("stdout=%q stderr=%q err=%v", r.stdout, r.stderr, r.err)
		}
		rv := run(t, srv, "", "buckets", "obj", "list", "bkt_1", "--versions", "-q")
		if rv.err != nil || rv.stdout != "images/logo.png\tv2\n" {
			t.Fatalf("versions stdout=%q err=%v", rv.stdout, rv.err)
		}
	})

	t.Run("meta", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("GET /buckets/bkt_1/objects/meta", 200, `{"key":"images/logo.png","size_bytes":2048,"etag":"abc123","content_type":"image/png","last_modified":"2026-11-24T10:00:00Z","version_id":"v1","metadata":{"author":"ada"},"tags":[{"key":"env","value":"prod"}],"retention_mode":"GOVERNANCE","retain_until":"2027-01-01T00:00:00Z","legal_hold":true,"public":true,"object_public":true,"public_source":"object","public_url":"https://api.pantechdynamics.com/storage/public/bkt_1/images/logo.png"}`)
		r := run(t, srv, "", "buckets", "objects", "meta", "bkt_1", "images/logo.png", "--version", "v1")
		if r.err != nil || !strings.Contains(r.stdout, "abc123") || !strings.Contains(r.stdout, "GOVERNANCE") || !strings.Contains(r.stdout, "env=prod") || !strings.Contains(r.stdout, "ada") {
			t.Fatalf("meta stdout=%q err=%v", r.stdout, r.err)
		}
		if q := f.sent("GET", "/buckets/bkt_1/objects/meta")[0].Query; !strings.Contains(q, "version_id=v1") {
			t.Fatalf("query=%q", q)
		}
	})

	t.Run("single-shot upload sends raw bytes, Content-Type and Idempotency-Key", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("PUT /buckets/bkt_1/objects/upload", 200, `{"key":"hello.txt","size_bytes":11,"etag":"e1","content_type":"text/plain","last_modified":"2026-11-24T10:00:00Z","public":false,"public_source":"private"}`)

		src := filepath.Join(t.TempDir(), "hello.txt")
		if err := os.WriteFile(src, []byte("hello world"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := run(t, srv, "", "buckets", "objects", "upload", "bkt_1", "hello.txt", "--file", src, "--content-type", "text/plain")
		if r.err != nil || !strings.Contains(r.stderr, "Uploaded hello.txt") {
			t.Fatalf("stderr=%q err=%v", r.stderr, r.err)
		}
		calls := f.sent("PUT", "/buckets/bkt_1/objects/upload")
		if len(calls) != 1 || calls[0].IdempotencyKey == "" || string(calls[0].Body) != "hello world" || calls[0].Header.Get("Content-Type") != "text/plain" {
			t.Fatalf("upload call=%+v", calls)
		}
	})

	t.Run("multipart upload splits large file into start, parts and complete", func(t *testing.T) {
		old := singleUploadMaxBytes
		singleUploadMaxBytes = 10
		t.Cleanup(func() { singleUploadMaxBytes = old })

		f, srv := newFakeAPI(t)
		f.on("POST /buckets/bkt_1/objects/multipart/start", 200, `{"upload_id":"up_1","mode":"proxy","part_size_bytes":6}`).
			on("PUT /buckets/bkt_1/objects/multipart/part", 200, `{"part_number":1,"etag":"etag1"}`).
			on("PUT /buckets/bkt_1/objects/multipart/part", 200, `{"part_number":2,"etag":"etag2"}`).
			on("PUT /buckets/bkt_1/objects/multipart/part", 200, `{"part_number":3,"etag":"etag3"}`).
			on("POST /buckets/bkt_1/objects/multipart/complete", 200, `{"key":"big.bin","etag":"final","public":false,"public_source":"private"}`)

		src := filepath.Join(t.TempDir(), "big.bin")
		if err := os.WriteFile(src, []byte("0123456789abcdef"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := run(t, srv, "", "buckets", "objects", "upload", "bkt_1", "big.bin", "--file", src, "-q")
		if r.err != nil || r.stdout != "big.bin\n" {
			t.Fatalf("stdout=%q err=%v", r.stdout, r.err)
		}
		parts := f.sent("PUT", "/buckets/bkt_1/objects/multipart/part")
		if len(parts) != 3 || string(parts[0].Body) != "012345" || string(parts[1].Body) != "6789ab" || string(parts[2].Body) != "cdef" {
			t.Fatalf("parts=%+v", parts)
		}
		comp := f.sentJSON("POST", "/buckets/bkt_1/objects/multipart/complete")
		if comp["upload_id"] != "up_1" || len(comp["parts"].([]any)) != 3 {
			t.Fatalf("complete=%v", comp)
		}
	})

	t.Run("download to stdout and to file with overwrite guard", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("GET /buckets/bkt_1/objects/download", 200, "binary-payload")

		r := run(t, srv, "", "buckets", "objects", "download", "bkt_1", "data.bin")
		if r.err != nil || r.stdout != "binary-payload" {
			t.Fatalf("stdout=%q err=%v", r.stdout, r.err)
		}

		dst := filepath.Join(t.TempDir(), "out.bin")
		rf := run(t, srv, "", "buckets", "objects", "download", "bkt_1", "data.bin", "--out", dst, "--version", "v2")
		if rf.err != nil {
			t.Fatal(rf.err)
		}
		got, err := os.ReadFile(dst)
		if err != nil || string(got) != "binary-payload" {
			t.Fatalf("file=%q err=%v", got, err)
		}
		// Existing file refused without --force
		if r2 := run(t, srv, "", "buckets", "objects", "download", "bkt_1", "data.bin", "--out", dst); r2.err == nil || !strings.Contains(r2.err.Error(), "--force") {
			t.Fatalf("expected overwrite error, got %v", r2.err)
		}
		// Succeeds with --force
		if r3 := run(t, srv, "", "buckets", "objects", "download", "bkt_1", "data.bin", "--out", dst, "--force", "-q"); r3.err != nil || strings.TrimSpace(r3.stdout) != dst {
			t.Fatalf("force stdout=%q err=%v", r3.stdout, r3.err)
		}
	})
}

func TestBucketObjectsPresignFolderCopyDeleteVisibilityTagsLock(t *testing.T) {
	t.Run("presign-download and presign-upload", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("POST /buckets/bkt_1/objects/download-url", 200, `{"direct":true,"url":"https://s3.af-abj.pantechdynamics.com/signed-get","expires_in_seconds":3600}`).
			on("POST /buckets/bkt_1/objects/upload-url", 200, `{"direct":true,"url":"https://s3.af-abj.pantechdynamics.com/signed-put","expires_in_seconds":900}`)

		rd := run(t, srv, "", "buckets", "objects", "presign-download", "bkt_1", "report.pdf", "--expires-in", "3600", "--version", "v1")
		if rd.err != nil || strings.TrimSpace(rd.stdout) != "https://s3.af-abj.pantechdynamics.com/signed-get" {
			t.Fatalf("stdout=%q err=%v", rd.stdout, rd.err)
		}
		wantDL := map[string]any{"key": "report.pdf", "version_id": "v1", "expires_in_seconds": float64(3600)}
		if got := f.sentJSON("POST", "/buckets/bkt_1/objects/download-url"); !reflect.DeepEqual(got, wantDL) {
			t.Fatalf("download-url body=%v want=%v", got, wantDL)
		}

		ru := run(t, srv, "", "buckets", "objects", "presign-upload", "bkt_1", "report.pdf", "--content-type", "application/pdf")
		if ru.err != nil || strings.TrimSpace(ru.stdout) != "https://s3.af-abj.pantechdynamics.com/signed-put" {
			t.Fatalf("stdout=%q err=%v", ru.stdout, ru.err)
		}
		wantUL := map[string]any{"key": "report.pdf", "content_type": "application/pdf"}
		if got := f.sentJSON("POST", "/buckets/bkt_1/objects/upload-url"); !reflect.DeepEqual(got, wantUL) {
			t.Fatalf("upload-url body=%v want=%v", got, wantUL)
		}
	})

	t.Run("folder and copy", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("POST /buckets/bkt_1/objects/folder", 200, `{"prefix":"images/2026/"}`).
			on("POST /buckets/bkt_1/objects/copy", 200, `{"key":"new/a.txt","etag":"e2","public":false,"public_source":"private"}`)

		rf := run(t, srv, "", "buckets", "objects", "folder", "bkt_1", "images/2026", "-q")
		if rf.err != nil || rf.stdout != "images/2026/\n" {
			t.Fatalf("folder stdout=%q err=%v", rf.stdout, rf.err)
		}
		rc := run(t, srv, "", "buckets", "objects", "copy", "bkt_1", "old/a.txt", "new/a.txt", "--move")
		if rc.err != nil || !strings.Contains(rc.stderr, "Moved") {
			t.Fatalf("copy stderr=%q err=%v", rc.stderr, rc.err)
		}
		wantCopy := map[string]any{"source_key": "old/a.txt", "destination_key": "new/a.txt", "delete_source": true}
		if got := f.sentJSON("POST", "/buckets/bkt_1/objects/copy"); !reflect.DeepEqual(got, wantCopy) {
			t.Fatalf("copy body=%v want=%v", got, wantCopy)
		}
		if rdiff := run(t, srv, "", "buckets", "objects", "copy", "bkt_1", "a", "b", "--dest-bucket", "bkt_2"); rdiff.err == nil || !strings.Contains(rdiff.err.Error(), "different buckets") {
			t.Fatalf("cross-bucket copy err=%v", rdiff.err)
		}
	})

	t.Run("delete requires confirm and handles batch + prefix", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("POST /buckets/bkt_1/objects/delete", 200, `{"deleted":2,"failed":[{"key":"locked.txt","version_id":"","code":"AccessDenied","locked":true}]}`)

		if r := run(t, srv, "", "buckets", "objects", "delete", "bkt_1", "a.txt", "b.txt"); r.err == nil || len(f.sent("POST", "/buckets/bkt_1/objects/delete")) != 0 {
			t.Fatalf("deleted without confirm: err=%v", r.err)
		}
		r := run(t, srv, "", "buckets", "objects", "delete", "bkt_1", "a.txt", "b.txt", "--yes")
		if r.err != nil || !strings.Contains(r.stderr, "2 objects") || !strings.Contains(r.stderr, "locked.txt") {
			t.Fatalf("stderr=%q err=%v", r.stderr, r.err)
		}
		calls := f.sent("POST", "/buckets/bkt_1/objects/delete")
		if len(calls) != 1 || calls[0].IdempotencyKey == "" {
			t.Fatalf("calls=%+v", calls)
		}
	})

	t.Run("visibility prints public URL when made public", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("PUT /buckets/bkt_1/objects/visibility", 200, `{"key":"logo.png","public":true,"object_public":true,"public_source":"object","public_url":"https://api.pantechdynamics.com/storage/public/bkt_1/logo.png"}`).
			on("PUT /buckets/bkt_1/objects/visibility", 200, `{"key":"logo.png","public":false,"object_public":false,"public_source":"private","public_url":""}`)

		if r := run(t, srv, "", "buckets", "objects", "visibility", "bkt_1", "logo.png"); !IsUsage(r.err) {
			t.Fatalf("missing flag err=%v", r.err)
		}
		rp := run(t, srv, "", "buckets", "objects", "visibility", "bkt_1", "logo.png", "--public")
		if rp.err != nil || strings.TrimSpace(rp.stdout) != "https://api.pantechdynamics.com/storage/public/bkt_1/logo.png" {
			t.Fatalf("public stdout=%q err=%v", rp.stdout, rp.err)
		}
		rpriv := run(t, srv, "", "buckets", "objects", "visibility", "bkt_1", "logo.png", "--private")
		if rpriv.err != nil || !strings.Contains(rpriv.stderr, "private") {
			t.Fatalf("private stderr=%q err=%v", rpriv.stderr, rpriv.err)
		}
	})

	t.Run("object tags and lock", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("PUT /buckets/bkt_1/objects/tags", 200, `{"tags":[{"key":"env","value":"prod"}]}`).
			on("PUT /buckets/bkt_1/objects/lock", 200, `{"ok":true}`)

		rt := run(t, srv, "", "buckets", "objects", "tags", "bkt_1", "doc.pdf", "--tag", "env=prod", "-q")
		if rt.err != nil || rt.stdout != "env=prod\n" {
			t.Fatalf("tags stdout=%q err=%v", rt.stdout, rt.err)
		}

		if r := run(t, srv, "", "buckets", "objects", "lock", "bkt_1", "doc.pdf"); !IsUsage(r.err) {
			t.Fatalf("empty lock err=%v", r.err)
		}
		if r := run(t, srv, "", "buckets", "objects", "lock", "bkt_1", "doc.pdf", "--mode", "governance", "--retain-until", "not-a-date"); !IsUsage(r.err) {
			t.Fatalf("bad date err=%v", r.err)
		}
		rl := run(t, srv, "", "buckets", "objects", "lock", "bkt_1", "doc.pdf", "--mode", "governance", "--retain-until", "2027-01-01T00:00:00Z", "--legal-hold", "on")
		if rl.err != nil || !strings.Contains(rl.stderr, "Updated object lock") {
			t.Fatalf("lock stderr=%q err=%v", rl.stderr, rl.err)
		}
		wantLock := map[string]any{
			"key":            "doc.pdf",
			"retention_mode": "GOVERNANCE",
			"retain_until":   "2027-01-01T00:00:00Z",
			"legal_hold":     true,
		}
		if got := f.sentJSON("PUT", "/buckets/bkt_1/objects/lock"); !reflect.DeepEqual(got, wantLock) {
			t.Fatalf("lock body=%v want=%v", got, wantLock)
		}
	})
}

func TestBucketObjectsUploadFormAndRestoreVersion(t *testing.T) {
	t.Run("upload-form mints signed POST policy and validates flags", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		formResp := `{"url":"https://s3.af-abj.pantechdynamics.com/uploads","fields":{"key":"avatars/u1.png","policy":"eyJ...","x-amz-signature":"sig"},"expires_at":"2026-11-28T13:00:00Z","bucket_name":"uploads"}`
		f.on("POST /buckets/bkt_1/objects/upload-form", 200, formResp).
			on("POST /buckets/bkt_1/objects/upload-form", 200, formResp)

		for _, tc := range []struct {
			name string
			args []string
		}{
			{"missing key and key-prefix", []string{"buckets", "objects", "upload-form", "bkt_1"}},
			{"both key and key-prefix", []string{"buckets", "objects", "upload-form", "bkt_1", "--key", "a", "--key-prefix", "b/"}},
			{"min-bytes greater than max-bytes", []string{"buckets", "objects", "upload-form", "bkt_1", "--key", "a", "--min-bytes", "100", "--max-bytes", "50"}},
			{"expires-in too low", []string{"buckets", "objects", "upload-form", "bkt_1", "--key", "a", "--expires-in", "10"}},
		} {
			if r := run(t, srv, "", tc.args...); !IsUsage(r.err) {
				t.Fatalf("%s: err=%v, want usage error", tc.name, r.err)
			}
		}

		r := run(t, srv, "", "buckets", "objects", "upload-form", "bkt_1",
			"--key", "avatars/u1.png",
			"--min-bytes", "1",
			"--max-bytes", "5242880",
			"--content-type", "image/png",
			"--expires-in", "900",
		)
		if r.err != nil || !strings.Contains(r.stdout, "https://s3.af-abj.pantechdynamics.com/uploads") || !strings.Contains(r.stdout, "x-amz-signature") {
			t.Fatalf("stdout=%q stderr=%q err=%v", r.stdout, r.stderr, r.err)
		}
		want := map[string]any{
			"key":                "avatars/u1.png",
			"min_bytes":          float64(1),
			"max_bytes":          float64(5242880),
			"content_type":       "image/png",
			"expires_in_seconds": float64(900),
		}
		if got := f.sentJSON("POST", "/buckets/bkt_1/objects/upload-form"); !reflect.DeepEqual(got, want) {
			t.Fatalf("body=%v want=%v", got, want)
		}

		rq := run(t, srv, "", "buckets", "objects", "upload-form", "bkt_1", "--key-prefix", "incoming/", "-q")
		if rq.err != nil || strings.TrimSpace(rq.stdout) != "https://s3.af-abj.pantechdynamics.com/uploads" {
			t.Fatalf("quiet stdout=%q err=%v", rq.stdout, rq.err)
		}
		calls := f.sent("POST", "/buckets/bkt_1/objects/upload-form")
		if len(calls) != 2 || calls[0].IdempotencyKey == "" || calls[1].IdempotencyKey == "" {
			t.Fatalf("calls=%+v", calls)
		}
	})

	t.Run("restore-version undeletes delete marker and restores specific version", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("POST /buckets/bkt_1/objects/restore-version", 200, `{"key":"report.pdf","action":"removed_delete_marker","restored_version_id":"v1","version_id":"","etag":"e1","last_modified":"2026-11-28T12:00:00Z"}`).
			on("POST /buckets/bkt_1/objects/restore-version", 200, `{"key":"report.pdf","action":"copied_version","restored_version_id":"v1","version_id":"v3","etag":"e1","last_modified":"2026-11-28T12:01:00Z"}`)

		r1 := run(t, srv, "", "buckets", "objects", "restore-version", "bkt_1", "report.pdf")
		if r1.err != nil || !strings.Contains(r1.stderr, "Removed delete marker") || !strings.Contains(r1.stderr, "v1") {
			t.Fatalf("undelete stderr=%q err=%v", r1.stderr, r1.err)
		}
		if got := f.sentJSON("POST", "/buckets/bkt_1/objects/restore-version"); !reflect.DeepEqual(got, map[string]any{"key": "report.pdf"}) {
			t.Fatalf("undelete body=%v", got)
		}

		r2 := run(t, srv, "", "buckets", "objects", "restore-version", "bkt_1", "report.pdf", "--version-id", "v1", "-q")
		if r2.err != nil || strings.TrimSpace(r2.stdout) != "v3" {
			t.Fatalf("restore stdout=%q err=%v", r2.stdout, r2.err)
		}
		calls := f.sent("POST", "/buckets/bkt_1/objects/restore-version")
		if len(calls) != 2 || calls[0].IdempotencyKey == "" || calls[1].IdempotencyKey == "" {
			t.Fatalf("calls=%+v", calls)
		}
		var second map[string]any
		_ = json.Unmarshal(calls[1].Body, &second)
		if !reflect.DeepEqual(second, map[string]any{"key": "report.pdf", "version_id": "v1"}) {
			t.Fatalf("second body=%v", second)
		}
	})
}
