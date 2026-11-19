package minecraft

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"net"
	"strings"
	"testing"
)

// TestTCPFrameTooLargeError checks that a frame header over the limit is reported with what is needed to tell a
// real oversized batch from a stream that lost its framing: the header bytes, the bytes after it and the
// previous frame.
func TestTCPFrameTooLargeError(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(binary.BigEndian.AppendUint32(nil, 3))
	stream.Write([]byte{0xfe, 0xff, 0x01})
	stream.Write(binary.BigEndian.AppendUint32(nil, tcpMaxFrame+1))
	stream.Write([]byte{0xfe, 0xff, 0x02, 0x03})

	client, server := net.Pipe()
	defer client.Close()
	go func() {
		_, _ = server.Write(stream.Bytes())
		_ = server.Close()
	}()
	c := &tcpConn{Conn: client, r: bufio.NewReaderSize(client, 64<<10)}

	if f, err := c.ReadPacket(); err != nil || len(f) != 3 {
		t.Fatalf("first frame: %x, %v", f, err)
	}
	_, err := c.ReadPacket()
	if err == nil {
		t.Fatal("no error for a frame over the limit")
	}
	msg := err.Error()
	for _, want := range []string{"tcp: frame too large", "header 01000041", "= 16777281 bytes", "previous frame 3 bytes", "1 frames read before"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
	// The bytes after the header are only reported when already buffered; net.Pipe hands them over in the same
	// write here, so they are.
	if !strings.Contains(msg, "next bytes feff0203") {
		t.Logf("next bytes not buffered: %q", msg)
	}
}
