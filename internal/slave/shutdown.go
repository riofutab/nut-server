package slave

import (
	"fmt"
	"log/slog"
	"os/exec"
)

type ShutdownExecutor interface {
	Execute(logger *slog.Logger) error
}

type CommandShutdownExecutor struct {
	Command []string
	DryRun  bool
}

func (s CommandShutdownExecutor) Execute(logger *slog.Logger) error {
	if len(s.Command) == 0 {
		return fmt.Errorf("shutdown command is empty")
	}
	if s.DryRun {
		if logger != nil {
			logger.Info("dry-run skip shutdown command", "command", s.Command)
		}
		return nil
	}
	cmd := exec.Command(s.Command[0], s.Command[1:]...)
	return cmd.Run()
}
