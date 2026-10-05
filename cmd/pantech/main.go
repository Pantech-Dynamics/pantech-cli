// Command pantech manages Pantech Dynamics cloud from the terminal.
package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/cli"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

// Exit codes, for scripts:
//
//	1  anything else
//	2  bad usage (cobra's own errors)
//	3  not signed in, or the key was refused (401) or lacks the scope (403)
//	4  not found (404)
//	5  an operation or order finished, but failed
//	130 interrupted
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := cli.NewRoot().ExecuteContext(ctx)
	if err == nil {
		return
	}
	if errors.Is(err, context.Canceled) {
		os.Exit(130)
	}
	// A command that ran another program (ssh) exits with its code, having
	// nothing of its own to say.
	if code, ok := cli.ExitStatus(err); ok {
		os.Exit(code)
	}
	output.New(false, false).Error(err)
	os.Exit(exitCode(err))
}

func exitCode(err error) int {
	var problem *api.Problem
	var failed *api.OperationFailed
	var orderFailed *api.OrderFailed
	switch {
	case cli.IsNotSignedIn(err):
		return 3
	case errors.As(err, &problem) && (problem.Status == 401 || problem.Status == 403):
		return 3
	case errors.As(err, &problem) && problem.Status == 404:
		return 4
	case errors.As(err, &failed), errors.As(err, &orderFailed):
		return 5
	case cli.IsUsage(err):
		return 2
	}
	return 1
}
