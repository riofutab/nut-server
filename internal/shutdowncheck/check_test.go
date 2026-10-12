package shutdowncheck

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type recordedCall struct {
	name string
	args []string
}

func recordingRunner(result error, calls *[]recordedCall) Runner {
	return func(_ context.Context, name string, args ...string) error {
		*calls = append(*calls, recordedCall{name: name, args: append([]string(nil), args...)})
		return result
	}
}

func TestCheckSudoAllowedRunsNonInteractiveListForTargetCommand(t *testing.T) {
	var calls []recordedCall
	argv := []string{"/usr/bin/sudo", "-n", "/sbin/shutdown", "-h", "now"}

	err := Check(context.Background(), argv, recordingRunner(nil, &calls))

	if err != nil {
		t.Fatalf("expected allowed sudo command to pass, got %v", err)
	}
	want := []recordedCall{{name: "/usr/bin/sudo", args: []string{"-n", "-l", "/sbin/shutdown", "-h", "now"}}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%+v want %+v", calls, want)
	}
}

func TestCheckSudoDeniedReturnsErrorNamingTargetCommand(t *testing.T) {
	var calls []recordedCall
	argv := []string{"/usr/bin/sudo", "-n", "/sbin/shutdown", "-h", "now"}

	err := Check(context.Background(), argv, recordingRunner(errors.New("exit status 1"), &calls))

	if err == nil {
		t.Fatalf("expected denied sudo command to fail")
	}
	if !strings.Contains(err.Error(), "/sbin/shutdown -h now") {
		t.Fatalf("expected error to name the target command, got %v", err)
	}
}

func TestCheckSudoTranslatesSupportedOptionsIntoListQuery(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want []string
	}{
		{
			name: "user and group with values",
			argv: []string{"sudo", "-n", "-u", "root", "-g", "wheel", "/sbin/poweroff"},
			want: []string{"-n", "-l", "-u", "root", "-g", "wheel", "/sbin/poweroff"},
		},
		{
			name: "long user and group with equals",
			argv: []string{"sudo", "--user=nut", "--group=wheel", "/sbin/poweroff"},
			want: []string{"-n", "-l", "-u", "nut", "-g", "wheel", "/sbin/poweroff"},
		},
		{
			name: "long user and group with separate values",
			argv: []string{"sudo", "--user", "root", "--group", "wheel", "/sbin/poweroff"},
			want: []string{"-n", "-l", "-u", "root", "-g", "wheel", "/sbin/poweroff"},
		},
		{
			name: "double dash ends options",
			argv: []string{"sudo", "-n", "--", "-weird-command", "arg"},
			want: []string{"-n", "-l", "-weird-command", "arg"},
		},
		{
			name: "attached user value",
			argv: []string{"sudo", "-uroot", "/sbin/shutdown"},
			want: []string{"-n", "-l", "-u", "root", "/sbin/shutdown"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []recordedCall

			err := Check(context.Background(), tc.argv, recordingRunner(nil, &calls))

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(calls) != 1 || calls[0].name != tc.argv[0] {
				t.Fatalf("expected one call to %s, got %+v", tc.argv[0], calls)
			}
			if !reflect.DeepEqual(calls[0].args, tc.want) {
				t.Fatalf("args=%q want %q", calls[0].args, tc.want)
			}
		})
	}
}

func TestCheckSudoRejectsUnsupportedOptions(t *testing.T) {
	cases := []struct {
		name       string
		argv       []string
		wantOption string
	}{
		{name: "chdir", argv: []string{"sudo", "-D", "/tmp", "/sbin/shutdown"}, wantOption: "-D"},
		{name: "chroot", argv: []string{"sudo", "-R", "/srv", "/sbin/shutdown"}, wantOption: "-R"},
		{name: "close from", argv: []string{"sudo", "-C", "3", "/sbin/shutdown"}, wantOption: "-C"},
		{name: "command timeout", argv: []string{"sudo", "-T", "5", "/sbin/shutdown"}, wantOption: "-T"},
		{name: "prompt", argv: []string{"sudo", "-p", "pw:", "/sbin/shutdown"}, wantOption: "-p"},
		{name: "combined short options", argv: []string{"sudo", "-nu", "root", "/sbin/shutdown"}, wantOption: "-nu"},
		{name: "unknown long option", argv: []string{"sudo", "--chdir=/tmp", "/sbin/shutdown"}, wantOption: "--chdir=/tmp"},
		{name: "user without value", argv: []string{"sudo", "-n", "-u"}, wantOption: "-u"},
		{name: "long user without value", argv: []string{"sudo", "--user"}, wantOption: "--user"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []recordedCall

			err := Check(context.Background(), tc.argv, recordingRunner(nil, &calls))

			if err == nil || !strings.Contains(err.Error(), tc.wantOption) {
				t.Fatalf("expected error naming %s, got %v", tc.wantOption, err)
			}
			if len(calls) != 0 {
				t.Fatalf("expected runner not to be called, got %+v", calls)
			}
		})
	}
}

func TestCheckSudoWithoutTargetCommandFails(t *testing.T) {
	var calls []recordedCall

	err := Check(context.Background(), []string{"/usr/bin/sudo", "-n", "-u", "root"}, recordingRunner(nil, &calls))

	if err == nil {
		t.Fatalf("expected error when sudo has no target command")
	}
	if len(calls) != 0 {
		t.Fatalf("expected runner not to be called, got %+v", calls)
	}
}

func TestCheckEmptyCommandFails(t *testing.T) {
	if err := Check(context.Background(), nil, nil); err == nil {
		t.Fatalf("expected error for empty command")
	}
}

func TestCheckNonSudoAbsoluteExecutablePasses(t *testing.T) {
	path := writeFile(t, 0o755)
	var calls []recordedCall

	err := Check(context.Background(), []string{path, "-h", "now"}, recordingRunner(errors.New("must not run"), &calls))

	if err != nil {
		t.Fatalf("expected executable file to pass, got %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("expected non-sudo check not to run anything, got %+v", calls)
	}
}

func TestCheckNonSudoRejectsUnusablePaths(t *testing.T) {
	cases := map[string]string{
		"relative path":  "shutdown",
		"missing file":   filepath.Join(t.TempDir(), "missing"),
		"not executable": writeFile(t, 0o644),
		"directory":      t.TempDir(),
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			if err := Check(context.Background(), []string{path, "now"}, nil); err == nil {
				t.Fatalf("expected %s to fail", path)
			}
		})
	}
}

func writeFile(t *testing.T, perm os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ups-poweroff.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), perm); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.Chmod(path, perm); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	return path
}

// withEUID makes Check see euid for the duration of the test.
func withEUID(t *testing.T, euid int) {
	t.Helper()
	previous := geteuid
	geteuid = func() int { return euid }
	t.Cleanup(func() { geteuid = previous })
}

func TestCheckNonSudoRootOnlyCommandFailsForNonRoot(t *testing.T) {
	withEUID(t, 1000)
	for _, argv := range [][]string{
		{"/sbin/shutdown", "-h", "now"},
		{"/usr/sbin/shutdown", "-h", "now"},
		{"/sbin/poweroff"},
		{"/sbin/halt"},
		{"/sbin/reboot"},
		{"/usr/bin/systemctl", "poweroff"},
	} {
		t.Run(argv[0], func(t *testing.T) {
			err := Check(context.Background(), argv, nil)

			if err == nil || !strings.Contains(err.Error(), "sudo -n") || !strings.Contains(err.Error(), "sudoers") {
				t.Fatalf("expected non-root %s to fail with a sudo hint, got %v", argv[0], err)
			}
		})
	}
}

func TestCheckNonSudoRootOnlyCommandPassesForRoot(t *testing.T) {
	withEUID(t, 0)
	path := filepath.Join(t.TempDir(), "shutdown")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write file: %v", err)
	}

	err := Check(context.Background(), []string{path, "-h", "now"}, nil)

	if err != nil {
		t.Fatalf("expected root to run %s directly, got %v", path, err)
	}
}

func TestCheckNonSudoCustomCommandPassesForNonRoot(t *testing.T) {
	withEUID(t, 1000)
	custom := writeFile(t, 0o755)

	err := Check(context.Background(), []string{custom}, nil)

	if err != nil {
		t.Fatalf("expected custom script to pass for non-root, got %v", err)
	}
}
