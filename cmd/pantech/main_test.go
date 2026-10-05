package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
)

func TestExitCode(t *testing.T) {
	code := "payment_expired"
	for name, c := range map[string]struct {
		err  error
		want int
	}{
		"unauthenticated":  {&api.Problem{Status: 401}, 3},
		"forbidden":        {&api.Problem{Status: 403}, 3},
		"not found":        {fmt.Errorf("reading: %w", &api.Problem{Status: 404}), 4},
		"operation failed": {&api.OperationFailed{Op: &api.Operation{ID: "op_1"}}, 5},
		"order failed":     {&api.OrderFailed{Order: &api.Order{ID: "ord_1", Status: "payment_failed", FailureCode: &code}}, 5},
		"wrapped order":    {fmt.Errorf("creating: %w", &api.OrderFailed{Order: &api.Order{ID: "ord_1", Status: "failed"}}), 5},
		"conflict":         {fmt.Errorf("attaching: %w", &api.Problem{Status: 409, Code: "SECURITY_GROUP_ALLOWS_PRIVATE_NETWORK"}), 1},
		"validation":       {&api.Problem{Status: 422, Code: "VALIDATION_FAILED"}, 1},
		"anything else":    {errors.New("boom"), 1},
	} {
		if got := exitCode(c.err); got != c.want {
			t.Errorf("%s: exitCode = %d, want %d", name, got, c.want)
		}
	}
}
