package recovery

import (
	"strings"
	"testing"
	"time"
)

func TestGenerateVerifyAndExpiry(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	token, expires, err := Generate(strings.Repeat("p", 32), 15*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	hash, verifiedExpiry, err := Verify(token, strings.Repeat("p", 32), now)
	if err != nil || len(hash) != 32 || !verifiedExpiry.Equal(expires) {
		t.Fatalf("hash=%d expires=%v err=%v", len(hash), verifiedExpiry, err)
	}
	if _, _, err = Verify(token+"x", strings.Repeat("p", 32), now); err == nil {
		t.Fatal("tampered token accepted")
	}
	if _, _, err = Verify(token, strings.Repeat("p", 32), expires); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestGenerateRejectsUnsafeTTL(t *testing.T) {
	for _, ttl := range []time.Duration{time.Second, 2 * time.Hour} {
		if _, _, err := Generate(strings.Repeat("p", 32), ttl, time.Now()); err == nil {
			t.Fatalf("ttl %s accepted", ttl)
		}
	}
}
