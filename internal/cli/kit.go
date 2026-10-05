package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

// kind describes one resource the CLI names: its id prefix, where its list
// lives, and how a person refers to it.
type kind struct {
	prefix   string // "vol_"
	path     string // "/volumes"
	noun     string // "volume"
	listCmd  string // "pantech volumes list"
	noSearch bool
}

func listItems[T any](cmd *cobra.Command, c *api.Client, path string, q url.Values) ([]T, []json.RawMessage, error) {
	raw, err := api.ListAllRaw(ctx(cmd), c, path, q)
	if err != nil {
		return nil, nil, err
	}
	items := make([]T, len(raw))
	for i, r := range raw {
		if err := json.Unmarshal(r, &items[i]); err != nil {
			return nil, nil, fmt.Errorf("reading the API's answer: %w", err)
		}
	}
	return items, raw, nil
}

func printList[T any](a *app, items []T, raw []json.RawMessage, id func(T) string, empty, next string) bool {
	switch {
	case a.out.JSON:
		if raw == nil {
			raw = []json.RawMessage{}
		}
		a.out.Value(raw)
	case a.out.Quiet:
		for _, it := range items {
			a.out.Line("%s", id(it))
		}
	case len(items) == 0:
		a.out.Note("%s", empty)
		if next != "" {
			a.out.Next("Create one", next)
		}
	default:
		return false
	}
	return true
}

func resolve[T any](cmd *cobra.Command, c *api.Client, k kind, ref string, idName func(T) (string, string)) (string, error) {
	if strings.HasPrefix(ref, k.prefix) {
		return ref, nil
	}
	q := url.Values{"q": {ref}}
	if k.noSearch {
		q = nil
	}
	items, err := api.ListAll[T](ctx(cmd), c, k.path, q)
	if err != nil {
		return "", err
	}
	var ids []string
	for _, it := range items {
		if id, name := idName(it); name == ref {
			ids = append(ids, id)
		}
	}
	switch len(ids) {
	case 0:
		return "", fmt.Errorf("no %s named %q\nSee: %s", k.noun, ref, k.listCmd)
	case 1:
		return ids[0], nil
	default:
		return "", fmt.Errorf("%d %ss are named %q: use its id\n(%s)", len(ids), k.noun, ref, strings.Join(ids, ", "))
	}
}

func getOne(a *app, cmd *cobra.Command, c *api.Client, path string, out any) (bool, error) {
	body, err := api.Get(ctx(cmd), c, path, out)
	if err != nil {
		return false, err
	}
	if a.out.JSON {
		a.out.RawJSON(body)
		return false, nil
	}
	return true, nil
}

func (a *app) runCreate(cmd *cobra.Command, c *api.Client, req api.Request, noWait bool, subject string) error {
	var accepted api.Accepted
	res, err := c.Do(ctx(cmd), req, &accepted)
	if err != nil {
		return err
	}
	if noWait || accepted.OperationID == "" {
		if a.out.JSON {
			a.out.RawJSON(res.Body)
			return nil
		}
		a.out.Line("%s", accepted.ResourceID)
		if accepted.OperationID != "" {
			a.out.Next("Follow it", "pantech operations wait "+accepted.OperationID)
		}
		return nil
	}
	steps := a.out.Steps()
	steps.Start("Creating " + subject)
	op, err := api.WaitOperation(ctx(cmd), c, accepted.OperationID, nil)
	if err != nil {
		steps.Fail("", "")
		return err
	}
	steps.Done("Created "+subject, "")
	if a.out.JSON {
		a.out.Value(op)
		return nil
	}
	a.out.Line("%s", accepted.ResourceID)
	return nil
}

func deleteCmd(a *app, k kind, warning string, resolveRef func(*cobra.Command, *api.Client, string) (string, error)) *cobra.Command {
	var noWait bool
	cmd := &cobra.Command{
		Use:     "delete <" + k.noun + ">",
		Aliases: []string{"rm"},
		Short:   "Delete a " + k.noun + ", by id or name",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveRef(cmd, c, args[0])
			if err != nil {
				return err
			}
			question := fmt.Sprintf("Delete %s %s (%s)?", k.noun, args[0], id)
			if warning != "" {
				question += " " + warning
			}
			if err := a.confirm(question); err != nil {
				return err
			}
			return a.runWrite(cmd, c, api.Request{Method: http.MethodDelete, Path: k.path + "/" + url.PathEscape(id)}, noWait, verb{"Deleting", "Deleted"}, args[0])
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	return cmd
}

// state is a resource's observed state, with where it is heading when that differs.
func state(out *output.Printer, observed, desired string) string {
	s := out.State(observed)
	if desired != "" && desired != observed && desired != "present" {
		s += out.Dim(" → " + desired)
	}
	return s
}

// newOrdersCmd is "orders list|get" for instance or database orders.
func newOrdersCmd(a *app, path, what string) *cobra.Command {
	noun := what
	if what != "VM" {
		noun = strings.ToLower(what)
	}
	cmd := &cobra.Command{
		Use:     "orders",
		Aliases: []string{"order"},
		Short:   "Follow the payment and provisioning of new " + noun + "s",
	}
	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List " + noun + " orders, newest first",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			orders, raw, err := listItems[api.Order](cmd, c, path, nil)
			if err != nil {
				return err
			}
			if printList(a, orders, raw, func(o api.Order) string { return o.ID }, "No "+noun+" orders yet.", "") {
				return nil
			}
			rows := make([][]string, len(orders))
			for i, o := range orders {
				rows[i] = []string{a.out.Dim(o.ID), a.out.State(o.Status), money(o.AmountMinor, o.Currency), o.ResourceID(), output.Or(o.FailureCode), output.Ago(o.CreatedAt)}
			}
			a.out.Table([]string{"order", "status", "amount", strings.ToLower(what), "failure", "created"}, rows)
			a.out.Summary(count(len(orders), "order"))
			return nil
		},
	}
	get := &cobra.Command{
		Use:     "get <order id>",
		Aliases: []string{"show"},
		Short:   "Show a " + noun + " order",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var o api.Order
			if ok, err := getOne(a, cmd, c, path+"/"+url.PathEscape(args[0]), &o); !ok {
				return err
			}
			d := output.Detail{
				Title:    what + " order",
				State:    a.out.State(o.Status),
				Subtitle: o.ID,
				Sections: [][]output.Pair{
					{{"Amount", money(o.AmountMinor, o.Currency)}, {what, o.ResourceID()}},
					{{"Operation", output.Or(o.OperationID)}, {"Failure", output.Or(o.FailureCode)}},
					{{"Created", output.When(o.CreatedAt)}},
				},
			}
			if o.OperationID != nil && !o.Done() {
				d.Next = [][2]string{{"Follow it", "pantech operations wait " + *o.OperationID}}
			}
			a.out.Print(d)
			return nil
		},
	}
	cmd.AddCommand(list, get)
	return cmd
}
