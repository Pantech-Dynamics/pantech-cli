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

// DefaultConsoleURL is where `pantech auth login` signs in.
const DefaultConsoleURL = "https://console.pantechdynamics.com"

// app is what every command shares: the flags, the config and a printer.
type app struct {
	profile string
	apiURL  string
	jsonOut bool
	quiet   bool
	yes     bool

	cfg *config.Config
	out *output.Printer
}

// NewRoot builds the command tree.
func NewRoot() *cobra.Command {
	a := &app{}
	root := &cobra.Command{
		Use:   "pantech",
		Short: "Manage Pantech Dynamics cloud from your terminal",
		Long: `Manage Pantech Dynamics cloud from your terminal: virtual machines, SSH keys
and the catalogue, through the public API.

Sign in with "pantech auth login". In CI, set PANTECH_API_KEY instead.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			a.cfg = cfg
			a.out = output.New(a.jsonOut, a.quiet)
			return nil
		},
	}
	root.SetVersionTemplate("pantech {{.Version}}\n")

	f := root.PersistentFlags()
	f.StringVarP(&a.profile, "profile", "p", os.Getenv("PANTECH_PROFILE"), "profile to use (default: the current one)")
	f.StringVar(&a.apiURL, "api-url", "", "API to call, e.g. https://api-dev.pantechdynamics.com (env PANTECH_API_URL)")
	// For the team, against api-dev or a local stack: it works, but help does
	// not offer it, as customers never need it (see README, Development).
	_ = f.MarkHidden("api-url")
	f.BoolVar(&a.jsonOut, "json", false, "print the API's JSON instead of a table")
	f.BoolVarP(&a.quiet, "quiet", "q", false, "print only ids")
	f.BoolVarP(&a.yes, "yes", "y", false, "do not ask before changes that delete or cost money")

	root.AddCommand(
		newAuthCmd(a),
		newVMCmd(a),
		newSSHKeysCmd(a),
		newPlansCmd(a),
		newImagesCmd(a),
		newRegionsCmd(a),
		newOperationsCmd(a),
		newAPICmd(a),
	)
	markUsageErrors(root)
	return root
}

// usageError is the command used wrongly: a bad flag or the wrong arguments.
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

// IsNotSignedIn reports whether err is there being no key to use.
func IsNotSignedIn(err error) bool { return errors.Is(err, errNotSignedIn) }

// markUsageErrors wraps every command's flag and argument errors as usage errors.
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

// profileName is the profile in use.
func (a *app) profileName() string { return a.cfg.Name(a.profile) }

// profileOrDefault is the profile's settings, with the defaults filled in.
func (a *app) profileOrDefault() config.Profile {
	p := a.cfg.Profiles[a.profileName()]
	if p.APIURL == "" {
		p.APIURL = api.DefaultBaseURL
	}
	if p.ConsoleURL == "" {
		p.ConsoleURL = DefaultConsoleURL
	}
	return p
}

// baseURL is --api-url, else $PANTECH_API_URL, else the profile's, else production.
func (a *app) baseURL() string {
	switch {
	case a.apiURL != "":
		return a.apiURL
	case os.Getenv("PANTECH_API_URL") != "":
		return os.Getenv("PANTECH_API_URL")
	default:
		return a.profileOrDefault().APIURL
	}
}

// errNotSignedIn is returned by client() when there is no key to use.
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
	return api.New(a.baseURL(), key, userAgent()), nil
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

// ctx is the command's context.
func ctx(cmd *cobra.Command) context.Context { return cmd.Context() }
