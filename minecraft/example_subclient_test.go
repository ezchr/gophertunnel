package minecraft_test

import (
	"context"
	"fmt"
	"time"

	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// ExampleListenConfig_subClients accepts split-screen guests as players of their own. A guest comes out
// of Accept like any connection, with SubClientID and Parent set, and is spawned with StartGame.
func ExampleListenConfig_subClients() {
	l, err := minecraft.ListenConfig{AllowSubClients: true}.Listen("raknet", "0.0.0.0:19132")
	if err != nil {
		panic(err)
	}
	defer l.Close()
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		conn := c.(*minecraft.Conn)
		go func() {
			// Every player on a console shares its entity runtime IDs: a guest needs its own ID for itself.
			rid := uint64(1)
			if conn.Parent() != nil {
				rid = uint64(10 + conn.SubClientID())
			}
			if err := conn.StartGame(minecraft.GameData{EntityRuntimeID: rid, EntityUniqueID: int64(rid)}); err != nil {
				return
			}
			if conn.Parent() != nil {
				fmt.Printf("%v joined as split-screen player %v of %v\n", conn.IdentityData().DisplayName, conn.SubClientID(), conn.Parent().IdentityData().DisplayName)
			}
			for {
				if _, err := conn.ReadPacket(); err != nil {
					// Closing the main player's Conn closes its guests too.
					return
				}
			}
		}()
	}
}

// ExampleConn_LoginSubClient adds a split-screen guest to a dialed connection.
func ExampleConn_LoginSubClient() {
	conn, err := minecraft.Dial("raknet", "example.com:19132")
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	guest, err := conn.LoginSubClient(ctx, login.IdentityData{DisplayName: "Guest"}, login.ClientData{})
	if err != nil {
		panic(err)
	}
	// The guest reads its own packets and writes with its sub-client ID; closing it leaves the
	// main connection open.
	_ = guest.WritePacket(&packet.Text{TextType: packet.TextTypeChat, SourceName: "Guest", Message: "hi"})
	_ = guest.Close()
}
