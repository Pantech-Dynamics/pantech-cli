package cli

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
)

func newVMChangeSSHKeyCmd(a *app) *cobra.Command {
	var noWait bool
	cmd := &cobra.Command{
		Use:   "change-ssh-key <vm> <key>",
		Short: "Give a running VM another of your SSH keys, by id or name",
		Long: `Replace the SSH key a VM accepts with another key on your account, for
example after losing the private key. The VM must be running: it restarts to
take the new key, about two minutes of downtime. Afterwards only the new key
logs in; keys you added to ~/.ssh/authorized_keys on the server yourself keep
working.

Add the key first with "pantech ssh-keys add".`,
		Example: `  pantech vm change-ssh-key web-1 ada@macbook-pro`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveVM(cmd, c, args[0])
			if err != nil {
				return err
			}
			keyID, err := resolveSSHKey(cmd, c, args[1])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Restart %s with key %s? It is down for about two minutes, and its current key stops working.", args[0], args[1])); err != nil {
				return err
			}
			req := api.Request{Method: http.MethodPost, Path: "/instances/" + url.PathEscape(id) + "/change-ssh-key", Body: map[string]string{"ssh_key_id": keyID}}
			err = a.runWrite(cmd, c, req, noWait, verb{"Changing the SSH key of", "Changed the SSH key of"}, args[0])
			if api.IsCode(err, "INVALID_RESOURCE_STATE") {
				return &hinted{err: err, hint: "The VM must be running, with no other change in progress.\nSee: pantech vm start " + args[0]}
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the operation id without waiting")
	return cmd
}
