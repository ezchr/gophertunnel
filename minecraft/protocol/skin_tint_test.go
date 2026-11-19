package protocol

import (
	"bytes"
	"image/color"
	"testing"
)

// The v2168 wire names of persona piece tints are the client's persona::PieceType names: a client
// sent "facial_hair" rejects the PlayerList packet and disconnects.
func TestPersonaPieceTintWireNames(t *testing.T) {
	want := map[string]string{
		"persona_hand":           "hands",
		"persona_facial_hair":    "facialhair",
		"persona_face_accessory": "faceaccessory",
		"persona_left_leg":       "leftleg",
		"persona_right_leg":      "rightleg",
		"persona_left_arm":       "leftarm",
		"persona_right_arm":      "rightarm",
		"persona_classic_skin":   "classicskin",
		"persona_high_pants":     "high_pants",
		"persona_hair":           "hair",
		"persona_body":           "body",
	}
	for login, wire := range want {
		if got := personaPieceTintWireType(login); got != wire {
			t.Errorf("%s: wire name %q, want %q", login, got, wire)
		}
		if got := personaPieceTintLoginType(wire); got != login {
			t.Errorf("%s: login name %q, want %q", wire, got, login)
		}
	}
}

// A tint written and read back keeps its login name, and the bytes carry the wire name.
func TestPersonaPieceTintRoundTrip(t *testing.T) {
	in := PersonaPieceTintColour{PieceType: "persona_facial_hair", Colours: [4]color.RGBA{{A: 255}, {A: 255}, {A: 255}, {A: 255}}}
	buf := bytes.NewBuffer(nil)
	w := NewWriter(buf, 0)
	in.Marshal(w)
	if !bytes.Contains(buf.Bytes(), []byte("facialhair")) || bytes.Contains(buf.Bytes(), []byte("facial_hair")) {
		t.Fatalf("written as %q", buf.Bytes())
	}
	var out PersonaPieceTintColour
	out.Marshal(NewReader(bytes.NewReader(buf.Bytes()), 0, false))
	if out.PieceType != "persona_facial_hair" {
		t.Fatalf("read back as %q", out.PieceType)
	}
}

// A tint of a piece type the client does not know is not written: the client would reject the packet.
func TestPersonaPieceTintUnknownDropped(t *testing.T) {
	skin := Skin{SkinImageWidth: 1, SkinImageHeight: 1, SkinData: make([]byte, 4), PieceTintColours: []PersonaPieceTintColour{
		{PieceType: "persona_facial_hair"}, {PieceType: "persona_beard_of_doom"}, {PieceType: "persona_hair"},
	}}
	buf := bytes.NewBuffer(nil)
	skin.Marshal(NewWriter(buf, 0))
	if bytes.Contains(buf.Bytes(), []byte("beard_of_doom")) {
		t.Fatal("unknown piece type written")
	}
	var back Skin
	back.Marshal(NewReader(bytes.NewReader(buf.Bytes()), 0, false))
	if len(back.PieceTintColours) != 2 || back.PieceTintColours[0].PieceType != "persona_facial_hair" || back.PieceTintColours[1].PieceType != "persona_hair" {
		t.Fatalf("tints read back: %+v", back.PieceTintColours)
	}
	if len(skin.PieceTintColours) != 3 {
		t.Error("writing changed the skin passed")
	}
	if PieceTypeCoCo != 28 || PieceTypeUnsupported != 29 {
		t.Errorf("CoCo %d, Unsupported %d, want 28 and 29", PieceTypeCoCo, PieceTypeUnsupported)
	}
}
