// Package minecraft implements Minecraft Bedrock Edition connections. It implements a Dial() function to dial
// a connection to a Minecraft server and a Listen() function to create a listener in order to listen for
// incoming Minecraft connections. Typically these connections are done over RakNet, which is implemented by
// the github.com/sandertv/go-raknet package.
//
// The minecraft package provides a high level abstraction over Minecraft network related features. It handles
// the authentication, encryption and spawning sequence by itself and users can send and read packets
// implemented in the minecraft/protocol/packet package.
//
// # Split-screen players
//
// A console playing split-screen sends the packets of up to three extra players (sub-clients, or
// guests) over the connection of its first player. A guest joins with a SubClientLogin packet and is
// marked by its sub-client ID (1-3) in the packet header: a client writes the ID of the sending guest
// in bits 12-13, a server writes the ID of the guest a packet is for in bits 10-11 (see packet.Header).
//
// With ListenConfig.AllowSubClients set, a Listener logs guests in and returns each from Accept as a
// *Conn of its own, with Conn.SubClientID and Conn.Parent set. A guest reads and writes only its own
// packets, can be closed on its own, and is closed with the Conn of its main player. All players on a
// console share one entity runtime ID space, so a guest must be given a runtime ID for itself that no
// entity it is shown uses. A guest without an Xbox account of its own logs in with the main player's
// token; it gets the identity from ListenConfig.SubClientIdentity.
//
// On the client side, Conn.LoginSubClient and Conn.LoginSubClientWith log a guest in over a dialed
// connection.
package minecraft
