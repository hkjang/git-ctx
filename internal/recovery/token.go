package recovery

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
	"time"
)

const prefix = "gctx_recovery_"

func Generate(pepper string, ttl time.Duration, now time.Time) (string, time.Time, error) {
	if len(pepper) < 32 {
		return "", time.Time{}, errors.New("recovery token signing key is too short")
	}
	if ttl < time.Minute || ttl > time.Hour {
		return "", time.Time{}, errors.New("recovery token TTL must be between 1 minute and 1 hour")
	}
	expires := now.UTC().Add(ttl)
	payload := make([]byte, 8+32)
	binary.BigEndian.PutUint64(payload[:8], uint64(expires.Unix()))
	if _, err := rand.Read(payload[8:]); err != nil {
		return "", time.Time{}, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signature := sign(pepper, encoded)
	return prefix + encoded + "." + base64.RawURLEncoding.EncodeToString(signature), expires, nil
}

func Verify(token, pepper string, now time.Time) ([]byte, time.Time, error) {
	if len(pepper) < 32 || !strings.HasPrefix(token, prefix) {
		return nil, time.Time{}, errors.New("invalid recovery token")
	}
	parts := strings.Split(strings.TrimPrefix(token, prefix), ".")
	if len(parts) != 2 {
		return nil, time.Time{}, errors.New("invalid recovery token")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, sign(pepper, parts[0])) {
		return nil, time.Time{}, errors.New("invalid recovery token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(payload) != 40 {
		return nil, time.Time{}, errors.New("invalid recovery token")
	}
	expires := time.Unix(int64(binary.BigEndian.Uint64(payload[:8])), 0).UTC()
	if !expires.After(now.UTC()) {
		return nil, time.Time{}, errors.New("recovery token has expired")
	}
	hash := sha256.Sum256([]byte(token))
	return hash[:], expires, nil
}

func sign(pepper, payload string) []byte {
	mac := hmac.New(sha256.New, []byte(pepper))
	mac.Write([]byte("git-ctx/admin-recovery/v1\x00"))
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}
