package cli

import (
	"reflect"
	"strings"
	"testing"
)

func TestBucketsSharedAndShares(t *testing.T) {
	t.Run("shared lists buckets shared with your org", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("GET /buckets/shared", 200, `{"data":[{"share_id":"bsh_1","bucket_id":"bkt_9","bucket_name":"partner-exports","region":"af-abj","endpoint":"https://s3.af-abj.pantechdynamics.com","owner_organization_id":"org_partner","permissions":"read_only","prefixes":["daily/"],"expires_at":null,"created_at":"2026-11-28T10:00:00Z","s3_bucket_ref":"partner-exports"}]}`).
			on("GET /buckets/shared", 200, `{"data":[]}`)

		r := run(t, srv, "", "buckets", "shared")
		if r.err != nil || !strings.Contains(r.stdout, "partner-exports") || !strings.Contains(r.stdout, "org_partner") || !strings.Contains(r.stdout, "read only") || !strings.Contains(r.stdout, "daily/") {
			t.Fatalf("stdout=%q err=%v", r.stdout, r.err)
		}
		re := run(t, srv, "", "buckets", "shared")
		if re.err != nil || !strings.Contains(re.stderr, "No buckets shared") {
			t.Fatalf("empty stderr=%q err=%v", re.stderr, re.err)
		}
	})

	t.Run("shares list, create and revoke", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		shareBody := `{"id":"bsh_1","bucket_id":"bkt_1","owner_organization_id":"org_me","grantee_organization_id":"org_2","permissions":"write_only","prefixes":["dropbox/"],"expires_at":"2027-01-01T00:00:00Z","revoked_at":null,"created_at":"2026-11-28T10:00:00Z","updated_at":"2026-11-28T10:00:00Z"}`
		bucketsByName(f)
		f.on("GET /buckets/bkt_1/shares", 200, `{"data":[`+shareBody+`]}`).
			on("POST /buckets/bkt_1/shares", 201, shareBody).
			on("DELETE /buckets/bkt_1/shares/bsh_1", 204, ``)

		rl := run(t, srv, "", "buckets", "shares", "list", "uploads")
		if rl.err != nil || !strings.Contains(rl.stdout, "bsh_1") || !strings.Contains(rl.stdout, "org_2") || !strings.Contains(rl.stdout, "write only") {
			t.Fatalf("list stdout=%q err=%v", rl.stdout, rl.err)
		}

		if r := run(t, srv, "", "buckets", "shares", "create", "bkt_1", "--grantee-org", "org_2", "--permissions", "bad"); !IsUsage(r.err) {
			t.Fatalf("bad permissions err=%v", r.err)
		}
		if r := run(t, srv, "", "buckets", "shares", "create", "bkt_1", "--grantee-org", "org_2", "--permissions", "read_only", "--expires-at", "bad-date"); !IsUsage(r.err) {
			t.Fatalf("bad expires-at err=%v", r.err)
		}

		rc := run(t, srv, "", "buckets", "shares", "create", "bkt_1",
			"--grantee-org", "org_2",
			"--permissions", "write_only",
			"--prefix", "dropbox/",
			"--expires-at", "2027-01-01T00:00:00Z",
		)
		if rc.err != nil || strings.TrimSpace(rc.stdout) != "bsh_1" {
			t.Fatalf("create stdout=%q stderr=%q err=%v", rc.stdout, rc.stderr, rc.err)
		}
		want := map[string]any{
			"grantee_organization_id": "org_2",
			"permissions":             "write_only",
			"prefixes":                []any{"dropbox/"},
			"expires_at":              "2027-01-01T00:00:00Z",
		}
		if got := f.sentJSON("POST", "/buckets/bkt_1/shares"); !reflect.DeepEqual(got, want) {
			t.Fatalf("create body=%v want=%v", got, want)
		}
		if calls := f.sent("POST", "/buckets/bkt_1/shares"); len(calls) != 1 || calls[0].IdempotencyKey == "" {
			t.Fatalf("create calls=%+v", calls)
		}

		if r := run(t, srv, "", "buckets", "shares", "revoke", "bkt_1", "bsh_1"); r.err == nil || len(f.sent("DELETE", "/buckets/bkt_1/shares/bsh_1")) != 0 {
			t.Fatalf("revoked without confirm: err=%v", r.err)
		}
		rr := run(t, srv, "", "buckets", "shares", "revoke", "bkt_1", "bsh_1", "--yes")
		if rr.err != nil || !strings.Contains(rr.stderr, "Revoked share bsh_1") {
			t.Fatalf("revoke stderr=%q err=%v", rr.stderr, rr.err)
		}
		if calls := f.sent("DELETE", "/buckets/bkt_1/shares/bsh_1"); len(calls) != 1 || calls[0].IdempotencyKey == "" {
			t.Fatalf("revoke calls=%+v", calls)
		}
	})
}

func TestBucketsNotifications(t *testing.T) {
	notifBody := `{"id":"bnt_1","bucket_id":"bkt_1","name":"thumbs","events":["object.created"],"prefix":"images/","suffix":".png","target_type":"function","target_id":"fn_1","enabled":true,"created_at":"2026-11-28T10:00:00Z","updated_at":"2026-11-28T10:00:00Z"}`

	t.Run("list, create, update and delete", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("GET /buckets/bkt_1/notifications", 200, `{"data":[`+notifBody+`]}`).
			on("POST /buckets/bkt_1/notifications", 201, notifBody).
			on("PATCH /buckets/bkt_1/notifications/bnt_1", 200, strings.Replace(notifBody, `"enabled":true`, `"enabled":false`, 1)).
			on("DELETE /buckets/bkt_1/notifications/bnt_1", 204, ``)

		rl := run(t, srv, "", "buckets", "notifications", "list", "bkt_1")
		if rl.err != nil || !strings.Contains(rl.stdout, "thumbs") || !strings.Contains(rl.stdout, "function:fn_1") || !strings.Contains(rl.stdout, "prefix=images/, suffix=.png") {
			t.Fatalf("list stdout=%q err=%v", rl.stdout, rl.err)
		}

		if r := run(t, srv, "", "buckets", "notifications", "create", "bkt_1", "--name", "n", "--event", "bad.event", "--target-type", "webhook", "--target-id", "whk_1"); !IsUsage(r.err) {
			t.Fatalf("bad event err=%v", r.err)
		}
		if r := run(t, srv, "", "buckets", "notifications", "create", "bkt_1", "--name", "n", "--event", "object.created", "--target-type", "email", "--target-id", "e_1"); !IsUsage(r.err) {
			t.Fatalf("bad target-type err=%v", r.err)
		}

		rc := run(t, srv, "", "buckets", "notifications", "create", "bkt_1",
			"--name", "thumbs",
			"--event", "object.created",
			"--target-type", "function",
			"--target-id", "fn_1",
			"--prefix", "images/",
			"--suffix", ".png",
			"--disabled",
		)
		if rc.err != nil || strings.TrimSpace(rc.stdout) != "bnt_1" {
			t.Fatalf("create stdout=%q err=%v", rc.stdout, rc.err)
		}
		wantCreate := map[string]any{
			"name":        "thumbs",
			"events":      []any{"object.created"},
			"target_type": "function",
			"target_id":   "fn_1",
			"prefix":      "images/",
			"suffix":      ".png",
			"enabled":     false,
		}
		if got := f.sentJSON("POST", "/buckets/bkt_1/notifications"); !reflect.DeepEqual(got, wantCreate) {
			t.Fatalf("create body=%v want=%v", got, wantCreate)
		}
		if calls := f.sent("POST", "/buckets/bkt_1/notifications"); len(calls) != 1 || calls[0].IdempotencyKey == "" {
			t.Fatalf("create calls=%+v", calls)
		}

		if r := run(t, srv, "", "buckets", "notifications", "update", "bkt_1", "bnt_1"); !IsUsage(r.err) {
			t.Fatalf("empty update err=%v", r.err)
		}
		if r := run(t, srv, "", "buckets", "notifications", "update", "bkt_1", "bnt_1", "--enable", "--disable"); !IsUsage(r.err) {
			t.Fatalf("conflicting enable/disable err=%v", r.err)
		}

		ru := run(t, srv, "", "buckets", "notifications", "update", "bkt_1", "bnt_1",
			"--event", "object.created",
			"--event", "object.removed",
			"--prefix", "",
			"--disable",
		)
		if ru.err != nil || strings.TrimSpace(ru.stdout) != "bnt_1" {
			t.Fatalf("update stdout=%q err=%v", ru.stdout, ru.err)
		}
		wantUpdate := map[string]any{
			"events":  []any{"object.created", "object.removed"},
			"prefix":  "",
			"enabled": false,
		}
		if got := f.sentJSON("PATCH", "/buckets/bkt_1/notifications/bnt_1"); !reflect.DeepEqual(got, wantUpdate) {
			t.Fatalf("update body=%v want=%v", got, wantUpdate)
		}
		if calls := f.sent("PATCH", "/buckets/bkt_1/notifications/bnt_1"); len(calls) != 1 || calls[0].IdempotencyKey == "" {
			t.Fatalf("update calls=%+v", calls)
		}

		if r := run(t, srv, "", "buckets", "notifications", "delete", "bkt_1", "bnt_1"); r.err == nil || len(f.sent("DELETE", "/buckets/bkt_1/notifications/bnt_1")) != 0 {
			t.Fatalf("deleted without confirm: err=%v", r.err)
		}
		rd := run(t, srv, "", "buckets", "notifications", "delete", "bkt_1", "bnt_1", "--yes")
		if rd.err != nil || !strings.Contains(rd.stderr, "Deleted notification rule bnt_1") {
			t.Fatalf("delete stderr=%q err=%v", rd.stderr, rd.err)
		}
		if calls := f.sent("DELETE", "/buckets/bkt_1/notifications/bnt_1"); len(calls) != 1 || calls[0].IdempotencyKey == "" {
			t.Fatalf("delete calls=%+v", calls)
		}
	})
}

func TestBucketsDomains(t *testing.T) {
	domainBody := `{"id":"bdom_1","bucket_id":"bkt_1","hostname":"assets.example.com","certificate_id":"cert_1","index_document":"index.html","error_document":"404.html","spa_mode":true,"cache_ttl_seconds":3600,"status":"active","created_at":"2026-11-28T10:00:00Z","updated_at":"2026-11-28T10:00:00Z"}`

	t.Run("list, create, update and delete", func(t *testing.T) {
		f, srv := newFakeAPI(t)
		f.on("GET /buckets/bkt_1/domains", 200, `{"data":[`+domainBody+`]}`).
			on("POST /buckets/bkt_1/domains", 201, domainBody).
			on("PATCH /buckets/bkt_1/domains/bdom_1", 200, domainBody).
			on("DELETE /buckets/bkt_1/domains/bdom_1", 204, ``)

		rl := run(t, srv, "", "buckets", "domains", "list", "bkt_1")
		if rl.err != nil || !strings.Contains(rl.stdout, "assets.example.com") || !strings.Contains(rl.stdout, "cert_1") || !strings.Contains(rl.stdout, "3600s") {
			t.Fatalf("list stdout=%q err=%v", rl.stdout, rl.err)
		}

		if r := run(t, srv, "", "buckets", "domains", "create", "bkt_1", "--hostname", "a.example.com", "--certificate", "cert_1", "--cache-ttl", "-1"); !IsUsage(r.err) {
			t.Fatalf("negative cache-ttl err=%v", r.err)
		}

		rc := run(t, srv, "", "buckets", "domains", "create", "bkt_1",
			"--hostname", "assets.example.com",
			"--certificate", "cert_1",
			"--index-document", "index.html",
			"--error-document", "404.html",
			"--spa-mode",
			"--cache-ttl", "3600",
		)
		if rc.err != nil || strings.TrimSpace(rc.stdout) != "bdom_1" {
			t.Fatalf("create stdout=%q err=%v", rc.stdout, rc.err)
		}
		wantCreate := map[string]any{
			"hostname":          "assets.example.com",
			"certificate_id":    "cert_1",
			"index_document":    "index.html",
			"error_document":    "404.html",
			"spa_mode":          true,
			"cache_ttl_seconds": float64(3600),
		}
		if got := f.sentJSON("POST", "/buckets/bkt_1/domains"); !reflect.DeepEqual(got, wantCreate) {
			t.Fatalf("create body=%v want=%v", got, wantCreate)
		}
		if calls := f.sent("POST", "/buckets/bkt_1/domains"); len(calls) != 1 || calls[0].IdempotencyKey == "" {
			t.Fatalf("create calls=%+v", calls)
		}

		if r := run(t, srv, "", "buckets", "domains", "update", "bkt_1", "bdom_1"); !IsUsage(r.err) {
			t.Fatalf("empty update err=%v", r.err)
		}
		if r := run(t, srv, "", "buckets", "domains", "update", "bkt_1", "bdom_1", "--spa-mode", "--no-spa-mode"); !IsUsage(r.err) {
			t.Fatalf("conflicting spa-mode err=%v", r.err)
		}

		ru := run(t, srv, "", "buckets", "domains", "update", "bkt_1", "bdom_1",
			"--certificate", "cert_2",
			"--no-spa-mode",
			"--cache-ttl", "600",
		)
		if ru.err != nil || strings.TrimSpace(ru.stdout) != "bdom_1" {
			t.Fatalf("update stdout=%q err=%v", ru.stdout, ru.err)
		}
		wantUpdate := map[string]any{
			"certificate_id":    "cert_2",
			"spa_mode":          false,
			"cache_ttl_seconds": float64(600),
		}
		if got := f.sentJSON("PATCH", "/buckets/bkt_1/domains/bdom_1"); !reflect.DeepEqual(got, wantUpdate) {
			t.Fatalf("update body=%v want=%v", got, wantUpdate)
		}
		if calls := f.sent("PATCH", "/buckets/bkt_1/domains/bdom_1"); len(calls) != 1 || calls[0].IdempotencyKey == "" {
			t.Fatalf("update calls=%+v", calls)
		}

		if r := run(t, srv, "", "buckets", "domains", "delete", "bkt_1", "bdom_1"); r.err == nil || len(f.sent("DELETE", "/buckets/bkt_1/domains/bdom_1")) != 0 {
			t.Fatalf("deleted without confirm: err=%v", r.err)
		}
		rd := run(t, srv, "", "buckets", "domains", "delete", "bkt_1", "bdom_1", "--yes")
		if rd.err != nil || !strings.Contains(rd.stderr, "Detached custom domain bdom_1") {
			t.Fatalf("delete stderr=%q err=%v", rd.stderr, rd.err)
		}
		if calls := f.sent("DELETE", "/buckets/bkt_1/domains/bdom_1"); len(calls) != 1 || calls[0].IdempotencyKey == "" {
			t.Fatalf("delete calls=%+v", calls)
		}
	})
}
