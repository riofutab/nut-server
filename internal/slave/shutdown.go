package slave

import (
	"fmt"
	"log"
	"os/exec"
)

type ShutdownExecutor struct {
	Command []string
	DryRun  bool
}

func (s ShutdownExecutor) Execute(logger *log.Logger) error {
	if len(s.Command) == 0 {
		return fmt.Errorf("shutdown command is empty")
	}
	if s.DryRun {
		if logger != nil {
			logger.Printf("dry-run enabled, skip shutdown command: %v", s.Command)
		}
		return nil
	}
	cmd := exec.Command(s.Command[0], s.Command[1:]...)
	return cmd.Run()
}
