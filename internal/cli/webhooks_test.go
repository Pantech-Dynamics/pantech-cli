package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const (
	webhookBody       = `{"id":"whk_1","url":"https://example.com/hooks","description":"Prod","events":["instance.*","invoice.paid"],"enabled":true,"disabled_reason":null,"failing_since":null,"created_at":"2026-10-20T10:00:00Z","updated_at":"2026-10-20T10:00:00Z"}`
	webhookSecretBody = `{"id":"whk_1","url":"https://example.com/hooks","description":"","events":["instance.*"],"enabled":true,"disabled_reason":null,"failing_since":null,"created_at":"2026-10-20T10:00:00Z","updated_at":"2026-10-20T10:00:00Z","secret":"whsec_s3cr3t"}`
	deliveryBody      = `{"id":"whd_1","webhook_id":"whk_1","event_id":"evt_1","event_type":"webhook.test","status":"pending","attempts":0,"next_attempt_at":null,"last_attempt_at":null,"last_status_code":null,"last_error":null,"last_response":null,"last_duration_ms":null,"delivered_at":null,"created_at":"2026-10-20T10:00:00Z"`
)

func TestWebhooksList(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /webhooks", 200, `{"data":[`+webhookBody+`,{"id":"whk_2","url":"https://example.com/b","description":"","events":["*"],"enabled":false,"disabled_reason":"failing","failing_since":"2026-10-17T10:00:00Z","created_at":"2026-10-01T10:00:00Z","updated_at":"2026-10-20T10:00:00Z"}],"next_cursor":null}`)

	r := run(t, srv, "", "webhooks", "list")
	if r.err != nil {
		t.Fatal(r.err)
	}
	for _, want := range []string{"whk_1", "https://example.com/hooks", "instance.*, invoice.paid", "enabled", "disabled (failing)"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("stdout %q lacks %q", r.stdout, want)
		}
	}

	r = run(t, srv, "", "webhooks", "list", "-q")
	if r.err != nil || r.stdout != "whk_1\nwhk_2\n" {
		t.Fatalf("stdout %q, err %v", r.stdout, r.err)
	}

	r = run(t, srv, "", "webhooks", "list", "--json")
	var items []map[string]any
	if r.err != nil || json.Unmarshal([]byte(r.stdout), &items) != nil || len(items) != 2 {
		t.Fatalf("stdout %q, err %v", r.stdout, r.err)
	}
}

func TestWebhooksCreatePrintsTheSecretOnce(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /webhooks", 201, webhookSecretBody)
	r := run(t, srv, "", "webhooks", "create", "--url", "https://example.com/hooks", "--event", "instance.*", "--event", "invoice.paid,volume.*", "--description", "Prod")
	if r.err != nil {
		t.Fatal(r.err)
	}
	want := map[string]any{"url": "https://example.com/hooks", "events": []any{"instance.*", "invoice.paid", "volume.*"}, "description": "Prod"}
	if got := f.sentJSON("POST", "/webhooks"); !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %v\nwant   %v", got, want)
	}
	// The secret alone on stdout, so it can be redirected; the warning on stderr.
	if r.stdout != "whsec_s3cr3t\n" {
		t.Fatalf("stdout %q", r.stdout)
	}
	if strings.Contains(r.stderr, "whsec_") || !strings.Contains(r.stderr, "shown once and never again") || !strings.Contains(r.stderr, "Created webhook whk_1") {
		t.Fatalf("stderr %q", r.stderr)
	}
}

func TestWebhooksCreateQuietAndJSON(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /webhooks", 201, webhookSecretBody)
	r := run(t, srv, "", "webhooks", "create", "--url", "https://example.com/hooks", "--event", "instance.*", "-q")
	if r.err != nil || r.stdout != "whsec_s3cr3t\nwhk_1\n" {
		t.Fatalf("stdout %q, err %v", r.stdout, r.err)
	}
	r = run(t, srv, "", "webhooks", "create", "--url", "https://example.com/hooks", "--event", "instance.*", "--json")
	var got map[string]any
	if r.err != nil || json.Unmarshal([]byte(r.stdout), &got) != nil || got["secret"] != "whsec_s3cr3t" {
		t.Fatalf("stdout %q, err %v", r.stdout, r.err)
	}
	if strings.Count(r.stdout, "whsec_s3cr3t") != 1 || r.stderr != "" {
		t.Fatalf("stdout %q stderr %q", r.stdout, r.stderr)
	}
}

func TestWebhooksCreateReplayedHasNoSecret(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /webhooks", 201, strings.Replace(webhookSecretBody, `"whsec_s3cr3t"`, "null", 1))
	r := run(t, srv, "", "webhooks", "create", "--url", "https://example.com/hooks", "--event", "instance.*")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.stdout != "" || !strings.Contains(r.stderr, "pantech webhooks rotate-secret whk_1") {
		t.Fatalf("stdout %q stderr %q", r.stdout, r.stderr)
	}
}

func TestWebhooksCreateNeedsAnEvent(t *testing.T) {
	f, srv := newFakeAPI(t)
	r := run(t, srv, "", "webhooks", "create", "--url", "https://example.com/hooks")
	if r.err == nil || !IsUsage(r.err) {
		t.Fatalf("err = %v, want a usage error", r.err)
	}
	if len(f.sent("POST", "/webhooks")) != 0 {
		t.Fatal("created without events")
	}
}

func TestWebhooksUpdateSendsOnlyWhatChanged(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want map[string]any
	}{
		{"url", []string{"--url", "https://example.com/new"}, map[string]any{"url": "https://example.com/new"}},
		{"clear the description", []string{"--description", ""}, map[string]any{"description": ""}},
		{"events replace the list", []string{"--event", "instance.*", "--event", "volume.*"}, map[string]any{"events": []any{"instance.*", "volume.*"}}},
		{"enable", []string{"--enable"}, map[string]any{"enabled": true}},
		{"disable with a new url", []string{"--disable", "--url", "https://example.com/x"}, map[string]any{"enabled": false, "url": "https://example.com/x"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newFakeAPI(t)
			f.on("PATCH /webhooks/whk_1", 200, webhookBody)
			r := run(t, srv, "", append([]string{"webhooks", "update", "whk_1"}, c.args...)...)
			if r.err != nil {
				t.Fatal(r.err)
			}
			if got := f.sentJSON("PATCH", "/webhooks/whk_1"); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("body = %v\nwant   %v", got, c.want)
			}
		})
	}
}

func TestWebhooksUpdateRefusesBadFlags(t *testing.T) {
	for _, args := range [][]string{{}, {"--enable", "--disable"}} {
		f, srv := newFakeAPI(t)
		r := run(t, srv, "", append([]string{"webhooks", "update", "whk_1"}, args...)...)
		if r.err == nil || !IsUsage(r.err) {
			t.Fatalf("%v: err = %v, want a usage error", args, r.err)
		}
		if len(f.sent("PATCH", "/webhooks/whk_1")) != 0 {
			t.Fatalf("%v: sent anyway", args)
		}
	}
}

func TestWebhooksDeleteAsks(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /webhooks/whk_1", 200, webhookBody).on("DELETE /webhooks/whk_1", 204, "")
	r := run(t, srv, "", "webhooks", "delete", "whk_1")
	if r.err == nil || !strings.Contains(r.err.Error(), "--yes") {
		t.Fatalf("err = %v, want a refusal without --yes", r.err)
	}
	if len(f.sent("DELETE", "/webhooks/whk_1")) != 0 {
		t.Fatal("deleted without asking")
	}

	r = run(t, srv, "", "webhooks", "delete", "whk_1", "-y")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if len(f.sent("DELETE", "/webhooks/whk_1")) != 1 || !strings.Contains(r.stderr, "Deleted webhook whk_1") {
		t.Fatalf("stderr %q", r.stderr)
	}
}

func TestWebhooksDeleteUnknown(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /webhooks/whk_9", 404, `{"status":404,"code":"WEBHOOK_NOT_FOUND","detail":"No webhook with this id in your organization.","request_id":"req_1"}`)
	r := run(t, srv, "", "webhooks", "delete", "whk_9", "-y")
	if r.err == nil || !strings.Contains(r.err.Error(), "WEBHOOK_NOT_FOUND") || !strings.Contains(r.err.Error(), "See: pantech webhooks list") {
		t.Fatalf("err = %v", r.err)
	}
	if len(f.sent("DELETE", "/webhooks/whk_9")) != 0 {
		t.Fatal("deleted an unknown webhook")
	}
}

func TestWebhooksRotateSecret(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /webhooks/whk_1/rotate-secret", 200, webhookSecretBody)
	if r := run(t, srv, "", "webhooks", "rotate-secret", "whk_1"); r.err == nil {
		t.Fatal("rotated without asking")
	}
	if len(f.sent("POST", "/webhooks/whk_1/rotate-secret")) != 0 {
		t.Fatal("sent before confirming")
	}
	r := run(t, srv, "", "webhooks", "rotate-secret", "whk_1", "--yes")
	if r.err != nil || r.stdout != "whsec_s3cr3t\n" || strings.Contains(r.stderr, "whsec_") {
		t.Fatalf("stdout %q stderr %q err %v", r.stdout, r.stderr, r.err)
	}
}

func TestWebhooksTestDisabledHints(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /webhooks/whk_1/test", 409, `{"type":"about:blank","title":"Conflict","status":409,"code":"WEBHOOK_DISABLED","detail":"The webhook is disabled.","request_id":"req_2"}`)
	r := run(t, srv, "", "webhooks", "test", "whk_1")
	if r.err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"The webhook is disabled.", "(WEBHOOK_DISABLED, request req_2)", "pantech webhooks update whk_1 --enable"} {
		if !strings.Contains(r.err.Error(), want) {
			t.Fatalf("err %q lacks %q", r.err, want)
		}
	}
}

func TestWebhooksTest(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /webhooks/whk_1/test", 202, deliveryBody+`,"payload":{"id":"evt_1","type":"webhook.test"}}`)
	r := run(t, srv, "", "webhooks", "test", "whk_1")
	if r.err != nil || r.stdout != "whd_1\n" {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
}

func TestWebhooksCreateInvalidURLShowsTheField(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /webhooks", 422, `{"title":"Invalid request","status":422,"code":"VALIDATION_FAILED","detail":"url: Send an https URL on the public internet.","errors":[{"field":"url","code":"INVALID_WEBHOOK_URL","message":"Send an https URL on the public internet."}],"request_id":"req_3"}`)
	r := run(t, srv, "", "webhooks", "create", "--url", "http://localhost", "--event", "*")
	if r.err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"Invalid request", "url: Send an https URL on the public internet.", "VALIDATION_FAILED"} {
		if !strings.Contains(r.err.Error(), want) {
			t.Fatalf("err %q lacks %q", r.err, want)
		}
	}
}

func TestWebhookDeliveriesListOnePage(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /webhooks/whk_1/deliveries", 200, `{"data":[`+deliveryBody+`},{"id":"whd_2","webhook_id":"whk_1","event_id":"evt_2","event_type":"instance.created","status":"failed","attempts":8,"last_status_code":500,"created_at":"2026-10-19T10:00:00Z"}],"next_cursor":"cur_2"}`)
	r := run(t, srv, "", "webhooks", "deliveries", "list", "whk_1", "--limit", "2")
	if r.err != nil {
		t.Fatal(r.err)
	}
	calls := f.sent("GET", "/webhooks/whk_1/deliveries")
	if len(calls) != 1 || calls[0].Query != "limit=2" {
		t.Fatalf("calls %+v: one page only, with the limit", calls)
	}
	for _, want := range []string{"whd_1", "webhook.test", "whd_2", "HTTP 500"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("stdout %q lacks %q", r.stdout, want)
		}
	}
	if !strings.Contains(r.stderr, "--cursor cur_2 --limit 2") {
		t.Fatalf("stderr %q: the next page's command", r.stderr)
	}

	r = run(t, srv, "", "webhooks", "deliveries", "list", "whk_1", "--cursor", "cur_2", "--json")
	var page struct {
		Data       []json.RawMessage `json:"data"`
		NextCursor *string           `json:"next_cursor"`
	}
	if r.err != nil || json.Unmarshal([]byte(r.stdout), &page) != nil || len(page.Data) != 2 {
		t.Fatalf("stdout %q err %v: --json is the API's page", r.stdout, r.err)
	}
	if calls := f.sent("GET", "/webhooks/whk_1/deliveries"); calls[len(calls)-1].Query != "cursor=cur_2" {
		t.Fatalf("query %q", calls[len(calls)-1].Query)
	}
}

func TestWebhookDeliveriesListAll(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /webhooks/whk_1/deliveries", 200, `{"data":[`+deliveryBody+`}],"next_cursor":"cur_2"}`).
		on("GET /webhooks/whk_1/deliveries", 200, `{"data":[{"id":"whd_2","status":"succeeded"}],"next_cursor":null}`)
	r := run(t, srv, "", "webhooks", "deliveries", "list", "whk_1", "--all", "-q", "--limit", "1")
	if r.err != nil || r.stdout != "whd_1\nwhd_2\n" {
		t.Fatalf("stdout %q, err %v: every page must be read", r.stdout, r.err)
	}
	calls := f.sent("GET", "/webhooks/whk_1/deliveries")
	if len(calls) != 2 || calls[1].Query != "cursor=cur_2&limit=1" {
		t.Fatalf("calls %+v", calls)
	}
}

func TestWebhookDeliveriesListRefusesBadFlags(t *testing.T) {
	for _, args := range [][]string{{"--limit", "0"}, {"--limit", "101"}, {"--all", "--cursor", "x"}} {
		f, srv := newFakeAPI(t)
		r := run(t, srv, "", append([]string{"webhooks", "deliveries", "list", "whk_1"}, args...)...)
		if r.err == nil || !IsUsage(r.err) {
			t.Fatalf("%v: err = %v, want a usage error", args, r.err)
		}
		if len(f.sent("GET", "/webhooks/whk_1/deliveries")) != 0 {
			t.Fatalf("%v: asked the API anyway", args)
		}
	}
}

func TestWebhookDeliveryGetShowsThePayload(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /webhooks/whk_1/deliveries/whd_1", 200, strings.Replace(deliveryBody, `"status":"pending"`, `"status":"failed"`, 1)+`,"payload":{"id":"evt_1","type":"webhook.test","data":{"hello":"world"}}}`)
	r := run(t, srv, "", "webhooks", "deliveries", "get", "whk_1", "whd_1")
	if r.err != nil {
		t.Fatal(r.err)
	}
	for _, want := range []string{"webhook.test", "whd_1", "Payload", `"hello": "world"`} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("stdout %q lacks %q", r.stdout, want)
		}
	}
	if !strings.Contains(r.stderr, "pantech webhooks deliveries redeliver whk_1 whd_1") {
		t.Fatalf("stderr %q", r.stderr)
	}
}

func TestWebhookRedeliver(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("POST /webhooks/whk_1/deliveries/whd_1/redeliver", 202, deliveryBody+`}`)
	r := run(t, srv, "", "webhooks", "deliveries", "redeliver", "whk_1", "whd_1")
	if r.err != nil || !strings.Contains(r.stderr, "Queued delivery whd_1") {
		t.Fatalf("stderr %q err %v", r.stderr, r.err)
	}

	f, srv = newFakeAPI(t)
	f.on("POST /webhooks/whk_1/deliveries/whd_1/redeliver", 409, `{"status":409,"code":"WEBHOOK_DELIVERY_IN_PROGRESS","detail":"The delivery is still being attempted."}`)
	r = run(t, srv, "", "webhooks", "deliveries", "redeliver", "whk_1", "whd_1")
	if r.err != nil || !strings.Contains(r.stderr, "already being sent") {
		t.Fatalf("stderr %q err %v: in progress is done", r.stderr, r.err)
	}
}

func TestWebhookEventTypes(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("GET /webhook-event-types", 200, `{"data":[{"type":"instance.created","resource":"instance","description":"A VM finished creating."},{"type":"invoice.paid","resource":"invoice","description":"An invoice was paid."}],"next_cursor":null}`)
	r := run(t, srv, "", "webhooks", "event-types")
	if r.err != nil || !strings.Contains(r.stdout, "instance.created") || !strings.Contains(r.stdout, "An invoice was paid.") {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
	r = run(t, srv, "", "webhooks", "event-types", "-q")
	if r.err != nil || r.stdout != "instance.created\ninvoice.paid\n" {
		t.Fatalf("stdout %q err %v", r.stdout, r.err)
	}
}
