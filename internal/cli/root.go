// Package cli is the pantech command tree.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/config"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

// Version is set at build time: -ldflags "-X …/internal/cli.Version=v0.1.0".
var Version = "dev"

const DefaultConsoleURL = "https://console.pantechdynamics.com"

type app struct {
	profile string
	jsonOut bool
	quiet   bool
	yes     bool

	cfg *config.Config
	out *output.Printer

	// apiURL replaces the production API, for tests only: there is no flag
	// or variable for it.
	apiURL string

	newerVersion <-chan string
}

// NewRoot builds the command tree. Call notice once the command has
// finished, and printed any error, to say if a newer version is out.
func NewRoot() (root *cobra.Command, notice func()) {
	a := &app{}
	return newRoot(a), func() { a.updateNotice(a.newerVersion) }
}

func newRoot(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:   "pantech",
		Short: "Manage Pantech Dynamics cloud from your terminal",
		Long: `Manage Pantech Dynamics cloud from your terminal: virtual machines, managed
databases, managed Kubernetes, volumes, snapshots, networks, public IPs, load
balancers, security groups, SSH keys and the catalogue, through the public API.

Sign in with "pantech auth login". In CI, set PANTECH_API_KEY instead.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if a.cfg == nil {
				cfg, err := config.Load()
				if err != nil {
					return err
				}
				a.cfg = cfg
			}
			if a.out == nil {
				a.out = output.New(a.jsonOut, a.quiet)
			}
			a.out.JSON, a.out.Quiet = a.jsonOut, a.quiet
			a.newerVersion = a.startUpdateCheck(cmd)
			return nil
		},
	}
	root.SetVersionTemplate("pantech {{.Version}}\n")

	f := root.PersistentFlags()
	f.StringVarP(&a.profile, "profile", "p", os.Getenv("PANTECH_PROFILE"), "profile to use (default: the current one)")
	f.BoolVar(&a.jsonOut, "json", false, "print the API's JSON instead of a table")
	f.BoolVarP(&a.quiet, "quiet", "q", false, "print only ids")
	f.BoolVarP(&a.yes, "yes", "y", false, "do not ask before changes that delete or cost money")

	root.AddCommand(
		newAuthCmd(a),
		newVMCmd(a),
		newDBCmd(a),
		newVolumesCmd(a),
		newSnapshotsCmd(a),
		newNetworksCmd(a),
		newPublicIPsCmd(a),
		newLoadBalancersCmd(a),
		newKubernetesCmd(a),
		newSecurityGroupsCmd(a),
		newSSHKeysCmd(a),
		newPlansCmd(a),
		newImagesCmd(a),
		newRegionsCmd(a),
		newOperationsCmd(a),
		newAPICmd(a),
		newUpgradeCmd(a),
	)
	markUsageErrors(root)
	return root
}

type usageError struct {
	err error
	cmd *cobra.Command
}

func (e *usageError) Error() string {
	return fmt.Sprintf("%v\nSee: %s --help", e.err, e.cmd.CommandPath())
}
func (e *usageError) Unwrap() error { return e.err }

// IsUsage reports whether err is the command used wrongly, so main can exit 2.
func IsUsage(err error) bool {
	var u *usageError
	return errors.As(err, &u) || strings.HasPrefix(err.Error(), "unknown command")
}

func IsNotSignedIn(err error) bool { return errors.Is(err, errNotSignedIn) }

func markUsageErrors(cmd *cobra.Command) {
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error { return &usageError{err, c} })
	if check := cmd.Args; check != nil {
		cmd.Args = func(c *cobra.Command, args []string) error {
			if err := check(c, args); err != nil {
				return &usageError{err, c}
			}
			return nil
		}
	}
	for _, sub := range cmd.Commands() {
		markUsageErrors(sub)
	}
}

func (a *app) profileName() string { return a.cfg.Name(a.profile) }

func (a *app) baseURL() string {
	if a.apiURL != "" {
		return a.apiURL
	}
	return api.DefaultBaseURL
}

var errNotSignedIn = errors.New(`not signed in: run "pantech auth login", or set PANTECH_API_KEY`)

// client is an API client with the key in effect: $PANTECH_API_KEY, else the profile's.
func (a *app) client() (*api.Client, error) {
	key := os.Getenv("PANTECH_API_KEY")
	if key == "" {
		stored, err := config.LoadKey(a.profileName())
		if err != nil {
			return nil, fmt.Errorf("reading the stored key: %w", err)
		}
		key = stored
	}
	if key == "" {
		return nil, errNotSignedIn
	}
	if os.Getenv("PANTECH_API_KEY") == "" {
		if p := a.cfg.Profiles[a.profileName()]; p.APIURL != "" && p.APIURL != api.DefaultBaseURL {
			return nil, &otherAPIError{profile: a.profileName(), apiURL: p.APIURL, login: a.loginCommand()}
		}
	}
	return api.New(a.baseURL(), key, userAgent()), nil
}

type otherAPIError struct{ profile, apiURL, login string }

func (e *otherAPIError) Error() string {
	return fmt.Sprintf("profile %q was signed in on %s, and its key works only there: this CLI always uses the production API\nSign in again: %s", e.profile, e.apiURL, e.login)
}

func (e *otherAPIError) Is(target error) bool { return target == errNotSignedIn }

func (a *app) loginCommand() string {
	if a.profile == "" {
		return "pantech auth login"
	}
	return "pantech --profile " + a.profile + " auth login"
}

func userAgent() string {
	return fmt.Sprintf("pantech-cli/%s (%s; %s)", Version, runtime.GOOS, runtime.GOARCH)
}

// confirm asks before something that deletes or costs money. --yes skips
// it; with no terminal to ask in, it refuses rather than guess.
func (a *app) confirm(question string) error {
	if a.yes {
		return nil
	}
	if !output.Interactive() {
		return errors.New(question + " Pass --yes to go ahead without asking.")
	}
	fmt.Fprintf(os.Stderr, "%s [y/N] ", question)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return errCancelled
}

var errCancelled = errors.New("cancelled")

type exitStatus struct{ code int }

func (e *exitStatus) Error() string { return fmt.Sprintf("exit status %d", e.code) }

// ExitStatus reports the exit code of a program the CLI ran in the
// foreground (ssh), which main exits with silently.
func ExitStatus(err error) (int, bool) {
	var e *exitStatus
	if errors.As(err, &e) {
		return e.code, true
	}
	return 0, false
}

func ctx(cmd *cobra.Command) context.Context { return cmd.Context() }
