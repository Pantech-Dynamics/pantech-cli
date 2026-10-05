package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Pantech-Dynamics/pantech-cli/internal/config"
	"github.com/Pantech-Dynamics/pantech-cli/internal/update"
)

const installCmd = "curl -fsSL https://pantechdynamics.com/install | bash"

func newUpgradeCmd(a *app) *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:     "upgrade [version]",
		Aliases: []string{"update"},
		Short:   "Install the latest version of the CLI",
		Long: `Install the latest version of the CLI, or the version given, in place of
this one. The download is checked against the release's SHA256SUMS first.

The CLI says when a newer version is out, at most once a day. Set
PANTECH_NO_UPDATE_NOTIFIER=1 to turn that off.`,
		Example: `  pantech upgrade
  pantech upgrade --check
  pantech upgrade v0.1.5`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			latest, err := update.Latest(ctx(cmd))
			if err != nil {
				return err
			}
			saveUpdateState(latest)
			target := latest
			if len(args) == 1 {
				target = args[0]
				if !strings.HasPrefix(target, "v") {
					target = "v" + target
				}
			}

			if check {
				if update.Newer(Version, latest) {
					a.out.Line("%s", latest)
					a.out.Next(fmt.Sprintf("pantech %s is out (you have %s). Install it", latest, Version), "pantech upgrade")
				} else {
					a.out.Line("%s", Version)
					a.out.Note("pantech %s is the latest version.", Version)
				}
				return nil
			}
			if len(args) == 0 {
				if !update.IsRelease(Version) {
					return fmt.Errorf("this pantech (%s) is not a release build; to replace it with %s, run: pantech upgrade %s", Version, latest, latest)
				}
				if !update.Newer(Version, latest) {
					a.out.Note("pantech %s is the latest version.", Version)
					return nil
				}
			}
			if target == Version {
				a.out.Note("pantech %s is already installed.", Version)
				return nil
			}

			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if exe, err = filepath.EvalSymlinks(exe); err != nil {
				return err
			}
			steps := a.out.Steps()
			steps.Start(fmt.Sprintf("Installing pantech %s", target))
			if err := update.Install(ctx(cmd), target, exe); err != nil {
				steps.Fail("", "")
				if errors.Is(err, update.ErrNotWritable) {
					return fmt.Errorf("%w\nRun it with sudo, or reinstall to your home directory: %s", err, installCmd)
				}
				return err
			}
			steps.Done(fmt.Sprintf("Installed pantech %s", target), exe)
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only print the latest version")
	return cmd
}

func updateStatePath() string {
	dir, err := config.Dir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "update-check.json")
}

func saveUpdateState(latest string) {
	if p := updateStatePath(); p != "" {
		update.SaveState(p, update.State{CheckedAt: time.Now(), Latest: latest})
	}
}

// startUpdateCheck looks for a newer version while the command runs: from
// the last day's answer if there is one, else from the network. It returns
// nil when the notice is not wanted.
func (a *app) startUpdateCheck(cmd *cobra.Command) <-chan string {
	switch {
	case !update.IsRelease(Version),
		a.out.JSON, a.out.Quiet,
		os.Getenv("PANTECH_NO_UPDATE_NOTIFIER") != "",
		os.Getenv("CI") != "",
		!term.IsTerminal(int(os.Stderr.Fd())):
		return nil
	}
	switch cmd.Name() {
	case "upgrade", "help", "completion", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
		return nil
	}
	path := updateStatePath()
	if path == "" {
		return nil
	}
	found := make(chan string, 1)
	if s := update.LoadState(path); time.Since(s.CheckedAt) < update.CheckInterval {
		found <- s.Latest
		return found
	}
	go func() {
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		latest, err := update.Latest(c)
		if err == nil {
			update.SaveState(path, update.State{CheckedAt: time.Now(), Latest: latest})
		}
		found <- latest
	}()
	return found
}

// updateNotice prints the notice if a newer version was found. A check on
// the network, once a day, is waited for up to two seconds; one that takes
// longer is dropped and runs again next time.
func (a *app) updateNotice(found <-chan string) {
	if found == nil {
		return
	}
	select {
	case latest := <-found:
		if update.Newer(Version, latest) {
			a.out.Note("")
			a.out.Note("pantech %s is out (you have %s).", latest, Version)
			a.out.Next("Upgrade", "pantech upgrade")
		}
	case <-time.After(2 * time.Second):
	}
}
