package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

// singleUploadMaxBytes is the threshold above which uploads switch from
// single-shot PUT to multipart upload. Overridable in tests.
var singleUploadMaxBytes int64 = 20 << 20

const defaultMultipartPartBytes int64 = 8 << 20

func newBucketsObjectsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "objects",
		Aliases: []string{"obj"},
		Short:   "List, upload, download and manage objects in a bucket",
	}
	cmd.AddCommand(
		newBucketObjectsListCmd(a),
		newBucketObjectsMetaCmd(a),
		newBucketObjectsUploadCmd(a),
		newBucketObjectsDownloadCmd(a),
		newBucketObjectsPresignDownloadCmd(a),
		newBucketObjectsPresignUploadCmd(a),
		newBucketObjectsFolderCmd(a),
		newBucketObjectsCopyCmd(a),
		newBucketObjectsDeleteCmd(a),
		newBucketObjectsVisibilityCmd(a),
		newBucketObjectsTagsCmd(a),
		newBucketObjectsLockCmd(a),
	)
	return cmd
}

func newBucketObjectsListCmd(a *app) *cobra.Command {
	var prefix, delimiter, search, cursor string
	var versions bool
	var limit int
	cmd := &cobra.Command{
		Use:     "list <bucket>",
		Aliases: []string{"ls"},
		Short:   "List folders, objects or versions in a bucket",
		Example: `  pantech buckets objects list uploads --prefix images/
  pantech buckets objects list uploads --search logo
  pantech buckets objects list uploads --versions`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("limit") && (limit < 1 || limit > 1000) {
				return &usageError{errors.New("--limit must be between 1 and 1000"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			q := url.Values{}
			if prefix != "" {
				q.Set("prefix", prefix)
			}
			if cmd.Flags().Changed("delimiter") {
				q.Set("delimiter", delimiter)
			}
			if search != "" {
				q.Set("q", search)
			}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			if versions {
				q.Set("versions", "true")
			}
			var list api.BucketObjectList
			res, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodGet,
				Path:   "/buckets/" + url.PathEscape(id) + "/objects",
				Query:  q,
			}, &list)
			if err != nil {
				return objectStorageError(err)
			}
			if a.out.JSON {
				a.out.RawJSON(res.Body)
				return nil
			}
			if versions {
				if a.out.Quiet {
					for _, v := range list.Versions {
						a.out.Line("%s\t%s", v.Key, v.VersionID)
					}
					return nil
				}
				if len(list.Versions) == 0 {
					a.out.Note("No object versions found.")
					return nil
				}
				rows := make([][]string, len(list.Versions))
				for i, v := range list.Versions {
					kind := "version"
					if v.IsDeleteMarker {
						kind = "delete marker"
					} else if v.IsLatest {
						kind = "latest"
					}
					rows[i] = []string{v.Key, a.out.Dim(v.VersionID), kind, bytesText(v.SizeBytes), output.Ago(&v.LastModified)}
				}
				a.out.Table([]string{"key", "version", "state", "size", "modified"}, rows)
				a.out.Summary(count(len(list.Versions), "version"))
				return nil
			}
			if a.out.Quiet {
				for _, f := range list.Folders {
					a.out.Line("%s", f)
				}
				for _, o := range list.Objects {
					a.out.Line("%s", o.Key)
				}
				return nil
			}
			if len(list.Folders) == 0 && len(list.Objects) == 0 {
				a.out.Note("No objects found.")
				return nil
			}
			rows := make([][]string, 0, len(list.Folders)+len(list.Objects))
			for _, folder := range list.Folders {
				rows = append(rows, []string{folder, "folder", "—", "—", "—"})
			}
			for _, o := range list.Objects {
				vis := "private"
				if o.Public {
					vis = "public"
				}
				rows = append(rows, []string{o.Key, bytesText(o.SizeBytes), vis, output.Or(&o.ContentType), output.Ago(&o.LastModified)})
			}
			a.out.Table([]string{"key", "size", "visibility", "content type", "modified"}, rows)
			if list.IsTruncated && list.NextCursor != "" {
				a.out.Next("Next page", fmt.Sprintf("pantech buckets objects list %s --cursor %s", args[0], list.NextCursor))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&prefix, "prefix", "", "only keys starting with this prefix")
	f.StringVar(&delimiter, "delimiter", "/", "folder delimiter (empty string lists flat)")
	f.StringVar(&search, "search", "", "search keys containing this text")
	f.BoolVar(&versions, "versions", false, "list object versions and delete markers")
	f.IntVar(&limit, "limit", 0, "maximum entries per page (1-1000)")
	f.StringVar(&cursor, "cursor", "", "continuation cursor from a previous page")
	return cmd
}

func newBucketObjectsMetaCmd(a *app) *cobra.Command {
	var version string
	cmd := &cobra.Command{
		Use:     "meta <bucket> <key>",
		Aliases: []string{"stat", "head"},
		Short:   "Show an object's properties, metadata, tags, visibility and lock status",
		Example: "  pantech buckets objects meta uploads images/logo.png",
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
			q := url.Values{"key": {args[1]}}
			if version != "" {
				q.Set("version_id", version)
			}
			var m api.BucketObjectMeta
			res, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodGet,
				Path:   "/buckets/" + url.PathEscape(id) + "/objects/meta",
				Query:  q,
			}, &m)
			if err != nil {
				return objectStorageError(err)
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
				return nil
			case a.out.Quiet:
				a.out.Line("%s", m.ETag)
				return nil
			}
			vis := "private"
			if m.Public {
				vis = fmt.Sprintf("public (%s)", m.PublicSource)
			}
			sec1 := []output.Pair{
				{"Size", bytesText(m.SizeBytes)},
				{"Content type", output.Or(&m.ContentType)},
				{"ETag", output.Or(&m.ETag)},
				{"Version", output.Or(&m.VersionID)},
				{"Modified", output.When(&m.LastModified)},
			}
			sec2 := []output.Pair{{"Visibility", vis}}
			if m.Public && m.PublicURL != "" {
				sec2 = append(sec2, output.Pair{"Public URL", m.PublicURL})
			}
			if m.RetentionMode != "" {
				sec2 = append(sec2, output.Pair{"Retention", fmt.Sprintf("%s until %s", m.RetentionMode, output.When(m.RetainUntil))})
			}
			sec2 = append(sec2, output.Pair{"Legal hold", onOff(m.LegalHold)})
			sections := [][]output.Pair{sec1, sec2}
			if len(m.Tags) > 0 {
				parts := make([]string, len(m.Tags))
				for i, t := range m.Tags {
					parts[i] = t.Key + "=" + t.Value
				}
				sections = append(sections, []output.Pair{{"Tags", strings.Join(parts, ", ")}})
			}
			if len(m.Metadata) > 0 {
				keys := make([]string, 0, len(m.Metadata))
				for k := range m.Metadata {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				pairs := make([]output.Pair, 0, len(keys))
				for _, k := range keys {
					pairs = append(pairs, output.Pair{"Meta " + k, m.Metadata[k]})
				}
				sections = append(sections, pairs)
			}
			a.out.Print(output.Detail{
				Title:    m.Key,
				Subtitle: args[0],
				Sections: sections,
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "specific object version id")
	return cmd
}

func newBucketObjectsUploadCmd(a *app) *cobra.Command {
	var filePath, contentType string
	cmd := &cobra.Command{
		Use:     "upload <bucket> <key> --file <path>",
		Aliases: []string{"put"},
		Short:   "Upload a file to a bucket (automatically uses multipart above 20 MiB)",
		Example: "  pantech buckets objects upload uploads images/logo.png --file ./logo.png",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := os.Stat(filePath)
			if err != nil {
				return fmt.Errorf("reading %s: %w", filePath, err)
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s is not a regular file", filePath)
			}
			ct := contentType
			if ct == "" {
				ct = mime.TypeByExtension(filepath.Ext(filePath))
				if ct == "" {
					ct = mime.TypeByExtension(filepath.Ext(args[1]))
				}
				if ct == "" {
					ct = "application/octet-stream"
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
			var obj api.BucketObject
			var rawBody []byte
			if info.Size() <= singleUploadMaxBytes {
				data, err := os.ReadFile(filePath)
				if err != nil {
					return fmt.Errorf("reading %s: %w", filePath, err)
				}
				res, err := c.Do(ctx(cmd), api.Request{
					Method:      http.MethodPut,
					Path:        "/buckets/" + url.PathEscape(id) + "/objects/upload",
					Query:       url.Values{"key": {args[1]}},
					RawBody:     data,
					ContentType: ct,
				}, &obj)
				if err != nil {
					return objectStorageError(err)
				}
				rawBody = res.Body
			} else {
				obj, rawBody, err = uploadMultipart(ctx(cmd), c, id, args[1], filePath, ct, info.Size())
				if err != nil {
					return objectStorageError(err)
				}
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(rawBody)
			case a.out.Quiet:
				a.out.Line("%s", obj.Key)
			default:
				a.out.Success("Uploaded %s (%s).", obj.Key, bytesText(info.Size()))
				if obj.Public && obj.PublicURL != "" {
					a.out.Line("%s", obj.PublicURL)
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&filePath, "file", "", "local file to upload (required)")
	f.StringVar(&contentType, "content-type", "", "MIME content type (default: inferred from extension)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func uploadMultipart(cctx context.Context, c *api.Client, bucketID, key, filePath, contentType string, totalSize int64) (api.BucketObject, []byte, error) {
	base := "/buckets/" + url.PathEscape(bucketID) + "/objects/multipart"
	var start api.MultipartStart
	if _, err := c.Do(cctx, api.Request{
		Method: http.MethodPost,
		Path:   base + "/start",
		Body: map[string]any{
			"key":          key,
			"content_type": contentType,
			"total_size":   totalSize,
		},
	}, &start); err != nil {
		return api.BucketObject{}, nil, err
	}
	abort := func() {
		_, _ = c.Do(context.WithoutCancel(cctx), api.Request{
			Method: http.MethodPost,
			Path:   base + "/abort",
			Body:   map[string]any{"key": key, "upload_id": start.UploadID},
		}, nil)
	}
	partSize := start.PartSizeBytes
	if partSize <= 0 || partSize > singleUploadMaxBytes {
		partSize = defaultMultipartPartBytes
		if partSize > singleUploadMaxBytes {
			partSize = singleUploadMaxBytes
		}
	}
	f, err := os.Open(filePath)
	if err != nil {
		abort()
		return api.BucketObject{}, nil, fmt.Errorf("opening %s: %w", filePath, err)
	}
	defer f.Close()

	buf := make([]byte, partSize)
	var parts []api.MultipartPart
	for partNum := 1; ; partNum++ {
		n, rerr := io.ReadFull(f, buf)
		if rerr != nil && !errors.Is(rerr, io.EOF) && !errors.Is(rerr, io.ErrUnexpectedEOF) {
			abort()
			return api.BucketObject{}, nil, fmt.Errorf("reading %s: %w", filePath, rerr)
		}
		if n == 0 {
			break
		}
		var part api.MultipartPart
		if _, err := c.Do(cctx, api.Request{
			Method: http.MethodPut,
			Path:   base + "/part",
			Query: url.Values{
				"key":         {key},
				"upload_id":   {start.UploadID},
				"part_number": {strconv.Itoa(partNum)},
			},
			RawBody:     buf[:n],
			ContentType: "application/octet-stream",
		}, &part); err != nil {
			abort()
			return api.BucketObject{}, nil, err
		}
		parts = append(parts, api.MultipartPart{PartNumber: partNum, ETag: part.ETag})
		if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
			break
		}
	}
	var obj api.BucketObject
	res, err := c.Do(cctx, api.Request{
		Method: http.MethodPost,
		Path:   base + "/complete",
		Body: map[string]any{
			"key":       key,
			"upload_id": start.UploadID,
			"parts":     parts,
		},
	}, &obj)
	if err != nil {
		abort()
		return api.BucketObject{}, nil, err
	}
	return obj, res.Body, nil
}

func newBucketObjectsDownloadCmd(a *app) *cobra.Command {
	var outPath, version string
	var force bool
	cmd := &cobra.Command{
		Use:     "download <bucket> <key>",
		Aliases: []string{"get"},
		Short:   "Download an object to a file or stdout",
		Example: "  pantech buckets objects download uploads images/logo.png --out ./logo.png\n  pantech buckets objects download uploads config.json > config.json",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if outPath != "" {
				if err := checkTarget(outPath, force); err != nil {
					if errors.Is(err, errFileExists) {
						return fmt.Errorf("%w (pass --force to overwrite)", err)
					}
					return err
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
			q := url.Values{"key": {args[1]}}
			if version != "" {
				q.Set("version_id", version)
			}
			req := api.Request{
				Method: http.MethodGet,
				Path:   "/buckets/" + url.PathEscape(id) + "/objects/download",
				Query:  q,
			}
			if outPath == "" {
				_, err := c.Download(ctx(cmd), req, a.out.Out)
				return objectStorageError(err)
			}
			tmp, err := os.CreateTemp(filepath.Dir(outPath), "."+filepath.Base(outPath)+".tmp-*")
			if err != nil {
				return fmt.Errorf("creating %s: %w", outPath, err)
			}
			tmpName := tmp.Name()
			cleanup := func() {
				_ = tmp.Close()
				_ = os.Remove(tmpName)
			}
			n, err := c.Download(ctx(cmd), req, tmp)
			if err != nil {
				cleanup()
				return objectStorageError(err)
			}
			if err := tmp.Close(); err != nil {
				_ = os.Remove(tmpName)
				return fmt.Errorf("writing %s: %w", outPath, err)
			}
			if err := os.Rename(tmpName, outPath); err != nil {
				_ = os.Remove(tmpName)
				return fmt.Errorf("writing %s: %w", outPath, err)
			}
			if !a.out.Quiet && !a.out.JSON {
				a.out.Success("Downloaded %s to %s (%s).", args[1], outPath, bytesText(n))
			} else if a.out.Quiet {
				a.out.Line("%s", outPath)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&outPath, "out", "o", "", "write to this file instead of stdout")
	f.StringVar(&version, "version", "", "specific object version id")
	f.BoolVar(&force, "force", false, "overwrite --out if it already exists")
	return cmd
}

func newBucketObjectsPresignDownloadCmd(a *app) *cobra.Command {
	var expiresIn int
	var version string
	var inline bool
	cmd := &cobra.Command{
		Use:     "presign-download <bucket> <key>",
		Short:   "Generate a short-lived download URL for an object",
		Example: "  pantech buckets objects presign-download uploads report.pdf --expires-in 3600",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("expires-in") && (expiresIn < 60 || expiresIn > 604800) {
				return &usageError{errors.New("--expires-in must be between 60 and 604800 seconds"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			body := map[string]any{"key": args[1]}
			if version != "" {
				body["version_id"] = version
			}
			if inline {
				body["inline"] = true
			}
			if cmd.Flags().Changed("expires-in") {
				body["expires_in_seconds"] = expiresIn
			}
			var out api.PresignedURL
			res, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodPost,
				Path:   "/buckets/" + url.PathEscape(id) + "/objects/download-url",
				Body:   body,
			}, &out)
			if err != nil {
				return objectStorageError(api.AsFlags(err, map[string]string{"expires_in_seconds": "--expires-in", "version_id": "--version"}))
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%s", out.URL)
			default:
				a.out.Line("%s", out.URL)
				if out.ExpiresInSeconds > 0 {
					a.out.Note("Expires in %ds.", out.ExpiresInSeconds)
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.IntVar(&expiresIn, "expires-in", 0, "URL lifetime in seconds (60 to 604800)")
	f.StringVar(&version, "version", "", "specific object version id")
	f.BoolVar(&inline, "inline", false, "serve inline instead of as an attachment")
	return cmd
}

func newBucketObjectsPresignUploadCmd(a *app) *cobra.Command {
	var expiresIn int
	var contentType string
	cmd := &cobra.Command{
		Use:     "presign-upload <bucket> <key>",
		Short:   "Generate a short-lived upload URL for an object",
		Example: "  pantech buckets objects presign-upload uploads report.pdf --content-type application/pdf",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("expires-in") && (expiresIn < 60 || expiresIn > 604800) {
				return &usageError{errors.New("--expires-in must be between 60 and 604800 seconds"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			body := map[string]any{"key": args[1]}
			if contentType != "" {
				body["content_type"] = contentType
			}
			if cmd.Flags().Changed("expires-in") {
				a.out.Warn("Upload URLs use the platform's fixed lifetime; ignoring --expires-in.")
			}
			var out api.PresignedURL
			res, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodPost,
				Path:   "/buckets/" + url.PathEscape(id) + "/objects/upload-url",
				Body:   body,
			}, &out)
			if err != nil {
				return objectStorageError(api.AsFlags(err, map[string]string{"content_type": "--content-type"}))
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%s", out.URL)
			default:
				a.out.Line("%s", out.URL)
				if out.ExpiresInSeconds > 0 {
					a.out.Note("Expires in %ds.", out.ExpiresInSeconds)
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.IntVar(&expiresIn, "expires-in", 0, "requested lifetime in seconds (upload URLs use the platform default)")
	f.StringVar(&contentType, "content-type", "", "MIME content type the upload must send")
	return cmd
}

func newBucketObjectsFolderCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "folder <bucket> <prefix>",
		Aliases: []string{"mkdir"},
		Short:   "Create a folder prefix in a bucket",
		Example: "  pantech buckets objects folder uploads images/2026/",
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
			var out struct {
				Prefix string `json:"prefix"`
			}
			res, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodPost,
				Path:   "/buckets/" + url.PathEscape(id) + "/objects/folder",
				Body:   map[string]any{"prefix": args[1]},
			}, &out)
			if err != nil {
				return objectStorageError(err)
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%s", out.Prefix)
			default:
				a.out.Success("Created folder %s in %s.", out.Prefix, args[0])
			}
			return nil
		},
	}
}

func newBucketObjectsCopyCmd(a *app) *cobra.Command {
	var destBucket string
	var move bool
	cmd := &cobra.Command{
		Use:     "copy <bucket> <source-key> <dest-key>",
		Aliases: []string{"cp"},
		Short:   "Copy (or move) an object within a bucket",
		Example: "  pantech buckets objects copy uploads old/a.txt new/a.txt\n  pantech buckets objects copy uploads old/a.txt new/a.txt --move",
		Args:    cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			if destBucket != "" {
				destID, err := bucketID(cmd, c, destBucket)
				if err != nil {
					return err
				}
				if destID != id {
					return errors.New("copying between different buckets is not supported by this endpoint: download and upload the object, or use an S3 client")
				}
			}
			body := map[string]any{
				"source_key":      args[1],
				"destination_key": args[2],
			}
			if move {
				body["delete_source"] = true
			}
			var obj api.BucketObject
			res, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodPost,
				Path:   "/buckets/" + url.PathEscape(id) + "/objects/copy",
				Body:   body,
			}, &obj)
			if err != nil {
				return objectStorageError(err)
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%s", obj.Key)
			default:
				if move {
					a.out.Success("Moved %s to %s in %s.", args[1], obj.Key, args[0])
				} else {
					a.out.Success("Copied %s to %s in %s.", args[1], obj.Key, args[0])
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&destBucket, "dest-bucket", "", "destination bucket (must be the same bucket)")
	f.BoolVar(&move, "move", false, "delete the source object after copying")
	return cmd
}

func newBucketObjectsDeleteCmd(a *app) *cobra.Command {
	var version, recursivePrefix string
	cmd := &cobra.Command{
		Use:     "delete <bucket> <key>...",
		Aliases: []string{"rm"},
		Short:   "Delete one or more objects (or a folder prefix) from a bucket",
		Example: `  pantech buckets objects delete uploads tmp/a.txt tmp/b.txt
  pantech buckets objects delete uploads --prefix tmp/`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			keys := args[1:]
			if recursivePrefix != "" && len(keys) > 0 {
				return &usageError{errors.New("pass either object keys or --prefix, not both"), cmd}
			}
			if recursivePrefix == "" && len(keys) == 0 {
				return &usageError{errors.New("pass at least one object key, or --prefix"), cmd}
			}
			if version != "" && len(keys) != 1 {
				return &usageError{errors.New("--version applies to a single object key"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var question string
			if recursivePrefix != "" {
				question = fmt.Sprintf("Delete every object under %q in %s (%s)?", recursivePrefix, args[0], id)
			} else if len(keys) == 1 {
				question = fmt.Sprintf("Delete %s from %s (%s)?", keys[0], args[0], id)
			} else {
				question = fmt.Sprintf("Delete %s from %s (%s)?", count(len(keys), "object"), args[0], id)
			}
			if err := a.confirm(question); err != nil {
				return err
			}
			body := map[string]any{}
			if recursivePrefix != "" {
				body["recursive_prefix"] = recursivePrefix
			} else {
				items := make([]map[string]any, len(keys))
				for i, k := range keys {
					it := map[string]any{"key": k}
					if version != "" {
						it["version_id"] = version
					}
					items[i] = it
				}
				body["items"] = items
			}
			var out api.DeleteObjectsResult
			res, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodPost,
				Path:   "/buckets/" + url.PathEscape(id) + "/objects/delete",
				Body:   body,
			}, &out)
			if err != nil {
				return objectStorageError(err)
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%d", out.Deleted)
			default:
				a.out.Success("Deleted %s from %s.", count(out.Deleted, "object"), args[0])
				for _, f := range out.Failed {
					a.out.Warn("Could not delete %s (%s).", f.Key, f.Code)
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&version, "version", "", "delete only this version id (single key)")
	f.StringVar(&recursivePrefix, "prefix", "", "delete all objects under this folder prefix")
	return cmd
}

func newBucketObjectsVisibilityCmd(a *app) *cobra.Command {
	var makePublic, makePrivate bool
	var version string
	cmd := &cobra.Command{
		Use:     "visibility <bucket> <key> (--public | --private)",
		Short:   "Make an individual object public or private",
		Example: "  pantech buckets objects visibility uploads images/logo.png --public\n  pantech buckets objects visibility uploads images/logo.png --private",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if makePublic == makePrivate {
				return &usageError{errors.New("pass exactly one of --public or --private"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			body := map[string]any{
				"key":    args[1],
				"public": makePublic,
			}
			if version != "" {
				body["version_id"] = version
			}
			var out api.ObjectVisibility
			res, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodPut,
				Path:   "/buckets/" + url.PathEscape(id) + "/objects/visibility",
				Body:   body,
			}, &out)
			if err != nil {
				return objectStorageError(err)
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				if out.Public && out.PublicURL != "" {
					a.out.Line("%s", out.PublicURL)
				} else {
					a.out.Line("%s", out.PublicSource)
				}
			default:
				if out.Public {
					a.out.Success("%s is public (%s).", out.Key, out.PublicSource)
					if out.PublicURL != "" {
						a.out.Line("%s", out.PublicURL)
					}
				} else {
					a.out.Success("%s is private.", out.Key)
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&makePublic, "public", false, "allow public read access to this object")
	f.BoolVar(&makePrivate, "private", false, "revoke object-level public read access")
	f.StringVar(&version, "version", "", "specific object version id")
	return cmd
}

func newBucketObjectsTagsCmd(a *app) *cobra.Command {
	var rawTags []string
	var clear bool
	var version string
	cmd := &cobra.Command{
		Use:     "tags <bucket> <key>",
		Short:   "Replace or clear tags on an object",
		Example: "  pantech buckets objects tags uploads report.pdf --tag env=prod --tag owner=fin\n  pantech buckets objects tags uploads report.pdf --clear",
		Args:    cobra.ExactArgs(2),
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
			body := map[string]any{
				"key":  args[1],
				"tags": tags,
			}
			if version != "" {
				body["version_id"] = version
			}
			var out struct {
				Tags []api.StorageTag `json:"tags"`
			}
			res, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodPut,
				Path:   "/buckets/" + url.PathEscape(id) + "/objects/tags",
				Body:   body,
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
					a.out.Success("Cleared tags on %s.", args[1])
				} else {
					a.out.Success("Saved %s on %s.", count(len(out.Tags), "tag"), args[1])
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&rawTags, "tag", nil, "tag as key=value (repeatable)")
	f.BoolVar(&clear, "clear", false, "remove all tags from the object")
	f.StringVar(&version, "version", "", "specific object version id")
	return cmd
}

func newBucketObjectsLockCmd(a *app) *cobra.Command {
	var mode, retainUntil, legalHold, version string
	var bypassGovernance bool
	cmd := &cobra.Command{
		Use:   "lock <bucket> <key>",
		Short: "Set retention or legal hold on an object version",
		Example: `  pantech buckets objects lock backups archive.tar.gz --mode governance --retain-until 2027-01-01T00:00:00Z
  pantech buckets objects lock backups archive.tar.gz --legal-hold on`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{"key": args[1]}
			hasChange := false
			if version != "" {
				body["version_id"] = version
			}
			if mode != "" || retainUntil != "" {
				m := strings.ToUpper(strings.TrimSpace(mode))
				if m != "GOVERNANCE" && m != "COMPLIANCE" {
					return &usageError{fmt.Errorf("--mode must be governance or compliance (got %q)", mode), cmd}
				}
				if retainUntil == "" {
					return &usageError{errors.New("--retain-until (RFC3339 timestamp) is required with --mode"), cmd}
				}
				if _, err := time.Parse(time.RFC3339, retainUntil); err != nil {
					return &usageError{fmt.Errorf("--retain-until must be an RFC3339 timestamp (e.g. 2027-01-01T00:00:00Z): %w", err), cmd}
				}
				body["retention_mode"] = m
				body["retain_until"] = retainUntil
				if bypassGovernance {
					body["bypass_governance"] = true
				}
				hasChange = true
			}
			if legalHold != "" {
				switch strings.ToLower(strings.TrimSpace(legalHold)) {
				case "on", "true":
					body["legal_hold"] = true
				case "off", "false":
					body["legal_hold"] = false
				default:
					return &usageError{fmt.Errorf("--legal-hold must be on or off (got %q)", legalHold), cmd}
				}
				hasChange = true
			}
			if !hasChange {
				return &usageError{errors.New("pass --mode and --retain-until, or --legal-hold on|off"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := bucketID(cmd, c, args[0])
			if err != nil {
				return err
			}
			res, err := c.Do(ctx(cmd), api.Request{
				Method: http.MethodPut,
				Path:   "/buckets/" + url.PathEscape(id) + "/objects/lock",
				Body:   body,
			}, nil)
			if err != nil {
				return objectStorageError(api.AsFlags(err, map[string]string{
					"retention_mode": "--mode",
					"retain_until":   "--retain-until",
					"legal_hold":     "--legal-hold",
				}))
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%s", args[1])
			default:
				a.out.Success("Updated object lock on %s.", args[1])
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&mode, "mode", "", "retention mode: governance or compliance")
	f.StringVar(&retainUntil, "retain-until", "", "retention end time as RFC3339 (e.g. 2027-01-01T00:00:00Z)")
	f.StringVar(&legalHold, "legal-hold", "", "legal hold: on or off")
	f.BoolVar(&bypassGovernance, "bypass-governance", false, "bypass governance retention when shortening it")
	f.StringVar(&version, "version", "", "specific object version id")
	return cmd
}
