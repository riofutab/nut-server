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

// sudoRunAsOptions select the run-as user or group, which changes which
// sudoers rule matches, so they are passed on to the `sudo -l` query. Other
// sudo options (-D, -R, -C, -T, ...) are rejected rather than modelled: each
// has its own argument rules and sudoers checks, and guessing them wrong gives
// a false pass or fail.
var sudoRunAsOptions = map[string]string{
	"-u": "-u", "--user": "-u",
	"-g": "-g", "--group": "-g",
}

// rootOnlyCommands are executable basenames that cannot power the host off
// without root (polkit refuses a service user), so a non-sudo command naming
// one would only fail when the power is actually going. Other custom commands
// (scripts, ipmitool, ...) may work without root and are left alone.
var rootOnlyCommands = map[string]bool{
	"shutdown": true, "poweroff": true, "halt": true, "reboot": true, "systemctl": true,
}

// geteuid is replaced in tests.
var geteuid = os.Geteuid

// Check verifies argv can be executed as the shutdown command without running it.
// run == nil uses os/exec. Bounded by an internal timeout constant (10s).
func Check(ctx context.Context, argv []string, run Runner) error {
	if len(argv) == 0 {
		return errors.New("shutdown command is empty")
	}
	if filepath.Base(argv[0]) != "sudo" {
		if rootOnlyCommands[filepath.Base(argv[0])] && geteuid() != 0 {
			return fmt.Errorf("shutdown command %q needs root but this process is not root; prefix it with /usr/bin/sudo -n (e.g. /usr/bin/sudo -n /sbin/shutdown -h now, allowed by the shipped sudoers entry in /etc/sudoers.d/nut-server-*)", strings.Join(argv, " "))
		}
		return checkExecutable(argv[0])
	}
	if run == nil {
		run = execRunner
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	return checkSudo(ctx, argv, run)
}

// checkSudo asks `sudo -n -l <cmd>` whether cmd may run without a password.
// Limitation: with the default listpw=any, sudo -l itself needs no password
// when any rule for the user has NOPASSWD, so this passes for a command that
// is allowed but still needs a password if another rule is NOPASSWD. The
// shipped sudoers files grant only NOPASSWD shutdown rules, so this does not
// arise with them; parsing `sudo -l` output to match tags per command is not
// reliable across aliases, wildcards and sudo versions.
func checkSudo(ctx context.Context, argv []string, run Runner) error {
	kept, target, err := splitSudoArgs(argv[1:])
	if err != nil {
		return err
	}
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

// splitSudoArgs separates the supported sudo options (-n and the run-as
// options, normalized to -u/-g with their values) from the target command.
func splitSudoArgs(args []string) (kept, target []string, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return kept, args[i+1:], nil
		case !strings.HasPrefix(arg, "-"):
			return kept, args[i:], nil
		case arg == "-n":
			// The check always runs non-interactively.
		case sudoRunAsOptions[arg] != "":
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("sudo option %s needs a value", arg)
			}
			kept = append(kept, sudoRunAsOptions[arg], args[i+1])
			i++
		default:
			option, value, ok := splitAttachedValue(arg)
			if !ok {
				return nil, nil, fmt.Errorf("sudo option %s is not supported by the shutdown self-check; it supports only -n, -u/--user and -g/--group", arg)
			}
			kept = append(kept, option, value)
		}
	}
	return kept, nil, nil
}

// splitAttachedValue parses -uroot, --user=root and their group forms.
func splitAttachedValue(arg string) (option, value string, ok bool) {
	if name, value, found := strings.Cut(arg, "="); found && strings.HasPrefix(arg, "--") {
		if option := sudoRunAsOptions[name]; option != "" && value != "" {
			return option, value, true
		}
		return "", "", false
	}
	if len(arg) > 2 && !strings.HasPrefix(arg, "--") {
		if option := sudoRunAsOptions[arg[:2]]; option != "" {
			return option, arg[2:], true
		}
	}
	return "", "", false
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
