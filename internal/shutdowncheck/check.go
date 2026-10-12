// Package shutdowncheck verifies at startup that the configured shutdown
// command could actually run, without running it.
package shutdowncheck

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// checkTimeout bounds the whole check so a hung sudo (e.g. waiting on a
// misconfigured PAM/LDAP backend) cannot stall process startup.
const checkTimeout = 10 * time.Second

// Runner executes name with args and returns nil only on exit status 0.
type Runner func(ctx context.Context, name string, args ...string) error

// sudoValueOptions are sudo short options that consume the next argument.
var sudoValueOptions = map[string]bool{
	"-u": true, "-g": true, "-U": true, "-C": true, "-D": true,
	"-h": true, "-p": true, "-r": true, "-t": true, "-T": true,
}

// sudoKeptOptions change which sudoers rule matches, so they must be part of
// the `sudo -l` query; the other options do not affect authorization.
var sudoKeptOptions = map[string]bool{"-u": true, "-g": true}

// Check verifies argv can be executed as the shutdown command without running it.
// run == nil uses os/exec. Bounded by an internal timeout constant (10s).
func Check(ctx context.Context, argv []string, run Runner) error {
	if len(argv) == 0 {
		return errors.New("shutdown command is empty")
	}
	if filepath.Base(argv[0]) != "sudo" {
		return checkExecutable(argv[0])
	}
	if run == nil {
		run = execRunner
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	return checkSudo(ctx, argv, run)
}

func checkSudo(ctx context.Context, argv []string, run Runner) error {
	kept, target := splitSudoArgs(argv[1:])
	if len(target) == 0 {
		return fmt.Errorf("sudo shutdown command %q has no target command", strings.Join(argv, " "))
	}
	args := append([]string{"-n", "-l"}, kept...)
	args = append(args, target...)
	if err := run(ctx, argv[0], args...); err != nil {
		return fmt.Errorf("sudo does not allow running %q non-interactively: %w", strings.Join(target, " "), err)
	}
	return nil
}

// splitSudoArgs separates sudo options (returning only those relevant to the
// authorization check, with their values) from the target command.
func splitSudoArgs(args []string) (kept, target []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return kept, args[i+1:]
		}
		if !strings.HasPrefix(arg, "-") {
			return kept, args[i:]
		}
		if sudoValueOptions[arg] {
			if i+1 < len(args) && sudoKeptOptions[arg] {
				kept = append(kept, arg, args[i+1])
			}
			i++
			continue
		}
		// Attached form such as -uroot; dropping it would silently check the
		// wrong run-as user.
		if prefix := arg[:min(len(arg), 2)]; len(arg) > 2 && sudoKeptOptions[prefix] {
			kept = append(kept, prefix, arg[2:])
		}
	}
	return kept, nil
}

func checkExecutable(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("shutdown command %q must be an absolute path", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("shutdown command %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("shutdown command %q is not a regular file", path)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("shutdown command %q is not executable", path)
	}
	return nil
}

func execRunner(ctx context.Context, name string, args ...string) error {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(output)); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}
