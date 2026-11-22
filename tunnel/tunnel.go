// Package tunnel is the wire format shared by the control plane, agents and
// the haco CLI for interactive sessions (`haco exec`, `haco port-forward`).
//
// A session is a byte stream the control plane splices between the CLI and
// the agent on the node that runs the service; both ends dial out to the
// control plane, so no node needs an inbound port. Exec sessions carry
// framed messages (data, end-of-input, resize, exit code); forward sessions
// carry the raw bytes of one TCP connection.
package tunnel

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Kinds of session.
const (
	KindExec    = "exec"
	KindForward = "forward"
)

// Upgrade is the HTTP Upgrade token for tunnel connections.
const Upgrade = "haco-tunnel"

// Spec describes one session; the control plane hands it to the agent.
type Spec struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Key is the workload key (project/service/revision) to run in.
	Key string `json:"key"`
	// Exec.
	Cmd  []string `json:"cmd,omitempty"`
	TTY  bool     `json:"tty,omitempty"`
	Cols int      `json:"cols,omitempty"`
	Rows int      `json:"rows,omitempty"`
	// Forward: the container port to reach.
	Port int `json:"port,omitempty"`
}

// Frame types.
const (
	FrameData   byte = 'd' // bytes for stdin (CLI to agent) or output (agent to CLI)
	FrameEOF    byte = 'e' // CLI has no more input
	FrameResize byte = 'r' // payload: cols, rows as two big-endian uint16
	FrameExit   byte = 'x' // payload: the exit code in decimal text
)

// MaxFrame bounds one frame's payload.
const MaxFrame = 1 << 20

// WriteFrame writes one frame: type, 4-byte big-endian length, payload.
func WriteFrame(w io.Writer, typ byte, payload []byte) error {
	if len(payload) > MaxFrame {
		return fmt.Errorf("frame of %d bytes is over the %d limit", len(payload), MaxFrame)
	}
	hdr := make([]byte, 5, 5+len(payload))
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	// One Write so frames from different goroutines can be serialized by the
	// caller with a single lock.
	_, err := w.Write(append(hdr, payload...))
	return err
}

// ReadFrame reads one frame.
func ReadFrame(r io.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > MaxFrame {
		return 0, nil, fmt.Errorf("frame of %d bytes is over the %d limit", n, MaxFrame)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return hdr[0], payload, nil
}

// ResizePayload encodes a terminal size.
func ResizePayload(cols, rows int) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b[0:], uint16(cols))
	binary.BigEndian.PutUint16(b[2:], uint16(rows))
	return b
}

// ParseResize decodes a FrameResize payload.
func ParseResize(p []byte) (cols, rows int, ok bool) {
	if len(p) != 4 {
		return 0, 0, false
	}
	return int(binary.BigEndian.Uint16(p[0:])), int(binary.BigEndian.Uint16(p[2:])), true
}
