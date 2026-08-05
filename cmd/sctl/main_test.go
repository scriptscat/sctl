package main

import (
	"errors"
	"testing"

	"github.com/scriptscat/sctl/internal/cli"
)

func TestExitCodeReservesOneForExplicitUserRejection(t *testing.T) {
	t.Parallel()

	if got := exitCode(errors.New("unknown command")); got != 3 {
		t.Fatalf("ordinary command error exit code = %d, want 3", got)
	}
	if got := exitCode(&cli.ExitError{Code: 1, Message: "rejected"}); got != 1 {
		t.Fatalf("explicit rejection exit code = %d, want 1", got)
	}
}
