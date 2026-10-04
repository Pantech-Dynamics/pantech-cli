package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

func newSSHKeysCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ssh-keys",
		Aliases: []string{"ssh-key", "keys"},
		Short:   "Manage the SSH keys installed on new VMs",
	}
	cmd.AddCommand(newSSHKeysListCmd(a), newSSHKeysAddCmd(a), newSSHKeysDeleteCmd(a))
	return cmd
}

func newSSHKeysListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List SSH keys",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			keys, err := api.ListAll[api.SSHKey](ctx(cmd), c, "/ssh-keys", nil)
			if err != nil {
				return err
			}
			switch {
			case a.out.JSON:
				a.out.Value(keys)
			case a.out.Quiet:
				for _, k := range keys {
					a.out.Line("%s", k.ID)
				}
			case len(keys) == 0:
				a.out.Note("No SSH keys yet.")
				a.out.Next("Add yours", "pantech ssh-keys add ~/.ssh/id_ed25519.pub")
			default:
				rows := make([][]string, len(keys))
				for i, k := range keys {
					rows[i] = []string{k.Name, k.Fingerprint, output.Ago(k.CreatedAt), a.out.Dim(k.ID)}
				}
				a.out.Table([]string{"name", "fingerprint", "added", "id"}, rows)
				a.out.Summary(count(len(keys), "SSH key"))
			}
			return nil
		},
	}
}

func newSSHKeysAddCmd(a *app) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "add [public key file]",
		Short: "Register your public key, or have a key pair generated",
		Long: `Register your public key, so new VMs can be created with it. Without a file,
a key pair is generated and its private key printed once: it is never shown
again, so save it.`,
		Example: `  pantech ssh-keys add ~/.ssh/id_ed25519.pub
  pantech ssh-keys add --name ci > ci_key`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			body := map[string]any{}
			if len(args) == 1 {
				data, err := os.ReadFile(args[0])
				if err != nil {
					return err
				}
				pub := strings.TrimSpace(string(data))
				if strings.Contains(pub, "PRIVATE KEY") {
					return fmt.Errorf("%s is a private key: pass the .pub file next to it", args[0])
				}
				body["public_key"] = pub
				if name == "" {
					// The key's own comment (user@host), else the file name.
					if fields := strings.Fields(pub); len(fields) >= 3 {
						name = fields[2]
					} else {
						name = strings.TrimSuffix(filepath.Base(args[0]), ".pub")
					}
				}
			}
			if name == "" {
				return fmt.Errorf("give the key a --name")
			}
			body["name"] = name

			var key api.SSHKey
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodPost, Path: "/ssh-keys", Body: body}, &key)
			if err != nil {
				if api.IsCode(err, "SSH_KEY_NAME_TAKEN") {
					return fmt.Errorf("%w\nIf a request to add it failed before, it may already exist: pantech ssh-keys list", err)
				}
				return err
			}
			if a.out.JSON {
				a.out.RawJSON(res.Body)
				return nil
			}
			if key.PrivateKey != "" {
				// The private key alone on stdout, so it can be redirected to a file.
				a.out.Line("%s", strings.TrimRight(key.PrivateKey, "\n"))
				a.out.Note("\nThat private key is shown once and never again: save it (e.g. pantech ssh-keys add --name %s > key && chmod 600 key).", key.Name)
			}
			a.out.Success("Added %s (%s, %s).", key.Name, key.ID, key.Fingerprint)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "its name (default: the key's comment)")
	return cmd
}

func newSSHKeysDeleteCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <key>",
		Aliases: []string{"rm"},
		Short:   "Delete an SSH key, by id or name",
		Long:    "Delete an SSH key, by id or name. VMs already created with it keep it.",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			id, err := resolveSSHKey(cmd, c, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Delete SSH key %s?", args[0])); err != nil {
				return err
			}
			_, err = c.Do(ctx(cmd), api.Request{Method: http.MethodDelete, Path: "/ssh-keys/" + url.PathEscape(id)}, nil)
			// Not replayed on retry: a 404 after a retry means the first attempt worked.
			if err != nil && !api.IsCode(err, "RESOURCE_NOT_FOUND") {
				return err
			}
			a.out.Success("Deleted %s.", args[0])
			return nil
		},
	}
}

// resolveSSHKey takes an id (sshk_…) or a name and returns the id.
func resolveSSHKey(cmd *cobra.Command, c *api.Client, ref string) (string, error) {
	if strings.HasPrefix(ref, "sshk_") {
		return ref, nil
	}
	keys, err := api.ListAll[api.SSHKey](ctx(cmd), c, "/ssh-keys", nil)
	if err != nil {
		return "", err
	}
	for _, k := range keys {
		if k.Name == ref {
			return k.ID, nil
		}
	}
	return "", fmt.Errorf("no SSH key named %q\nSee: pantech ssh-keys list", ref)
}
