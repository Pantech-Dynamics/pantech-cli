package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

func newWebhooksCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "webhooks",
		Aliases: []string{"webhook"},
		Short:   "Have your organization's events sent to a URL",
		Long: `Webhooks send your organization's events (a VM created, a volume attach
failed, an invoice paid…) to an https URL as signed JSON POSTs.

Every delivery carries a Pantech-Signature header: t=<unix seconds>,v1=<hex
HMAC-SHA256 of "t.body" with the webhook's secret>. The secret (whsec_…) is
printed once, by create and rotate-secret, and never again. Deliveries are at
least once and unordered: deduplicate on the event id. Failed deliveries are
retried for about 28 hours; a webhook failing for three days is disabled.`,
	}
	cmd.AddCommand(
		newWebhooksListCmd(a),
		newWebhooksGetCmd(a),
		newWebhooksCreateCmd(a),
		newWebhooksUpdateCmd(a),
		newWebhooksDeleteCmd(a),
		newWebhooksTestCmd(a),
		newWebhooksRotateSecretCmd(a),
		newWebhookDeliveriesCmd(a),
		newWebhookEventTypesCmd(a),
	)
	return cmd
}

func webhookPath(id string) string { return "/webhooks/" + url.PathEscape(id) }

func webhookDeliveryPath(webhookID, deliveryID string) string {
	return webhookPath(webhookID) + "/deliveries/" + url.PathEscape(deliveryID)
}

// webhookError adds what to run next to the API's webhook errors.
func webhookError(err error, webhookID string) error {
	switch {
	case api.IsCode(err, "WEBHOOK_NOT_FOUND"):
		return fmt.Errorf("%w\nSee: pantech webhooks list", err)
	case api.IsCode(err, "WEBHOOK_DELIVERY_NOT_FOUND"):
		return fmt.Errorf("%w\nSee: pantech webhooks deliveries list %s", err, webhookID)
	case api.IsCode(err, "WEBHOOK_DISABLED"):
		return fmt.Errorf("%w\nEnable it: pantech webhooks update %s --enable", err, webhookID)
	}
	return err
}

func webhookStatus(out *output.Printer, w *api.Webhook) string {
	if w.Enabled {
		return out.State("enabled")
	}
	s := out.State("disabled")
	switch output.Or(w.DisabledReason) {
	case "failing":
		s += out.Dim(" (failing)")
	case "disabled_by_user":
		s += out.Dim(" (by you)")
	}
	return s
}

func newWebhooksListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List webhooks",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			hooks, raw, err := listItems[api.Webhook](cmd, c, "/webhooks", nil)
			if err != nil {
				return err
			}
			if printList(a, hooks, raw, func(w api.Webhook) string { return w.ID }, "No webhooks yet.", "pantech webhooks create --url https://example.com/hooks/pantech --event 'instance.*'") {
				return nil
			}
			rows := make([][]string, len(hooks))
			for i := range hooks {
				w := &hooks[i]
				failing := "—"
				if w.FailingSince != nil {
					failing = "since " + output.Ago(w.FailingSince)
				}
				rows[i] = []string{a.out.Dim(w.ID), w.URL, strings.Join(w.Events, ", "), webhookStatus(a.out, w), failing, output.Ago(w.CreatedAt)}
			}
			a.out.Table([]string{"id", "url", "events", "status", "failing", "created"}, rows)
			a.out.Summary(count(len(hooks), "webhook"))
			return nil
		},
	}
}

func webhookDetail(out *output.Printer, w *api.Webhook) output.Detail {
	description := w.Description
	if description == "" {
		description = "—"
	}
	return output.Detail{
		Title:    w.URL,
		State:    webhookStatus(out, w),
		Subtitle: w.ID,
		Sections: [][]output.Pair{
			{{"Description", description}, {"Events", strings.Join(w.Events, ", ")}},
			{{"Failing since", output.When(w.FailingSince)}},
			{{"Created", output.When(w.CreatedAt)}, {"Updated", output.When(w.UpdatedAt)}},
		},
	}
}

func newWebhooksGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <webhook id>",
		Aliases: []string{"show"},
		Short:   "Show a webhook",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var w api.Webhook
			if ok, err := getOne(a, cmd, c, webhookPath(args[0]), &w); !ok {
				return webhookError(err, args[0])
			}
			if a.out.Quiet {
				a.out.Line("%s", w.ID)
				return nil
			}
			d := webhookDetail(a.out, &w)
			d.Next = [][2]string{
				{"Send a test event", "pantech webhooks test " + w.ID},
				{"See its deliveries", "pantech webhooks deliveries list " + w.ID},
			}
			if !w.Enabled {
				d.Next = [][2]string{{"Enable it", "pantech webhooks update " + w.ID + " --enable"}}
			}
			a.out.Print(d)
			return nil
		},
	}
}

// showSecret prints a webhook's signing secret alone on stdout, so it can
// be redirected to a file, and says on stderr that it is never shown again.
func (a *app) showSecret(w *api.WebhookWithSecret, what string) bool {
	if w.Secret == nil || *w.Secret == "" {
		return false
	}
	a.out.Note("%s for %s, shown once and never again. Save it now:", what, w.ID)
	a.out.Line("%s", *w.Secret)
	return true
}

func newWebhooksCreateCmd(a *app) *cobra.Command {
	var webhookURL, description string
	var events []string
	cmd := &cobra.Command{
		Use:   "create --url URL --event TYPE…",
		Short: "Add a URL to send events to; prints its signing secret once",
		Long: `Add an https URL Pantech sends events to. --event subscribes to an event
type (pantech webhooks event-types), every type of one resource
("instance.*"), or everything ("*"); repeat it or comma-separate.

The signing secret (whsec_…) is printed once, alone on stdout, and is never
shown again: save it now (pantech webhooks rotate-secret makes a new one).
With --quiet it is the line before the id; with --json it is the "secret"
field. At most 10 webhooks per organization; owners and admins only.`,
		Example: `  pantech webhooks create --url https://example.com/hooks/pantech --event 'instance.*' --event invoice.paid
  pantech webhooks create --url https://example.com/hooks/all --event '*' --description "Everything" > secret.txt`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(events) == 0 {
				return &usageError{errors.New("pass at least one --event, e.g. --event 'instance.*'"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			body := map[string]any{"url": webhookURL, "events": events}
			if description != "" {
				body["description"] = description
			}
			var w api.WebhookWithSecret
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: "/webhooks", Body: body}, &w)
			if err != nil {
				return err
			}
			if a.out.JSON {
				a.out.RawJSON(res.Body)
				return nil
			}
			if !a.showSecret(&w, "Signing secret") {
				a.out.Warn("The signing secret was returned to an earlier attempt of this request and cannot be shown again. Make a new one: pantech webhooks rotate-secret %s", w.ID)
			}
			if a.out.Quiet {
				a.out.Line("%s", w.ID)
				return nil
			}
			a.out.Success("Created webhook %s for %s.", w.ID, w.URL)
			a.out.Next("Send a test event", "pantech webhooks test "+w.ID)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&webhookURL, "url", "", "the https URL to send events to (required)")
	f.StringSliceVar(&events, "event", nil, "an event type, '<resource>.*' or '*'; repeat or comma-separate (required)")
	f.StringVar(&description, "description", "", "a note for yourself, at most 255 characters")
	_ = cmd.MarkFlagRequired("url")
	return cmd
}

func newWebhooksUpdateCmd(a *app) *cobra.Command {
	var webhookURL, description string
	var events []string
	var enable, disable bool
	cmd := &cobra.Command{
		Use:   "update <webhook id>",
		Short: "Change a webhook's URL, description or events, or enable or disable it",
		Long: `Change a webhook. Only what you pass changes: --event replaces the whole
list of subscriptions. --disable stops deliveries; --enable resumes them and
clears the failure history, also for a webhook disabled after failing.`,
		Example: `  pantech webhooks update whk_123 --event 'instance.*' --event 'volume.*'
  pantech webhooks update whk_123 --enable
  pantech webhooks update whk_123 --url https://example.com/new --description ""`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fl := cmd.Flags()
			body := map[string]any{}
			if fl.Changed("url") {
				body["url"] = webhookURL
			}
			if fl.Changed("description") {
				body["description"] = description
			}
			if fl.Changed("event") {
				if len(events) == 0 {
					return &usageError{errors.New("--event needs a value: a webhook subscribes to at least one event"), cmd}
				}
				body["events"] = events
			}
			switch {
			case enable && disable:
				return &usageError{errors.New("pass --enable or --disable, not both"), cmd}
			case enable:
				body["enabled"] = true
			case disable:
				body["enabled"] = false
			}
			if len(body) == 0 {
				return &usageError{errors.New("nothing to change: pass --url, --description, --event, --enable or --disable"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var w api.Webhook
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPatch, Path: webhookPath(args[0]), Body: body}, &w)
			if err != nil {
				return webhookError(err, args[0])
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%s", w.ID)
			default:
				a.out.Success("Updated webhook %s.", w.ID)
				a.out.Print(webhookDetail(a.out, &w))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&webhookURL, "url", "", "a new https URL")
	f.StringVar(&description, "description", "", `a new note ("" clears it)`)
	f.StringSliceVar(&events, "event", nil, "the whole new list of subscriptions; repeat or comma-separate")
	f.BoolVar(&enable, "enable", false, "resume deliveries and clear the failure history")
	f.BoolVar(&disable, "disable", false, "stop deliveries")
	return cmd
}

func newWebhooksDeleteCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <webhook id>",
		Aliases: []string{"rm"},
		Short:   "Delete a webhook and its delivery history",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			// Read it first: the question names its URL, and a 404 from the
			// delete below then means a retried attempt already deleted it.
			var w api.Webhook
			if _, err := api.Get(ctx(cmd), c, webhookPath(args[0]), &w); err != nil {
				return webhookError(err, args[0])
			}
			if err := a.confirm(fmt.Sprintf("Delete webhook %s (%s) and its delivery history?", w.ID, w.URL)); err != nil {
				return err
			}
			_, err = c.Do(ctx(cmd), api.Request{Method: http.MethodDelete, Path: webhookPath(w.ID)}, nil)
			// Not replayed on retry: a 404 after a retry means the first attempt worked.
			if err != nil && !api.IsCode(err, "WEBHOOK_NOT_FOUND") && !api.IsCode(err, "RESOURCE_NOT_FOUND") {
				return err
			}
			if a.out.Quiet {
				a.out.Line("%s", w.ID)
				return nil
			}
			a.out.Success("Deleted webhook %s.", w.ID)
			return nil
		},
	}
}

func newWebhooksTestCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "test <webhook id>",
		Short: "Send a webhook.test event to a webhook",
		Long: `Queue a webhook.test event to a webhook, whatever it subscribes to. It is
sent within a few seconds and retried like any other delivery. Prints the
delivery's id.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var d api.WebhookDeliveryDetail
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: webhookPath(args[0]) + "/test"}, &d)
			if err != nil {
				return webhookError(err, args[0])
			}
			if a.out.JSON {
				a.out.RawJSON(res.Body)
				return nil
			}
			a.out.Line("%s", d.ID)
			if !a.out.Quiet {
				a.out.Success("Queued a test event (%s) to %s.", d.EventID, args[0])
				a.out.Next("See how it went", "pantech webhooks deliveries get "+args[0]+" "+d.ID)
			}
			return nil
		},
	}
}

func newWebhooksRotateSecretCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "rotate-secret <webhook id>",
		Short: "Replace a webhook's signing secret; prints the new one once",
		Long: `Replace a webhook's signing secret. The new secret is printed once, alone on
stdout, and never shown again. For 24 hours every delivery is signed with both
the new and the old secret, so you can update your receiver without dropping
events. Each run rotates again: only the latest secret and the one before it
sign.`,
		Example: `  pantech webhooks rotate-secret whk_123 > secret.txt`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Rotate the signing secret of webhook %s? The current secret keeps signing for 24 hours.", args[0])); err != nil {
				return err
			}
			var w api.WebhookWithSecret
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: webhookPath(args[0]) + "/rotate-secret"}, &w)
			if err != nil {
				return webhookError(err, args[0])
			}
			if a.out.JSON {
				a.out.RawJSON(res.Body)
				return nil
			}
			if !a.showSecret(&w, "New signing secret") {
				return fmt.Errorf("the API returned no secret: run pantech webhooks rotate-secret %s again", args[0])
			}
			if !a.out.Quiet {
				a.out.Success("Rotated the signing secret of %s. The old one keeps signing for 24 hours.", w.ID)
			}
			return nil
		},
	}
}

func newWebhookEventTypesCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "event-types",
		Aliases: []string{"events"},
		Short:   "List the event types a webhook can subscribe to",
		Long: `List the event types a webhook can subscribe to. Subscribe to an exact type,
to every type of one resource with "<resource>.*", or to everything with "*".`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			types, raw, err := listItems[api.WebhookEventType](cmd, c, "/webhook-event-types", nil)
			if err != nil {
				return err
			}
			if printList(a, types, raw, func(t api.WebhookEventType) string { return t.Type }, "No event types.", "") {
				return nil
			}
			rows := make([][]string, len(types))
			for i, t := range types {
				rows[i] = []string{t.Type, a.out.Dim(t.Resource), t.Description}
			}
			a.out.Table([]string{"type", "resource", "description"}, rows)
			a.out.Summary(count(len(types), "event type"))
			return nil
		},
	}
}

func newWebhookDeliveriesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "deliveries",
		Aliases: []string{"delivery"},
		Short:   "See what was sent to a webhook, and send it again",
	}
	cmd.AddCommand(newWebhookDeliveriesListCmd(a), newWebhookDeliveriesGetCmd(a), newWebhookRedeliverCmd(a))
	return cmd
}

// deliveryOutcome is how the last attempt went: the status code the
// endpoint answered, else why it failed.
func deliveryOutcome(d *api.WebhookDelivery) string {
	switch {
	case d.LastStatusCode != nil:
		return "HTTP " + strconv.Itoa(*d.LastStatusCode)
	case d.LastError != nil && *d.LastError != "":
		return *d.LastError
	}
	return "—"
}

func newWebhookDeliveriesListCmd(a *app) *cobra.Command {
	var limit int
	var cursor string
	var all bool
	cmd := &cobra.Command{
		Use:     "list <webhook id>",
		Aliases: []string{"ls"},
		Short:   "List a webhook's deliveries, newest first",
		Long: `List a webhook's deliveries, newest first, kept for 30 days. One page at a
time: --limit sets its size (1 to 100, default 50) and --cursor, printed under
a page that has more, gets the next. --all reads every page. With --json, one
page is the API's own {data, next_cursor}; --all prints every delivery.`,
		Example: `  pantech webhooks deliveries list whk_123
  pantech webhooks deliveries list whk_123 --limit 10 --cursor <next cursor>
  pantech webhooks deliveries list whk_123 --all --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("limit") && (limit < 1 || limit > 100) {
				return &usageError{errors.New("--limit must be from 1 to 100"), cmd}
			}
			if all && cursor != "" {
				return &usageError{errors.New("pass --cursor or --all, not both"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			q := url.Values{}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			path := webhookPath(args[0]) + "/deliveries"

			var deliveries []api.WebhookDelivery
			var raw []json.RawMessage
			var next *string
			if all {
				if deliveries, raw, err = listItems[api.WebhookDelivery](cmd, c, path, q); err != nil {
					return webhookError(err, args[0])
				}
			} else {
				if cursor != "" {
					q.Set("cursor", cursor)
				}
				var page api.Page[json.RawMessage]
				res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: path, Query: q}, &page)
				if err != nil {
					return webhookError(err, args[0])
				}
				if a.out.JSON {
					a.out.RawJSON(res.Body)
					return nil
				}
				raw, next = page.Data, page.NextCursor
				deliveries = make([]api.WebhookDelivery, len(raw))
				for i, r := range raw {
					if err := json.Unmarshal(r, &deliveries[i]); err != nil {
						return fmt.Errorf("reading the API's answer: %w", err)
					}
				}
			}
			if !printList(a, deliveries, raw, func(d api.WebhookDelivery) string { return d.ID }, "No deliveries in the last 30 days.", "") {
				rows := make([][]string, len(deliveries))
				for i := range deliveries {
					d := &deliveries[i]
					when := output.Ago(d.CreatedAt)
					rows[i] = []string{a.out.Dim(d.ID), d.EventType, a.out.State(d.Status), strconv.Itoa(d.Attempts), deliveryOutcome(d), when}
				}
				a.out.Table([]string{"id", "event", "status", "attempts", "last result", "created"}, rows)
				a.out.Summary(count(len(deliveries), "delivery"))
			}
			if next != nil && *next != "" && !a.out.JSON {
				more := "pantech webhooks deliveries list " + args[0] + " --cursor " + *next
				if limit > 0 {
					more += " --limit " + strconv.Itoa(limit)
				}
				a.out.Next("More", more)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.IntVar(&limit, "limit", 0, "deliveries per page, 1 to 100 (default 50)")
	f.StringVar(&cursor, "cursor", "", "the next_cursor of the previous page")
	f.BoolVar(&all, "all", false, "read every page")
	return cmd
}

func newWebhookDeliveriesGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <webhook id> <delivery id>",
		Aliases: []string{"show"},
		Short:   "Show a delivery, with the exact body sent",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var d api.WebhookDeliveryDetail
			if ok, err := getOne(a, cmd, c, webhookDeliveryPath(args[0], args[1]), &d); !ok {
				return webhookError(err, args[0])
			}
			if a.out.Quiet {
				a.out.Line("%s", d.ID)
				return nil
			}
			duration := "—"
			if d.LastDurationMS != nil {
				duration = strconv.Itoa(*d.LastDurationMS) + " ms"
			}
			detail := output.Detail{
				Title:    d.EventType,
				State:    a.out.State(d.Status),
				Subtitle: d.ID,
				Sections: [][]output.Pair{
					{{"Webhook", d.WebhookID}, {"Event", d.EventID}},
					{{"Attempts", strconv.Itoa(d.Attempts)}, {"Last result", deliveryOutcome(&d.WebhookDelivery)}, {"Took", duration}, {"Last attempt", output.When(d.LastAttemptAt)}, {"Next attempt", output.When(d.NextAttemptAt)}, {"Delivered", output.When(d.DeliveredAt)}},
					{{"Created", output.When(d.CreatedAt)}},
				},
			}
			if d.Status != "pending" {
				detail.Next = [][2]string{{"Send it again", "pantech webhooks deliveries redeliver " + args[0] + " " + d.ID}}
			}
			a.out.Print(detail)
			if d.LastResponse != nil && *d.LastResponse != "" {
				a.out.Line("\n%s", a.out.Dim("Last response"))
				a.out.Line("%s", strings.TrimRight(*d.LastResponse, "\n"))
			}
			if len(d.Payload) > 0 {
				a.out.Line("\n%s", a.out.Dim("Payload"))
				a.out.RawJSON(d.Payload)
			}
			return nil
		},
	}
}

func newWebhookRedeliverCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "redeliver <webhook id> <delivery id>",
		Aliases: []string{"resend"},
		Short:   "Send a delivery again, with the same event id",
		Long: `Queue a delivery that succeeded, failed or was cancelled to be sent again
from its first attempt, with the same event id.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: webhookDeliveryPath(args[0], args[1]) + "/redeliver"}, nil)
			if api.IsCode(err, "WEBHOOK_DELIVERY_IN_PROGRESS") {
				// Also what a retried request sees once the first one queued it.
				if !a.out.JSON && !a.out.Quiet {
					a.out.Note("Delivery %s is already being sent.", args[1])
				}
				return nil
			}
			if err != nil {
				return webhookError(err, args[0])
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%s", args[1])
			default:
				a.out.Success("Queued delivery %s to be sent again.", args[1])
				a.out.Next("See how it went", "pantech webhooks deliveries get "+args[0]+" "+args[1])
			}
			return nil
		},
	}
}
