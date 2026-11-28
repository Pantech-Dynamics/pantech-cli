package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

const gib = int64(1) << 30

// nowFunc returns the current time; overridable in tests.
var nowFunc = time.Now

var bucketKind = kind{prefix: "bkt_", path: "/buckets", noun: "bucket", listCmd: "pantech buckets list", noSearch: true}

func newObjectStorageCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "object-storage",
		Aliases: []string{"s3"},
		Short:   "S3-compatible object storage (closed beta): status, settings, usage and access keys",
		Long: `S3-compatible object storage, in closed beta.

  1. pantech object-storage                            is it on, and the endpoint
  2. pantech object-storage usage --range 7d           transfer and request counts
  3. pantech buckets create my-app-uploads
  4. pantech object-storage keys create --name app     prints the secret once
  5. aws s3 ls s3://my-app-uploads --endpoint-url <endpoint>

Use "pantech object-storage settings --block-public-access=true|false" to block
or allow public access across all buckets in your organization.

Storage is billed at 500 naira per GiB a month, measured hourly. Data out and
requests are free during the beta.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.objectStorageStatus(cmd) },
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show whether object storage is on, and its endpoint",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return a.objectStorageStatus(cmd) },
	}, newObjectStorageSettingsCmd(a), newObjectStorageUsageCmd(a), newObjectStorageKeysCmd(a))
	return cmd
}

// objectStorageError adds what to do next to the API's object storage errors.
func objectStorageError(err error) error {
	switch {
	case api.IsCode(err, "OBJECT_STORAGE_NOT_ENABLED"):
		return fmt.Errorf("%w\nObject storage is in closed beta: ask support to switch it on for your organization", err)
	case api.IsCode(err, "PUBLIC_ACCESS_BLOCKED"):
		return fmt.Errorf("%w\nSee: pantech object-storage settings --block-public-access=false", err)
	case api.IsCode(err, "BUCKET_NOT_FOUND"):
		return fmt.Errorf("%w\nSee: pantech buckets list", err)
	case api.IsCode(err, "BUCKET_NOT_EMPTY"):
		return fmt.Errorf("%w\nEmpty the bucket first (pantech buckets empty <bucket>) or delete its objects too with --force", err)
	case api.IsCode(err, "BUCKET_NAME_TAKEN"):
		return fmt.Errorf("%w\nBucket names are shared by everyone: choose another", err)
	case api.IsCode(err, "OBJECT_STORAGE_KEY_NOT_FOUND"):
		return fmt.Errorf("%w\nSee: pantech object-storage keys list", err)
	}
	return err
}

func (a *app) objectStorageStatus(cmd *cobra.Command) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	var s api.ObjectStorageStatus
	res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: "/object-storage"}, &s)
	if err != nil {
		return objectStorageError(err)
	}
	switch {
	case a.out.JSON:
		a.out.RawJSON(res.Body)
	case a.out.Quiet:
		a.out.Line("%s", output.Clean(s.Endpoint))
	case !s.Enabled:
		a.out.Note("Object storage is in closed beta and is not on for this organization. Ask support to join.")
	default:
		blockPublic := "—"
		if s.BlockPublicAccess != nil {
			blockPublic = onOff(*s.BlockPublicAccess)
		}
		sections := [][]output.Pair{
			{
				{"Endpoint", s.Endpoint},
				{"Direct uploads", onOff(s.DirectUploadEnabled)},
				{"Block public access", blockPublic},
			},
		}
		if s.Limits != nil {
			var lim []output.Pair
			if s.Limits.MaxBuckets > 0 {
				lim = append(lim, output.Pair{"Max buckets", fmt.Sprintf("%d", s.Limits.MaxBuckets)})
			}
			if s.Limits.MaxStorageBytes > 0 {
				lim = append(lim, output.Pair{"Max storage", bytesText(s.Limits.MaxStorageBytes)})
			}
			if s.Limits.MaxObjects > 0 {
				lim = append(lim, output.Pair{"Max objects", fmt.Sprintf("%d", s.Limits.MaxObjects)})
			}
			if len(lim) > 0 {
				sections = append(sections, lim)
			}
		}
		a.out.Print(output.Detail{
			Title:    "Object storage",
			State:    a.out.State("active"),
			Subtitle: "closed beta",
			Sections: sections,
			Next:     [][2]string{{"Create a bucket", "pantech buckets create <name>"}, {"Create a key", "pantech object-storage keys create --name <name>"}},
		})
	}
	return nil
}

func newObjectStorageSettingsCmd(a *app) *cobra.Command {
	var blockPublicAccess bool
	cmd := &cobra.Command{
		Use:   "settings --block-public-access=true|false",
		Short: "Change organization-level object storage settings",
		Long: `Turn Block All Public Access on or off for your organization's buckets.
When turned on, public prefixes and public objects stop being served anonymously
at once, and making anything public is refused until turned off.`,
		Example: `  pantech object-storage settings --block-public-access=true
  pantech object-storage settings --block-public-access=false`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !cmd.Flags().Changed("block-public-access") {
				return &usageError{errors.New("pass --block-public-access=true or --block-public-access=false"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			if blockPublicAccess {
				if err := a.confirm("Block all public access for your organization's buckets? Public prefixes and public objects stop being served anonymously at once."); err != nil {
					return err
				}
			}
			var out api.ObjectStorageSettings
			res, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodPatch,
				Path:   "/object-storage/settings",
				Body:   map[string]any{"block_public_access": blockPublicAccess},
			}, &out)
			if err != nil {
				return objectStorageError(api.AsFlags(err, map[string]string{"block_public_access": "--block-public-access"}))
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%s", onOff(out.BlockPublicAccess))
			default:
				a.out.Success("Block public access is now %s.", onOff(out.BlockPublicAccess))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&blockPublicAccess, "block-public-access", false, "block all public access across your organization's buckets (true or false)")
	return cmd
}

func newObjectStorageUsageCmd(a *app) *cobra.Command {
	var rng string
	cmd := &cobra.Command{
		Use:     "usage",
		Short:   "Show object storage transfer and request usage",
		Example: "  pantech object-storage usage --range 7d",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch rng {
			case "24h", "7d", "30d":
			default:
				return &usageError{fmt.Errorf("--range must be 24h, 7d or 30d (got %q)", rng), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var u api.ObjectStorageUsage
			res, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodGet,
				Path:   "/object-storage/usage",
				Query:  url.Values{"range": {rng}},
			}, &u)
			if err != nil {
				return objectStorageError(err)
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
				return nil
			case a.out.Quiet:
				a.out.Line("%d %d %d %d", u.TotalBytesSent, u.TotalBytesReceived, u.TotalReadOps, u.TotalWriteOps)
				return nil
			}
			label := u.Range
			if label == "" {
				label = rng
			}
			a.out.Print(output.Detail{
				Title:    "Object storage usage",
				Subtitle: label,
				Sections: [][]output.Pair{
					{
						{"Data sent", bytesText(u.TotalBytesSent)},
						{"Data received", bytesText(u.TotalBytesReceived)},
						{"Read requests", fmt.Sprintf("%d", u.TotalReadOps)},
						{"Write requests", fmt.Sprintf("%d", u.TotalWriteOps)},
					},
				},
			})
			if len(u.Hours) > 0 {
				a.out.Line("")
				rows := make([][]string, len(u.Hours))
				for i, h := range u.Hours {
					rows[i] = []string{
						output.When(&h.Hour),
						bytesText(h.BytesSent),
						bytesText(h.BytesReceived),
						fmt.Sprintf("%d", h.ReadOps),
						fmt.Sprintf("%d", h.WriteOps),
					}
				}
				a.out.Table([]string{"hour", "sent", "received", "reads", "writes"}, rows)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&rng, "range", "24h", "time range: 24h, 7d or 30d")
	return cmd
}

func quotaText(b int64) string {
	if b <= 0 {
		return "no limit"
	}
	return bytesText(b)
}

func bucketID(cmd *cobra.Command, c *api.Client, ref string) (string, error) {
	id, err := resolve(cmd, c, bucketKind, ref, func(b api.Bucket) (string, string) {
		if b.State == "deleting" {
			return "", ""
		}
		return b.ID, b.Name
	})
	return id, objectStorageError(err)
}

func newBucketsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "buckets",
		Aliases: []string{"bucket"},
		Short:   "Object storage buckets (closed beta): manage buckets, rules and objects",
		Long: `Object storage buckets, in closed beta. Buckets are named by id (bkt_…) or by
name. Reach them with pantech buckets objects or any S3 tool, the endpoint from
pantech object-storage and an access key from pantech object-storage keys create.`,
	}
	cmd.AddCommand(
		newBucketsListCmd(a),
		newBucketsSharedCmd(a),
		newBucketsGetCmd(a),
		newBucketsCreateCmd(a),
		newBucketsUpdateCmd(a),
		newBucketsStatsCmd(a),
		newBucketsEmptyCmd(a),
		newBucketsDeleteCmd(a),
		newBucketsCORSCmd(a),
		newBucketsLifecycleCmd(a),
		newBucketsTagsCmd(a),
		newBucketsObjectLockCmd(a),
		newBucketsSharesCmd(a),
		newBucketsNotificationsCmd(a),
		newBucketsDomainsCmd(a),
		newBucketsObjectsCmd(a),
	)
	return cmd
}

func encryptionText(enc string) string {
	if enc == "" {
		return "none"
	}
	return enc
}

func validateEncryption(enc string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(enc))
	switch v {
	case "none", "sse_s3":
		return v, nil
	default:
		return "", fmt.Errorf("--encryption must be none or sse_s3 (got %q)", enc)
	}
}

func newBucketsListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List buckets",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			buckets, raw, err := listItems[api.Bucket](cmd, c, "/buckets", nil)
			if err != nil {
				return objectStorageError(err)
			}
			if printList(a, buckets, raw, func(b api.Bucket) string { return b.ID }, "No buckets yet.", "pantech buckets create <name>") {
				return nil
			}
			rows := make([][]string, len(buckets))
			for i, b := range buckets {
				rows[i] = []string{a.out.Dim(b.ID), output.Clean(b.Name), a.out.State(b.State), b.Region, bytesText(b.SizeBytes), quotaText(b.QuotaBytes), onOff(b.Versioning), encryptionText(b.Encryption), output.Ago(&b.CreatedAt)}
			}
			a.out.Table([]string{"id", "name", "state", "region", "stored", "size limit", "versioning", "encryption", "created"}, rows)
			a.out.Summary(count(len(buckets), "bucket"))
			return nil
		},
	}
}

func bucketDetail(out *output.Printer, b *api.Bucket) output.Detail {
	public := "none"
	if len(b.PublicPrefixes) > 0 {
		public = strings.Join(b.PublicPrefixes, ", ")
	}
	return output.Detail{
		Title:    b.Name,
		State:    out.State(b.State),
		Subtitle: b.ID,
		Sections: [][]output.Pair{
			{{"Region", b.Region}, {"Endpoint", b.Endpoint}},
			{
				{"Stored", fmt.Sprintf("%s (%s)", bytesText(b.SizeBytes), count(int(b.ObjectCount), "object"))},
				{"Size limit", quotaText(b.QuotaBytes)},
				{"Versioning", onOff(b.Versioning)},
				{"Object lock", onOff(b.ObjectLock)},
				{"Encryption", encryptionText(b.Encryption)},
				{"Public prefixes", public},
			},
			{{"Created", output.When(&b.CreatedAt)}},
		},
	}
}

func newBucketsGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <bucket>",
		Aliases: []string{"show"},
		Short:   "Show a bucket, by id or name",
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
			var b api.Bucket
			ok, err := getOne(a, cmd, c, "/buckets/"+url.PathEscape(id), &b)
			if !ok {
				return objectStorageError(err)
			}
			a.out.Print(bucketDetail(a.out, &b))
			return nil
		},
	}
}

func newBucketsCreateCmd(a *app) *cobra.Command {
	var region, encryption string
	var quotaGiB int64
	var versioning, objectLock, noWait bool
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a bucket (billed per GiB stored)",
		Long: `Create a bucket. Its name is 3 to 63 lower-case letters, digits and hyphens,
starting and ending with a letter or digit, and is shared by everyone, so it
must not be taken already. Storage is billed at 500 naira per GiB a month,
measured hourly; data out and requests are free during the beta.`,
		Example: "  pantech buckets create my-app-uploads\n  pantech buckets create backups --quota 100 --versioning --encryption sse_s3",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if quotaGiB < 0 {
				return &usageError{errors.New("--quota must be 0 (no limit) or more"), cmd}
			}
			body := map[string]any{"name": strings.TrimSpace(args[0]), "versioning": versioning, "object_lock": objectLock}
			if region != "" {
				body["region"] = region
			}
			if quotaGiB > 0 {
				body["quota_bytes"] = quotaGiB * gib
			}
			if cmd.Flags().Changed("encryption") {
				enc, err := validateEncryption(encryption)
				if err != nil {
					return &usageError{err, cmd}
				}
				body["encryption"] = enc
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Create bucket %s? Storage is billed at 500 naira per GiB a month.", args[0])); err != nil {
				return err
			}
			var b api.Bucket
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: "/buckets", Body: body}, &b)
			if err != nil {
				return objectStorageError(api.AsFlags(err, map[string]string{
					"name":        "<name>",
					"region":      "--region",
					"quota_bytes": "--quota",
					"object_lock": "--object-lock",
					"encryption":  "--encryption",
				}))
			}
			if noWait || b.OperationID == "" {
				if a.out.JSON {
					a.out.RawJSON(res.Body)
					return nil
				}
				a.out.Line("%s", b.ID)
				if b.OperationID != "" {
					a.out.Next("Follow it", "pantech operations wait "+b.OperationID)
				}
				return nil
			}
			steps := a.out.Steps()
			steps.Start("Creating bucket " + b.Name)
			op, err := api.WaitOperation(ctx(cmd), c, b.OperationID, nil)
			if err != nil {
				steps.Fail("", "")
				return err
			}
			steps.Done("Created bucket "+b.Name, "")
			if a.out.JSON {
				a.out.Value(op)
				return nil
			}
			a.out.Line("%s", b.ID)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&region, "region", "", "region, such as af-abj (default: the only one)")
	f.Int64Var(&quotaGiB, "quota", 0, "size limit in GiB (default: no limit)")
	f.BoolVar(&versioning, "versioning", false, "keep earlier versions of objects")
	f.BoolVar(&objectLock, "object-lock", false, "allow retention on objects (cannot be turned off later)")
	f.StringVar(&encryption, "encryption", "", "default server-side encryption for new objects: none or sse_s3")
	f.BoolVar(&noWait, "no-wait", false, "return the bucket id without waiting")
	return cmd
}

func newBucketsUpdateCmd(a *app) *cobra.Command {
	var quotaGB int64
	var encryption string
	var noQuota, versioning, noVersioning, clearPublicPrefixes, noWait bool
	var publicPrefixes []string
	cmd := &cobra.Command{
		Use:   "update <bucket>",
		Short: "Change a bucket's quota, versioning, encryption or public prefixes",
		Example: `  pantech buckets update uploads --quota-gb 100 --versioning --encryption sse_s3
  pantech buckets update uploads --public-prefix public/ --public-prefix assets/
  pantech buckets update uploads --clear-public-prefixes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f := cmd.Flags()
			if f.Changed("quota-gb") && noQuota {
				return &usageError{errors.New("use either --quota-gb or --no-quota, not both"), cmd}
			}
			if versioning && noVersioning {
				return &usageError{errors.New("use either --versioning or --no-versioning, not both"), cmd}
			}
			if f.Changed("public-prefix") && clearPublicPrefixes {
				return &usageError{errors.New("use either --public-prefix or --clear-public-prefixes, not both"), cmd}
			}
			if f.Changed("quota-gb") && quotaGB <= 0 {
				return &usageError{errors.New("--quota-gb must be positive; pass --no-quota to remove the limit"), cmd}
			}
			body := map[string]any{}
			if f.Changed("quota-gb") {
				body["quota_bytes"] = quotaGB * gib
			}
			if noQuota {
				body["quota_bytes"] = int64(0)
			}
			if versioning {
				body["versioning"] = true
			}
			if noVersioning {
				body["versioning"] = false
			}
			if f.Changed("encryption") {
				enc, err := validateEncryption(encryption)
				if err != nil {
					return &usageError{err, cmd}
				}
				body["encryption"] = enc
			}
			if f.Changed("public-prefix") {
				body["public_prefixes"] = publicPrefixes
			}
			if clearPublicPrefixes {
				body["public_prefixes"] = []string{}
			}
			if len(body) == 0 {
				return &usageError{errors.New("nothing to change: pass --quota-gb, --no-quota, --versioning, --no-versioning, --encryption, --public-prefix or --clear-public-prefixes"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var b api.Bucket
			res, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodPatch,
				Path:   "/buckets/" + url.PathEscape(id),
				Body:   body,
			}, &b)
			if err != nil {
				return objectStorageError(api.AsFlags(err, map[string]string{
					"quota_bytes":     "--quota-gb",
					"versioning":      "--versioning",
					"encryption":      "--encryption",
					"public_prefixes": "--public-prefix",
				}))
			}
			if noWait || b.OperationID == "" {
				switch {
				case a.out.JSON:
					a.out.RawJSON(res.Body)
				case a.out.Quiet:
					a.out.Line("%s", b.ID)
				default:
					a.out.Print(bucketDetail(a.out, &b))
					if b.OperationID != "" {
						a.out.Next("Follow it", "pantech operations wait "+b.OperationID)
					}
				}
				return nil
			}
			steps := a.out.Steps()
			steps.Start("Updating bucket " + args[0])
			op, err := api.WaitOperation(ctx(cmd), c, b.OperationID, nil)
			if err != nil {
				steps.Fail("", "")
				return err
			}
			steps.Done("Updated bucket "+args[0], "")
			switch {
			case a.out.JSON:
				a.out.Value(op)
			case a.out.Quiet:
				a.out.Line("%s", b.ID)
			default:
				a.out.Print(bucketDetail(a.out, &b))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.Int64Var(&quotaGB, "quota-gb", 0, "size limit in GiB")
	f.BoolVar(&noQuota, "no-quota", false, "remove the bucket's size limit")
	f.BoolVar(&versioning, "versioning", false, "turn versioning on")
	f.BoolVar(&noVersioning, "no-versioning", false, "turn versioning off")
	f.StringVar(&encryption, "encryption", "", "default server-side encryption for new objects: none or sse_s3")
	f.StringSliceVar(&publicPrefixes, "public-prefix", nil, "public read prefix (repeatable; replaces the list)")
	f.BoolVar(&clearPublicPrefixes, "clear-public-prefixes", false, "remove all public prefixes (make bucket wholly private)")
	f.BoolVar(&noWait, "no-wait", false, "return without waiting for the update operation")
	return cmd
}

func newBucketsStatsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "stats <bucket>",
		Short:   "Show live stored bytes and object count for a bucket",
		Example: "  pantech buckets stats uploads",
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
			var st api.BucketStats
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: "/buckets/" + url.PathEscape(id) + "/stats"}, &st)
			if err != nil {
				return objectStorageError(err)
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%d %d", st.SizeBytes, st.ObjectCount)
			default:
				a.out.Print(output.Detail{
					Title:    args[0],
					Subtitle: id,
					Sections: [][]output.Pair{{
						{"Stored", bytesText(st.SizeBytes)},
						{"Objects", fmt.Sprintf("%d", st.ObjectCount)},
					}},
				})
			}
			return nil
		},
	}
}

func newBucketsEmptyCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "empty <bucket>",
		Short: "Delete all objects, versions and incomplete uploads in a bucket",
		Long: `Delete every object, previous version and delete marker in the bucket, and
abort incomplete multipart uploads. Objects protected by object lock are kept.`,
		Example: "  pantech buckets empty uploads",
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
			if err := a.confirm(fmt.Sprintf("Empty bucket %s (%s)? Every object, previous version and incomplete upload in it is deleted and cannot be recovered.", args[0], id)); err != nil {
				return err
			}
			var resBody api.EmptyBucketResult
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: "/buckets/" + url.PathEscape(id) + "/empty"}, &resBody)
			if err != nil {
				return objectStorageError(err)
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%d", resBody.DeletedCount)
			default:
				a.out.Success("Emptied %s: deleted %s, aborted %s.", args[0], count(resBody.DeletedCount, "object"), count(resBody.AbortedUploads, "incomplete upload"))
				if resBody.LockedCount > 0 {
					a.out.Warn("%s could not be deleted because of object lock.", count(resBody.LockedCount, "object"))
					for _, k := range resBody.SampleLockedKeys {
						a.out.Note("  locked: %s", k)
					}
				}
			}
			return nil
		},
	}
}

func newBucketsDeleteCmd(a *app) *cobra.Command {
	var force, noWait bool
	cmd := &cobra.Command{
		Use:     "delete <bucket>",
		Aliases: []string{"rm"},
		Short:   "Delete a bucket, by id or name",
		Long: `Delete a bucket. It stops answering and being billed at once. A bucket that
still holds objects is refused unless --force, which deletes them too.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			question := fmt.Sprintf("Delete bucket %s (%s)?", args[0], id)
			if force {
				question += " Every object in it is deleted too."
			}
			if err := a.confirm(question); err != nil {
				return err
			}
			path := "/buckets/" + url.PathEscape(id)
			if force {
				path += "?force=true"
			}
			return objectStorageError(a.runWrite(cmd, c, api.Request{Method: http.MethodDelete, Path: path}, noWait, verb{"Deleting", "Deleted"}, args[0]))
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "delete the bucket's objects too")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	return cmd
}

// --- Bucket rules: CORS, lifecycle, tags, object-lock ---

func readJSONInput(file string) ([]byte, error) {
	if file == "-" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("reading stdin: %w", err)
		}
		return data, nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", file, err)
	}
	return data, nil
}

func parseWrappedOrList[T any](data []byte, field string) ([]T, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, errors.New("JSON input is empty")
	}
	if strings.HasPrefix(trimmed, "[") {
		var list []T
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, fmt.Errorf("parsing JSON array: %w", err)
		}
		if list == nil {
			list = []T{}
		}
		return list, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("parsing JSON: %w", err)
	}
	raw, ok := obj[field]
	if !ok {
		return nil, fmt.Errorf("JSON object must contain %q (or pass a JSON array)", field)
	}
	var list []T
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parsing %q: %w", field, err)
	}
	if list == nil {
		list = []T{}
	}
	return list, nil
}

func parseStorageTags(raw []string) ([]api.StorageTag, error) {
	tags := make([]api.StorageTag, 0, len(raw))
	for _, item := range raw {
		k, v, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("invalid tag %q: use key=value", item)
		}
		tags = append(tags, api.StorageTag{Key: k, Value: v})
	}
	return tags, nil
}

func newBucketsCORSCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cors",
		Short: "Read or replace a bucket's CORS rules",
	}
	get := &cobra.Command{
		Use:     "get <bucket>",
		Aliases: []string{"show"},
		Short:   "Show a bucket's CORS rules",
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
			var out struct {
				Rules []api.BucketCORSRule `json:"rules"`
			}
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: "/buckets/" + url.PathEscape(id) + "/cors"}, &out)
			if err != nil {
				return objectStorageError(err)
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
				return nil
			case a.out.Quiet:
				for _, r := range out.Rules {
					a.out.Line("%s", r.ID)
				}
				return nil
			case len(out.Rules) == 0:
				a.out.Note("No CORS rules on %s.", args[0])
				return nil
			}
			rows := make([][]string, len(out.Rules))
			for i, r := range out.Rules {
				rows[i] = []string{
					r.ID,
					strings.Join(r.AllowedOrigins, ", "),
					strings.Join(r.AllowedMethods, ", "),
					strings.Join(r.AllowedHeaders, ", "),
					fmt.Sprintf("%ds", r.MaxAgeSeconds),
				}
			}
			a.out.Table([]string{"id", "origins", "methods", "headers", "max age"}, rows)
			a.out.Summary(count(len(out.Rules), "rule"))
			return nil
		},
	}
	var file string
	set := &cobra.Command{
		Use:   "set <bucket> --file <json-path>",
		Short: "Replace a bucket's CORS rules from a JSON file (or - for stdin)",
		Long: `Replace a bucket's CORS rules from a JSON file (use - to read stdin).
The file may be {"rules": [...]} or a bare JSON array of rules.`,
		Example: "  pantech buckets cors set uploads --file cors.json\n  cat cors.json | pantech buckets cors set uploads --file -",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := readJSONInput(file)
			if err != nil {
				return err
			}
			rules, err := parseWrappedOrList[api.BucketCORSRule](data, "rules")
			if err != nil {
				return &usageError{err, cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var out struct {
				Rules []api.BucketCORSRule `json:"rules"`
			}
			res, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodPut,
				Path:   "/buckets/" + url.PathEscape(id) + "/cors",
				Body:   map[string]any{"rules": rules},
			}, &out)
			if err != nil {
				return objectStorageError(err)
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%d", len(out.Rules))
			default:
				a.out.Success("Saved %s on %s.", count(len(out.Rules), "CORS rule"), args[0])
			}
			return nil
		},
	}
	set.Flags().StringVar(&file, "file", "", "JSON file with CORS rules, or - for stdin (required)")
	_ = set.MarkFlagRequired("file")
	cmd.AddCommand(get, set)
	return cmd
}

func newBucketsLifecycleCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lifecycle",
		Short: "Read or replace a bucket's lifecycle rules",
	}
	get := &cobra.Command{
		Use:     "get <bucket>",
		Aliases: []string{"show"},
		Short:   "Show a bucket's lifecycle rules",
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
			var out struct {
				Rules []api.BucketLifecycleRule `json:"rules"`
			}
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: "/buckets/" + url.PathEscape(id) + "/lifecycle"}, &out)
			if err != nil {
				return objectStorageError(err)
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
				return nil
			case a.out.Quiet:
				for _, r := range out.Rules {
					a.out.Line("%s", r.ID)
				}
				return nil
			case len(out.Rules) == 0:
				a.out.Note("No lifecycle rules on %s.", args[0])
				return nil
			}
			rows := make([][]string, len(out.Rules))
			for i, r := range out.Rules {
				prefix := r.Prefix
				if prefix == "" {
					prefix = "*"
				}
				rows[i] = []string{
					r.ID,
					prefix,
					onOff(r.Enabled),
					daysOrDash(r.ExpirationDays),
					daysOrDash(r.NoncurrentExpirationDays),
					daysOrDash(r.AbortIncompleteMultipartDays),
				}
			}
			a.out.Table([]string{"id", "prefix", "enabled", "expire", "noncurrent", "abort multipart"}, rows)
			a.out.Summary(count(len(out.Rules), "rule"))
			return nil
		},
	}
	var file string
	set := &cobra.Command{
		Use:   "set <bucket> --file <json-path>",
		Short: "Replace a bucket's lifecycle rules from a JSON file (or - for stdin)",
		Long: `Replace a bucket's lifecycle rules from a JSON file (use - to read stdin).
The file may be {"rules": [...]} or a bare JSON array of rules.`,
		Example: "  pantech buckets lifecycle set uploads --file lifecycle.json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := readJSONInput(file)
			if err != nil {
				return err
			}
			rules, err := parseWrappedOrList[api.BucketLifecycleRule](data, "rules")
			if err != nil {
				return &usageError{err, cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var out struct {
				Rules []api.BucketLifecycleRule `json:"rules"`
			}
			res, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodPut,
				Path:   "/buckets/" + url.PathEscape(id) + "/lifecycle",
				Body:   map[string]any{"rules": rules},
			}, &out)
			if err != nil {
				return objectStorageError(err)
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%d", len(out.Rules))
			default:
				a.out.Success("Saved %s on %s.", count(len(out.Rules), "lifecycle rule"), args[0])
			}
			return nil
		},
	}
	set.Flags().StringVar(&file, "file", "", "JSON file with lifecycle rules, or - for stdin (required)")
	_ = set.MarkFlagRequired("file")
	cmd.AddCommand(get, set)
	return cmd
}

func daysOrDash(d int) string {
	if d <= 0 {
		return "—"
	}
	return fmt.Sprintf("%dd", d)
}

func newBucketsTagsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tags",
		Short: "Read or replace a bucket's tags",
	}
	get := &cobra.Command{
		Use:     "get <bucket>",
		Aliases: []string{"show"},
		Short:   "Show a bucket's tags",
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
			var out struct {
				Tags []api.StorageTag `json:"tags"`
			}
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: "/buckets/" + url.PathEscape(id) + "/tags"}, &out)
			if err != nil {
				return objectStorageError(err)
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
				return nil
			case a.out.Quiet:
				for _, t := range out.Tags {
					a.out.Line("%s=%s", t.Key, t.Value)
				}
				return nil
			case len(out.Tags) == 0:
				a.out.Note("No tags on %s.", args[0])
				return nil
			}
			rows := make([][]string, len(out.Tags))
			for i, t := range out.Tags {
				rows[i] = []string{t.Key, t.Value}
			}
			a.out.Table([]string{"key", "value"}, rows)
			a.out.Summary(count(len(out.Tags), "tag"))
			return nil
		},
	}
	var rawTags []string
	var clear bool
	set := &cobra.Command{
		Use:     "set <bucket>",
		Short:   "Replace a bucket's tags",
		Example: "  pantech buckets tags set uploads --tag env=prod --tag team=data\n  pantech buckets tags set uploads --clear",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if clear && len(rawTags) > 0 {
				return &usageError{errors.New("use either --tag or --clear, not both"), cmd}
			}
			if !clear && len(rawTags) == 0 {
				return &usageError{errors.New("pass at least one --tag key=value, or --clear"), cmd}
			}
			tags := []api.StorageTag{}
			if !clear {
				var err error
				tags, err = parseStorageTags(rawTags)
				if err != nil {
					return &usageError{err, cmd}
				}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var out struct {
				Tags []api.StorageTag `json:"tags"`
			}
			res, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodPut,
				Path:   "/buckets/" + url.PathEscape(id) + "/tags",
				Body:   map[string]any{"tags": tags},
			}, &out)
			if err != nil {
				return objectStorageError(err)
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				for _, t := range out.Tags {
					a.out.Line("%s=%s", t.Key, t.Value)
				}
			default:
				if len(out.Tags) == 0 {
					a.out.Success("Cleared tags on %s.", args[0])
				} else {
					a.out.Success("Saved %s on %s.", count(len(out.Tags), "tag"), args[0])
				}
			}
			return nil
		},
	}
	set.Flags().StringArrayVar(&rawTags, "tag", nil, "tag as key=value (repeatable)")
	set.Flags().BoolVar(&clear, "clear", false, "remove all tags")
	cmd.AddCommand(get, set)
	return cmd
}

func newBucketsObjectLockCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "object-lock",
		Aliases: []string{"lock"},
		Short:   "Read or set a bucket's default object-lock retention",
	}
	get := &cobra.Command{
		Use:     "get <bucket>",
		Aliases: []string{"show"},
		Short:   "Show a bucket's default object-lock retention",
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
			var cfg api.BucketObjectLockConfig
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: "/buckets/" + url.PathEscape(id) + "/object-lock"}, &cfg)
			if err != nil {
				return objectStorageError(err)
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
				return nil
			case a.out.Quiet:
				if !cfg.Enabled {
					a.out.Line("off")
				} else {
					a.out.Line("%s", cfg.Mode)
				}
				return nil
			}
			period := "—"
			switch {
			case cfg.Days > 0:
				period = count(cfg.Days, "day")
			case cfg.Years > 0:
				period = count(cfg.Years, "year")
			}
			mode := cfg.Mode
			if mode == "" {
				mode = "—"
			}
			a.out.Print(output.Detail{
				Title:    args[0],
				State:    a.out.State(onOff(cfg.Enabled)),
				Subtitle: id,
				Sections: [][]output.Pair{{
					{"Default retention", onOff(cfg.Enabled)},
					{"Mode", mode},
					{"Period", period},
				}},
			})
			return nil
		},
	}
	var mode string
	var days, years int
	var clear bool
	set := &cobra.Command{
		Use:   "set <bucket>",
		Short: "Set or clear a bucket's default object-lock retention",
		Example: `  pantech buckets object-lock set backups --mode governance --days 30
  pantech buckets object-lock set backups --mode compliance --years 1
  pantech buckets object-lock set backups --clear`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var body api.BucketObjectLockConfig
			if clear {
				if mode != "" || days != 0 || years != 0 {
					return &usageError{errors.New("do not combine --clear with --mode, --days or --years"), cmd}
				}
				body = api.BucketObjectLockConfig{Enabled: false}
			} else {
				m := strings.ToUpper(strings.TrimSpace(mode))
				if m != "GOVERNANCE" && m != "COMPLIANCE" {
					return &usageError{fmt.Errorf("--mode must be governance or compliance (got %q)", mode), cmd}
				}
				if (days > 0 && years > 0) || (days <= 0 && years <= 0) {
					return &usageError{errors.New("set either --days or --years (a positive number), not both"), cmd}
				}
				body = api.BucketObjectLockConfig{Enabled: true, Mode: m, Days: days, Years: years}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var out api.BucketObjectLockConfig
			res, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodPut,
				Path:   "/buckets/" + url.PathEscape(id) + "/object-lock",
				Body:   body,
			}, &out)
			if err != nil {
				return objectStorageError(api.AsFlags(err, map[string]string{"mode": "--mode", "days": "--days", "years": "--years"}))
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				if !out.Enabled {
					a.out.Line("off")
				} else {
					a.out.Line("%s", out.Mode)
				}
			default:
				if !out.Enabled {
					a.out.Success("Cleared default object-lock retention on %s.", args[0])
				} else {
					a.out.Success("Set default object-lock retention on %s (%s).", args[0], out.Mode)
				}
			}
			return nil
		},
	}
	f := set.Flags()
	f.StringVar(&mode, "mode", "", "retention mode: governance or compliance")
	f.IntVar(&days, "days", 0, "default retention period in days")
	f.IntVar(&years, "years", 0, "default retention period in years")
	f.BoolVar(&clear, "clear", false, "clear default retention")
	cmd.AddCommand(get, set)
	return cmd
}

// --- access keys ---

func newObjectStorageKeysCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "keys",
		Aliases: []string{"key"},
		Short:   "S3 access keys: list, create, revoke",
	}
	cmd.AddCommand(newObjectStorageKeysListCmd(a), newObjectStorageKeysCreateCmd(a), newObjectStorageKeysRevokeCmd(a))
	return cmd
}

func keyBuckets(k *api.ObjectStorageKey) string {
	if len(k.Buckets) == 0 {
		return "all"
	}
	return strings.Join(k.Buckets, ", ")
}

func prefixesText(prefixes []string) string {
	if len(prefixes) == 0 {
		return "all"
	}
	return strings.Join(prefixes, ", ")
}

func expiresText(t *string) string {
	if t == nil || *t == "" {
		return "never"
	}
	return output.When(t)
}

func permissionsText(perms string, readOnly bool) string {
	switch perms {
	case "read_only":
		return "read only"
	case "write_only":
		return "write only"
	case "read_write":
		return "read and write"
	default:
		if readOnly {
			return "read only"
		}
		return "read and write"
	}
}

func validateStoragePermissions(perms string) (string, error) {
	p := strings.ToLower(strings.TrimSpace(perms))
	switch p {
	case "read_write", "read_only", "write_only":
		return p, nil
	default:
		return "", fmt.Errorf("--permissions must be read_write, read_only or write_only (got %q)", perms)
	}
}

// parseExpiresIn parses a duration such as 24h, 30m, 7d or 90d.
func parseExpiresIn(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("--expires-in must not be empty")
	}
	if d, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(d)
		if err != nil || n <= 0 || n > 3650 {
			return 0, fmt.Errorf("--expires-in days must be between 1d and 3650d (got %q)", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	dur, err := time.ParseDuration(s)
	if err != nil || dur <= 0 || dur > 3650*24*time.Hour {
		return 0, fmt.Errorf("--expires-in must be a positive duration such as 24h, 7d or 90d (got %q)", s)
	}
	return dur, nil
}

func newObjectStorageKeysListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List access keys (never their secrets)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			keys, raw, err := listItems[api.ObjectStorageKey](cmd, c, "/object-storage/keys", nil)
			if err != nil {
				return objectStorageError(err)
			}
			if printList(a, keys, raw, func(k api.ObjectStorageKey) string { return k.ID }, "No access keys yet.", "pantech object-storage keys create --name <name>") {
				return nil
			}
			rows := make([][]string, len(keys))
			for i := range keys {
				k := &keys[i]
				rows[i] = []string{
					a.out.Dim(k.ID),
					output.Clean(k.Name),
					k.AccessKeyID,
					permissionsText(k.Permissions, k.ReadOnly),
					keyBuckets(k),
					prefixesText(k.Prefixes),
					expiresText(k.ExpiresAt),
					output.Ago(&k.CreatedAt),
				}
			}
			a.out.Table([]string{"id", "name", "access key id", "permissions", "buckets", "prefixes", "expires", "created"}, rows)
			a.out.Summary(count(len(keys), "key"))
			return nil
		},
	}
}

func newObjectStorageKeysCreateCmd(a *app) *cobra.Command {
	var name, region, permissions, expiresAt, expiresIn string
	var readOnly bool
	var buckets, prefixes []string
	cmd := &cobra.Command{
		Use:   "create --name NAME",
		Short: "Create an access key; prints its secret once",
		Long: `Create an S3 access key. The access key id and secret are printed once, and
the secret is never shown again: save it now. With --json they are the
"access_key_id" and "secret_access_key" fields.`,
		Example: `  pantech object-storage keys create --name app
  pantech object-storage keys create --name uploader --permissions write_only --bucket uploads --prefix incoming/ --expires-in 30d
  pantech object-storage keys create --name backups-reader --read-only --bucket backups`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f := cmd.Flags()
			if expiresAt != "" && expiresIn != "" {
				return &usageError{errors.New("use either --expires-at or --expires-in, not both"), cmd}
			}
			body := map[string]any{"name": name}
			if f.Changed("permissions") {
				p, err := validateStoragePermissions(permissions)
				if err != nil {
					return &usageError{err, cmd}
				}
				if readOnly && p != "read_only" {
					return &usageError{fmt.Errorf("cannot combine --read-only with --permissions %s", p), cmd}
				}
				body["permissions"] = p
				if readOnly {
					body["read_only"] = true
				}
			} else {
				body["read_only"] = readOnly
			}
			if region != "" {
				body["region"] = region
			}
			if len(buckets) > 0 {
				body["buckets"] = buckets
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
			if expiresIn != "" {
				dur, err := parseExpiresIn(expiresIn)
				if err != nil {
					return &usageError{err, cmd}
				}
				body["expires_at"] = nowFunc().Add(dur).UTC().Truncate(time.Second).Format(time.RFC3339)
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var k api.ObjectStorageKey
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: "/object-storage/keys", Body: body}, &k)
			if err != nil {
				return objectStorageError(api.AsFlags(err, map[string]string{
					"name":        "--name",
					"region":      "--region",
					"permissions": "--permissions",
					"buckets":     "--bucket",
					"prefixes":    "--prefix",
					"expires_at":  "--expires-at",
				}))
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
				return nil
			case k.SecretAccessKey == "":
				return fmt.Errorf("the API returned no secret for key %s: revoke it (pantech object-storage keys revoke %s) and create another", k.ID, k.ID)
			case a.out.Quiet:
				a.out.Line("%s", k.AccessKeyID)
				a.out.Line("%s", k.SecretAccessKey)
				return nil
			}
			a.out.Success("Created access key %s (%s).", k.Name, k.ID)
			a.out.Note("Its secret is shown once and never again. Save it now:")
			a.out.Line("endpoint:          %s", k.Endpoint)
			a.out.Line("access key id:     %s", k.AccessKeyID)
			a.out.Line("secret access key: %s", k.SecretAccessKey)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "a name such as app (required)")
	f.StringVar(&region, "region", "", "region, such as af-abj (default: the only one)")
	f.StringVar(&permissions, "permissions", "", "access level: read_write, read_only or write_only (default: read_write)")
	f.BoolVar(&readOnly, "read-only", false, "may read but not write or delete (same as --permissions read_only)")
	f.StringSliceVar(&buckets, "bucket", nil, "only this bucket, by bucket name (not bkt_ id); repeat for more (default: all)")
	f.StringSliceVar(&prefixes, "prefix", nil, "only object keys starting with this prefix, e.g. uploads/; repeat for more (default: all)")
	f.StringVar(&expiresAt, "expires-at", "", "when the key stops working, RFC 3339 timestamp (default: never)")
	f.StringVar(&expiresIn, "expires-in", "", "key lifetime from now, e.g. 24h, 30d or 90d (default: never)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newObjectStorageKeysRevokeCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "revoke <key id>",
		Aliases: []string{"delete", "rm"},
		Short:   "Revoke an access key",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Revoke access key %s? Anything using it is refused from now on.", args[0])); err != nil {
				return err
			}
			if _, err := c.Do(ctx(cmd), api.Request{Method: http.MethodDelete, Path: "/object-storage/keys/" + url.PathEscape(args[0])}, nil); err != nil {
				return objectStorageError(err)
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Revoked %s.", args[0])
			}
			return nil
		},
	}
}
