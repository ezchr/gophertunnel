package minecraft

import (
	"bytes"
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

func TestTCPLoginSpawnAndPing(t *testing.T) {
	l, err := ListenConfig{AuthenticationDisabled: true, StatusProvider: NewStatusProvider("tcp test", "sub")}.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	addr := l.Addr().String()

	got := make(chan string, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		conn := c.(*Conn)
		if err := conn.StartGame(GameData{}); err != nil {
			got <- "start game: " + err.Error()
			return
		}
		pk, err := conn.ReadPacket()
		if err != nil {
			got <- "read: " + err.Error()
			return
		}
		if txt, ok := pk.(*packet.Text); ok {
			got <- txt.Message
			// A big packet, several frames' worth of a RakNet message, must arrive whole.
			_ = conn.WritePacket(&packet.Text{TextType: packet.TextTypeRaw, Message: strings.Repeat("a", 60000)})
			return
		}
		got <- "unexpected packet"
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := Dialer{}.DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.DoSpawnContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := conn.WritePacket(&packet.Text{TextType: packet.TextTypeRaw, Message: "hello over tcp"}); err != nil {
		t.Fatal(err)
	}
	if msg := <-got; msg != "hello over tcp" {
		t.Fatalf("server got %q", msg)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		pk, err := conn.ReadPacket()
		if err != nil {
			t.Fatal(err)
		}
		if txt, ok := pk.(*packet.Text); ok {
			if len(txt.Message) != 60000 {
				t.Fatalf("big packet arrived with %d bytes", len(txt.Message))
			}
			break
		}
	}

	pong, err := TCP{}.PingContext(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	if len(pong) == 0 {
		t.Fatal("empty pong")
	}
}

// TestTCPPlainLink checks that a client asking for no encryption and no compression gets a link whose packets
// can be read on the wire, and that without the request they cannot.
func TestTCPPlainLink(t *testing.T) {
	for _, plain := range []bool{true, false} {
		l, err := ListenConfig{AuthenticationDisabled: true}.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		// A relay between the two that records what passes through.
		mid, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		var mu sync.Mutex
		var seen bytes.Buffer
		go func() {
			a, err := mid.Accept()
			if err != nil {
				return
			}
			b, err := net.Dial("tcp", l.Addr().String())
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(b, a) }()
			buf := make([]byte, 64<<10)
			for {
				n, err := b.Read(buf)
				mu.Lock()
				seen.Write(buf[:n])
				mu.Unlock()
				if err != nil {
					return
				}
				if _, err := a.Write(buf[:n]); err != nil {
					return
				}
			}
		}()
		marker := strings.Repeat("plain-link-marker ", 20)
		go func() {
			c, err := l.Accept()
			if err != nil {
				return
			}
			conn := c.(*Conn)
			if conn.StartGame(GameData{}) != nil {
				return
			}
			_ = conn.WritePacket(&packet.Text{TextType: packet.TextTypeRaw, Message: marker})
			_ = conn.Flush()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		conn, err := Dialer{DisablePortFollowing: true}.DialContextNetwork(ctx, TCP{NoEncryption: plain, NoCompression: plain}, mid.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.DoSpawnContext(ctx); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			pk, err := conn.ReadPacket()
			if err != nil {
				t.Fatal(err)
			}
			if txt, ok := pk.(*packet.Text); ok && txt.Message == marker {
				break
			}
		}
		mu.Lock()
		readable := bytes.Contains(seen.Bytes(), []byte(marker))
		mu.Unlock()
		if readable != plain {
			t.Errorf("plain %v: packet readable on the wire = %v", plain, readable)
		}
		cancel()
		_ = conn.Close()
		_ = l.Close()
		_ = mid.Close()
	}
}
