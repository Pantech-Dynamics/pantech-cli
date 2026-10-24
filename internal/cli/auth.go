package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/auth"
	"github.com/Pantech-Dynamics/pantech-cli/internal/config"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

func newAuthCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Sign in, sign out and see who the CLI acts as",
	}
	cmd.AddCommand(newLoginCmd(a), newLogoutCmd(a), newStatusCmd(a))
	return cmd
}

func newLoginCmd(a *app) *cobra.Command {
	var withToken, noBrowser bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in through the console in your browser",
		Long: `Sign in through the console in your browser. You approve the CLI there and it
receives an API key for your organization, which is stored in your system's
keychain.

Without a browser (a server, CI), create a key in the console under
Organization › API keys and pass it on stdin:

  pantech auth login --with-token < key.txt

or skip storing anything and set PANTECH_API_KEY.`,
		Example: `  pantech auth login
  pantech --profile acme auth login
  echo "$PANTECH_KEY" | pantech auth login --with-token`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			name := a.profileName()
			profile := a.cfg.Profiles[name]
			// The key has to work against its API before it is kept.
			var me *api.Me
			verify := func(c context.Context, apiURL, key string) error {
				var err error
				if me, err = whoAmI(c, apiURL, key); err != nil {
					return fmt.Errorf("the key did not work against %s: %w", apiURL, err)
				}
				return nil
			}

			var key string
			if withToken {
				var err error
				if key, err = readToken(); err != nil {
					return err
				}
				if err := verify(ctx(cmd), api.DefaultBaseURL, key); err != nil {
					return err
				}
			} else {
				host, _ := os.Hostname()
				host = strings.TrimSuffix(host, ".local")
				b := &auth.Browser{
					ConsoleURL: DefaultConsoleURL,
					Host:       host,
					Prompt: func(url string, opened bool) {
						if opened {
							a.out.Note("Opened your browser to approve the CLI. If it did not open, go to:\n\n  %s\n", url)
						} else {
							a.out.Note("Open this in your browser to approve the CLI:\n\n  %s\n", url)
						}
						a.out.Note("Waiting for approval…")
					},
					// Checked before the browser tab is told it worked.
					Verify: func(c context.Context, g *auth.Grant) error {
						if err := verify(c, api.DefaultBaseURL, g.APIKey); err != nil {
							return fmt.Errorf("%w\nThe console created key %s for this sign-in: revoke it under Organization › API keys", err, g.KeyID)
						}
						return nil
					},
				}
				if !noBrowser {
					b.Open = auth.OpenBrowser
				}
				grant, err := b.Login(ctx(cmd))
				if err != nil {
					return err
				}
				key = grant.APIKey
				profile.OrganizationName = ""
				if grant.OrganizationName != nil {
					profile.OrganizationName = *grant.OrganizationName
				}
			}
			profile.APIURL = ""
			profile.KeyID = me.APIKey.ID
			profile.OrganizationID = me.OrganizationID
			profile.Scopes = me.APIKey.Scopes
			profile.ExpiresAt = ""
			if me.APIKey.ExpiresAt != nil {
				profile.ExpiresAt = *me.APIKey.ExpiresAt
			}

			where, err := config.SaveKey(name, key)
			if err != nil {
				return fmt.Errorf("storing the key: %w", err)
			}
			a.cfg.Profiles[name] = profile
			if a.cfg.Current == "" {
				a.cfg.Current = name
			}
			if err := a.cfg.Save(); err != nil {
				return err
			}

			org := profile.OrganizationName
			if org == "" {
				org = profile.OrganizationID
			}
			a.out.Success("Signed in to %s (%s), profile %q.", a.out.Bold(org), strings.Join(profile.Scopes, ", "), name)
			if where == config.InFile {
				dir, _ := config.Dir()
				a.out.Note("No keychain here, so the key is in %s/credentials.json, readable only by you.", dir)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&withToken, "with-token", false, "read an API key from stdin instead of signing in in a browser")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "print the sign-in link instead of opening a browser")
	return cmd
}

// readToken reads a key from stdin, hidden when typed at a terminal.
func readToken() (string, error) {
	var key string
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "Paste your API key: ")
		raw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		key = string(raw)
	} else {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", errors.New("no key on stdin")
		}
		key = line
	}
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, "PAN_") {
		return "", errors.New("that is not an API key: keys start with PAN_. Copy the whole key from the console")
	}
	return key, nil
}

func whoAmI(c context.Context, baseURL, key string) (*api.Me, error) {
	var me api.Me
	_, err := api.New(baseURL, key, userAgent()).Do(c, api.Request{Method: http.MethodGet, Path: "/me"}, &me)
	return &me, err
}

func newLogoutCmd(a *app) *cobra.Command {
	var keepKey bool
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Sign out: forget this profile's key and revoke it",
		Long: `Sign out: forget this profile's key on this machine, then revoke the key so it
stops working everywhere. --keep-key only forgets it, leaving the key valid
until it expires or is revoked in the console under Organization › API keys.

PANTECH_API_KEY is never revoked: unset it yourself.`,
		Example: `  pantech auth logout
  pantech --profile acme auth logout --keep-key`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			name := a.profileName()
			profile, known := a.cfg.Profiles[name]
			// Read before it is forgotten, to revoke it afterwards.
			key, _ := config.LoadKey(name)
			if err := config.DeleteKey(name); err != nil {
				return err
			}
			delete(a.cfg.Profiles, name)
			if a.cfg.Current == name {
				a.cfg.Current = ""
			}
			if err := a.cfg.Save(); err != nil {
				return err
			}
			if !known && key == "" {
				a.out.Note("Profile %q was not signed in.", name)
				return nil
			}
			a.out.Success("Signed out of profile %q.", name)

			keyName := "Its key"
			if profile.KeyID != "" {
				keyName = fmt.Sprintf("Its key (%s)", profile.KeyID)
			}
			stillWorks := func() {
				a.out.Note("%s still works until it expires. Revoke it under Organization › API keys to end it now.", keyName)
			}
			// A key from another API (v0.1.2 or older on staging) works
			// only there: never send it to this one.
			otherAPI := profile.APIURL != "" && profile.APIURL != api.DefaultBaseURL
			switch {
			case key == "":
				return nil
			case keepKey || otherAPI:
				stillWorks()
				return nil
			}
			switch result, err := a.revokeKey(ctx(cmd), key); {
			case err != nil:
				a.out.Warn("Could not revoke the key: %v", err)
				stillWorks()
			case result == keyRevoked:
				a.out.Note("%s is revoked.", keyName)
			case result == keyUnrevoked:
				// An API from before DELETE /me: the key is untouched.
				stillWorks()
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&keepKey, "keep-key", false, "only forget the key here; do not revoke it")
	return cmd
}

type revokeResult int

const (
	keyRevoked   revokeResult = iota
	keyGone                   // 401: expired or revoked already
	keyUnrevoked              // 404, 405: an API without DELETE /me
)

// revokeKey ends key with DELETE /me, once and briefly: signing out never
// waits long on the API.
func (a *app) revokeKey(c context.Context, key string) (revokeResult, error) {
	client := api.New(a.baseURL(), key, userAgent())
	client.Retries = 0
	client.HTTP.Timeout = 5 * time.Second
	c, cancel := context.WithTimeout(c, 5*time.Second)
	defer cancel()
	_, err := client.Do(c, api.Request{Method: http.MethodDelete, Path: "/me", NoRetry: true}, nil)
	var p *api.Problem
	switch {
	case err == nil:
		return keyRevoked, nil
	case errors.As(err, &p) && p.Status == http.StatusUnauthorized:
		return keyGone, nil
	case errors.As(err, &p) && (p.Status == http.StatusNotFound || p.Status == http.StatusMethodNotAllowed):
		return keyUnrevoked, nil
	}
	return keyUnrevoked, err
}

func newStatusCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show which organization and key the CLI acts with",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var me api.Me
			res, err := c.Do(ctx(cmd), api.Request{Method: http.MethodGet, Path: "/me"}, &me)
			if err != nil {
				return err
			}
			if a.out.JSON {
				a.out.RawJSON(res.Body)
				return nil
			}
			source := fmt.Sprintf("profile %s", a.profileName())
			if os.Getenv("PANTECH_API_KEY") != "" {
				source = "PANTECH_API_KEY"
			}
			profile := a.cfg.Profiles[a.profileName()]
			title := me.OrganizationID
			subtitle := ""
			if profile.OrganizationName != "" && profile.OrganizationID == me.OrganizationID {
				title, subtitle = profile.OrganizationName, me.OrganizationID
			}
			expires := "never"
			if me.APIKey.ExpiresAt != nil {
				expires = output.When(me.APIKey.ExpiresAt)
			}
			a.out.Print(output.Detail{
				Title:    title,
				State:    a.out.State("active"),
				Subtitle: subtitle,
				Sections: [][]output.Pair{
					{{"Key", me.APIKey.Name + "  " + a.out.Dim(me.APIKey.ID)}, {"Access", strings.Join(me.APIKey.Scopes, ", ")}, {"Expires", expires}},
					{{"API", c.BaseURL}, {"From", source}},
				},
			})
			return nil
		},
	}
}
