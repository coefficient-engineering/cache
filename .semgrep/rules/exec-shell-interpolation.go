package rules

import (
	"context"
	"os/exec"
)

func run(ctx context.Context, name string) {
	// ruleid: exec-shell-interpolation
	_ = exec.Command("sh", "-c", "echo "+name)
	// ruleid: exec-shell-interpolation
	_ = exec.CommandContext(ctx, "/bin/bash", "-c", name)
	// ok: exec-shell-interpolation
	_ = exec.Command("sh", "-c", "echo hello")
	// ok: exec-shell-interpolation
	_ = exec.Command("echo", name)
}
