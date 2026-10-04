package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
)

func newOperationsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "operations",
		Aliases: []string{"operation", "op", "ops"},
		Short:   "Follow asynchronous changes",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "get <operation id>",
			Short: "Show an operation",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				var op api.Operation
				res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: "/operations/" + url.PathEscape(args[0])}, &op)
				if err != nil {
					return err
				}
				if a.out.JSON {
					a.out.RawJSON(res.Body)
					return nil
				}
				printOperation(a, &op)
				return nil
			},
		},
		&cobra.Command{
			Use:   "wait <operation id>",
			Short: "Wait for an operation to finish; exits non-zero if it failed",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := a.client()
				if err != nil {
					return err
				}
				op, err := api.WaitOperation(ctx(cmd), c, args[0], func(op *api.Operation) {
					if !a.out.JSON && !a.out.Quiet {
						a.out.Note("  %s %s", op.Kind, a.out.State(op.Status))
					}
				})
				if a.out.JSON && op != nil {
					a.out.Value(op)
				}
				return err
			},
		},
	)
	return cmd
}

func printOperation(a *app, op *api.Operation) {
	pairs := [][2]string{
		{"ID", op.ID},
		{"Kind", op.Kind},
		{"Status", a.out.State(op.Status)},
		{"Resource", op.ResourceType + " " + op.ResourceID},
	}
	if op.Failure != nil {
		pairs = append(pairs, [2]string{"Failure", fmt.Sprintf("%s (%s)", op.Failure.Reason, op.Failure.Code)})
	}
	a.out.Fields(pairs)
}

func newAPICmd(a *app) *cobra.Command {
	var data string
	var query []string
	cmd := &cobra.Command{
		Use:   "api <method> <path>",
		Short: "Call any public API endpoint directly",
		Long: `Call any public API endpoint directly, with your key, an Idempotency-Key on
writes and the CLI's retries. The path is relative to /public/v1. The answer is
printed as JSON. For everything the CLI has no command for yet: networks,
public IPs, volumes, snapshots, usage.

--data takes JSON, or @file, or @- for stdin.

The reference: https://docs.pantechdynamics.com/api`,
		Example: `  pantech api GET /networks
  pantech api GET /usage/by-resource -f period=month
  pantech api POST /networks --data '{"name":"prod","cidr":"10.0.0.0/16"}'`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			method := strings.ToUpper(args[0])
			path := args[1]
			path = strings.TrimPrefix(path, "/public/v1")
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}
			req := api.Request{Method: method, Path: path, Query: url.Values{}}
			if u, err := url.Parse(path); err == nil && u.RawQuery != "" {
				req.Path = u.Path
				req.Query = u.Query()
			}
			for _, kv := range query {
				k, v, _ := strings.Cut(kv, "=")
				req.Query.Add(k, v)
			}
			if data != "" {
				body, err := readData(data)
				if err != nil {
					return err
				}
				req.Body = json.RawMessage(body)
			}
			res, err := c.Do(ctx(cmd), req, nil)
			if res != nil && len(res.Body) > 0 {
				a.out.RawJSON(res.Body)
			}
			return err
		},
	}
	cmd.Flags().StringVarP(&data, "data", "d", "", "request body: JSON, @file or @- for stdin")
	cmd.Flags().StringArrayVarP(&query, "field", "f", nil, "query parameter key=value (repeatable)")
	return cmd
}

func readData(data string) ([]byte, error) {
	var body []byte
	var err error
	switch {
	case data == "@-":
		body, err = io.ReadAll(os.Stdin)
	case strings.HasPrefix(data, "@"):
		body, err = os.ReadFile(data[1:])
	default:
		body = []byte(data)
	}
	if err != nil {
		return nil, err
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("--data is not valid JSON")
	}
	return body, nil
}
