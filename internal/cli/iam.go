package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

func newIAMCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "iam",
		Short: "Teams and access policies: who in your organization may do what",
		Long: `Teams and access policies decide what each member of your organization may do.

  - Owners are allowed everything; no policy applies to them.
  - A deny statement that matches wins, for admins and members alike.
  - Admins are otherwise allowed everything.
  - Members get what their policies allow, attached to them or to their teams,
    plus every Read action while the member baseline is "read" (the default).

  pantech iam actions                              the actions policies are written in
  pantech iam policies create --file policy.json   a policy, attached to nobody yet
  pantech iam policies attach <policy> --team Platform
  pantech iam simulate --user ada@example.com --action instances:Delete --resource-name prod-api

Teams and policies are named by id (team_…, pol_…) or by name; members by id
(usr_…) or by email.`,
	}
	cmd.AddCommand(newIAMActionsCmd(a), newIAMMembersCmd(a), newIAMSettingsCmd(a), newIAMTeamsCmd(a), newIAMPoliciesCmd(a), newIAMAccessCmd(a), newIAMSimulateCmd(a))
	return cmd
}

// iamError adds what to run next to the API's IAM errors.
func iamError(err error) error {
	switch {
	case api.IsCode(err, "IAM_TEAM_NOT_FOUND"):
		return fmt.Errorf("%w\nSee: pantech iam teams list", err)
	case api.IsCode(err, "IAM_POLICY_NOT_FOUND"):
		return fmt.Errorf("%w\nSee: pantech iam policies list", err)
	case api.IsCode(err, "IAM_MANAGED_POLICY"):
		return fmt.Errorf("%w\nPantech's policies cannot change: copy it into your own with pantech iam policies get <policy> --json", err)
	case api.IsCode(err, "IAM_ADMIN_TARGET"):
		return fmt.Errorf("%w\nOnly an owner can do this", err)
	case api.IsCode(err, "VERSION_CONFLICT"):
		return fmt.Errorf("%w\nSomeone changed the policy since it was read: read it again with pantech iam policies get", err)
	case api.IsCode(err, "INVALID_POLICY"):
		return fmt.Errorf("%w\nSee the actions: pantech iam actions", err)
	}
	return err
}

func joinOr(items []string, empty string) string {
	if len(items) == 0 {
		return empty
	}
	return strings.Join(items, ", ")
}

// --- resolving names ---

func iamTeamPath(id string) string   { return "/iam/teams/" + url.PathEscape(id) }
func iamPolicyPath(id string) string { return "/iam/policies/" + url.PathEscape(id) }

// teamID resolves a team's id or its name.
func teamID(cmd *cobra.Command, c *api.Client, ref string) (string, error) {
	if strings.HasPrefix(ref, "team_") {
		return ref, nil
	}
	teams, err := api.ListAll[api.IAMTeam](ctx(cmd), c, "/iam/teams", nil)
	if err != nil {
		return "", iamError(err)
	}
	return pickOne("team", ref, "pantech iam teams list", func(yield func(id, name string)) {
		for _, t := range teams {
			yield(t.ID, t.Name)
		}
	})
}

// policyID resolves a policy's id or its name.
func policyID(cmd *cobra.Command, c *api.Client, ref string) (string, error) {
	if strings.HasPrefix(ref, "pol_") {
		return ref, nil
	}
	policies, err := api.ListAll[api.IAMPolicy](ctx(cmd), c, "/iam/policies", nil)
	if err != nil {
		return "", iamError(err)
	}
	return pickOne("policy", ref, "pantech iam policies list", func(yield func(id, name string)) {
		for _, p := range policies {
			yield(p.ID, p.Name)
		}
	})
}

func pickOne(noun, ref, listCmd string, each func(yield func(id, name string))) (string, error) {
	var ids []string
	each(func(id, name string) {
		if name == ref {
			ids = append(ids, id)
		}
	})
	switch len(ids) {
	case 0:
		return "", fmt.Errorf("no %s named %q\nSee: %s", noun, ref, listCmd)
	case 1:
		return ids[0], nil
	}
	return "", fmt.Errorf("%d %ss are named %q: use its id\n(%s)", len(ids), noun, ref, strings.Join(ids, ", "))
}

// userID resolves a member's id or email: an email must match exactly one
// member, ignoring case.
func userID(cmd *cobra.Command, c *api.Client, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "usr_") {
		return ref, nil
	}
	if !strings.Contains(ref, "@") {
		return "", fmt.Errorf("%q is neither a member id (usr_…) nor an email", ref)
	}
	members, err := api.ListAll[api.IAMMember](ctx(cmd), c, "/iam/members", url.Values{"q": {ref}})
	if err != nil {
		return "", iamError(err)
	}
	var ids []string
	for _, m := range members {
		if strings.EqualFold(m.Email, ref) {
			ids = append(ids, m.UserID)
		}
	}
	switch len(ids) {
	case 0:
		return "", fmt.Errorf("no member has the email %s\nSee: pantech iam members", ref)
	case 1:
		return ids[0], nil
	}
	return "", fmt.Errorf("%d members have the email %s: use their id\n(%s)", len(ids), ref, strings.Join(ids, ", "))
}

// --- actions ---

func newIAMActionsCmd(a *app) *cobra.Command {
	var service string
	cmd := &cobra.Command{
		Use:     "actions",
		Short:   "List the actions policies are written in",
		Example: "  pantech iam actions\n  pantech iam actions --service instances",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			actions, raw, err := listItems[api.IAMAction](cmd, c, "/iam/actions", nil)
			if err != nil {
				return iamError(err)
			}
			if service != "" {
				var keptA []api.IAMAction
				var keptR []json.RawMessage
				for i, x := range actions {
					if x.Service == service {
						keptA, keptR = append(keptA, x), append(keptR, raw[i])
					}
				}
				actions, raw = keptA, keptR
			}
			if printList(a, actions, raw, func(x api.IAMAction) string { return x.Action }, "No actions.", "") {
				return nil
			}
			rows := make([][]string, len(actions))
			for i, x := range actions {
				read := ""
				if x.Read {
					read = "read"
				}
				rows[i] = []string{x.Action, joinOr(x.ResourceTypes, "—"), read, x.Description}
			}
			a.out.Table([]string{"action", "resources", "baseline", "description"}, rows)
			a.out.Summary(count(len(actions), "action"))
			return nil
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "only this service's actions, such as instances or dns")
	return cmd
}

// --- members ---

func newIAMMembersCmd(a *app) *cobra.Command {
	var q string
	cmd := &cobra.Command{
		Use:     "members",
		Short:   "List your organization's members, their roles and teams",
		Example: "  pantech iam members\n  pantech iam members --q ada",
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
			members, raw, err := listItems[api.IAMMember](cmd, c, "/iam/members", query)
			if err != nil {
				return iamError(err)
			}
			if printList(a, members, raw, func(m api.IAMMember) string { return m.UserID }, "No members match.", "") {
				return nil
			}
			rows := make([][]string, len(members))
			for i, m := range members {
				rows[i] = []string{a.out.Dim(m.UserID), m.Email, m.Name, m.Role, joinOr(m.TeamIDs, "—"), strconv.Itoa(m.PolicyCount)}
			}
			a.out.Table([]string{"id", "email", "name", "role", "teams", "policies"}, rows)
			a.out.Summary(count(len(members), "member"))
			return nil
		},
	}
	cmd.Flags().StringVar(&q, "q", "", "only members whose email or name contains this text")
	return cmd
}

// --- settings ---

func (a *app) printSettings(s *api.IAMSettings, raw []byte) {
	switch {
	case a.out.JSON:
		a.out.RawJSON(raw)
	case a.out.Quiet:
		a.out.Line("%s", output.Clean(s.MemberBaseline))
	default:
		what := "members may read everything; policies add more"
		if s.MemberBaseline == "none" {
			what = "members may do only what their policies allow"
		}
		a.out.Print(output.Detail{
			Title:    "Access setting",
			Sections: [][]output.Pair{{{"Member baseline", s.MemberBaseline + " (" + what + ")"}, {"Changed", output.When(s.UpdatedAt)}}},
		})
	}
}

func newIAMSettingsCmd(a *app) *cobra.Command {
	get := func(cmd *cobra.Command, _ []string) error {
		c, err := a.client()
		if err != nil {
			return err
		}
		var s api.IAMSettings
		res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: "/iam/settings"}, &s)
		if err != nil {
			return iamError(err)
		}
		a.printSettings(&s, res.Body)
		return nil
	}
	cmd := &cobra.Command{
		Use:   "settings",
		Short: "Show or change the member baseline",
		Args:  cobra.NoArgs,
		RunE:  get,
	}
	cmd.AddCommand(&cobra.Command{Use: "get", Short: "Show the member baseline", Args: cobra.NoArgs, RunE: get})
	var baseline string
	set := &cobra.Command{
		Use:   "set --member-baseline read|none",
		Short: "Change what members may do without a policy (owners only)",
		Long: `--member-baseline read (the default) lets every member read everything, and
policies add more. none lets members do only what their policies allow: it
takes away the read access of every member without a policy granting it, so
it asks first. Owners are emailed when it changes.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch baseline {
			case "read":
			case "none":
				if err := a.confirm("Set the member baseline to none? Every member loses read access to everything their policies do not grant, at once."); err != nil {
					return err
				}
			default:
				return &usageError{errors.New("--member-baseline must be read or none"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var s api.IAMSettings
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPut, Path: "/iam/settings", Body: map[string]any{"member_baseline": baseline}}, &s)
			if err != nil {
				return iamError(api.AsFlags(err, map[string]string{"member_baseline": "--member-baseline"}))
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("The member baseline is %s.", s.MemberBaseline)
			}
			a.printSettings(&s, res.Body)
			return nil
		},
	}
	set.Flags().StringVar(&baseline, "member-baseline", "", "read or none (required)")
	_ = set.MarkFlagRequired("member-baseline")
	cmd.AddCommand(set)
	return cmd
}

// --- teams ---

func (a *app) printTeam(t *api.IAMTeam, raw []byte) {
	switch {
	case a.out.JSON:
		a.out.RawJSON(raw)
	case a.out.Quiet:
		a.out.Line("%s", output.Clean(t.ID))
	default:
		desc := t.Description
		if desc == "" {
			desc = "—"
		}
		a.out.Print(output.Detail{
			Title:    t.Name,
			Subtitle: t.ID,
			Sections: [][]output.Pair{
				{{"Members", strconv.Itoa(t.MemberCount)}, {"Description", desc}, {"Created", output.When(t.CreatedAt)}},
				listPairs("Policies", t.PolicyIDs, "— (attach one: pantech iam policies attach <policy> --team "+t.ID+")"),
			},
			Next: [][2]string{{"Its members", "pantech iam teams members " + t.ID}},
		})
	}
}

func newIAMTeamsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "teams",
		Aliases: []string{"team"},
		Short:   "Teams: list, create, show, change, delete, and their members",
	}
	cmd.AddCommand(newIAMTeamsListCmd(a), newIAMTeamsCreateCmd(a), newIAMTeamsGetCmd(a), newIAMTeamsUpdateCmd(a), newIAMTeamsDeleteCmd(a), newIAMTeamMembersCmd(a))
	return cmd
}

func newIAMTeamsListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List teams",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			teams, raw, err := listItems[api.IAMTeam](cmd, c, "/iam/teams", nil)
			if err != nil {
				return iamError(err)
			}
			if printList(a, teams, raw, func(t api.IAMTeam) string { return t.ID }, "No teams yet.", `pantech iam teams create "Platform"`) {
				return nil
			}
			rows := make([][]string, len(teams))
			for i, t := range teams {
				rows[i] = []string{a.out.Dim(t.ID), t.Name, strconv.Itoa(t.MemberCount), joinOr(t.PolicyIDs, "—")}
			}
			a.out.Table([]string{"id", "name", "members", "policies"}, rows)
			a.out.Summary(count(len(teams), "team"))
			return nil
		},
	}
}

func newIAMTeamsCreateCmd(a *app) *cobra.Command {
	var description string
	cmd := &cobra.Command{
		Use:     "create <name>",
		Short:   "Create a team",
		Example: `  pantech iam teams create Platform --description "Runs the clusters"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			body := map[string]any{"name": args[0]}
			if cmd.Flags().Changed("description") {
				body["description"] = description
			}
			var t api.IAMTeam
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: "/iam/teams", Body: body}, &t)
			if err != nil {
				return iamError(api.AsFlags(err, map[string]string{"name": "<name>", "description": "--description"}))
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Created team %s (%s).", t.Name, t.ID)
			}
			a.printTeam(&t, res.Body)
			return nil
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "what the team is for")
	return cmd
}

func newIAMTeamsGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <team>",
		Aliases: []string{"show"},
		Short:   "Show a team",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := teamID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var t api.IAMTeam
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: iamTeamPath(id)}, &t)
			if err != nil {
				return iamError(err)
			}
			a.printTeam(&t, res.Body)
			return nil
		},
	}
}

func newIAMTeamsUpdateCmd(a *app) *cobra.Command {
	var name, description string
	cmd := &cobra.Command{
		Use:   "update <team> [--name NAME] [--description TEXT]",
		Short: "Rename a team or change its description",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			if cmd.Flags().Changed("name") {
				body["name"] = name
			}
			if cmd.Flags().Changed("description") {
				body["description"] = description
			}
			if len(body) == 0 {
				return &usageError{errors.New("nothing to change: pass --name or --description"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := teamID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var t api.IAMTeam
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPatch, Path: iamTeamPath(id), Body: body}, &t)
			if err != nil {
				return iamError(api.AsFlags(err, map[string]string{"name": "--name", "description": "--description"}))
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Updated team %s.", t.Name)
			}
			a.printTeam(&t, res.Body)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "a new name")
	cmd.Flags().StringVar(&description, "description", "", `a new description ("" clears it)`)
	return cmd
}

func newIAMTeamsDeleteCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <team>",
		Aliases: []string{"rm"},
		Short:   "Delete a team; its members lose the policies attached to it",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := teamID(cmd, c, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Delete team %s (%s)? Its members lose every policy attached to it, including any deny.", args[0], id)); err != nil {
				return err
			}
			if _, err := c.Do(ctx(cmd), api.Request{Method: http.MethodDelete, Path: iamTeamPath(id)}, nil); err != nil {
				return iamError(err)
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Deleted team %s.", args[0])
			}
			return nil
		},
	}
}

func newIAMTeamMembersCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "members <team> [add|remove <user id or email>]",
		Short: "List a team's members, or add or remove one",
		Long: `List a team's members, or add or remove one, named by member id (usr_…) or by
email. A member in a team gets every policy attached to the team, so adding
and removing ask first (--yes to skip). Admins can add and remove members
only; owners can add admins too.`,
		Example: `  pantech iam teams members Platform
  pantech iam teams members Platform add usr_01JD…
  pantech iam teams members Platform remove ada@example.com`,
		Args: func(cmd *cobra.Command, args []string) error {
			switch {
			case len(args) == 1:
				return nil
			case len(args) == 3 && (args[1] == "add" || args[1] == "remove"):
				return nil
			}
			return errors.New(`usage: pantech iam teams members <team> [add|remove <user id or email>]`)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			tid, err := teamID(cmd, c, args[0])
			if err != nil {
				return err
			}
			if len(args) == 1 {
				members, raw, err := listItems[api.IAMTeamMember](cmd, c, iamTeamPath(tid)+"/members", nil)
				if err != nil {
					return iamError(err)
				}
				if printList(a, members, raw, func(m api.IAMTeamMember) string { return m.UserID }, "No members in this team yet.", "pantech iam teams members "+tid+" add <user id or email>") {
					return nil
				}
				rows := make([][]string, len(members))
				for i, m := range members {
					rows[i] = []string{a.out.Dim(m.UserID), m.Email, m.Name, m.Role, output.Ago(m.AddedAt)}
				}
				a.out.Table([]string{"id", "email", "name", "role", "added"}, rows)
				a.out.Summary(count(len(members), "member"))
				return nil
			}
			uid, err := userID(cmd, c, args[2])
			if err != nil {
				return err
			}
			path := iamTeamPath(tid) + "/members/" + url.PathEscape(uid)
			question := fmt.Sprintf("Add %s to team %s? They get every policy attached to the team within seconds.", args[2], args[0])
			if args[1] == "remove" {
				question = fmt.Sprintf("Remove %s from team %s? They lose the team's policies, and any deny in them stops applying to them.", args[2], args[0])
			}
			if err := a.confirm(question); err != nil {
				return err
			}
			if args[1] == "add" {
				var m api.IAMTeamMember
				res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPut, Path: path}, &m)
				if err != nil {
					return iamError(err)
				}
				switch {
				case a.out.JSON:
					a.out.RawJSON(res.Body)
				case a.out.Quiet:
					a.out.Line("%s", output.Clean(m.UserID))
				default:
					a.out.Success("Added %s to %s.", m.Email, args[0])
				}
				return nil
			}
			if _, err := c.Do(ctx(cmd), api.Request{Method: http.MethodDelete, Path: path}, nil); err != nil {
				return iamError(err)
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Removed %s from %s.", args[2], args[0])
			}
			return nil
		},
	}
}

// --- policies ---

const maxPolicyFile = 256 << 10

// readPolicyFile reads a policy document: an object with name, description
// and statements, or a bare array of statements.
func readPolicyFile(path string) (*api.PolicyDocument, error) {
	var r io.Reader = stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, maxPolicyFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPolicyFile {
		return nil, fmt.Errorf("%s is larger than %d KiB", path, maxPolicyFile>>10)
	}
	data = bytes.TrimSpace(data)
	var doc api.PolicyDocument
	if len(data) > 0 && data[0] == '[' {
		err = json.Unmarshal(data, &doc.Statements)
	} else {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		err = dec.Decode(&doc)
	}
	if err != nil {
		return nil, fmt.Errorf("%s is not a policy: want {\"name\": …, \"statements\": [{\"effect\": \"allow\", \"actions\": […], \"resources\": […]}]} or an array of statements (%v)", path, err)
	}
	if len(doc.Statements) == 0 {
		return nil, fmt.Errorf("%s has no statements", path)
	}
	return &doc, nil
}

func statementRows(statements []api.PolicyStatement) [][]string {
	rows := make([][]string, len(statements))
	for i, s := range statements {
		var cond []string
		if c := s.Conditions; c != nil {
			if len(c.SourceIP) > 0 {
				cond = append(cond, "from "+strings.Join(c.SourceIP, ", "))
			}
			if c.MFA != nil {
				if *c.MFA {
					cond = append(cond, "with MFA")
				} else {
					cond = append(cond, "without MFA (API keys too)")
				}
			}
		}
		sid := s.Sid
		if sid == "" {
			sid = strconv.Itoa(i + 1)
		}
		rows[i] = []string{sid, s.Effect, strings.Join(s.Actions, ", "), strings.Join(s.Resources, ", "), joinOr(cond, "—")}
	}
	return rows
}

func (a *app) printPolicy(p *api.IAMPolicy, raw []byte) {
	switch {
	case a.out.JSON:
		a.out.RawJSON(raw)
	case a.out.Quiet:
		a.out.Line("%s", output.Clean(p.ID))
	default:
		desc := p.Description
		if desc == "" {
			desc = "—"
		}
		kind := "yours"
		if p.Managed {
			kind = "Pantech's (cannot be changed)"
		}
		a.out.Print(output.Detail{
			Title:    p.Name,
			Subtitle: p.ID,
			Sections: [][]output.Pair{{{"Description", desc}, {"Kind", kind}, {"Attached to", count(p.AttachmentCount, "principal")}, {"Version", strconv.Itoa(p.Version)}}},
		})
		a.out.Table([]string{"sid", "effect", "actions", "resources", "conditions"}, statementRows(p.Statements))
	}
}

func newIAMPoliciesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "policies",
		Aliases: []string{"policy"},
		Short:   "Policies: list, create, show, change, delete, attach and detach",
		Long: `A policy is 1 to 20 statements. Each allows or denies actions (pantech iam
actions; "instances:*" and "*:Read" work) on resources: "*", a resource id
(vm_…), or "name:<glob>" matched against the resource's name. Conditions limit
a statement to client addresses (source_ip) or to sessions signed in with MFA.

  {
    "name": "staging-operators",
    "statements": [
      {"sid": "RestartStaging", "effect": "allow",
       "actions": ["instances:Read", "instances:Reboot", "instances:Resize"],
       "resources": ["name:staging-*"],
       "conditions": {"source_ip": ["102.89.0.0/16"]}},
      {"sid": "NoProdDeletes", "effect": "deny",
       "actions": ["instances:Delete"], "resources": ["name:prod-*"]}
    ]
  }

Pantech's managed policies (pol_pantech_…) can be attached but not changed.`,
	}
	cmd.AddCommand(newIAMPoliciesListCmd(a), newIAMPoliciesCreateCmd(a), newIAMPoliciesGetCmd(a), newIAMPoliciesUpdateCmd(a), newIAMPoliciesDeleteCmd(a),
		newIAMPoliciesAttachmentsCmd(a), newIAMAttachCmd(a, true), newIAMAttachCmd(a, false))
	return cmd
}

func newIAMPoliciesListCmd(a *app) *cobra.Command {
	var managed, custom bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List policies, yours and Pantech's",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if managed && custom {
				return &usageError{errors.New("pass --managed or --custom, not both"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			q := url.Values{}
			if managed {
				q.Set("managed", "true")
			}
			if custom {
				q.Set("managed", "false")
			}
			policies, raw, err := listItems[api.IAMPolicy](cmd, c, "/iam/policies", q)
			if err != nil {
				return iamError(err)
			}
			if printList(a, policies, raw, func(p api.IAMPolicy) string { return p.ID }, "No policies.", "pantech iam policies create --file policy.json") {
				return nil
			}
			rows := make([][]string, len(policies))
			for i, p := range policies {
				kind := "yours"
				if p.Managed {
					kind = "Pantech"
				}
				rows[i] = []string{a.out.Dim(p.ID), p.Name, kind, strconv.Itoa(len(p.Statements)), strconv.Itoa(p.AttachmentCount)}
			}
			a.out.Table([]string{"id", "name", "kind", "statements", "attached"}, rows)
			a.out.Summary(count(len(policies), "policy"))
			return nil
		},
	}
	cmd.Flags().BoolVar(&managed, "managed", false, "only Pantech's policies")
	cmd.Flags().BoolVar(&custom, "custom", false, "only your organization's policies")
	return cmd
}

var policyFlagNames = map[string]string{"name": "--name", "description": "--description"}

func newIAMPoliciesCreateCmd(a *app) *cobra.Command {
	var file, name, description string
	cmd := &cobra.Command{
		Use:   "create --file policy.json [--name NAME]",
		Short: "Create a policy from a JSON file; it applies to nobody until attached",
		Long: `Create a policy from a JSON file ("-" for stdin): an object with name,
description and statements, or a bare array of statements with --name. --name
and --description replace the file's. See pantech iam policies --help for the
format.`,
		Example: `  pantech iam policies create --file staging-operators.json
  pantech iam policies get pol_pantech_developer --json | jq '.statements' | pantech iam policies create --file - --name my-developer`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			doc, err := readPolicyFile(file)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("name") {
				doc.Name = name
			}
			if cmd.Flags().Changed("description") {
				doc.Description = &description
			}
			if doc.Name == "" {
				return &usageError{errors.New(`the policy needs a name: put "name" in the file or pass --name`), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var p api.IAMPolicy
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: "/iam/policies", Body: doc}, &p)
			if err != nil {
				return iamError(api.AsFlags(err, policyFlagNames))
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Created policy %s (%s). It applies to nobody until attached.", p.Name, p.ID)
				a.out.Next("Attach it", "pantech iam policies attach "+p.ID+" --team <team> | --member <user id or email>")
			}
			a.printPolicy(&p, res.Body)
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", `the policy JSON ("-" for stdin) (required)`)
	cmd.Flags().StringVar(&name, "name", "", "the policy's name")
	cmd.Flags().StringVar(&description, "description", "", "what it is for")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func newIAMPoliciesGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <policy>",
		Aliases: []string{"show"},
		Short:   "Show a policy and its statements (--json for the document)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := policyID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var p api.IAMPolicy
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: iamPolicyPath(id)}, &p)
			if err != nil {
				return iamError(err)
			}
			a.printPolicy(&p, res.Body)
			return nil
		},
	}
}

func newIAMPoliciesUpdateCmd(a *app) *cobra.Command {
	var file, name, description string
	var version int
	cmd := &cobra.Command{
		Use:   "update <policy> --file policy.json",
		Short: "Replace a policy's statements (and name or description) from a JSON file",
		Long: `Replace a policy from a JSON file, shaped as for create. A name or
description the file and flags leave out is kept. The change applies only if
the policy is still the version read just before (or --if-match VERSION), so
a change someone made meanwhile is never overwritten. It applies to everyone
the policy is attached to within seconds, so it asks first.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := readPolicyFile(file)
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := policyID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var cur api.IAMPolicy
			if _, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: iamPolicyPath(id)}, &cur); err != nil {
				return iamError(err)
			}
			if cur.Managed {
				return fmt.Errorf("%s is one of Pantech's policies and cannot be changed\nCreate your own from it: pantech iam policies get %s --json", cur.Name, cur.ID)
			}
			if cmd.Flags().Changed("name") {
				doc.Name = name
			}
			if doc.Name == "" {
				doc.Name = cur.Name
			}
			if cmd.Flags().Changed("description") {
				doc.Description = &description
			}
			if doc.Description == nil {
				doc.Description = &cur.Description
			}
			if !cmd.Flags().Changed("if-match") {
				version = cur.Version
			}
			if err := a.confirm(fmt.Sprintf("Replace policy %s (%s)? It is attached to %s, and the change applies to them within seconds.", cur.Name, cur.ID, count(cur.AttachmentCount, "principal"))); err != nil {
				return err
			}
			var p api.IAMPolicy
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPut, Path: iamPolicyPath(id), Body: doc, Header: http.Header{"If-Match": {strconv.Itoa(version)}}}, &p)
			if err != nil {
				return iamError(api.AsFlags(err, policyFlagNames))
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Updated policy %s (version %d).", p.Name, p.Version)
			}
			a.printPolicy(&p, res.Body)
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", `the policy JSON ("-" for stdin) (required)`)
	cmd.Flags().StringVar(&name, "name", "", "a new name")
	cmd.Flags().StringVar(&description, "description", "", "a new description")
	cmd.Flags().IntVar(&version, "if-match", 0, "replace it only if its version is still this one (default: the version just read)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func newIAMPoliciesDeleteCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <policy>",
		Aliases: []string{"rm"},
		Short:   "Delete a policy and detach it everywhere",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := policyID(cmd, c, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Delete policy %s (%s)? It is detached from every member and team, including any deny it holds.", args[0], id)); err != nil {
				return err
			}
			if _, err := c.Do(ctx(cmd), api.Request{Method: http.MethodDelete, Path: iamPolicyPath(id)}, nil); err != nil {
				return iamError(err)
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Deleted policy %s.", args[0])
			}
			return nil
		},
	}
}

func newIAMPoliciesAttachmentsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "attachments <policy>",
		Short: "List the members and teams a policy is attached to",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := policyID(cmd, c, args[0])
			if err != nil {
				return err
			}
			atts, raw, err := listItems[api.IAMAttachment](cmd, c, iamPolicyPath(id)+"/attachments", nil)
			if err != nil {
				return iamError(err)
			}
			if printList(a, atts, raw, func(x api.IAMAttachment) string { return x.PrincipalID }, "Attached to nobody.", "pantech iam policies attach "+id+" --team <team> | --member <user id or email>") {
				return nil
			}
			rows := make([][]string, len(atts))
			for i, x := range atts {
				rows[i] = []string{x.PrincipalType, x.PrincipalID, output.Ago(x.AttachedAt)}
			}
			a.out.Table([]string{"type", "id", "attached"}, rows)
			return nil
		},
	}
}

func newIAMAttachCmd(a *app, attach bool) *cobra.Command {
	var member, team string
	verb, Verb, prep := "detach", "Detach", "from"
	if attach {
		verb, Verb, prep = "attach", "Attach", "to"
	}
	cmd := &cobra.Command{
		Use:   verb + " <policy> --member <user id or email> | --team <team>",
		Short: Verb + " a policy " + prep + " a member or a team",
		Long: Verb + ` a policy ` + prep + ` a member (by id or email) or a team (by id or name). It
changes what they may do within seconds, so it asks first. Only owners can
` + verb + ` a policy for an admin or a team containing one.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (member == "") == (team == "") {
				return &usageError{errors.New("pass --member or --team"), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			pid, err := policyID(cmd, c, args[0])
			if err != nil {
				return err
			}
			kind, ref := "member", member
			var principal string
			if team != "" {
				kind, ref = "team", team
				principal, err = teamID(cmd, c, team)
			} else {
				principal, err = userID(cmd, c, member)
			}
			if err != nil {
				return err
			}
			question := fmt.Sprintf("Attach policy %s to %s %s? What it allows or denies applies to them within seconds.", args[0], kind, ref)
			if !attach {
				question = fmt.Sprintf("Detach policy %s from %s %s? They lose what it allows, and any deny in it stops applying to them.", args[0], kind, ref)
			}
			if err := a.confirm(question); err != nil {
				return err
			}
			if attach {
				var att api.IAMAttachment
				res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: iamPolicyPath(pid) + "/attachments", Body: map[string]any{"principal_type": kind, "principal_id": principal}}, &att)
				if err != nil {
					return iamError(err)
				}
				switch {
				case a.out.JSON:
					a.out.RawJSON(res.Body)
				case a.out.Quiet:
					a.out.Line("%s", output.Clean(att.PrincipalID))
				default:
					a.out.Success("Attached %s to %s %s.", args[0], kind, ref)
				}
				return nil
			}
			path := iamPolicyPath(pid) + "/attachments/" + kind + "/" + url.PathEscape(principal)
			if _, err := c.Do(ctx(cmd), api.Request{Method: http.MethodDelete, Path: path}, nil); err != nil {
				return iamError(err)
			}
			if !a.out.JSON && !a.out.Quiet {
				a.out.Success("Detached %s from %s %s.", args[0], kind, ref)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&member, "member", "", "a member's id (usr_…) or email")
	cmd.Flags().StringVar(&team, "team", "", "a team's id (team_…) or name")
	return cmd
}

// --- effective access and the simulator ---

func newIAMAccessCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "access <user id or email>",
		Short:   "Show what applies to a member: role, teams, policies, baseline",
		Example: "  pantech iam access ada@example.com",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			uid, err := userID(cmd, c, args[0])
			if err != nil {
				return err
			}
			var e api.IAMEffectiveAccess
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: "/iam/members/" + url.PathEscape(uid) + "/effective-access"}, &e)
			if err != nil {
				return iamError(err)
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
				return nil
			case a.out.Quiet:
				for _, p := range e.Policies {
					a.out.Line("%s", output.Clean(p.PolicyID))
				}
				return nil
			}
			summary := map[string]string{
				"owner":  "allowed everything; no policy applies",
				"admin":  "allowed everything except what a deny statement refuses",
				"member": "allowed what the policies below allow",
			}[e.Role]
			if e.Role == "member" && e.MemberBaseline == "read" {
				summary += ", plus every Read action"
			}
			policies := make([]string, len(e.Policies))
			for i, p := range e.Policies {
				policies[i] = p.PolicyID
				if p.TeamID != nil {
					policies[i] += " (through " + *p.TeamID + ")"
				} else {
					policies[i] += " (attached directly)"
				}
			}
			sort.Strings(policies)
			a.out.Print(output.Detail{
				Title:    args[0],
				Subtitle: e.UserID,
				Sections: [][]output.Pair{
					{{"Role", e.Role}, {"Access", summary}, {"Member baseline", e.MemberBaseline}},
					listPairs("Teams", e.Teams, "—"),
					listPairs("Policies", policies, "—"),
				},
				Next: [][2]string{{"Check one action", "pantech iam simulate --user " + e.UserID + " --action <service:Verb>"}},
			})
			return nil
		},
	}
}

func newIAMSimulateCmd(a *app) *cobra.Command {
	var user, action, resource, resourceName, ip string
	var mfa bool
	cmd := &cobra.Command{
		Use:   "simulate --user <user id or email> --action <service:Verb>",
		Short: "Check whether a member would be allowed an action, and why; changes nothing",
		Example: `  pantech iam simulate --user ada@example.com --action instances:Delete --resource-name prod-api
  pantech iam simulate --user usr_01JD… --action dns:ChangeRecords --resource dnsz_01JD… --ip 102.89.1.2 --mfa`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			uid, err := userID(cmd, c, user)
			if err != nil {
				return err
			}
			body := map[string]any{"user_id": uid, "action": action}
			if resource != "" {
				body["resource"] = resource
			}
			if resourceName != "" {
				body["resource_name"] = resourceName
			}
			if ip != "" {
				body["source_ip"] = ip
			}
			if cmd.Flags().Changed("mfa") {
				body["mfa"] = mfa
			}
			var s api.IAMSimulation
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: "/iam/simulate", Body: body}, &s)
			if err != nil {
				return iamError(api.AsFlags(err, map[string]string{"user_id": "--user", "action": "--action", "resource": "--resource", "resource_name": "--resource-name", "source_ip": "--ip", "mfa": "--mfa"}))
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
				return nil
			case a.out.Quiet:
				a.out.Line("%s", output.Clean(s.Decision))
				return nil
			}
			why := map[string]string{
				"owner":         "owners are allowed everything",
				"admin":         "admins are allowed everything no deny refuses",
				"allow":         "a policy allows it",
				"baseline_read": "it is a Read action and the member baseline is read",
				"explicit_deny": "a policy denies it",
				"implicit_deny": "no policy allows it",
			}[s.Decision]
			if why == "" {
				why = s.Decision
			}
			verdict := "Allowed"
			if !s.Allowed {
				verdict = "Denied"
			}
			pairs := []output.Pair{{"Decision", s.Decision}, {"Why", why}}
			if s.PolicyID != nil {
				pairs = append(pairs, output.Pair{"Policy", *s.PolicyID})
			}
			if s.Sid != nil && *s.Sid != "" {
				pairs = append(pairs, output.Pair{"Statement", *s.Sid})
			}
			a.out.Print(output.Detail{Title: verdict + ": " + action, State: a.out.State(map[bool]string{true: "allowed", false: "denied"}[s.Allowed]), Sections: [][]output.Pair{pairs}})
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&user, "user", "", "the member: id (usr_…) or email (required)")
	f.StringVar(&action, "action", "", "the action, such as instances:Delete (required)")
	f.StringVar(&resource, "resource", "", "the resource's id, such as vm_…")
	f.StringVar(&resourceName, "resource-name", "", "the resource's name, for name: globs")
	f.StringVar(&ip, "ip", "", "the client address, for source_ip conditions")
	f.BoolVar(&mfa, "mfa", false, "the session was signed in with MFA")
	_ = cmd.MarkFlagRequired("user")
	_ = cmd.MarkFlagRequired("action")
	return cmd
}
