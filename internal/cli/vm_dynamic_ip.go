package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

const dynamicIPOnDemandSurcharge = 95

var dynamicIPSchedules = map[string]int{
	"every_12_hours": 60,
	"daily":          50,
	"weekly":         40,
	"monthly":        30,
}

func validDynamicIPSchedule(s string) (int, bool) {
	pct, ok := dynamicIPSchedules[s]
	return pct, ok
}

func validDynamicIPConnections(s string) bool {
	return s == "keep" || s == "move"
}

func newVMDynamicIPCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "dynamic-ip",
		Aliases: []string{"dip", "rotation", "address-rotation"},
		Short:   "Manage a VM's dynamic IP (scheduled outbound IPv4 rotation)",
		Long: `Manage a standard VM's dynamic IP: scheduled outbound IPv4 rotation on a
schedule (every_12_hours, daily, weekly or monthly), with optional on-demand
changes.

Surcharges are added to the VM's plan price:
  every_12_hours  +60%
  daily           +50%
  weekly          +40%
  monthly         +30%
  on-demand       +95% (up to 10 manual rotations per 24 hours)

The VM must be a standard VM with the private network attached, a security
group that allows nothing from the private network's range, and the Pantech
agent 0.5.0 or newer.`,
	}
	cmd.AddCommand(
		newVMDynamicIPGetCmd(a),
		newVMDynamicIPSetCmd(a),
		newVMDynamicIPRotateCmd(a),
		newVMDynamicIPOffCmd(a),
	)
	return cmd
}

func newVMDynamicIPGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "get <vm>",
		Aliases: []string{"show"},
		Short:   "Show a VM's dynamic IP status and recent rotations",
		Example: "  pantech vm dynamic-ip get web-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveVM(cmd, c, args[0])
			if err != nil {
				return err
			}
			var d api.DynamicIP
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: dynamicIPPath(id)}, &d)
			if err != nil {
				if api.IsCode(err, "ADDRESS_ROTATION_NOT_FOUND") {
					switch {
					case a.out.JSON:
						return err
					case a.out.Quiet:
						a.out.Line("off")
						return nil
					default:
						a.out.Note("Dynamic IP is off on %s.", args[0])
						a.out.Next("Turn it on", "pantech vm dynamic-ip set "+args[0]+" --schedule weekly")
						return nil
					}
				}
				return dynamicIPError(cmd, c, id, args[0], err)
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
				return nil
			case a.out.Quiet:
				a.out.Line("%s", dynamicIPQuiet(&d))
				return nil
			}
			printDynamicIP(a, args[0], &d, true)
			return nil
		},
	}
}

func newVMDynamicIPSetCmd(a *app) *cobra.Command {
	var schedule, connections string
	var onDemand, noWait bool
	cmd := &cobra.Command{
		Use:     "set <vm>",
		Aliases: []string{"enable", "on", "update"},
		Short:   "Turn on or update dynamic IP on a VM",
		Long: `Turn on dynamic IP on a standard VM, or update its schedule, connection
handling or on-demand setting.

Surcharges on the VM's plan price: every_12_hours (+60%), daily (+50%),
weekly (+40%), monthly (+30%), and --on-demand (+95%).

Waits while dynamic IP is enabling unless --no-wait is given. Exits with 5
if enabling finishes in the failed state.`,
		Example: `  pantech vm dynamic-ip set web-1 --schedule weekly
  pantech vm dynamic-ip set web-1 --schedule daily --connections move --on-demand`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pct, ok := validDynamicIPSchedule(schedule)
			if !ok {
				return &usageError{fmt.Errorf("--schedule must be one of every_12_hours, daily, weekly, monthly (got %q)", schedule), cmd}
			}
			if !validDynamicIPConnections(connections) {
				return &usageError{fmt.Errorf("--connections must be keep or move (got %q)", connections), cmd}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveVM(cmd, c, args[0])
			if err != nil {
				return err
			}
			question := fmt.Sprintf("Set dynamic IP on %s (%s, connections: %s; +%d%% surcharge on the VM's plan)?", args[0], schedule, connections, pct)
			if onDemand {
				question = fmt.Sprintf("Set dynamic IP on %s (%s, connections: %s, on-demand; +%d%% schedule, +%d%% on-demand surcharge on the VM's plan)?", args[0], schedule, connections, pct, dynamicIPOnDemandSurcharge)
			}
			if err := a.confirm(question); err != nil {
				return err
			}
			body := map[string]any{
				"schedule":    schedule,
				"connections": connections,
			}
			if cmd.Flags().Changed("on-demand") {
				body["on_demand"] = onDemand
			}
			path := dynamicIPPath(id)
			var d api.DynamicIP
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPut, Path: path, Body: body}, &d)
			if err != nil {
				err = api.AsFlags(err, map[string]string{
					"schedule":    "--schedule",
					"connections": "--connections",
					"on_demand":   "--on-demand",
				})
				return dynamicIPError(cmd, c, id, args[0], err)
			}
			lastBody := res.Body
			if noWait && d.State == "enabling" {
				switch {
				case a.out.JSON:
					a.out.RawJSON(lastBody)
				case a.out.Quiet:
					a.out.Line("%s", dynamicIPQuiet(&d))
				default:
					a.out.Note("Enabling dynamic IP on %s.", args[0])
					a.out.Next("Check status", "pantech vm dynamic-ip get "+args[0])
				}
				return nil
			}
			if d.State == "enabling" {
				steps := a.out.Steps()
				steps.Start("Enabling dynamic IP on " + args[0])
				for d.State == "enabling" {
					select {
					case <-ctx(cmd).Done():
						steps.Fail("", "")
						return ctx(cmd).Err()
					case <-time.After(api.PollInterval):
					}
					var next api.DynamicIP
					gres, gerr := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: path}, &next)
					if gerr != nil {
						steps.Fail("", "")
						return dynamicIPError(cmd, c, id, args[0], gerr)
					}
					d = next
					lastBody = gres.Body
				}
				if d.State == "failed" {
					steps.Fail("", "")
					return &api.DynamicIPFailed{DynamicIP: &d}
				}
				note := ""
				if d.CurrentAddress != nil && *d.CurrentAddress != "" {
					note = *d.CurrentAddress
				}
				steps.Done("Dynamic IP "+d.State+" on "+args[0], note)
			}
			if d.State == "failed" {
				return &api.DynamicIPFailed{DynamicIP: &d}
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(lastBody)
			case a.out.Quiet:
				a.out.Line("%s", dynamicIPQuiet(&d))
			default:
				printDynamicIP(a, args[0], &d, false)
				if d.State == "degraded" {
					a.out.Warn("Dynamic IP is degraded: check recent events with \"pantech vm dynamic-ip get %s\".", args[0])
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&schedule, "schedule", "", "rotation schedule: every_12_hours (+60%), daily (+50%), weekly (+40%), monthly (+30%) (required)")
	f.StringVar(&connections, "connections", "keep", "what to do with open outbound connections on rotation: keep or move")
	f.BoolVar(&onDemand, "on-demand", false, "allow immediate manual rotations (+95% surcharge; up to 10 per 24h)")
	f.BoolVar(&noWait, "no-wait", false, "return immediately without waiting for dynamic IP to become active")
	_ = cmd.MarkFlagRequired("schedule")
	return cmd
}

func newVMDynamicIPRotateCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "rotate <vm>",
		Aliases: []string{"now"},
		Short:   "Rotate a VM's dynamic IP immediately (uses 1 on-demand change)",
		Long: `Rotate a VM's dynamic IP immediately. Requires on-demand rotation to be
enabled on the VM and consumes 1 of the 10 on-demand rotations allowed per
24 hours. The request is never retried automatically.`,
		Example: "  pantech vm dynamic-ip rotate web-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveVM(cmd, c, args[0])
			if err != nil {
				return err
			}
			var d api.DynamicIP
			// Non-replaying: a retry after a lost answer would consume another
			// of the daily on-demand rotation allowance.
			res, err := c.Do(ctx(cmd), api.Request{
				Method:  http.MethodPost,
				Path:    dynamicIPPath(id) + "/rotate",
				NoRetry: true,
			}, &d)
			if err != nil {
				return dynamicIPError(cmd, c, id, args[0], err)
			}
			switch {
			case a.out.JSON:
				a.out.RawJSON(res.Body)
				return nil
			case a.out.Quiet:
				a.out.Line("%s", dynamicIPQuiet(&d))
				return nil
			}
			addr := output.Or(d.CurrentAddress)
			a.out.Line("%s", addr)
			if d.OnDemandLimit > 0 {
				a.out.Note("Rotated %s to %s (%d/%d in 24h).", args[0], addr, d.OnDemandUsed, d.OnDemandLimit)
			} else {
				a.out.Note("Rotated %s to %s.", args[0], addr)
			}
			if d.State == "degraded" {
				a.out.Warn("Dynamic IP is degraded: check status with \"pantech vm dynamic-ip get %s\".", args[0])
			}
			return nil
		},
	}
}

func newVMDynamicIPOffCmd(a *app) *cobra.Command {
	var noWait bool
	cmd := &cobra.Command{
		Use:     "off <vm>",
		Aliases: []string{"disable", "delete", "remove"},
		Short:   "Turn off dynamic IP on a VM",
		Long: `Turn off dynamic IP on a VM. Outbound traffic returns to the VM's static
IP and the dynamic IP surcharge stops once turned off. Waits until off
unless --no-wait is given.`,
		Example: "  pantech vm dynamic-ip off web-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveVM(cmd, c, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Turn off dynamic IP on %s (%s)? Outbound traffic will return to the VM's static IP.", args[0], id)); err != nil {
				return err
			}
			path := dynamicIPPath(id)
			var d api.DynamicIP
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodDelete, Path: path}, &d)
			if err != nil {
				return dynamicIPError(cmd, c, id, args[0], err)
			}
			lastBody := res.Body
			if noWait || d.State == "off" {
				switch {
				case a.out.JSON:
					a.out.RawJSON(lastBody)
				case a.out.Quiet:
					a.out.Line("%s", d.State)
				case d.State == "off":
					a.out.Success("Turned off dynamic IP on %s.", args[0])
				default:
					a.out.Note("Turning off dynamic IP on %s (%s).", args[0], d.State)
					a.out.Next("Check status", "pantech vm dynamic-ip get "+args[0])
				}
				return nil
			}
			steps := a.out.Steps()
			steps.Start("Turning off dynamic IP on " + args[0])
			for d.State != "off" {
				select {
				case <-ctx(cmd).Done():
					steps.Fail("", "")
					return ctx(cmd).Err()
				case <-time.After(api.PollInterval):
				}
				var next api.DynamicIP
				gres, gerr := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: path}, &next)
				if gerr != nil {
					var p *api.Problem
					if api.IsCode(gerr, "ADDRESS_ROTATION_NOT_FOUND") || (errors.As(gerr, &p) && p.Status == http.StatusNotFound) {
						d.State = "off"
						lastBody = nil
						break
					}
					steps.Fail("", "")
					return dynamicIPError(cmd, c, id, args[0], gerr)
				}
				d = next
				lastBody = gres.Body
			}
			steps.Done("Turned off dynamic IP on "+args[0], "")
			switch {
			case a.out.JSON:
				if lastBody != nil {
					a.out.RawJSON(lastBody)
				} else {
					a.out.Value(map[string]any{"instance_id": id, "state": "off"})
				}
			case a.out.Quiet:
				a.out.Line("off")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return immediately without waiting for dynamic IP to turn off")
	return cmd
}

func dynamicIPPath(id string) string {
	return "/instances/" + url.PathEscape(id) + "/address-rotation"
}

func dynamicIPQuiet(d *api.DynamicIP) string {
	if d.CurrentAddress != nil && *d.CurrentAddress != "" {
		return *d.CurrentAddress
	}
	return d.State
}

func formatSurcharge(pct float64) string {
	if pct == 0 {
		return "0%"
	}
	return fmt.Sprintf("+%g%%", pct)
}

func onDemandText(d *api.DynamicIP) string {
	if !d.OnDemand {
		return "off"
	}
	if d.OnDemandLimit > 0 {
		return fmt.Sprintf("on (%d/%d used in 24h)", d.OnDemandUsed, d.OnDemandLimit)
	}
	return "on"
}

func printDynamicIP(a *app, ref string, d *api.DynamicIP, showEvents bool) {
	sections := [][]output.Pair{
		{
			{"Current address", output.Or(d.CurrentAddress)},
			{"Schedule", d.Schedule},
			{"Connections", d.Connections},
			{"On-demand", onDemandText(d)},
			{"Surcharge", formatSurcharge(d.SurchargePercent)},
		},
		{
			{"Next rotation", output.When(d.NextRotationAt)},
			{"Last rotated", output.When(d.LastRotatedAt)},
		},
	}
	if d.FailureReason != nil && *d.FailureReason != "" {
		sections = append(sections, []output.Pair{{"Failure", *d.FailureReason}})
	}
	a.out.Print(output.Detail{
		Title:    ref,
		State:    a.out.State(d.State),
		Subtitle: d.InstanceID,
		Sections: sections,
	})
	if showEvents && len(d.Events) > 0 {
		a.out.Line("")
		rows := make([][]string, len(d.Events))
		for i, ev := range d.Events {
			notes := "—"
			if ev.Degraded {
				notes = "degraded"
			}
			rows[i] = []string{
				output.Ago(&ev.OccurredAt),
				ev.Kind,
				output.Or(ev.FromAddress),
				output.Or(ev.ToAddress),
				notes,
			}
		}
		a.out.Table([]string{"when", "event", "from", "to", "notes"}, rows)
	}
	if d.State == "active" || d.State == "degraded" {
		if d.OnDemand {
			a.out.Next("Rotate now", "pantech vm dynamic-ip rotate "+ref)
		}
		a.out.Next("Turn off", "pantech vm dynamic-ip off "+ref)
	}
}

func dynamicIPError(cmd *cobra.Command, c *api.Client, vmID, ref string, err error) error {
	switch {
	case api.IsCode(err, "ADDRESS_ROTATION_NOT_FOUND"):
		return &hinted{err: err, hint: fmt.Sprintf("Dynamic IP is off on %s.\nTurn it on: pantech vm dynamic-ip set %s --schedule weekly", ref, ref)}
	case api.IsCode(err, "ADDRESS_ROTATION_NEEDS_PRIVATE_NETWORK"):
		return &hinted{err: err, hint: fmt.Sprintf("Attach the VM to the private network first, then try again:\n  pantech vm private-network attach %s", ref)}
	case api.IsCode(err, "ADDRESS_ROTATION_NEEDS_AGENT"):
		return &hinted{err: err, hint: "Install or upgrade the Pantech agent to 0.5.0 or newer on the VM, then try again."}
	case api.IsCode(err, "ADDRESS_ROTATION_NOT_ENABLED"):
		return &hinted{err: err, hint: "Dynamic IP is in closed beta: ask support to switch it on for your organization."}
	case api.IsCode(err, "ADDRESS_ROTATION_ON_DEMAND_OFF"):
		return &hinted{err: err, hint: fmt.Sprintf("On-demand rotation is off on %s. Turn it on first:\n  pantech vm dynamic-ip set %s --schedule <schedule> --on-demand", ref, ref)}
	case api.IsCode(err, "ADDRESS_ROTATION_LIMIT_REACHED"):
		return &hinted{err: err, hint: "The 24-hour on-demand rotation limit has been reached. Wait for an earlier rotation to age out or let the next scheduled rotation run."}
	case api.IsCode(err, "ADDRESS_ROTATION_NEEDS_SECURITY_GROUP"):
		return &hinted{err: err, hint: "Choose a security group that allows nothing from the private network's range.\nSee: pantech security-groups list"}
	case api.IsCode(err, "INSTANCE_HAS_DYNAMIC_IP"):
		return &hinted{err: err, hint: fmt.Sprintf("Turn off dynamic IP on %s first, then try again:\n  pantech vm dynamic-ip off %s", ref, ref)}
	case api.IsCode(err, "SECURITY_GROUP_ALLOWS_PRIVATE_NETWORK"):
		if vmID != "" {
			return privateNetworkHint(cmd, c, vmID, err)
		}
		return &hinted{err: err, hint: "Narrow the security group's ingress rules so none covers the private network's range, then try again:\n" +
			"  pantech security-groups list\n" +
			"  pantech security-groups rules remove <security group> --rule <a rule it lists>"}
	}
	return err
}
