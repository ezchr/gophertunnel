package packet

import (
	"bytes"
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

func TestHeaderSubClientBits(t *testing.T) {
	for _, tc := range []struct {
		h    Header
		want uint32
	}{
		{Header{PacketID: IDText}, IDText},
		{Header{PacketID: IDText, SenderSubClient: 1}, IDText | 1<<10},
		{Header{PacketID: IDSubClientLogin, TargetSubClient: 3}, IDSubClientLogin | 3<<12},
		{Header{PacketID: 0x3FF, SenderSubClient: 2, TargetSubClient: 1}, 0x3FF | 2<<10 | 1<<12},
	} {
		var buf bytes.Buffer
		if err := tc.h.Write(&buf); err != nil {
			t.Fatal(err)
		}
		var v uint32
		if err := protocol.Varuint32(bytes.NewReader(buf.Bytes()), &v); err != nil {
			t.Fatal(err)
		}
		if v != tc.want {
			t.Fatalf("%+v: wrote %#x, want %#x", tc.h, v, tc.want)
		}
		var got Header
		if err := got.Read(bytes.NewReader(buf.Bytes())); err != nil {
			t.Fatal(err)
		}
		if got != tc.h {
			t.Fatalf("read %+v, want %+v", got, tc.h)
		}
		want := tc.h.TargetSubClient
		if want == 0 {
			want = tc.h.SenderSubClient
		}
		if got.SubClient() != want {
			t.Fatalf("SubClient() = %v, want %v", got.SubClient(), want)
		}
	}
}
