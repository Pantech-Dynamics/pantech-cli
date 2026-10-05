package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

var securityGroupKind = kind{prefix: "sg_", path: "/security-groups", noun: "security group", listCmd: "pantech security-groups list"}

func newSecurityGroupsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "security-groups",
		Aliases: []string{"security-group", "sg"},
		Short:   "Manage the firewalls of standard VMs",
		Long: `Manage security groups: reusable firewalls for standard VMs. A VM in a VPC
subnet is protected by the subnet's firewall rules instead.

A rule is DIRECTION:PROTOCOL:PORTS:CIDR, e.g. ingress:tcp:443:0.0.0.0/0,
ingress:tcp:8000-8100:203.0.113.4/32 or ingress:icmp::0.0.0.0/0 (no ports for
icmp and all).`,
	}
	cmd.AddCommand(
		newSecurityGroupsListCmd(a),
		newSecurityGroupsGetCmd(a),
		newSecurityGroupsCreateCmd(a),
		newSecurityGroupsRulesCmd(a),
		deleteCmd(a, securityGroupKind, "", resolveSecurityGroup),
	)
	return cmd
}

func resolveSecurityGroup(cmd *cobra.Command, c *api.Client, ref string) (string, error) {
	return resolve(cmd, c, securityGroupKind, ref, func(g api.SecurityGroup) (string, string) { return g.ID, g.Name })
}

func newSecurityGroupsListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List security groups",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			groups, raw, err := listItems[api.SecurityGroup](cmd, c, "/security-groups", nil)
			if err != nil {
				return err
			}
			if printList(a, groups, raw, func(g api.SecurityGroup) string { return g.ID }, "No security groups yet.", "pantech security-groups create --name web --rule ingress:tcp:443:0.0.0.0/0") {
				return nil
			}
			rows := make([][]string, len(groups))
			for i, g := range groups {
				rows[i] = []string{g.Name, a.out.State(g.ObservedState), fmt.Sprint(len(g.Rules)), a.out.Dim(g.ID)}
			}
			a.out.Table([]string{"name", "state", "rules", "id"}, rows)
			a.out.Summary(count(len(groups), "security group"))
			return nil
		},
	}
}

func newSecurityGroupsGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <security group>",
		Aliases: []string{"show"},
		Short:   "Show a security group and its rules, by id or name",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveSecurityGroup(cmd, c, args[0])
			if err != nil {
				return err
			}
			var g api.SecurityGroup
			if ok, err := getOne(a, cmd, c, "/security-groups/"+url.PathEscape(id), &g); !ok {
				return err
			}
			rules := make([]output.Pair, 0, len(g.Rules))
			for _, r := range g.Rules {
				rules = append(rules, output.Pair{r.Direction, formatRule(r)})
			}
			if len(rules) == 0 {
				rules = append(rules, output.Pair{"Rules", "none: nothing gets in"})
			}
			a.out.Print(output.Detail{
				Title:    g.Name,
				State:    state(a.out, g.ObservedState, g.DesiredState),
				Subtitle: g.ID,
				Sections: [][]output.Pair{rules, {{"Created", output.When(g.CreatedAt)}}},
			})
			return nil
		},
	}
}

func newSecurityGroupsCreateCmd(a *app) *cobra.Command {
	var name string
	var rules []string
	var noWait bool
	cmd := &cobra.Command{
		Use:     "create",
		Short:   "Create a security group",
		Example: `  pantech security-groups create --name web --rule ingress:tcp:80:0.0.0.0/0 --rule ingress:tcp:443:0.0.0.0/0`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			parsed, err := parseRules(rules)
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			body := map[string]any{"name": name, "rules": parsed}
			return a.runCreate(cmd, c, api.Request{Method: http.MethodPost, Path: "/security-groups", Body: body}, noWait, name)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "its name (required)")
	cmd.Flags().StringArrayVar(&rules, "rule", nil, "a rule, DIRECTION:PROTOCOL:PORTS:CIDR (repeatable)")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the id without waiting")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newSecurityGroupsRulesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rules",
		Short: "Change a security group's rules",
	}
	var rules []string
	var none, noWait bool
	set := &cobra.Command{
		Use:   "set <security group>",
		Short: "Replace every rule of a security group",
		Long: `Replace every rule of a security group with the --rule flags given. To remove
every rule, pass --none. To change some rules and keep the others, use rules
add or rules remove.`,
		Example: `  pantech security-groups rules set web --rule ingress:tcp:22:203.0.113.4/32 --rule ingress:tcp:443:0.0.0.0/0`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(rules) == 0 && !none {
				return &usageError{fmt.Errorf("give the new rules with --rule, or --none to remove them all"), cmd}
			}
			parsed, err := parseRules(rules)
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveSecurityGroup(cmd, c, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Replace every rule of %s with %d rule(s)?", args[0], len(parsed))); err != nil {
				return err
			}
			req := api.Request{Method: http.MethodPut, Path: "/security-groups/" + url.PathEscape(id) + "/rules", Body: map[string]any{"rules": parsed}}
			return a.runWrite(cmd, c, req, noWait, verb{"Updating", "Updated"}, args[0])
		},
	}
	set.Flags().StringArrayVar(&rules, "rule", nil, "a rule, DIRECTION:PROTOCOL:PORTS:CIDR (repeatable)")
	set.Flags().BoolVar(&none, "none", false, "remove every rule")
	set.Flags().BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	cmd.AddCommand(set, newSecurityGroupRulesChangeCmd(a, true), newSecurityGroupRulesChangeCmd(a, false))
	return cmd
}

// newSecurityGroupRulesChangeCmd is rules add (add) or rules remove: the
// group's rules are read, changed and written back whole, keeping the rest.
func newSecurityGroupRulesChangeCmd(a *app, add bool) *cobra.Command {
	var rules []string
	var noWait bool
	cmd := &cobra.Command{
		Use:   "add <security group>",
		Short: "Add rules to a security group, keeping its others",
		Long: `Add the --rule flags to a security group's rules. Rules it already has are
left as they are. The group's rules are read and written back with the new
ones, so run one change to a group at a time.`,
		Example: `  pantech security-groups rules add web --rule ingress:tcp:22:203.0.113.4/32`,
		Args:    cobra.ExactArgs(1),
	}
	if !add {
		cmd.Use = "remove <security group>"
		cmd.Aliases = []string{"rm"}
		cmd.Short = "Remove rules from a security group, keeping its others"
		cmd.Long = `Remove the --rule flags from a security group's rules; each must match one
exactly, as "pantech security-groups get" lists them. The group's rules are
read and written back without them, so run one change to a group at a time.`
		cmd.Example = `  pantech security-groups rules remove web --rule ingress:tcp:22:0.0.0.0/0`
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if len(rules) == 0 {
			return &usageError{errors.New("give at least one --rule"), cmd}
		}
		parsed, err := parseRules(rules)
		if err != nil {
			return err
		}
		c, err := a.client()
		if err != nil {
			return err
		}
		id, err := resolveSecurityGroup(cmd, c, args[0])
		if err != nil {
			return err
		}
		var g api.SecurityGroup
		if _, err := api.Get(ctx(cmd), c, "/security-groups/"+url.PathEscape(id), &g); err != nil {
			return err
		}
		var next []api.SecurityGroupRule
		if add {
			next = append(next, g.Rules...)
			for _, r := range parsed {
				if !slices.Contains(next, r) {
					next = append(next, r)
				}
			}
			if len(next) == len(g.Rules) {
				a.out.Note("%s already has these rules.", args[0])
				return nil
			}
		} else {
			for _, r := range parsed {
				if !slices.Contains(g.Rules, r) {
					return fmt.Errorf("%s has no rule %s %s\nSee: pantech security-groups get %s", args[0], r.Direction, formatRule(r), args[0])
				}
			}
			for _, r := range g.Rules {
				if !slices.Contains(parsed, r) {
					next = append(next, r)
				}
			}
			if err := a.confirm(fmt.Sprintf("Remove %d rule(s) from %s, leaving %d?", len(g.Rules)-len(next), args[0], len(next))); err != nil {
				return err
			}
		}
		if next == nil {
			next = []api.SecurityGroupRule{}
		}
		req := api.Request{Method: http.MethodPut, Path: "/security-groups/" + url.PathEscape(id) + "/rules", Body: map[string]any{"rules": next}}
		return a.runWrite(cmd, c, req, noWait, verb{"Updating", "Updated"}, args[0])
	}
	cmd.Flags().StringArrayVar(&rules, "rule", nil, "a rule, DIRECTION:PROTOCOL:PORTS:CIDR (repeatable)")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	return cmd
}

func parseRules(specs []string) ([]api.SecurityGroupRule, error) {
	rules := make([]api.SecurityGroupRule, 0, len(specs))
	for _, spec := range specs {
		parts := strings.SplitN(spec, ":", 4)
		if len(parts) != 4 || parts[3] == "" {
			return nil, fmt.Errorf("rule %q: write it as DIRECTION:PROTOCOL:PORTS:CIDR, e.g. ingress:tcp:443:0.0.0.0/0", spec)
		}
		r := api.SecurityGroupRule{Direction: parts[0], Protocol: parts[1], PortRange: parts[2], CIDR: parts[3]}
		switch r.Direction {
		case "ingress", "egress":
		default:
			return nil, fmt.Errorf("rule %q: the direction is ingress or egress", spec)
		}
		switch r.Protocol {
		case "tcp", "udp":
			if r.PortRange == "" {
				return nil, fmt.Errorf("rule %q: give a port (22) or range (8000-8100) for %s", spec, r.Protocol)
			}
		case "icmp", "all":
			if r.PortRange != "" {
				return nil, fmt.Errorf("rule %q: %s takes no ports: %s:%s::%s", spec, r.Protocol, r.Direction, r.Protocol, r.CIDR)
			}
		default:
			return nil, fmt.Errorf("rule %q: the protocol is tcp, udp, icmp or all", spec)
		}
		rules = append(rules, r)
	}
	return rules, nil
}

func formatRule(r api.SecurityGroupRule) string {
	s := r.Protocol
	if r.PortRange != "" {
		s += " " + r.PortRange
	}
	if r.Direction == "egress" {
		return s + " to " + r.CIDR
	}
	return s + " from " + r.CIDR
}
