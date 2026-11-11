package minecraft

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// tcpTestConn returns a Conn on the TCP network and the server end of its socket.
func tcpTestConn(t *testing.T) (*Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	peer := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		peer <- c
	}()
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	p := <-peer
	if p == nil {
		t.Fatal("accept failed")
	}
	t.Cleanup(func() { _ = p.Close(); _ = raw.Close() })
	key, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	return newConn(newTCPConn(raw, tcpGame|tcpNoEncryption|tcpNoCompression), key, slog.Default(), DefaultProtocol, 0, false), p
}

// TestTCPCloseWhileWriteStuck checks that a peer that stops reading neither hangs Close nor panics: the stuck
// Flush fails within the write timeout and Close returns.
func TestTCPCloseWhileWriteStuck(t *testing.T) {
	conn, _ := tcpTestConn(t) // the peer never reads
	big := make([]byte, 1<<20)
	flushErr := make(chan error, 1)
	go func() {
		for i := 0; i < 256; i++ {
			_ = conn.WritePacket(&packet.Unknown{PacketID: 0x09, Payload: big})
			if err := conn.Flush(); err != nil {
				flushErr <- err
				return
			}
		}
		flushErr <- nil
	}()
	select {
	case err := <-flushErr:
		t.Fatalf("flush did not block on a peer that does not read: %v", err)
	case <-time.After(time.Second):
	}
	closed := make(chan struct{})
	start := time.Now()
	go func() {
		_ = conn.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(tcpWriteTimeout + time.Second):
		t.Fatal("Close blocked behind a Flush stuck on a peer that stopped reading")
	}
	select {
	case err := <-flushErr:
		if err == nil {
			t.Fatal("flush succeeded on a peer that does not read")
		}
	case <-time.After(tcpWriteTimeout + time.Second):
		t.Fatal("stuck Flush never returned")
	}
	t.Logf("Close returned after %v", time.Since(start))
}

// TestTCPCloseFlushesPending checks that closing a healthy connection still sends what is pending.
func TestTCPCloseFlushesPending(t *testing.T) {
	conn, peer := tcpTestConn(t)
	payload := []byte{1, 2, 3, 4, 5}
	if err := conn.WritePacket(&packet.Unknown{PacketID: 0x09, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(5 * time.Second))
	f, err := readTCPFrame(bufio.NewReader(peer), nil)
	if err != nil {
		t.Fatalf("pending packet not sent on close: %v", err)
	}
	// Batch header, varuint32 length, packet id 0x09, payload.
	want := append([]byte{0xfe, byte(1 + len(payload)), 0x09}, payload...)
	if string(f) != string(want) {
		t.Fatalf("frame %x, want %x", f, want)
	}
}
