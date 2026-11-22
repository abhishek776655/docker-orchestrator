package tunnel

import (
	"bytes"
	"strings"
	"testing"
)

func TestFramesRoundTripInOrder(t *testing.T) {
	var buf bytes.Buffer
	_ = WriteFrame(&buf, FrameData, []byte("hello"))
	_ = WriteFrame(&buf, FrameResize, ResizePayload(120, 40))
	_ = WriteFrame(&buf, FrameExit, []byte("3"))
	_ = WriteFrame(&buf, FrameEOF, nil)

	typ, p, err := ReadFrame(&buf)
	if err != nil || typ != FrameData || string(p) != "hello" {
		t.Fatalf("1: %c %q %v", typ, p, err)
	}
	typ, p, _ = ReadFrame(&buf)
	if cols, rows, ok := ParseResize(p); typ != FrameResize || !ok || cols != 120 || rows != 40 {
		t.Fatalf("2: %c %v %v", typ, p, ok)
	}
	typ, p, _ = ReadFrame(&buf)
	if typ != FrameExit || string(p) != "3" {
		t.Fatalf("3: %c %q", typ, p)
	}
	typ, p, err = ReadFrame(&buf)
	if typ != FrameEOF || len(p) != 0 || err != nil {
		t.Fatalf("4: %c %q %v", typ, p, err)
	}
	if _, _, err := ReadFrame(&buf); err == nil {
		t.Fatal("reading past the end should fail")
	}
}

func TestOversizedFramesAreRefusedBothWays(t *testing.T) {
	if err := WriteFrame(&bytes.Buffer{}, FrameData, make([]byte, MaxFrame+1)); err == nil {
		t.Fatal("write should refuse an oversized frame")
	}
	hdr := []byte{FrameData, 0xff, 0xff, 0xff, 0xff}
	if _, _, err := ReadFrame(bytes.NewReader(hdr)); err == nil || !strings.Contains(err.Error(), "over the") {
		t.Fatalf("read should refuse a huge length, got %v", err)
	}
}

func TestTruncatedFrameIsAnError(t *testing.T) {
	var buf bytes.Buffer
	_ = WriteFrame(&buf, FrameData, []byte("abcdef"))
	if _, _, err := ReadFrame(bytes.NewReader(buf.Bytes()[:8])); err == nil {
		t.Fatal("a cut-off payload must not be returned as data")
	}
}
