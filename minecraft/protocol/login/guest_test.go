package login

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"
)

// guestRequest turns a request made by EncodeOffline into a guest login (AuthenticationType 1), as a
// console sends for a split-screen guest.
func guestRequest(t *testing.T, req []byte) []byte {
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

// guestClientData is client data as a console sends it for a guest: no game version, server address,
// language, device model or PlayFab ID.
func guestClientData() ClientData {
	return ClientData{
		DeviceOS:          12,
		SkinID:            "skin",
		SkinData:          base64.StdEncoding.EncodeToString(make([]byte, 64*32*4)),
		SkinImageWidth:    64,
		SkinImageHeight:   32,
		SkinResourcePatch: base64.StdEncoding.EncodeToString([]byte(`{"geometry":{"default":"geometry.humanoid.custom"}}`)),
		ThirdPartyName:    "Player 2",
	}
}

func TestParseGuestLogin(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	req := guestRequest(t, EncodeOffline(IdentityData{XUID: "2535400000000000", DisplayName: "Host", Identity: "c0ffee00-0000-4000-8000-000000000001"}, guestClientData(), key, false))

	if _, _, _, err := Parse(req, nil); err == nil {
		t.Fatal("Parse accepted a guest login: it is only valid for a sub-client")
	}
	host := ClientData{GameVersion: "1.26.60", ServerAddress: "example.com:19132", LanguageCode: "en_US", DeviceModel: "Switch", PlayFabID: "abcdef"}
	id, cd, res, err := ParseSubClient(req, nil, host)
	if err != nil {
		t.Fatalf("ParseSubClient: %v", err)
	}
	if !res.Guest || res.XBOXLiveAuthenticated {
		t.Fatalf("guest %v authenticated %v", res.Guest, res.XBOXLiveAuthenticated)
	}
	if id.XUID != "2535400000000000" {
		t.Fatalf("token XUID not read: %q", id.XUID)
	}
	if cd.GameVersion != host.GameVersion || cd.LanguageCode != host.LanguageCode || cd.ServerAddress != host.ServerAddress || cd.PlayFabID != host.PlayFabID {
		t.Fatalf("missing fields not taken from the host: %+v", cd)
	}
	if cd.ThirdPartyName != "Player 2" {
		t.Fatalf("guest's own fields replaced: %q", cd.ThirdPartyName)
	}
	// A regular login still parses with ParseSubClient and is not a guest.
	full := guestClientData()
	full.GameVersion, full.ServerAddress, full.LanguageCode = "1.26.60", "example.com:19132", "en_US"
	if _, _, res, err := ParseSubClient(EncodeOffline(IdentityData{DisplayName: "Other"}, full, key, false), nil, host); err != nil || res.Guest {
		t.Fatalf("regular sub-client login: guest %v err %v", res.Guest, err)
	}
}

func TestEncodeGuest(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	full := guestClientData()
	full.GameVersion, full.ServerAddress, full.LanguageCode = "1.26.60", "example.com:19132", "en_US"
	host := EncodeOffline(IdentityData{XUID: "2535400000000000", DisplayName: "Host"}, full, key, false)
	n := binary.LittleEndian.Uint32(host)
	var m struct{ Token string }
	if err := json.Unmarshal(host[4:4+n], &m); err != nil || m.Token == "" {
		t.Fatalf("host token: %v", err)
	}
	req := EncodeGuest(m.Token, guestClientData(), key)
	if _, _, _, err := Parse(req, nil); err == nil {
		t.Fatal("Parse accepted a guest login")
	}
	id, cd, res, err := ParseSubClient(req, nil, full)
	if err != nil || !res.Guest || id.XUID != "2535400000000000" || cd.GameVersion != "1.26.60" {
		t.Fatalf("guest %v xuid %q version %q err %v", res.Guest, id.XUID, cd.GameVersion, err)
	}
}
