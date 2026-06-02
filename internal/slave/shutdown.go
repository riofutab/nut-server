package slave

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
)

type ShutdownExecutor interface {
	Execute(ctx context.Context, logger *slog.Logger) error
}

type CommandShutdownExecutor struct {
	Command []string
	DryRun  bool
}

func (s CommandShutdownExecutor) Execute(ctx context.Context, logger *slog.Logger) error {
	if len(s.Command) == 0 {
		return fmt.Errorf("shutdown command is empty")
	}
	if s.DryRun {
		if logger != nil {
			logger.Info("dry-run skip shutdown command", "command", s.Command)
		}
		return nil
	}
	// CommandContext kills the process when ctx is cancelled (timeout), turning a
	// hung shutdown script into a bounded, retriable failure instead of leaving
	// the node stuck in "executing" forever.
	cmd := exec.CommandContext(ctx, s.Command[0], s.Command[1:]...)
	return cmd.Run()
}
