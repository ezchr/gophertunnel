package login

import (
	"strings"
	"testing"
)

// Cinnabar's Friends-tab target passes the ServerAddress check; anything else after the prefix does not.
func TestCinnabarFriendServerAddress(t *testing.T) {
	for addr, ok := range map[string]bool{
		"friend_xuid/2535470912054074": true,
		"friend_xuid/abc":              false,
		"friend_xuid/":                 false,
	} {
		err := ClientData{ServerAddress: addr, DeviceOS: 7, GameVersion: "1.26.60", LanguageCode: "en_US"}.Validate()
		bad := err != nil && strings.Contains(err.Error(), "ServerAddress")
		if bad == ok {
			t.Errorf("%q: %v", addr, err)
		}
	}
}
