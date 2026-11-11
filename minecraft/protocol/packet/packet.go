package packet

import (
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"io"
)

// Packet represents a packet that may be sent over a Minecraft network connection. The packet needs to hold
// a method to encode itself to binary and decode itself from binary.
type Packet interface {
	// ID returns the ID of the packet. All of these identifiers of packets may be found in id.go.
	ID() uint32
	// Marshal encodes or decodes a Packet, depending on the protocol.IO
	// implementation passed. When passing a protocol.Writer, Marshal will
	// encode the Packet into its binary representation and write it to the
	// protocol.Writer. On the other hand, when passing a protocol.Reader,
	// Marshal will decode the bytes from the reader into the Packet.
	Marshal(io protocol.IO)
}

// Header is the header of a packet. It exists out of a single varuint32 which is composed of a packet ID and
// two sub-client IDs. These IDs are used for split screen functionality.
type Header struct {
	// PacketID is the ID of the packet, held in bits 0-9.
	PacketID uint32
	// SenderSubClient holds bits 10-11. Despite its name, the game writes the sub-client the packet is
	// addressed to here: a server packet for a split-screen guest carries the guest's ID, and a client
	// packet carries 0 (the server).
	SenderSubClient byte
	// TargetSubClient holds bits 12-13. Despite its name, the game writes the sub-client that sent the
	// packet here: a client packet of a split-screen guest, such as its SubClientLogin, carries the
	// guest's ID, and a server packet normally carries 0.
	TargetSubClient byte
}

// SubClient returns the split-screen sub-client a packet belongs to: the guest that sent it (bits 12-13,
// client packets) or, if that is 0, the guest it is addressed to (bits 10-11, server packets). 0 is the
// main client, 1-3 are guests.
func (header *Header) SubClient() byte {
	if header.TargetSubClient != 0 {
		return header.TargetSubClient
	}
	return header.SenderSubClient
}

// Write writes the header as a single varuint32 to buf.
func (header *Header) Write(w io.ByteWriter) error {
	return protocol.WriteVaruint32(w, header.PacketID|(uint32(header.SenderSubClient)<<10)|(uint32(header.TargetSubClient)<<12))
}

// Read reads a varuint32 from buf and sets the corresponding values to the Header.
func (header *Header) Read(r io.ByteReader) error {
	var value uint32
	if err := protocol.Varuint32(r, &value); err != nil {
		return err
	}
	header.PacketID = value & 0x3FF
	header.SenderSubClient = byte((value >> 10) & 0x3)
	header.TargetSubClient = byte((value >> 12) & 0x3)
	return nil
}
