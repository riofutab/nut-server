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

func TestCheckSudoKeepsUserAndGroupAndDropsOtherOptions(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want []string
	}{
		{
			name: "user and group with values",
			argv: []string{"sudo", "-n", "-u", "root", "-g", "wheel", "-p", "prompt", "/sbin/poweroff"},
			want: []string{"-n", "-l", "-u", "root", "-g", "wheel", "/sbin/poweroff"},
		},
		{
			name: "value options dropped with their values",
			argv: []string{"/usr/bin/sudo", "-C", "3", "-D", "/tmp", "-h", "host", "-r", "role", "-t", "type", "-T", "5", "-U", "bob", "/sbin/shutdown", "-h", "now"},
			want: []string{"-n", "-l", "/sbin/shutdown", "-h", "now"},
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
	path := filepath.Join(t.TempDir(), "shutdown")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), perm); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.Chmod(path, perm); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	return path
}
