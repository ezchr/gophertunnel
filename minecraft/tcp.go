package minecraft

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// TCP is a Network for links between a proxy and its servers, typically on the same machine: Minecraft packet
// batches go over a TCP stream instead of RakNet, which spends work on acknowledgements, resends and splitting
// that a reliable stream does not need.
//
// The stream starts with one byte from the client: tcpGame for a game connection, tcpPing for a ping. A game
// connection may add tcpNoEncryption and tcpNoCompression to that byte: the server then neither encrypts nor
// compresses it, which on a link that never leaves the machine only costs CPU. A game connection then carries
// frames both ways, each a big-endian uint32 length followed by that many bytes: what a
// RakNet message would hold (a batch, with its header). A ping is answered with one frame holding the pong data,
// after which the server closes the connection.
//
// TCP has no handshake of its own and nothing that limits who connects, so it should only be listened on for
// addresses that only the proxy can reach, such as 127.0.0.1.
type TCP struct {
	l *slog.Logger

	// NoEncryption and NoCompression, when dialing, ask the server not to encrypt or compress the connection.
	// A server on the TCP network grants both: only a proxy should be able to reach it.
	NoEncryption, NoCompression bool
}

const (
	tcpGame byte = 0
	tcpPing byte = 1

	tcpNoEncryption  byte = 1 << 1
	tcpNoCompression byte = 1 << 2

	// tcpMaxFrame is the largest frame accepted. Batches are compressed and well below this.
	tcpMaxFrame = 64 << 20
)

// DialContext ...
func (t TCP) DialContext(ctx context.Context, address string) (net.Conn, error) {
	kind := tcpGame
	if t.NoEncryption {
		kind |= tcpNoEncryption
	}
	if t.NoCompression {
		kind |= tcpNoCompression
	}
	c, err := t.dial(ctx, address, kind)
	if err != nil {
		return nil, err
	}
	return newTCPConn(c, 0), nil
}

// PingContext ...
func (t TCP) PingContext(ctx context.Context, address string) ([]byte, error) {
	c, err := t.dial(ctx, address, tcpPing)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetReadDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = c.SetReadDeadline(time.Unix(1, 0)) })
	defer stop()
	return readTCPFrame(bufio.NewReader(c), nil)
}

func (t TCP) dial(ctx context.Context, address string, kind byte) (net.Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	if _, err := c.Write([]byte{kind}); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// Listen ...
func (t TCP) Listen(address string) (NetworkListener, error) {
	l, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	tl := &tcpListener{l: l, log: t.l.With("net origin", "tcp"), id: rand.Int64(), conns: make(chan net.Conn), closed: make(chan struct{})}
	tl.pong.Store(&[]byte{})
	go tl.accept()
	return tl, nil
}

// tcpListener is the NetworkListener of the TCP network.
type tcpListener struct {
	l      net.Listener
	log    *slog.Logger
	id     int64
	pong   atomic.Pointer[[]byte]
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func (l *tcpListener) accept() {
	for {
		c, err := l.l.Accept()
		if err != nil {
			l.Close()
			return
		}
		go l.handshake(c)
	}
}

// handshake reads what the client wants: a ping is answered here, a game connection is handed to Accept.
func (l *tcpListener) handshake(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	var kind [1]byte
	if _, err := io.ReadFull(c, kind[:]); err != nil {
		_ = c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	switch k := kind[0]; {
	case k == tcpPing:
		_ = writeTCPFrame(c, *l.pong.Load())
		_ = c.Close()
	case k&^(tcpNoEncryption|tcpNoCompression) == tcpGame:
		select {
		case l.conns <- newTCPConn(c, k):
		case <-l.closed:
			_ = c.Close()
		}
	default:
		l.log.Debug("tcp: unknown connection kind", "kind", kind[0], "raddr", c.RemoteAddr())
		_ = c.Close()
	}
}

// Accept ...
func (l *tcpListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

// Close ...
func (l *tcpListener) Close() error {
	var err error
	l.once.Do(func() {
		close(l.closed)
		err = l.l.Close()
	})
	return err
}

// Addr ...
func (l *tcpListener) Addr() net.Addr { return l.l.Addr() }

// ID ...
func (l *tcpListener) ID() int64 { return l.id }

// PongData ...
func (l *tcpListener) PongData(data []byte) {
	if len(data) > tcpMaxFrame {
		panic(fmt.Sprintf("tcp: pong data of %d bytes is too long", len(data)))
	}
	d := append([]byte(nil), data...)
	l.pong.Store(&d)
}

// tcpConn is a game connection of the TCP network: one frame per message, both ways.
type tcpConn struct {
	net.Conn
	r     *bufio.Reader
	flags byte

	wmu  sync.Mutex
	wbuf []byte
}

func newTCPConn(c net.Conn, flags byte) *tcpConn {
	return &tcpConn{Conn: c, r: bufio.NewReaderSize(c, 64<<10), flags: flags}
}

// Latency reports the round trip time of the connection, which Conn.Latency requires. A TCP link
// is meant for a proxy on the same machine, where it is close to nothing, so it reports 0.
func (c *tcpConn) Latency() time.Duration { return 0 }

// DisableEncryption reports whether the client asked for no encryption. It implements packet.EncryptionDisabler.
func (c *tcpConn) DisableEncryption() bool { return c.flags&tcpNoEncryption != 0 }

// DisableCompression reports whether the client asked for no compression. It implements
// packet.CompressionDisabler.
func (c *tcpConn) DisableCompression() bool { return c.flags&tcpNoCompression != 0 }

// Write sends b as one frame.
func (c *tcpConn) Write(b []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.wbuf = binary.BigEndian.AppendUint32(c.wbuf[:0], uint32(len(b)))
	c.wbuf = append(c.wbuf, b...)
	if _, err := c.Conn.Write(c.wbuf); err != nil {
		return 0, err
	}
	return len(b), nil
}

// ReadPacket reads one frame.
func (c *tcpConn) ReadPacket() ([]byte, error) {
	return readTCPFrame(c.r, nil)
}

// Read reads one frame into b, as a message based connection does.
func (c *tcpConn) Read(b []byte) (int, error) {
	f, err := c.ReadPacket()
	if err != nil {
		return 0, err
	}
	if len(f) > len(b) {
		return 0, io.ErrShortBuffer
	}
	return copy(b, f), nil
}

func writeTCPFrame(w io.Writer, b []byte) error {
	buf := binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(b)), uint32(len(b)))
	_, err := w.Write(append(buf, b...))
	return err
}

func readTCPFrame(r *bufio.Reader, buf []byte) ([]byte, error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(head[:])
	if n > tcpMaxFrame {
		return nil, errors.New("tcp: frame too large")
	}
	if cap(buf) < int(n) {
		buf = make([]byte, n)
	}
	buf = buf[:n]
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// init registers the TCP network.
func init() {
	RegisterNetwork("tcp", func(l *slog.Logger) Network { return TCP{l: l} })
}
