package api

import (
	"regexp"
	"strings"
	"testing"
)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestRandomUUIDIsValidV4(t *testing.T) {
	for i := 0; i < 1000; i++ {
		u := randomUUID()
		if !uuidRe.MatchString(u) {
			t.Fatalf("invalid uuid: %q", u)
		}
		if strings.ContainsRune(u, '\x00') {
			t.Fatalf("uuid contains null byte: %q", u)
		}
	}
}
