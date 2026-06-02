package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadLimitedLine(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		max     int
		want    string
		wantErr error
	}{
		{name: "simple line", input: "hello\n", max: 16, want: "hello"},
		{name: "empty line", input: "\n", max: 16, want: ""},
		{name: "exactly at limit", input: "abcd\n", max: 4, want: "abcd"},
		{name: "over limit before newline", input: "abcde\n", max: 4, wantErr: ErrEnvelopeTooLarge},
		{name: "no newline is EOF", input: "abc", max: 16, wantErr: io.EOF},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := bufio.NewReader(strings.NewReader(tc.input))
			got, err := readLimitedLine(reader, tc.max)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("want error %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("want %q, got %q", tc.want, string(got))
			}
		})
	}
}

func TestReadLimitedLineConsumesMultipleFrames(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("one\ntwo\nthree\n"))
	for _, want := range []string{"one", "two", "three"} {
		got, err := readLimitedLine(reader, 16)
		if err != nil {
			t.Fatalf("read %q: %v", want, err)
		}
		if string(got) != want {
			t.Fatalf("want %q, got %q", want, string(got))
		}
	}
	if _, err := readLimitedLine(reader, 16); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after last frame, got %v", err)
	}
}

func TestReadEnvelope(t *testing.T) {
	t.Run("valid envelope", func(t *testing.T) {
		raw, err := json.Marshal(Envelope{Type: TypePing})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reader := bufio.NewReader(bytes.NewReader(append(raw, '\n')))
		env, err := ReadEnvelope(reader)
		if err != nil {
			t.Fatalf("read envelope: %v", err)
		}
		if env.Type != TypePing {
			t.Fatalf("want type %q, got %q", TypePing, env.Type)
		}
	})

	t.Run("invalid json is wrapped not panicked", func(t *testing.T) {
		reader := bufio.NewReader(strings.NewReader("{not json}\n"))
		if _, err := ReadEnvelope(reader); err == nil {
			t.Fatalf("expected decode error for malformed json")
		}
	})

	t.Run("oversize frame rejected before allocation", func(t *testing.T) {
		huge := strings.Repeat("a", MaxEnvelopeBytes+10)
		reader := bufio.NewReader(strings.NewReader(huge + "\n"))
		if _, err := ReadEnvelope(reader); !errors.Is(err, ErrEnvelopeTooLarge) {
			t.Fatalf("want ErrEnvelopeTooLarge, got %v", err)
		}
	})
}

func TestDecodePayloadRoundTrip(t *testing.T) {
	env := Envelope{Type: TypeShutdownAck, Data: ShutdownAckMessage{
		CommandID: "cmd-1",
		NodeID:    "node-1",
		Status:    ShutdownStatusExecuted,
	}}
	// Simulate the wire round-trip: marshal whole envelope, decode, re-decode data.
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Envelope
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	var ack ShutdownAckMessage
	if err := DecodePayload(decoded.Data, &ack); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if ack.CommandID != "cmd-1" || ack.NodeID != "node-1" || ack.Status != ShutdownStatusExecuted {
		t.Fatalf("payload round-trip mismatch: %+v", ack)
	}
}

// FuzzReadEnvelope ensures the parser never panics and respects the size cap on
// arbitrary untrusted byte streams — this is the only entry point for network
// input on both master and slave.
func FuzzReadEnvelope(f *testing.F) {
	f.Add([]byte("{\"type\":\"ping\"}\n"))
	f.Add([]byte("\n"))
	f.Add([]byte("garbage without newline"))
	f.Add([]byte("{}\n{}\n"))
	f.Add(append([]byte(strings.Repeat("x", MaxEnvelopeBytes+1)), '\n'))

	f.Fuzz(func(t *testing.T, data []byte) {
		reader := bufio.NewReader(bytes.NewReader(data))
		// Drain frames until error; must terminate and never panic.
		for i := 0; i < 1000; i++ {
			env, err := ReadEnvelope(reader)
			if err != nil {
				return
			}
			_ = env.Type
		}
	})
}
