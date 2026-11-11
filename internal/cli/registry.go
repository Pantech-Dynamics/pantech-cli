package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

func newRegistryCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "registry",
		Aliases: []string{"cr"},
		Short:   "Your private container registry: namespace, repositories, tags, tokens, login",
		Long: `Pantech's private container registry, at registry.pantechdynamics.com.

  1. pantech registry namespace set acme       claim your organization's namespace
  2. pantech registry login                    log docker in with your API key
  3. docker push registry.pantechdynamics.com/acme/api/server:v1

Repositories are private by default and are created on first push (or with
pantech registry repos create). Give CI and clusters a registry token instead
of your API key: pantech registry tokens create --name ci --permission push.
Repositories are named by id (crr_…) or by name. Any member can read; owners
and admins make changes.`,
	}
	cmd.AddCommand(newRegistryNamespaceCmd(a), newRegistryReposCmd(a), newRegistryTagsCmd(a), newRegistryTokensCmd(a), newRegistryLoginCmd(a))
	return cmd
}

// registryError adds what to run next to the API's registry errors.
func registryError(err error) error {
	switch {
	case api.IsCode(err, "REGISTRY_UNAVAILABLE"):
		return fmt.Errorf("%w\nThe container registry is not available on this deployment", err)
	case api.IsCode(err, "REGISTRY_NAMESPACE_NOT_SET"):
		return fmt.Errorf("%w\nClaim one first: pantech registry namespace set <name>", err)
	case api.IsCode(err, "REGISTRY_NAMESPACE_LOCKED"):
		return fmt.Errorf("%w\nThe namespace cannot change once a repository has ever been created in it", err)
	case api.IsCode(err, "REPOSITORY_NOT_FOUND"):
		return fmt.Errorf("%w\nSee: pantech registry repos list", err)
	case api.IsCode(err, "REGISTRY_TOKEN_NOT_FOUND"):
		return fmt.Errorf("%w\nSee: pantech registry tokens list", err)
	case api.IsCode(err, "TAG_IMMUTABLE"):
		return fmt.Errorf("%w\nThe repository has immutable tags: turn them off with pantech registry repos update <repo> --immutable-tags=false", err)
	case api.IsCode(err, "REPOSITORY_LIMIT_REACHED"), api.IsCode(err, "REGISTRY_STORAGE_LIMIT_REACHED"):
		return fmt.Errorf("%w\nSee: pantech registry namespace", err)
	}
	return err
}

func bytesText(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// --- namespace ---

func namespaceDetail(ns *api.RegistryNamespace) output.Detail {
	host := ns.RegistryHost
	if host == "" {
		host = api.DefaultRegistryHost
	}
	return output.Detail{
		Title:    ns.Name,
		Subtitle: host + "/" + ns.Name,
		Sections: [][]output.Pair{
			{{"Create on push", onOff(ns.CreateOnPush)}},
			{{"Repositories", fmt.Sprintf("%d of %d", ns.RepositoryCount, ns.RepositoryLimit)}, {"Storage", bytesText(ns.StorageBytes) + " of " + bytesText(ns.StorageLimitBytes)}},
		},
		Next: [][2]string{{"Log in", "pantech registry login"}, {"Push", "docker push " + host + "/" + ns.Name + "/<repository>:<tag>"}},
	}
}

func (a *app) printNamespace(ns *api.RegistryNamespace, raw []byte) {
	switch {
	case a.out.JSON:
		a.out.RawJSON(raw)
	case a.out.Quiet:
		a.out.Line("%s", output.Clean(ns.Name))
	default:
		a.out.Print(namespaceDetail(ns))
	}
}

func newRegistryNamespaceCmd(a *app) *cobra.Command {
	get := func(cmd *cobra.Command, _ []string) error {
		c, err := a.client()
		if err != nil {
			return err
		}
		var ns api.RegistryNamespace
		res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: "/registry"}, &ns)
		if err != nil {
			return registryError(err)
		}
		a.printNamespace(&ns, res.Body)
		return nil
	}
	cmd := &cobra.Command{
		Use:     "namespace",
		Aliases: []string{"ns"},
		Short:   "Show or claim your organization's namespace",
		Args:    cobra.NoArgs,
		RunE:    get,
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "get",
		Short: "Show the namespace, its usage and limits",
		Args:  cobra.NoArgs,
		RunE:  get,
	})
	var createOnPush bool
	set := &cobra.Command{
		Use:   "set <name>",
		Short: "Claim the namespace, or change whether a push creates repositories",
		Long: `Claim your organization's namespace: 3 to 30 lower-case letters, digits and
single hyphens, starting with a letter, unique across Pantech. It cannot
change once a repository has ever been created in it. Run it again with the same name to change
--create-on-push.`,
		Example: "  pantech registry namespace set acme\n  pantech registry namespace set acme --create-on-push=false",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			body := map[string]any{"name": strings.TrimSpace(args[0])}
			if cmd.Flags().Changed("create-on-push") {
				body["create_on_push"] = createOnPush
			}
			var ns api.RegistryNamespace
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPut, Path: "/registry", Body: body}, &ns)
			if err != nil {
				return registryError(api.AsFlags(err, map[string]string{"name": "<name>", "create_on_push": "--create-on-push"}))
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Namespace %s is yours.", ns.Name)
			}
			a.printNamespace(&ns, res.Body)
			return nil
		},
	}
	set.Flags().BoolVar(&createOnPush, "create-on-push", true, "a push to a repository that does not exist creates it")
	cmd.AddCommand(set)
	return cmd
}

// --- repositories ---

func repositoryPath(id string) string { return "/registry/repositories/" + url.PathEscape(id) }

// repositoryID resolves a repository's id, its name (api/server), its
// namespace/name, or its full image name.
func repositoryID(cmd *cobra.Command, c *api.Client, ref string) (string, error) {
	if strings.HasPrefix(ref, "crr_") {
		return ref, nil
	}
	ref = strings.Trim(strings.TrimSpace(ref), "/")
	name := ref
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name = name[:i] // a tag after the name
	}
	repos, err := api.ListAll[api.RegistryRepository](ctx(cmd), c, "/registry/repositories", url.Values{"q": {lastComponent(name)}})
	if err != nil {
		return "", registryError(err)
	}
	var ids []string
	for _, r := range repos {
		if r.Status == "deleting" {
			continue
		}
		if r.Name == name || r.Namespace+"/"+r.Name == name || r.FullName == name {
			ids = append(ids, r.ID)
		}
	}
	switch len(ids) {
	case 0:
		return "", fmt.Errorf("no repository named %q\nSee: pantech registry repos list", ref)
	case 1:
		return ids[0], nil
	}
	return "", fmt.Errorf("%d repositories are named %q: use its id\n(%s)", len(ids), ref, strings.Join(ids, ", "))
}

func lastComponent(s string) string { return s[strings.LastIndex(s, "/")+1:] }

func retentionText(r *api.RegistryRetention) string {
	if r == nil {
		return "none"
	}
	var parts []string
	if r.KeepLast != nil {
		parts = append(parts, fmt.Sprintf("keep the newest %d tags", *r.KeepLast))
	}
	if r.UntaggedAfterDays != nil {
		parts = append(parts, fmt.Sprintf("delete untagged images after %d days", *r.UntaggedAfterDays))
	}
	if r.KeepPattern != nil && *r.KeepPattern != "" {
		parts = append(parts, "never remove "+*r.KeepPattern)
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, "; ")
}

func repositoryDetail(out *output.Printer, r *api.RegistryRepository) output.Detail {
	desc := r.Description
	if desc == "" {
		desc = "—"
	}
	d := output.Detail{
		Title:    r.FullName,
		State:    out.State(r.Status),
		Subtitle: r.ID,
		Sections: [][]output.Pair{
			{{"Visibility", r.Visibility}, {"Immutable tags", onOff(r.ImmutableTags)}, {"Retention", retentionText(r.Retention)}, {"Description", desc}},
			{{"Tags", strconv.Itoa(r.TagCount)}, {"Storage", bytesText(r.StorageBytes)}, {"Last push", output.When(r.LastPushedAt)}, {"Created", output.When(r.CreatedAt)}},
		},
		Next: [][2]string{{"Its tags", "pantech registry tags list " + r.ID}, {"Push", "docker push " + r.FullName + ":<tag>"}},
	}
	if r.SuspendedReason != nil {
		d.Sections = append([][]output.Pair{{{"Suspended", output.Or(r.SuspendedReason)}}}, d.Sections...)
	}
	return d
}

func (a *app) printRepository(r *api.RegistryRepository, raw []byte) {
	switch {
	case a.out.JSON:
		a.out.RawJSON(raw)
	case a.out.Quiet:
		a.out.Line("%s", output.Clean(r.ID))
	default:
		a.out.Print(repositoryDetail(a.out, r))
	}
}

func newRegistryReposCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "repos",
		Aliases: []string{"repositories", "repo"},
		Short:   "Repositories: list, create, show, change, delete",
	}
	cmd.AddCommand(newRegistryReposListCmd(a), newRegistryReposCreateCmd(a), newRegistryReposGetCmd(a), newRegistryReposUpdateCmd(a), newRegistryReposDeleteCmd(a))
	return cmd
}

func newRegistryReposListCmd(a *app) *cobra.Command {
	var q string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List repositories",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			query := url.Values{}
			if q != "" {
				query.Set("q", q)
			}
			repos, raw, err := listItems[api.RegistryRepository](cmd, c, "/registry/repositories", query)
			if err != nil {
				return registryError(err)
			}
			if printList(a, repos, raw, func(r api.RegistryRepository) string { return r.ID }, "No repositories yet.", "pantech registry repos create <name>") {
				return nil
			}
			rows := make([][]string, len(repos))
			for i := range repos {
				r := &repos[i]
				rows[i] = []string{a.out.Dim(r.ID), r.Name, r.Visibility, a.out.State(r.Status), strconv.Itoa(r.TagCount), bytesText(r.StorageBytes), output.Ago(r.LastPushedAt)}
			}
			a.out.Table([]string{"id", "name", "visibility", "status", "tags", "storage", "last push"}, rows)
			a.out.Summary(count(len(repos), "repository"))
			return nil
		},
	}
	cmd.Flags().StringVar(&q, "q", "", "only repositories whose name contains this text")
	return cmd
}

// repoFlags are the settings create and update share.
type repoFlags struct {
	description, visibility, keepPattern string
	immutable, noRetention               bool
	keepLast, untaggedDays               int
}

func addRepoFlags(cmd *cobra.Command, f *repoFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.description, "description", "", "a note for yourself")
	fl.StringVar(&f.visibility, "visibility", "", "private (anyone in your organization with a credential) or public (anyone can pull)")
	fl.BoolVar(&f.immutable, "immutable-tags", false, "a tag once pushed cannot be moved or deleted")
	fl.IntVar(&f.keepLast, "keep-last", 0, "retention: keep the newest N tags (1 to 1000)")
	fl.IntVar(&f.untaggedDays, "untagged-after-days", 0, "retention: delete untagged images older than this many days (1 to 365)")
	fl.StringVar(&f.keepPattern, "keep-pattern", "", `retention: never remove tags matching this glob, such as "v*"`)
}

var repoFlagNames = map[string]string{"name": "<name>", "description": "--description", "visibility": "--visibility", "immutable_tags": "--immutable-tags",
	"retention.keep_last": "--keep-last", "retention.untagged_after_days": "--untagged-after-days", "retention.keep_pattern": "--keep-pattern"}

func retentionChanged(cmd *cobra.Command) bool {
	fl := cmd.Flags()
	return fl.Changed("keep-last") || fl.Changed("untagged-after-days") || fl.Changed("keep-pattern")
}

// retention applies the retention flags to base, the current policy. A flag
// given as 0 or "" removes that rule.
func (f *repoFlags) retention(cmd *cobra.Command, base *api.RegistryRetention) *api.RegistryRetention {
	r := api.RegistryRetention{}
	if base != nil {
		r = *base
	}
	fl := cmd.Flags()
	if fl.Changed("keep-last") {
		r.KeepLast = nil
		if f.keepLast != 0 {
			r.KeepLast = &f.keepLast
		}
	}
	if fl.Changed("untagged-after-days") {
		r.UntaggedAfterDays = nil
		if f.untaggedDays != 0 {
			r.UntaggedAfterDays = &f.untaggedDays
		}
	}
	if fl.Changed("keep-pattern") {
		r.KeepPattern = nil
		if f.keepPattern != "" {
			r.KeepPattern = &f.keepPattern
		}
	}
	return &r
}

func (a *app) confirmPublic(cmd *cobra.Command, f *repoFlags, name string) error {
	if !cmd.Flags().Changed("visibility") {
		return nil
	}
	switch f.visibility {
	case "public":
		return a.confirm(fmt.Sprintf("Make %s public? Anyone on the internet can then pull its images without signing in.", name))
	case "private":
		return nil
	}
	return &usageError{errors.New("--visibility must be private or public"), cmd}
}

func newRegistryReposCreateCmd(a *app) *cobra.Command {
	var f repoFlags
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a repository (a first push creates one too)",
		Long: `Create a repository in your namespace. Its name is one or more lower-case
path components such as "api" or "api/server". It is private unless
--visibility public, which asks first. Retention removes old tags daily:
--keep-last keeps the newest N, --untagged-after-days removes untagged images,
and tags matching --keep-pattern are never removed.`,
		Example: `  pantech registry repos create api/server --keep-last 50 --keep-pattern "v*"
  pantech registry repos create base-images --immutable-tags`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.confirmPublic(cmd, &f, args[0]); err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			fl := cmd.Flags()
			body := map[string]any{"name": strings.Trim(strings.TrimSpace(args[0]), "/")}
			if fl.Changed("description") {
				body["description"] = f.description
			}
			if fl.Changed("visibility") {
				body["visibility"] = f.visibility
			}
			if fl.Changed("immutable-tags") {
				body["immutable_tags"] = f.immutable
			}
			if retentionChanged(cmd) {
				body["retention"] = f.retention(cmd, nil)
			}
			var r api.RegistryRepository
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: "/registry/repositories", Body: body}, &r)
			if err != nil {
				return registryError(api.AsFlags(err, repoFlagNames))
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Created %s (%s).", r.FullName, r.ID)
			}
			a.printRepository(&r, res.Body)
			return nil
		},
	}
	addRepoFlags(cmd, &f)
	return cmd
}

func newRegistryReposGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <repo>",
		Aliases: []string{"show"},
		Short:   "Show a repository",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := repositoryID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var r api.RegistryRepository
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: repositoryPath(id)}, &r)
			if err != nil {
				return registryError(err)
			}
			a.printRepository(&r, res.Body)
			return nil
		},
	}
}

func newRegistryReposUpdateCmd(a *app) *cobra.Command {
	var f repoFlags
	cmd := &cobra.Command{
		Use:   "update <repo>",
		Short: "Change a repository's description, visibility, tag immutability or retention",
		Long: `Change a repository. Only what you pass changes. A retention flag given as 0
(or --keep-pattern "") removes that rule; --no-retention removes them all.
--visibility public asks first.`,
		Example: `  pantech registry repos update api/server --keep-last 20
  pantech registry repos update api/server --visibility public`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fl := cmd.Flags()
			if f.noRetention && retentionChanged(cmd) {
				return &usageError{errors.New("pass --no-retention or retention flags, not both"), cmd}
			}
			body := map[string]any{}
			if fl.Changed("description") {
				body["description"] = f.description
			}
			if fl.Changed("visibility") {
				body["visibility"] = f.visibility
			}
			if fl.Changed("immutable-tags") {
				body["immutable_tags"] = f.immutable
			}
			if len(body) == 0 && !retentionChanged(cmd) && !f.noRetention {
				return &usageError{errors.New("nothing to change: pass --description, --visibility, --immutable-tags, a retention flag or --no-retention"), cmd}
			}
			if err := a.confirmPublic(cmd, &f, args[0]); err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := repositoryID(cmd, c, args[0])
			if err != nil {
				return err
			}
			switch {
			case f.noRetention:
				body["retention"] = api.RegistryRetention{}
			case retentionChanged(cmd):
				// The API replaces the whole policy: keep the rules not named.
				var cur api.RegistryRepository
				if _, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: repositoryPath(id)}, &cur); err != nil {
					return registryError(err)
				}
				body["retention"] = f.retention(cmd, cur.Retention)
			}
			var r api.RegistryRepository
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPatch, Path: repositoryPath(id), Body: body}, &r)
			if err != nil {
				return registryError(api.AsFlags(err, repoFlagNames))
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Updated %s.", r.FullName)
			}
			a.printRepository(&r, res.Body)
			return nil
		},
	}
	addRepoFlags(cmd, &f)
	cmd.Flags().BoolVar(&f.noRetention, "no-retention", false, "remove every retention rule")
	return cmd
}

func newRegistryReposDeleteCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <repo>",
		Aliases: []string{"rm"},
		Short:   "Delete a repository and every image in it",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := repositoryID(cmd, c, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Delete repository %s (%s) and every image in it? Pulls of its images stop working.", args[0], id)); err != nil {
				return err
			}
			var r api.RegistryRepository
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodDelete, Path: repositoryPath(id)}, &r)
			if err != nil {
				return registryError(err)
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%s", output.Clean(id))
			default:
				a.out.Success("Deleting %s.", args[0])
			}
			return nil
		},
	}
}

// --- tags ---

func tagPath(repoID, tag string) string {
	return repositoryPath(repoID) + "/tags/" + url.PathEscape(tag)
}

func newRegistryTagsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "tags",
		Aliases: []string{"tag"},
		Short:   "A repository's tags: list, show, delete",
	}
	cmd.AddCommand(newRegistryTagsListCmd(a), newRegistryTagsGetCmd(a), newRegistryTagsDeleteCmd(a))
	return cmd
}

func shortDigest(d string) string {
	if i := strings.IndexByte(d, ':'); i >= 0 && len(d) > i+13 {
		return d[:i+13]
	}
	return d
}

func newRegistryTagsListCmd(a *app) *cobra.Command {
	var q string
	cmd := &cobra.Command{
		Use:     "list <repo>",
		Aliases: []string{"ls"},
		Short:   "List a repository's tags, newest push first",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := repositoryID(cmd, c, args[0])
			if err != nil {
				return err
			}
			query := url.Values{}
			if q != "" {
				query.Set("q", q)
			}
			tags, raw, err := listItems[api.RegistryTag](cmd, c, repositoryPath(id)+"/tags", query)
			if err != nil {
				return registryError(err)
			}
			if printList(a, tags, raw, func(t api.RegistryTag) string { return t.Name }, "No tags yet.", "") {
				return nil
			}
			rows := make([][]string, len(tags))
			for i := range tags {
				t := &tags[i]
				rows[i] = []string{t.Name, a.out.Dim(shortDigest(t.Digest)), bytesText(t.SizeBytes), strings.Join(t.Platforms, ", "), output.Ago(t.PushedAt), output.Ago(t.LastPulledAt)}
			}
			a.out.Table([]string{"tag", "digest", "size", "platforms", "pushed", "last pull"}, rows)
			a.out.Summary(count(len(tags), "tag"))
			return nil
		},
	}
	cmd.Flags().StringVar(&q, "q", "", "only tags containing this text")
	return cmd
}

func newRegistryTagsGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <repo> <tag>",
		Aliases: []string{"show"},
		Short:   "Show a tag: its digest, size and platforms",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := repositoryID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var t api.RegistryTag
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: tagPath(id, args[1])}, &t)
			if err != nil {
				return registryError(err)
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
			case a.out.Quiet:
				a.out.Line("%s", output.Clean(t.Digest))
			default:
				a.out.Print(output.Detail{
					Title:    t.Name,
					Subtitle: t.Digest,
					Sections: [][]output.Pair{
						{{"Media type", t.MediaType}, {"Size", bytesText(t.SizeBytes)}, {"Immutable", onOff(t.Immutable)}},
						listPairs("Platforms", t.Platforms, "—"),
						{{"Pushed", output.When(t.PushedAt)}, {"Last pulled", output.When(t.LastPulledAt)}},
					},
				})
			}
			return nil
		},
	}
}

func newRegistryTagsDeleteCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <repo> <tag>",
		Aliases: []string{"rm"},
		Short:   "Delete a tag",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := repositoryID(cmd, c, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Delete tag %s of %s? Pulls of %s:%s stop working.", args[1], args[0], args[0], args[1])); err != nil {
				return err
			}
			if _, err := c.Do(ctx(cmd), api.Request{Method: http.MethodDelete, Path: tagPath(id, args[1])}, nil); err != nil {
				return registryError(err)
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Deleted tag %s of %s.", args[1], args[0])
			}
			return nil
		},
	}
}

// --- tokens ---

func newRegistryTokensCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "tokens",
		Aliases: []string{"token"},
		Short:   "Registry tokens for CI and clusters: list, create, revoke",
		Long: `Registry tokens log in to the registry only, never to the Pantech API: give
them to CI and clusters instead of an API key. A token can pull, or push (and
pull), every repository or only those you name.`,
	}
	cmd.AddCommand(newRegistryTokensListCmd(a), newRegistryTokensCreateCmd(a), newRegistryTokensRevokeCmd(a))
	return cmd
}

func tokenRepos(t *api.RegistryToken) string {
	if len(t.Repositories) == 0 {
		return "all"
	}
	return strings.Join(t.Repositories, ", ")
}

func newRegistryTokensListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List registry tokens (never their secrets)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			toks, raw, err := listItems[api.RegistryToken](cmd, c, "/registry/tokens", nil)
			if err != nil {
				return registryError(err)
			}
			if printList(a, toks, raw, func(t api.RegistryToken) string { return t.ID }, "No registry tokens yet.", "pantech registry tokens create --name ci --permission push") {
				return nil
			}
			rows := make([][]string, len(toks))
			for i := range toks {
				t := &toks[i]
				expires := output.When(t.ExpiresAt)
				if t.ExpiresAt == nil {
					expires = "never"
				}
				if t.RevokedAt != nil {
					expires = "revoked " + output.Ago(t.RevokedAt)
				}
				rows[i] = []string{a.out.Dim(t.ID), t.Name, t.Permission, tokenRepos(t), expires, output.Ago(t.LastUsedAt)}
			}
			a.out.Table([]string{"id", "name", "permission", "repositories", "expires", "last used"}, rows)
			a.out.Summary(count(len(toks), "token"))
			return nil
		},
	}
}

// parseExpiry reads --expires: a date, an RFC 3339 time, or a number of days
// from now such as 90d.
func parseExpiry(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if d, ok := strings.CutSuffix(s, "d"); ok {
		if n, err := strconv.Atoi(d); err == nil && n > 0 && n <= 3650 {
			return now.Add(time.Duration(n) * 24 * time.Hour).UTC().Truncate(time.Second), nil
		}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("--expires must be a number of days such as 90d, a date such as 2027-11-10, or an RFC 3339 time")
}

func newRegistryTokensCreateCmd(a *app) *cobra.Command {
	var name, permission, expires string
	var repos []string
	cmd := &cobra.Command{
		Use:   "create --name NAME --permission pull|push",
		Short: "Create a registry token; prints its secret once",
		Long: `Create a registry token. Its secret is printed once, alone on stdout, and is
never shown again: save it in your CI's secrets now. With --json it is the
"secret" field. Log in with it:

  echo "$TOKEN" | docker login registry.pantechdynamics.com -u pantech --password-stdin

or: pantech registry login --token-stdin < token.txt`,
		Example: `  pantech registry tokens create --name github-actions --permission push --repo api/server > token.txt
  pantech registry tokens create --name prod-cluster --permission pull --expires 365d`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if permission != "pull" && permission != "push" {
				return &usageError{errors.New("--permission must be pull or push"), cmd}
			}
			body := map[string]any{"name": name, "permission": permission}
			if len(repos) > 0 {
				body["repositories"] = repos
			}
			if expires != "" {
				t, err := parseExpiry(expires, time.Now())
				if err != nil {
					return &usageError{err, cmd}
				}
				body["expires_at"] = t.Format(time.RFC3339)
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var t api.RegistryToken
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: "/registry/tokens", Body: body}, &t)
			if err != nil {
				return registryError(api.AsFlags(err, map[string]string{"name": "--name", "permission": "--permission", "repositories": "--repo", "expires_at": "--expires"}))
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
				return nil
			case t.Secret == "":
				return fmt.Errorf("the API returned no secret for token %s: revoke it (pantech registry tokens revoke %s) and create another", t.ID, t.ID)
			}
			if !a.out.Quiet {
				a.out.Success("Created registry token %s (%s, %s).", t.Name, t.ID, t.Permission)
				a.out.Note("Its secret, shown once and never again. Save it now:")
			}
			a.out.Line("%s", t.Secret)
			if a.out.Quiet {
				a.out.Line("%s", output.Clean(t.ID))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "a name such as github-actions (required)")
	f.StringVar(&permission, "permission", "", "pull, or push (which includes pull) (required)")
	f.StringSliceVar(&repos, "repo", nil, "only this repository, such as api/server; repeat for more (default: all)")
	f.StringVar(&expires, "expires", "", "when it stops working: 90d, 2027-11-10 or an RFC 3339 time (default: never)")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("permission")
	return cmd
}

func newRegistryTokensRevokeCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "revoke <token id>",
		Aliases: []string{"delete", "rm"},
		Short:   "Revoke a registry token",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Revoke registry token %s? Logins with it stop within 5 minutes.", args[0])); err != nil {
				return err
			}
			if _, err := c.Do(ctx(cmd), api.Request{Method: http.MethodDelete, Path: "/registry/tokens/" + url.PathEscape(args[0])}, nil); err != nil {
				return registryError(err)
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Revoked %s.", args[0])
			}
			return nil
		},
	}
}

// --- login ---

// runEngine runs docker or podman with the secret on stdin; tests replace it.
var runEngine = func(ctx context.Context, path string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	return cmd.Run()
}

// registryHost is a host name with an optional port: nothing that docker
// could read as a flag or a path.
var registryHost = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*(:[0-9]{1,5})?$`)

var engineInstall = map[string]string{
	"docker": "Install Docker (https://docs.docker.com/get-docker/), or use podman: --engine podman",
	"podman": "Install Podman (https://podman.io/docs/installation), or use docker: --engine docker",
}

func newRegistryLoginCmd(a *app) *cobra.Command {
	var registry, engine, token string
	var tokenStdin bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log docker (or podman) in to the registry with your API key or a registry token",
		Long: `Run "docker login registry.pantechdynamics.com -u pantech --password-stdin",
giving it this CLI's API key on stdin, so the key never appears in a process
list or your shell history. --token-stdin reads a registry token (prt_…) from
stdin to log in with instead, as CI should; --token works too, but puts the
token in your shell history. --engine podman runs podman instead of docker.
docker keeps the credential in its own store (see "docker logout").`,
		Example: `  pantech registry login
  pantech registry login --token-stdin < token.txt
  pantech registry login --engine podman`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, ok := engineInstall[engine]; !ok {
				return &usageError{errors.New("--engine must be docker or podman"), cmd}
			}
			if !registryHost.MatchString(registry) {
				return &usageError{fmt.Errorf("--registry must be a host name such as %s", api.DefaultRegistryHost), cmd}
			}
			if tokenStdin && token != "" {
				return &usageError{errors.New("pass --token or --token-stdin, not both"), cmd}
			}
			secret := token
			if tokenStdin {
				line, err := bufio.NewReader(stdin).ReadString('\n')
				if err != nil && line == "" {
					return errors.New("no registry token on stdin")
				}
				secret = strings.TrimSpace(line)
				if secret == "" {
					return errors.New("no registry token on stdin")
				}
			}
			if secret == "" {
				c, err := a.client()
				if err != nil {
					return err
				}
				secret = c.Key
			}
			path, err := lookPath(engine)
			if err != nil {
				return fmt.Errorf("%s was not found on your PATH\n%s", engine, engineInstall[engine])
			}
			args := []string{"login", registry, "-u", "pantech", "--password-stdin"}
			// docker prints "Login Succeeded" and warnings; they go to stderr
			// so stdout stays for results.
			if err := runEngine(ctx(cmd), path, args, strings.NewReader(secret+"\n"), a.out.Err, a.out.Err); err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					return fmt.Errorf("%s login %s failed (exit status %d)\nCheck the credential: an API key needs the read scope to pull and write to push", engine, registry, exitErr.ExitCode())
				}
				return fmt.Errorf("running %s: %w", engine, err)
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("%s is logged in to %s.", engine, registry)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&registry, "registry", registryDefault(), "the registry's host (default: $PANTECH_REGISTRY, else "+api.DefaultRegistryHost+")")
	f.StringVar(&engine, "engine", "docker", "docker or podman")
	f.StringVar(&token, "token", "", "log in with this registry token instead of the API key (prefer --token-stdin)")
	f.BoolVar(&tokenStdin, "token-stdin", false, "read a registry token from stdin to log in with")
	return cmd
}

func registryDefault() string {
	if h := strings.TrimSpace(os.Getenv("PANTECH_REGISTRY")); h != "" {
		return h
	}
	return api.DefaultRegistryHost
}
