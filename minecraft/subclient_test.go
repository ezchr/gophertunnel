package minecraft

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// readText reads packets from conn until a Text packet arrives and returns its message.
func readText(conn *Conn, timeout time.Duration) (string, error) {
	type result struct {
		msg string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		for {
			pk, err := conn.ReadPacket()
			if err != nil {
				ch <- result{err: err}
				return
			}
			if t, ok := pk.(*packet.Text); ok {
				ch <- result{msg: t.Message}
				return
			}
		}
	}()
	select {
	case r := <-ch:
		return r.msg, r.err
	case <-time.After(timeout):
		return "", errors.New("timed out waiting for Text")
	}
}

func sendText(t *testing.T, conn *Conn, msg string) {
	t.Helper()
	if err := conn.WritePacket(&packet.Text{TextType: packet.TextTypeRaw, Message: msg}); err != nil {
		t.Fatalf("write %q: %v", msg, err)
	}
	if err := conn.Flush(); err != nil {
		t.Fatalf("flush %q: %v", msg, err)
	}
}

// acceptAndSpawn accepts the next connection of l and spawns it with the runtime ID passed.
func acceptAndSpawn(l *Listener, rid uint64) (*Conn, error) {
	c, err := l.Accept()
	if err != nil {
		return nil, err
	}
	conn := c.(*Conn)
	err = conn.StartGameTimeout(GameData{EntityUniqueID: int64(rid), EntityRuntimeID: rid, WorldName: "test"}, 10*time.Second)
	return conn, err
}

func TestSubClientEndToEnd(t *testing.T) {
	l, err := ListenConfig{AuthenticationDisabled: true, AllowSubClients: true}.Listen("raknet", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	type accepted struct {
		conn *Conn
		err  error
	}
	acc := make(chan accepted, 4)
	go func() {
		for rid := uint64(1); ; rid++ {
			c, err := acceptAndSpawn(l, rid)
			acc <- accepted{c, err}
			if err != nil {
				return
			}
		}
	}()
	next := func() *Conn {
		t.Helper()
		select {
		case a := <-acc:
			if a.err != nil {
				t.Fatalf("accept: %v", a.err)
			}
			return a.conn
		case <-time.After(15 * time.Second):
			t.Fatal("timed out waiting for a connection")
		}
		return nil
	}

	client, err := Dialer{IdentityData: login.IdentityData{DisplayName: "Host"}}.DialTimeout("raknet", l.Addr().String(), 15*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	server := next()
	if server.SubClientID() != 0 || server.Parent() != nil {
		t.Fatalf("host: unexpected sub-client %v", server.SubClientID())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	guestClient, err := client.LoginSubClient(ctx, login.IdentityData{DisplayName: "Guest"}, login.ClientData{})
	if err != nil {
		t.Fatalf("login sub-client: %v", err)
	}
	guestServer := next()
	if guestServer.SubClientID() != 1 || guestClient.SubClientID() != 1 {
		t.Fatalf("guest IDs: server %v client %v, want 1", guestServer.SubClientID(), guestClient.SubClientID())
	}
	if guestServer.Parent() != server {
		t.Fatal("guest parent is not the host conn")
	}
	if name := guestServer.IdentityData().DisplayName; name != "Guest" {
		t.Fatalf("guest name %q", name)
	}
	if guestServer.Authenticated() {
		t.Fatal("offline guest reported as authenticated")
	}
	if got := len(server.SubClients()); got != 1 {
		t.Fatalf("server SubClients: %v", got)
	}
	if guestClient.GameData().EntityRuntimeID != 2 {
		t.Fatalf("guest runtime ID %v, want 2", guestClient.GameData().EntityRuntimeID)
	}

	// A packet of a sub-client that did not log in must not reach the host.
	var raw bytes.Buffer
	_ = (&packet.Header{PacketID: packet.IDText, TargetSubClient: 3}).Write(&raw)
	(&packet.Text{TextType: packet.TextTypeRaw, Message: "stray"}).Marshal(client.proto.NewWriter(&raw, 0))
	_, _ = client.Write(raw.Bytes())

	// Each side only sees its own packets.
	sendText(t, client, "from host")
	sendText(t, guestClient, "from guest")
	if msg, err := readText(server, 5*time.Second); err != nil || msg != "from host" {
		t.Fatalf("server host read %q, %v", msg, err)
	}
	if msg, err := readText(guestServer, 5*time.Second); err != nil || msg != "from guest" {
		t.Fatalf("server guest read %q, %v", msg, err)
	}
	sendText(t, guestServer, "to guest")
	sendText(t, server, "to host")
	if msg, err := readText(client, 5*time.Second); err != nil || msg != "to host" {
		t.Fatalf("client host read %q, %v", msg, err)
	}
	if msg, err := readText(guestClient, 5*time.Second); err != nil || msg != "to guest" {
		t.Fatalf("client guest read %q, %v", msg, err)
	}

	// The guest leaving closes only the guest.
	if err := guestClient.Close(); err != nil {
		t.Fatalf("close guest: %v", err)
	}
	if _, err := readText(guestServer, 5*time.Second); err == nil {
		t.Fatal("server guest still open after the guest left")
	}
	sendText(t, client, "host still here")
	if msg, err := readText(server, 5*time.Second); err != nil || msg != "host still here" {
		t.Fatalf("host after guest left: %q, %v", msg, err)
	}
	if got := len(server.SubClients()); got != 0 {
		t.Fatalf("server SubClients after leave: %v", got)
	}

	// A new guest can take the free ID, and closing the host closes it.
	guestClient2, err := client.LoginSubClient(ctx, login.IdentityData{DisplayName: "Guest2"}, login.ClientData{})
	if err != nil {
		t.Fatalf("second sub-client: %v", err)
	}
	guestServer2 := next()
	if guestServer2.SubClientID() != 1 {
		t.Fatalf("second guest ID %v", guestServer2.SubClientID())
	}
	if l.PlayerCount() != 2 {
		t.Fatalf("player count %v, want 2", l.PlayerCount())
	}
	_ = server.Close()
	select {
	case <-guestServer2.Context().Done():
	case <-time.After(5 * time.Second):
		t.Fatal("server guest not closed with its host")
	}
	select {
	case <-guestClient2.Context().Done():
	case <-time.After(10 * time.Second):
		t.Fatal("client guest not closed with its host")
	}
	if _, err := guestServer2.ReadPacket(); err == nil {
		t.Fatal("read on closed guest succeeded")
	}
}

func TestSubClientPacketsWithoutOptIn(t *testing.T) {
	// Without AllowSubClients, packets of sub-clients reach the main conn as before.
	l, err := ListenConfig{AuthenticationDisabled: true}.Listen("raknet", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	acc := make(chan *Conn, 1)
	go func() {
		c, err := acceptAndSpawn(l, 1)
		if err == nil {
			acc <- c
		}
	}()
	client, err := Dialer{}.DialTimeout("raknet", l.Addr().String(), 15*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	var server *Conn
	select {
	case server = <-acc:
	case <-time.After(15 * time.Second):
		t.Fatal("accept timed out")
	}
	var raw bytes.Buffer
	_ = (&packet.Header{PacketID: packet.IDText, TargetSubClient: 2}).Write(&raw)
	(&packet.Text{TextType: packet.TextTypeRaw, Message: "guest packet"}).Marshal(client.proto.NewWriter(&raw, 0))
	_, _ = client.Write(raw.Bytes())
	_ = client.Flush()
	if msg, err := readText(server, 5*time.Second); err != nil || msg != "guest packet" {
		t.Fatalf("read %q, %v", msg, err)
	}
	if _, err := server.LoginSubClient(context.Background(), login.IdentityData{}, login.ClientData{}); err == nil {
		t.Fatal("LoginSubClient on a Listener conn succeeded")
	}
}

func TestSubClientWithHostIdentityRejected(t *testing.T) {
	l, err := ListenConfig{AuthenticationDisabled: true, AllowSubClients: true}.Listen("raknet", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	go func() { _, _ = acceptAndSpawn(l, 1) }()
	client, err := Dialer{}.DialTimeout("raknet", l.Addr().String(), 15*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = client.LoginSubClient(ctx, login.IdentityData{Identity: client.IdentityData().Identity, DisplayName: "Copy"}, login.ClientData{})
	var d DisconnectError
	if !errors.As(err, &d) {
		t.Fatalf("guest with the host's identity: got %v, want a disconnect", err)
	}
	sendText(t, client, "host fine")
	_ = client.Flush()
	if client.Context().Err() != nil {
		t.Fatal("host closed")
	}
}

// guestLoginRequest makes a guest login (AuthenticationType 1) signed like the main player's offline
// login, as a console sends for a split-screen guest without an account of its own.
func guestLoginRequest(t *testing.T, host login.IdentityData, cd login.ClientData) []byte {
	key, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	defaultClientData("example.com:19132", host.DisplayName, &cd)
	cd.GameVersion, cd.ServerAddress, cd.LanguageCode, cd.PlayFabID, cd.DeviceModel = "", "", "", "", ""
	req := login.EncodeOffline(host, cd, key, false)
	n := binary.LittleEndian.Uint32(req)
	var m map[string]any
	if err := json.Unmarshal(req[4:4+n], &m); err != nil {
		t.Fatal(err)
	}
	m["AuthenticationType"] = 1
	delete(m, "Certificate")
	chain, _ := json.Marshal(m)
	var out bytes.Buffer
	_ = binary.Write(&out, binary.LittleEndian, int32(len(chain)))
	out.Write(chain)
	out.Write(req[4+n:])
	return out.Bytes()
}

func TestSubClientGuestLogin(t *testing.T) {
	l, err := ListenConfig{AuthenticationDisabled: true, AllowSubClients: true}.Listen("raknet", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	acc := make(chan *Conn, 2)
	go func() {
		for rid := uint64(1); rid <= 2; rid++ {
			c, err := acceptAndSpawn(l, rid)
			if err != nil {
				return
			}
			acc <- c
		}
	}()
	client, err := Dialer{IdentityData: login.IdentityData{DisplayName: "Host"}}.DialTimeout("raknet", l.Addr().String(), 15*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	server := <-acc

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Signed with the host's identity: the guest gets one derived from it.
	guest, err := client.loginSubClient(ctx, guestLoginRequest(t, client.IdentityData(), login.ClientData{}), client.IdentityData(), login.ClientData{})
	if err != nil {
		t.Fatalf("guest login: %v", err)
	}
	var sg *Conn
	select {
	case sg = <-acc:
	case <-time.After(10 * time.Second):
		t.Fatal("guest not accepted")
	}
	want := DefaultSubClientIdentity(server.IdentityData(), 1, login.ClientData{})
	if got := sg.IdentityData(); got.Identity != want.Identity || got.DisplayName != "Host_1" || got.XUID != "" {
		t.Fatalf("guest identity %+v, want %+v", got, want)
	}
	if sg.ClientData().GameVersion == "" {
		t.Fatal("guest game version not taken from the host")
	}
	sendText(t, guest, "guest here")
	if msg, err := readText(sg, 5*time.Second); err != nil || msg != "guest here" {
		t.Fatalf("guest read %q, %v", msg, err)
	}

	// A guest login signed for another account than the host's is refused.
	other := client.IdentityData()
	other.XUID = "2535400000000001"
	if _, err := client.loginSubClient(ctx, guestLoginRequest(t, other, login.ClientData{}), other, login.ClientData{}); err == nil {
		t.Fatal("guest login of another account accepted")
	}
}
