package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

func newBucketsSharedCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "shared",
		Short: "List cross-account buckets shared with your organization",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			items, raw, err := listItems[api.SharedBucket](cmd, c, "/buckets/shared", nil)
			if err != nil {
				return objectStorageError(err)
			}
			if printList(a, items, raw, func(b api.SharedBucket) string { return b.BucketID }, "No buckets shared with your organization.", "") {
				return nil
			}
			rows := make([][]string, len(items))
			for i, b := range items {
				rows[i] = []string{
					a.out.Dim(b.ShareID),
					output.Clean(b.BucketName),
					b.Region,
					output.Clean(b.OwnerOrganizationID),
					permissionsText(b.Permissions, false),
					prefixesText(b.Prefixes),
					expiresText(b.ExpiresAt),
					output.Or(&b.S3BucketRef),
				}
			}
			a.out.Table([]string{"share id", "bucket", "region", "owner", "permissions", "prefixes", "expires", "s3 ref"}, rows)
			a.out.Summary(count(len(items), "shared bucket"))
			return nil
		},
	}
}

// --- Bucket shares ---

func newBucketsSharesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "shares",
		Aliases: []string{"share"},
		Short:   "Manage cross-organization shares on a bucket",
	}
	cmd.AddCommand(
		newBucketsSharesListCmd(a),
		newBucketsSharesCreateCmd(a),
		newBucketsSharesRevokeCmd(a),
	)
	return cmd
}

func newBucketsSharesListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list <bucket>",
		Aliases: []string{"ls"},
		Short:   "List cross-organization shares on a bucket",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			shares, raw, err := listItems[api.BucketShare](cmd, c, "/buckets/"+url.PathEscape(id)+"/shares", nil)
			if err != nil {
				return objectStorageError(err)
			}
			if printList(
				a,
				shares,
				raw,
				func(s api.BucketShare) string { return s.ID },
				fmt.Sprintf("No shares on %s.", args[0]),
				fmt.Sprintf("pantech buckets shares create %s --grantee-org <org> --permissions read_only", args[0]),
			) {
				return nil
			}
			rows := make([][]string, len(shares))
			for i, s := range shares {
				rows[i] = []string{
					a.out.Dim(s.ID),
					output.Clean(s.GranteeOrganizationID),
					permissionsText(s.Permissions, false),
					prefixesText(s.Prefixes),
					expiresText(s.ExpiresAt),
					output.Ago(&s.CreatedAt),
				}
			}
			a.out.Table([]string{"id", "grantee org", "permissions", "prefixes", "expires", "created"}, rows)
			a.out.Summary(count(len(shares), "share"))
			return nil
		},
	}
}

func newBucketsSharesCreateCmd(a *app) *cobra.Command {
	var granteeOrg, permissions, expiresAt string
	var prefixes []string
	cmd := &cobra.Command{
		Use:   "create <bucket> --grantee-org ORG --permissions read_only|write_only|read_write",
		Short: "Share a bucket (or specific prefixes) with another organization",
		Example: `  pantech buckets shares create uploads --grantee-org org_01abc --permissions read_only
  pantech buckets shares create uploads --grantee-org org_01abc --permissions write_only --prefix dropbox/ --expires-at 2027-01-01T00:00:00Z`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := validateStoragePermissions(permissions)
			if err != nil {
				return &usageError{err, cmd}
			}
			body := map[string]any{
				"grantee_organization_id": strings.TrimSpace(granteeOrg),
				"permissions":             p,
			}
			if len(prefixes) > 0 {
				body["prefixes"] = prefixes
			}
			if expiresAt != "" {
				t, err := time.Parse(time.RFC3339, strings.TrimSpace(expiresAt))
				if err != nil {
					return &usageError{fmt.Errorf("--expires-at must be an RFC 3339 timestamp (e.g. 2027-01-01T00:00:00Z): %w", err), cmd}
				}
				body["expires_at"] = t.UTC().Format(time.RFC3339)
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var sh api.BucketShare
			res, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodPost,
				Path:   "/buckets/" + url.PathEscape(id) + "/shares",
				Body:   body,
			}, &sh)
			if err != nil {
				return objectStorageError(api.AsFlags(err, map[string]string{
					"grantee_organization_id": "--grantee-org",
					"permissions":             "--permissions",
					"prefixes":                "--prefix",
					"expires_at":              "--expires-at",
				}))
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%s", sh.ID)
			default:
				a.out.Success("Shared %s with %s (%s).", args[0], output.Clean(sh.GranteeOrganizationID), permissionsText(sh.Permissions, false))
				a.out.Line("%s", sh.ID)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&granteeOrg, "grantee-org", "", "organization id to share with, e.g. org_... (required)")
	f.StringVar(&permissions, "permissions", "", "access level: read_only, write_only or read_write (required)")
	f.StringSliceVar(&prefixes, "prefix", nil, "limit access to object keys under this prefix (repeatable; default: all)")
	f.StringVar(&expiresAt, "expires-at", "", "when the share expires, RFC 3339 timestamp (default: never)")
	_ = cmd.MarkFlagRequired("grantee-org")
	_ = cmd.MarkFlagRequired("permissions")
	return cmd
}

func newBucketsSharesRevokeCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "revoke <bucket> <share-id>",
		Aliases: []string{"delete", "rm"},
		Short:   "Revoke a cross-organization share on a bucket",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Revoke share %s on bucket %s (%s)? The grantee organization will lose access at once.", args[1], args[0], id)); err != nil {
				return err
			}
			if _, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodDelete,
				Path:   "/buckets/" + url.PathEscape(id) + "/shares/" + url.PathEscape(args[1]),
			}, nil); err != nil {
				return objectStorageError(err)
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Revoked share %s on %s.", args[1], args[0])
			}
			return nil
		},
	}
}

// --- Bucket notifications ---

func validateNotificationEvents(events []string) ([]string, error) {
	if len(events) == 0 {
		return nil, errors.New("pass at least one --event (object.created or object.removed)")
	}
	out := make([]string, 0, len(events))
	for _, ev := range events {
		v := strings.TrimSpace(ev)
		switch v {
		case "object.created", "object.removed":
			out = append(out, v)
		default:
			return nil, fmt.Errorf("--event must be object.created or object.removed (got %q)", ev)
		}
	}
	return out, nil
}

func notificationFilterText(prefix, suffix string) string {
	var parts []string
	if prefix != "" {
		parts = append(parts, "prefix="+prefix)
	}
	if suffix != "" {
		parts = append(parts, "suffix="+suffix)
	}
	if len(parts) == 0 {
		return "*"
	}
	return strings.Join(parts, ", ")
}

func newBucketsNotificationsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "notifications",
		Aliases: []string{"notification", "notif"},
		Short:   "Manage object event notification rules on a bucket",
	}
	cmd.AddCommand(
		newBucketsNotificationsListCmd(a),
		newBucketsNotificationsCreateCmd(a),
		newBucketsNotificationsUpdateCmd(a),
		newBucketsNotificationsDeleteCmd(a),
	)
	return cmd
}

func newBucketsNotificationsListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list <bucket>",
		Aliases: []string{"ls"},
		Short:   "List event notification rules on a bucket",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			items, raw, err := listItems[api.BucketNotification](cmd, c, "/buckets/"+url.PathEscape(id)+"/notifications", nil)
			if err != nil {
				return objectStorageError(err)
			}
			if printList(
				a,
				items,
				raw,
				func(n api.BucketNotification) string { return n.ID },
				fmt.Sprintf("No notification rules on %s.", args[0]),
				fmt.Sprintf("pantech buckets notifications create %s --name <name> --event object.created --target-type webhook --target-id <id>", args[0]),
			) {
				return nil
			}
			rows := make([][]string, len(items))
			for i, n := range items {
				rows[i] = []string{
					a.out.Dim(n.ID),
					output.Clean(n.Name),
					strings.Join(n.Events, ", "),
					notificationFilterText(n.Prefix, n.Suffix),
					fmt.Sprintf("%s:%s", n.TargetType, n.TargetID),
					onOff(n.Enabled),
					output.Ago(&n.CreatedAt),
				}
			}
			a.out.Table([]string{"id", "name", "events", "filter", "target", "enabled", "created"}, rows)
			a.out.Summary(count(len(items), "notification rule"))
			return nil
		},
	}
}

func newBucketsNotificationsCreateCmd(a *app) *cobra.Command {
	var name, targetType, targetID, prefix, suffix string
	var events []string
	var disabled bool
	cmd := &cobra.Command{
		Use:   "create <bucket> --name NAME --event EVENT --target-type webhook|function --target-id ID",
		Short: "Create an object event notification rule on a bucket",
		Example: `  pantech buckets notifications create uploads --name thumbnails --event object.created --prefix images/ --suffix .png --target-type function --target-id fn_01abc
  pantech buckets notifications create uploads --name audit --event object.created --event object.removed --target-type webhook --target-id whk_01abc`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			evs, err := validateNotificationEvents(events)
			if err != nil {
				return &usageError{err, cmd}
			}
			tt := strings.ToLower(strings.TrimSpace(targetType))
			if tt != "webhook" && tt != "function" {
				return &usageError{fmt.Errorf("--target-type must be webhook or function (got %q)", targetType), cmd}
			}
			body := map[string]any{
				"name":        strings.TrimSpace(name),
				"events":      evs,
				"target_type": tt,
				"target_id":   strings.TrimSpace(targetID),
				"enabled":     !disabled,
			}
			if prefix != "" {
				body["prefix"] = prefix
			}
			if suffix != "" {
				body["suffix"] = suffix
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var n api.BucketNotification
			res, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodPost,
				Path:   "/buckets/" + url.PathEscape(id) + "/notifications",
				Body:   body,
			}, &n)
			if err != nil {
				return objectStorageError(api.AsFlags(err, map[string]string{
					"name":        "--name",
					"events":      "--event",
					"prefix":      "--prefix",
					"suffix":      "--suffix",
					"target_type": "--target-type",
					"target_id":   "--target-id",
					"enabled":     "--disabled",
				}))
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%s", n.ID)
			default:
				a.out.Success("Created notification rule %s on %s.", output.Clean(n.Name), args[0])
				a.out.Line("%s", n.ID)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "rule name (required)")
	f.StringSliceVar(&events, "event", nil, "event to subscribe to: object.created or object.removed (repeatable, required)")
	f.StringVar(&targetType, "target-type", "", "destination type: webhook or function (required)")
	f.StringVar(&targetID, "target-id", "", "webhook or function id (required)")
	f.StringVar(&prefix, "prefix", "", "only match object keys starting with this prefix")
	f.StringVar(&suffix, "suffix", "", "only match object keys ending with this suffix")
	f.BoolVar(&disabled, "disabled", false, "create the rule disabled")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("event")
	_ = cmd.MarkFlagRequired("target-type")
	_ = cmd.MarkFlagRequired("target-id")
	return cmd
}

func newBucketsNotificationsUpdateCmd(a *app) *cobra.Command {
	var events []string
	var prefix, suffix string
	var enable, disable bool
	cmd := &cobra.Command{
		Use:   "update <bucket> <notification-id>",
		Short: "Update an object event notification rule on a bucket",
		Example: `  pantech buckets notifications update uploads bnt_01abc --disable
  pantech buckets notifications update uploads bnt_01abc --event object.created --prefix logs/ --suffix .json`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			f := cmd.Flags()
			if enable && disable {
				return &usageError{errors.New("use either --enable or --disable, not both"), cmd}
			}
			body := map[string]any{}
			if f.Changed("event") {
				evs, err := validateNotificationEvents(events)
				if err != nil {
					return &usageError{err, cmd}
				}
				body["events"] = evs
			}
			if f.Changed("prefix") {
				body["prefix"] = prefix
			}
			if f.Changed("suffix") {
				body["suffix"] = suffix
			}
			if enable {
				body["enabled"] = true
			}
			if disable {
				body["enabled"] = false
			}
			if len(body) == 0 {
				return &usageError{errors.New("nothing to change: pass --event, --prefix, --suffix, --enable or --disable"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var n api.BucketNotification
			res, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodPatch,
				Path:   "/buckets/" + url.PathEscape(id) + "/notifications/" + url.PathEscape(args[1]),
				Body:   body,
			}, &n)
			if err != nil {
				return objectStorageError(api.AsFlags(err, map[string]string{
					"events":  "--event",
					"prefix":  "--prefix",
					"suffix":  "--suffix",
					"enabled": "--enable",
				}))
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%s", n.ID)
			default:
				a.out.Success("Updated notification rule %s on %s.", n.ID, args[0])
				a.out.Line("%s", n.ID)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringSliceVar(&events, "event", nil, "replace subscribed events: object.created or object.removed (repeatable)")
	f.StringVar(&prefix, "prefix", "", "object key prefix filter (pass empty string to clear)")
	f.StringVar(&suffix, "suffix", "", "object key suffix filter (pass empty string to clear)")
	f.BoolVar(&enable, "enable", false, "enable the rule")
	f.BoolVar(&disable, "disable", false, "disable the rule")
	return cmd
}

func newBucketsNotificationsDeleteCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <bucket> <notification-id>",
		Aliases: []string{"rm"},
		Short:   "Delete an object event notification rule on a bucket",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Delete notification rule %s on bucket %s (%s)?", args[1], args[0], id)); err != nil {
				return err
			}
			if _, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodDelete,
				Path:   "/buckets/" + url.PathEscape(id) + "/notifications/" + url.PathEscape(args[1]),
			}, nil); err != nil {
				return objectStorageError(err)
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Deleted notification rule %s on %s.", args[1], args[0])
			}
			return nil
		},
	}
}

// --- Bucket custom domains ---

func newBucketsDomainsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "domains",
		Aliases: []string{"domain"},
		Short:   "Manage custom domains and static website hosting on a bucket",
	}
	cmd.AddCommand(
		newBucketsDomainsListCmd(a),
		newBucketsDomainsCreateCmd(a),
		newBucketsDomainsUpdateCmd(a),
		newBucketsDomainsDeleteCmd(a),
	)
	return cmd
}

func newBucketsDomainsListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list <bucket>",
		Aliases: []string{"ls"},
		Short:   "List custom domains attached to a bucket",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			items, raw, err := listItems[api.BucketDomain](cmd, c, "/buckets/"+url.PathEscape(id)+"/domains", nil)
			if err != nil {
				return objectStorageError(err)
			}
			if printList(
				a,
				items,
				raw,
				func(d api.BucketDomain) string { return d.ID },
				fmt.Sprintf("No custom domains on %s.", args[0]),
				fmt.Sprintf("pantech buckets domains create %s --hostname assets.example.com --certificate <cert id>", args[0]),
			) {
				return nil
			}
			rows := make([][]string, len(items))
			for i, d := range items {
				rows[i] = []string{
					a.out.Dim(d.ID),
					output.Clean(d.Hostname),
					a.out.State(d.Status),
					d.CertificateID,
					output.Or(&d.IndexDocument),
					onOff(d.SPAMode),
					fmt.Sprintf("%ds", d.CacheTTLSeconds),
					output.Ago(&d.CreatedAt),
				}
			}
			a.out.Table([]string{"id", "hostname", "status", "certificate", "index", "spa", "cache ttl", "created"}, rows)
			a.out.Summary(count(len(items), "custom domain"))
			return nil
		},
	}
}

func newBucketsDomainsCreateCmd(a *app) *cobra.Command {
	var hostname, certificate, indexDoc, errorDoc string
	var spaMode bool
	var cacheTTL int
	cmd := &cobra.Command{
		Use:   "create <bucket> --hostname HOST --certificate CERT_ID",
		Short: "Attach a custom domain with a TLS certificate to a bucket",
		Example: `  pantech buckets domains create website --hostname www.example.com --certificate cert_01abc --index-document index.html --error-document 404.html --cache-ttl 3600
  pantech buckets domains create app-web --hostname app.example.com --certificate cert_01abc --index-document index.html --spa-mode`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f := cmd.Flags()
			if f.Changed("cache-ttl") && cacheTTL < 0 {
				return &usageError{errors.New("--cache-ttl must be 0 or more"), cmd}
			}
			body := map[string]any{
				"hostname":       strings.TrimSpace(hostname),
				"certificate_id": strings.TrimSpace(certificate),
			}
			if f.Changed("index-document") {
				body["index_document"] = indexDoc
			}
			if f.Changed("error-document") {
				body["error_document"] = errorDoc
			}
			if f.Changed("spa-mode") {
				body["spa_mode"] = spaMode
			}
			if f.Changed("cache-ttl") {
				body["cache_ttl_seconds"] = cacheTTL
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var d api.BucketDomain
			res, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodPost,
				Path:   "/buckets/" + url.PathEscape(id) + "/domains",
				Body:   body,
			}, &d)
			if err != nil {
				return objectStorageError(api.AsFlags(err, map[string]string{
					"hostname":          "--hostname",
					"certificate_id":    "--certificate",
					"index_document":    "--index-document",
					"error_document":    "--error-document",
					"spa_mode":          "--spa-mode",
					"cache_ttl_seconds": "--cache-ttl",
				}))
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%s", d.ID)
			default:
				a.out.Success("Attached custom domain %s to %s.", output.Clean(d.Hostname), args[0])
				a.out.Line("%s", d.ID)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&hostname, "hostname", "", "custom domain hostname, e.g. assets.example.com (required)")
	f.StringVar(&certificate, "certificate", "", "issued TLS certificate id, e.g. cert_... (required)")
	f.StringVar(&indexDoc, "index-document", "", "default document for directory paths, e.g. index.html")
	f.StringVar(&errorDoc, "error-document", "", "object key served on 404 responses, e.g. 404.html")
	f.BoolVar(&spaMode, "spa-mode", false, "serve the index document on missing paths for single-page apps")
	f.IntVar(&cacheTTL, "cache-ttl", 0, "cache TTL in seconds (0 or more)")
	_ = cmd.MarkFlagRequired("hostname")
	_ = cmd.MarkFlagRequired("certificate")
	return cmd
}

func newBucketsDomainsUpdateCmd(a *app) *cobra.Command {
	var certificate, indexDoc, errorDoc string
	var spaMode, noSPAMode bool
	var cacheTTL int
	cmd := &cobra.Command{
		Use:   "update <bucket> <domain-id>",
		Short: "Update a custom domain's certificate or static website settings",
		Example: `  pantech buckets domains update website bdom_01abc --certificate cert_02def --cache-ttl 600
  pantech buckets domains update website bdom_01abc --spa-mode`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			f := cmd.Flags()
			if spaMode && noSPAMode {
				return &usageError{errors.New("use either --spa-mode or --no-spa-mode, not both"), cmd}
			}
			if f.Changed("cache-ttl") && cacheTTL < 0 {
				return &usageError{errors.New("--cache-ttl must be 0 or more"), cmd}
			}
			body := map[string]any{}
			if f.Changed("certificate") {
				body["certificate_id"] = strings.TrimSpace(certificate)
			}
			if f.Changed("index-document") {
				body["index_document"] = indexDoc
			}
			if f.Changed("error-document") {
				body["error_document"] = errorDoc
			}
			if f.Changed("spa-mode") {
				body["spa_mode"] = spaMode
			} else if noSPAMode {
				body["spa_mode"] = false
			}
			if f.Changed("cache-ttl") {
				body["cache_ttl_seconds"] = cacheTTL
			}
			if len(body) == 0 {
				return &usageError{errors.New("nothing to change: pass --certificate, --index-document, --error-document, --spa-mode, --no-spa-mode or --cache-ttl"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var d api.BucketDomain
			res, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodPatch,
				Path:   "/buckets/" + url.PathEscape(id) + "/domains/" + url.PathEscape(args[1]),
				Body:   body,
			}, &d)
			if err != nil {
				return objectStorageError(api.AsFlags(err, map[string]string{
					"certificate_id":    "--certificate",
					"index_document":    "--index-document",
					"error_document":    "--error-document",
					"spa_mode":          "--spa-mode",
					"cache_ttl_seconds": "--cache-ttl",
				}))
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%s", d.ID)
			default:
				a.out.Success("Updated custom domain %s on %s.", d.ID, args[0])
				a.out.Line("%s", d.ID)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&certificate, "certificate", "", "issued TLS certificate id, e.g. cert_...")
	f.StringVar(&indexDoc, "index-document", "", "default document for directory paths (empty string to clear)")
	f.StringVar(&errorDoc, "error-document", "", "object key served on 404 responses (empty string to clear)")
	f.BoolVar(&spaMode, "spa-mode", false, "serve the index document on missing paths")
	f.BoolVar(&noSPAMode, "no-spa-mode", false, "turn SPA mode off")
	f.IntVar(&cacheTTL, "cache-ttl", 0, "cache TTL in seconds (0 or more)")
	return cmd
}

func newBucketsDomainsDeleteCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <bucket> <domain-id>",
		Aliases: []string{"rm"},
		Short:   "Detach a custom domain from a bucket",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Detach custom domain %s from bucket %s (%s)?", args[1], args[0], id)); err != nil {
				return err
			}
			if _, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodDelete,
				Path:   "/buckets/" + url.PathEscape(id) + "/domains/" + url.PathEscape(args[1]),
			}, nil); err != nil {
				return objectStorageError(err)
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Detached custom domain %s from %s.", args[1], args[0])
			}
			return nil
		},
	}
}
