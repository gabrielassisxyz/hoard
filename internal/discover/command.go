package discover

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/gabrielassisxyz/hoard/internal/fetch"
	"github.com/gabrielassisxyz/hoard/internal/recipe"
)

// How long a site's own command may take. A variable so a test need not wait it out.
var commandTimeout = 10 * time.Minute

// addressesFromCommand runs the program the recipe names and reads one address per line
// from what it prints. It runs without a shell, in the recipe's directory, so a recipe can
// name a script that sits beside it.
func addressesFromCommand(ctx context.Context, r recipe.Recipe) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, r.Discover.Command[0], r.Discover.Command[1:]...)
	command.Dir = r.Dir
	command.Stderr = os.Stderr
	// Without this a command that left a child holding its output open would be waited on
	// long after the timeout killed it.
	command.WaitDelay = time.Second
	var printed bytes.Buffer
	command.Stdout = &printed
	if err := command.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("command %q did not finish within %s", r.Discover.Command[0], commandTimeout)
		}
		return nil, fmt.Errorf("command %q: %w", r.Discover.Command[0], err)
	}
	addresses, err := fetch.ParseList(&printed)
	if err != nil {
		return nil, fmt.Errorf("output of command %q: %w", r.Discover.Command[0], err)
	}
	return addresses, nil
}
