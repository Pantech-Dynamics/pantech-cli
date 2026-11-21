package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

const gib = int64(1) << 30

var bucketKind = kind{prefix: "bkt_", path: "/buckets", noun: "bucket", listCmd: "pantech buckets list", noSearch: true}

func newObjectStorageCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "object-storage",
		Aliases: []string{"s3"},
		Short:   "S3-compatible object storage (closed beta): status and access keys",
		Long: `S3-compatible object storage, in closed beta.

  1. pantech object-storage                            is it on, and the endpoint
  2. pantech buckets create my-app-uploads
  3. pantech object-storage keys create --name app     prints the secret once
  4. aws s3 ls s3://my-app-uploads --endpoint-url <endpoint>

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
	}, newObjectStorageKeysCmd(a))
	return cmd
}

// objectStorageError adds what to do next to the API's object storage errors.
func objectStorageError(err error) error {
	switch {
	case api.IsCode(err, "OBJECT_STORAGE_NOT_ENABLED"):
		return fmt.Errorf("%w\nObject storage is in closed beta: ask support to switch it on for your organization", err)
	case api.IsCode(err, "BUCKET_NOT_FOUND"):
		return fmt.Errorf("%w\nSee: pantech buckets list", err)
	case api.IsCode(err, "BUCKET_NOT_EMPTY"):
		return fmt.Errorf("%w\nDelete its objects too with --force", err)
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
		a.out.Print(output.Detail{
			Title:    "Object storage",
			State:    a.out.State("active"),
			Subtitle: "closed beta",
			Sections: [][]output.Pair{{{"Endpoint", s.Endpoint}}},
			Next:     [][2]string{{"Create a bucket", "pantech buckets create <name>"}, {"Create a key", "pantech object-storage keys create --name <name>"}},
		})
	}
	return nil
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
		Short:   "Object storage buckets (closed beta): list, create, get, delete",
		Long: `Object storage buckets, in closed beta. Buckets are named by id (bkt_…) or by
name. Reach them with any S3 tool, the endpoint from pantech object-storage and
an access key from pantech object-storage keys create.`,
	}
	cmd.AddCommand(newBucketsListCmd(a), newBucketsGetCmd(a), newBucketsCreateCmd(a), newBucketsDeleteCmd(a))
	return cmd
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
				rows[i] = []string{a.out.Dim(b.ID), output.Clean(b.Name), a.out.State(b.State), b.Region, quotaText(b.QuotaBytes), onOff(b.Versioning), output.Ago(&b.CreatedAt)}
			}
			a.out.Table([]string{"id", "name", "state", "region", "size limit", "versioning", "created"}, rows)
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
			{{"Size limit", quotaText(b.QuotaBytes)}, {"Versioning", onOff(b.Versioning)}, {"Object lock", onOff(b.ObjectLock)}, {"Public prefixes", public}},
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
	var region string
	var quotaGiB int64
	var versioning, objectLock, noWait bool
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a bucket (billed per GiB stored)",
		Long: `Create a bucket. Its name is 3 to 63 lower-case letters, digits and hyphens,
starting and ending with a letter or digit, and is shared by everyone, so it
must not be taken already. Storage is billed at 500 naira per GiB a month,
measured hourly; data out and requests are free during the beta.`,
		Example: "  pantech buckets create my-app-uploads\n  pantech buckets create backups --quota 100 --versioning",
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
				return objectStorageError(api.AsFlags(err, map[string]string{"name": "<name>", "region": "--region", "quota_bytes": "--quota", "object_lock": "--object-lock"}))
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
	f.BoolVar(&noWait, "no-wait", false, "return the bucket id without waiting")
	return cmd
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
				access := "read and write"
				if k.ReadOnly {
					access = "read only"
				}
				rows[i] = []string{a.out.Dim(k.ID), output.Clean(k.Name), k.AccessKeyID, access, keyBuckets(k), output.Ago(&k.CreatedAt)}
			}
			a.out.Table([]string{"id", "name", "access key id", "access", "buckets", "created"}, rows)
			a.out.Summary(count(len(keys), "key"))
			return nil
		},
	}
}

func newObjectStorageKeysCreateCmd(a *app) *cobra.Command {
	var name, region string
	var readOnly bool
	var buckets []string
	cmd := &cobra.Command{
		Use:   "create --name NAME",
		Short: "Create an access key; prints its secret once",
		Long: `Create an S3 access key. The access key id and secret are printed once, and
the secret is never shown again: save it now. With --json they are the
"access_key_id" and "secret_access_key" fields.`,
		Example: `  pantech object-storage keys create --name app
  pantech object-storage keys create --name backups-reader --read-only --bucket backups`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body := map[string]any{"name": name, "read_only": readOnly}
			if region != "" {
				body["region"] = region
			}
			if len(buckets) > 0 {
				body["buckets"] = buckets
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var k api.ObjectStorageKey
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: "/object-storage/keys", Body: body}, &k)
			if err != nil {
				return objectStorageError(api.AsFlags(err, map[string]string{"name": "--name", "region": "--region", "buckets": "--bucket"}))
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
	f.BoolVar(&readOnly, "read-only", false, "may read but not write or delete")
	f.StringSliceVar(&buckets, "bucket", nil, "only this bucket, by name; repeat for more (default: all)")
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
