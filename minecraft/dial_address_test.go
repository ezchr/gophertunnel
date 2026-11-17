package minecraft

import (
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
)

// A NetherNet world dialed by its host's ID logs in with an empty ServerAddress, as the game does,
// which passes the server side login check; real addresses are kept.
func TestLoginServerAddress(t *testing.T) {
	for _, c := range []struct {
		network Network
		address string
		want    string
	}{
		{NetherNet{}, "3dcc87ae-e56e-9214-2e96-7a0b9e29ecdd", ""},
		{NetherNet{}, "17462772707752532694", ""},
		{NetherNet{}, "https://example.com:19132", "https://example.com:19132:19132"},
		{RakNet{}, "example.com:19132", "example.com:19132"},
	} {
		var d login.ClientData
		defaultClientData(loginServerAddress(c.network, c.address), "Steve", &d)
		if d.ServerAddress != c.want {
			t.Errorf("%T %q: ServerAddress %q, want %q", c.network, c.address, d.ServerAddress, c.want)
		}
	}
}
