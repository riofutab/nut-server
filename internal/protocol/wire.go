package protocol

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
)

// MaxEnvelopeBytes bounds a single newline-delimited wire frame. Frames are
// small JSON objects; the cap exists purely to stop a malicious or buggy peer
// from streaming an unbounded line and exhausting process memory before the
// frame can be rejected.
const MaxEnvelopeBytes = 1 << 20 // 1 MiB

// ErrEnvelopeTooLarge is returned when a frame exceeds MaxEnvelopeBytes.
var ErrEnvelopeTooLarge = errors.New("envelope exceeds maximum size")

// ReadEnvelope reads one newline-delimited JSON envelope from reader, refusing
// to buffer more than MaxEnvelopeBytes for a single frame.
func ReadEnvelope(reader *bufio.Reader) (Envelope, error) {
	line, err := readLimitedLine(reader, MaxEnvelopeBytes)
	if err != nil {
		return Envelope{}, err
	}
	var env Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return Envelope{}, fmt.Errorf("decode envelope: %w", err)
	}
	return env, nil
}

// DecodePayload re-decodes the loosely-typed Envelope.Data into a concrete
// message type. It is shared by master and slave so any future hardening (e.g.
// DisallowUnknownFields) applies to both sides at once.
func DecodePayload(data interface{}, dst interface{}) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, dst)
}

// readLimitedLine reads bytes up to and including the next '\n', returning the
// line without the terminator. It fails with ErrEnvelopeTooLarge once the
// accumulated line would exceed max bytes, so a peer cannot force an unbounded
// allocation by withholding the newline.
func readLimitedLine(reader *bufio.Reader, max int) ([]byte, error) {
	buf := make([]byte, 0, 256)
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return nil, err
		}
		if b == '\n' {
			return buf, nil
		}
		if len(buf) >= max {
			return nil, ErrEnvelopeTooLarge
		}
		buf = append(buf, b)
	}
}
