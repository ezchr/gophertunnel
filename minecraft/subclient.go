package minecraft

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
	"github.com/sandertv/gophertunnel/minecraft/text"
)

// Split-screen players (sub-clients) share the connection of the main client. A client packet of a guest
// carries the guest's ID in bits 12-13 of its header (packet.Header.TargetSubClient), and a server packet
// for a guest carries it in bits 10-11 (packet.Header.SenderSubClient), as the vanilla game writes them.
// 0 is the main client, 1-3 are guests. A guest joins with a SubClientLogin, which the server answers with PlayStatus(LoginSuccess)
// and then the StartGame sequence. There is no network settings, handshake or resource pack step for a
// guest: it uses the compression, encryption and packs of the main connection.
//
// A guest is represented by its own *Conn. It reads the packets addressed to its sub-client ID, writes
// packets with its ID in the header over the main connection, and can be closed on its own. Closing the
// main Conn closes all of its guests.

// maxSubClients is the number of guests a connection can have next to the main client.
const maxSubClients = 3

// errSubClientsDisabled is returned by LoginSubClient on a Conn that cannot have sub-clients.
var errSubClientsDisabled = errors.New("sub-clients are only supported on the main connection of a Dialer")

// SubClientID returns the split-screen sub-client ID of the Conn: 0 for a main connection and 1-3 for a
// guest returned by Listener.Accept or Conn.LoginSubClient.
func (conn *Conn) SubClientID() byte {
	return conn.subClientID
}

// Parent returns the main Conn a guest shares its network connection with, or nil if the Conn is a main
// connection itself.
func (conn *Conn) Parent() *Conn {
	return conn.parent
}

// SubClients returns the guests currently open on the Conn, ordered by sub-client ID.
func (conn *Conn) SubClients() []*Conn {
	conn.subMu.Lock()
	defer conn.subMu.Unlock()
	out := make([]*Conn, 0, len(conn.subClients))
	for id := byte(1); id <= maxSubClients; id++ {
		if g, ok := conn.subClients[id]; ok {
			out = append(out, g)
		}
	}
	return out
}

// LoginSubClient logs a split-screen guest in over the connection of a Conn obtained using a Dialer. The
// guest logs in offline with the identity and client data passed (unset fields are filled like a Dialer
// does) and is spawned before LoginSubClient returns. The guest gets the lowest free sub-client ID.
func (conn *Conn) LoginSubClient(ctx context.Context, identityData login.IdentityData, clientData login.ClientData) (*Conn, error) {
	if conn.parent != nil || !conn.dialer {
		return nil, errSubClientsDisabled
	}
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	defaultIdentityData(&identityData)
	clearXBLIdentityData(&identityData)
	defaultClientData(conn.dialAddress, identityData.DisplayName, &clientData)
	request := login.EncodeOffline(identityData, clientData, key, conn.legacyAuth)
	return conn.loginSubClient(ctx, request, identityData, clientData)
}

// loginSubClient logs a guest in with the login request passed.
func (conn *Conn) loginSubClient(ctx context.Context, request []byte, identityData login.IdentityData, clientData login.ClientData) (*Conn, error) {
	conn.subMu.Lock()
	var id byte
	for i := byte(1); i <= maxSubClients; i++ {
		if _, ok := conn.subClients[i]; !ok {
			id = i
			break
		}
	}
	if id == 0 {
		conn.subMu.Unlock()
		return nil, fmt.Errorf("login sub-client: all %v sub-client IDs are in use", maxSubClients)
	}
	guest := conn.newSubClient(id)
	guest.identityData, guest.clientData = identityData, clientData
	// The server answers with PlayStatus(LoginSuccess) and then the StartGame sequence.
	guest.expect(packet.IDPlayStatus, packet.IDDimensionData, packet.IDStartGame)
	conn.addSubClientLocked(guest)
	conn.subMu.Unlock()
	conn.subClientsEnabled.Store(true)

	if err := guest.WritePacket(&packet.SubClientLogin{ConnectionRequest: request}); err != nil {
		_ = guest.Close()
		return nil, err
	}
	_ = guest.Flush()

	select {
	case <-ctx.Done():
		_ = guest.Close()
		return nil, guest.wrap(context.Cause(ctx), "login sub-client")
	case <-guest.ctx.Done():
		return nil, guest.closeErr("login sub-client")
	case <-guest.spawn:
		return guest, nil
	}
}

// newSubClient creates a guest Conn with the sub-client ID passed that shares the transport of conn.
func (conn *Conn) newSubClient(id byte) *Conn {
	guest := &Conn{
		parent:      conn,
		subClientID: id,
		conn:        subClientNetConn{parent: conn},
		log:         conn.log.With("subclient", id),
		authEnabled: conn.authEnabled,
		dialer:      conn.dialer,

		proto:         conn.proto,
		acceptedProto: conn.acceptedProto,
		pool:          conn.pool,
		readerLimits:  conn.readerLimits,

		disconnectOnUnknownPacket: conn.disconnectOnUnknownPacket,
		disconnectOnInvalidPacket: conn.disconnectOnInvalidPacket,

		verifier:      conn.verifier,
		allow:         conn.allow,
		packetFunc:    conn.packetFunc,
		cacheEnabled:  conn.cacheEnabled,
		resourcePacks: slices.Clone(conn.resourcePacks),

		packets:    make(chan *packetData, 8),
		additional: make(chan packet.Packet, 16),
		spawn:      make(chan struct{}),
		hdr:        subClientHeader(conn.dialer, id),

		resourcePackDelivery: conn.resourcePackDelivery,
	}
	guest.gameData.WorldName = conn.gameData.WorldName
	guest.ctx, guest.cancelFunc = context.WithCancelCause(conn.ctx)
	guest.expectedIDs.Store([]uint32{})
	guest.shieldID.Store(conn.shieldID.Load())
	return guest
}

// addSubClientLocked registers a guest. conn.subMu must be held.
func (conn *Conn) addSubClientLocked(guest *Conn) {
	if conn.subClients == nil {
		conn.subClients = make(map[byte]*Conn, maxSubClients)
	}
	conn.subClients[guest.subClientID] = guest
}

// removeSubClient unregisters a guest if it is still the one registered under its ID.
func (conn *Conn) removeSubClient(guest *Conn) {
	conn.subMu.Lock()
	if conn.subClients[guest.subClientID] == guest {
		delete(conn.subClients, guest.subClientID)
	}
	conn.subMu.Unlock()
}

// receiveSubClient handles a packet of the main connection that belongs to a guest. Packets for a guest
// that does not exist are dropped, except for a SubClientLogin on a Listener connection.
func (conn *Conn) receiveSubClient(pkData *packetData) error {
	id := conn.incomingSubClient(pkData.h)
	conn.subMu.Lock()
	guest, ok := conn.subClients[id]
	conn.subMu.Unlock()
	if !ok {
		if pkData.h.PacketID == packet.IDSubClientLogin && !conn.dialer && conn.loggedIn {
			conn.handleSubClientLogin(id, pkData)
		}
		return nil
	}
	if err := guest.receivePacket(pkData); err != nil {
		// A broken guest is closed without affecting the main connection.
		guest.log.Error("receive: " + err.Error())
		_ = guest.Close()
	}
	return nil
}

// handleSubClientLogin logs in a guest that sent a SubClientLogin on a Listener connection. Once logged in,
// the guest is passed to conn.onSubClient, which hands it to Listener.Accept.
func (conn *Conn) handleSubClientLogin(id byte, pkData *packetData) {
	guest := conn.newSubClient(id)
	pks, err := pkData.decode(guest)
	if err != nil || len(pks) == 0 {
		guest.log.Error("decode SubClientLogin", "err", err)
		return
	}
	pk, ok := pks[0].(*packet.SubClientLogin)
	if !ok {
		return
	}
	reject := func(err error, pk *packet.Disconnect) {
		guest.log.Debug("sub-client login rejected: " + err.Error())
		_ = guest.WritePacket(pk)
		_ = guest.close(err)
	}

	verifier := conn.verifier
	if verifier == nil {
		verifier = conn.subClientVerifier
	}
	identityData, clientData, authResult, err := login.ParseSubClient(pk.ConnectionRequest, verifier, conn.clientData)
	if err != nil && conn.verifier == nil && verifier != nil {
		// Authentication is off: a login that fails the VerifySubClients check (a self-signed one) is
		// read without it, as unauthenticated.
		identityData, clientData, authResult, err = login.ParseSubClient(pk.ConnectionRequest, nil, conn.clientData)
	}
	if err != nil {
		reject(fmt.Errorf("parse login request: %w", err), &packet.Disconnect{Reason: packet.DisconnectReasonNotAuthenticated})
		return
	}
	if authResult.Guest {
		// A guest without an account of its own: the console signs its login with the main player's
		// token. It plays as an identity derived from the main player's.
		if host := conn.identityData; identityData.XUID != host.XUID || (conn.authEnabled && !authResult.XBOXLiveAuthenticated) {
			reject(errors.New("guest login not signed by the main player"), &packet.Disconnect{Reason: packet.DisconnectReasonNotAuthenticated})
			return
		}
		derive := conn.subClientIdentity
		if derive == nil {
			derive = DefaultSubClientIdentity
		}
		identityData = derive(conn.identityData, id, clientData)
	} else if !authResult.XBOXLiveAuthenticated && conn.authEnabled {
		reject(errors.New("sub-client was not authenticated to XBOX Live"), &packet.Disconnect{Message: text.Colourf("<red>You must be logged in with XBOX Live to join.</red>")})
		return
	}
	if !authResult.XBOXLiveAuthenticated && !authResult.Guest {
		// Without authentication the XUID cannot be trusted.
		identityData.XUID = ""
	}
	if identityData.Identity == conn.identityData.Identity || (identityData.XUID != "" && identityData.XUID == conn.identityData.XUID) {
		// A guest with the main player's identity would replace the main player on most servers.
		reject(errors.New("sub-client has the identity of the main player"), &packet.Disconnect{Reason: packet.DisconnectReasonLoggedInOtherLocation, Message: "This account is already playing on this console."})
		return
	}
	guest.identityData, guest.clientData = identityData, clientData
	if conn.allow != nil {
		if reason, ok := conn.allow(conn.RemoteAddr(), identityData, clientData); !ok {
			reject(errors.New("sub-client not allowed"), &packet.Disconnect{Reason: packet.DisconnectReasonKicked, Message: reason})
			return
		}
	}
	conn.subMu.Lock()
	conn.addSubClientLocked(guest)
	conn.subMu.Unlock()

	guest.authenticated.Store(true)
	guest.loggedIn = true
	if err := guest.WritePacket(&packet.PlayStatus{Status: packet.PlayStatusLoginSuccess}); err != nil {
		_ = guest.Close()
		return
	}
	_ = guest.Flush()
	if conn.onSubClient != nil {
		conn.onSubClient(guest)
	}
}

// DefaultSubClientIdentity is the identity of a split-screen guest without an account of its own when
// ListenConfig.SubClientIdentity is nil: no XUID, a UUID derived from the main player's and the guest's
// sub-client ID, and the main player's name followed by the ID ("Steve_1").
func DefaultSubClientIdentity(host login.IdentityData, sub byte, _ login.ClientData) login.IdentityData {
	name := host.DisplayName
	if len(name) > 13 {
		name = name[:13]
	}
	return login.IdentityData{
		Identity:    uuid.NewSHA1(uuid.NameSpaceOID, []byte(host.Identity+":subclient:"+strconv.Itoa(int(sub)))).String(),
		DisplayName: name + "_" + strconv.Itoa(int(sub)),
		TitleID:     host.TitleID,
	}
}

// closeSubClient closes a guest without closing the main connection. A guest of a Dialer connection tells
// the server it left with a Disconnect packet, unless the server disconnected it.
func (conn *Conn) closeSubClient(cause error) error {
	var err error
	conn.once.Do(func() {
		var disconnected DisconnectError
		if conn.dialer && !errors.As(cause, &disconnected) && conn.parent.ctx.Err() == nil && conn.ctx.Err() == nil {
			_ = conn.WritePacket(&packet.Disconnect{HideDisconnectionScreen: true})
		}
		err = conn.Flush()
		conn.cancelFunc(cause)
		conn.parent.removeSubClient(conn)
	})
	return err
}

// subClientHeader returns the header a guest writes its packets with: a client writes its ID in bits
// 12-13, a server writes the ID of the guest it sends to in bits 10-11.
func subClientHeader(client bool, id byte) *packet.Header {
	if client {
		return &packet.Header{TargetSubClient: id}
	}
	return &packet.Header{SenderSubClient: id}
}

// incomingSubClient returns the guest an incoming packet belongs to. A server reads the sending guest
// from bits 12-13, or bits 10-11 if those are 0; a client reads the addressed guest from bits 10-11.
func (conn *Conn) incomingSubClient(h *packet.Header) byte {
	if conn.dialer {
		return h.SenderSubClient
	}
	return h.SubClient()
}

// subClientNetConn is the net.Conn of a guest. It reports the addresses and latency of the main
// connection; reading and writing go through the main Conn instead.
type subClientNetConn struct {
	parent *Conn
}

func (c subClientNetConn) Read([]byte) (int, error)  { return 0, net.ErrClosed }
func (c subClientNetConn) Write([]byte) (int, error) { return 0, net.ErrClosed }

// Close does nothing: closing a guest never closes the main connection.
func (c subClientNetConn) Close() error                     { return nil }
func (c subClientNetConn) LocalAddr() net.Addr              { return c.parent.LocalAddr() }
func (c subClientNetConn) RemoteAddr() net.Addr             { return c.parent.RemoteAddr() }
func (c subClientNetConn) SetDeadline(time.Time) error      { return nil }
func (c subClientNetConn) SetReadDeadline(time.Time) error  { return nil }
func (c subClientNetConn) SetWriteDeadline(time.Time) error { return nil }
func (c subClientNetConn) Latency() time.Duration           { return c.parent.Latency() }
