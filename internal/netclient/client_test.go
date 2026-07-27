package netclient

import (
	"net/http"
	"testing"
	"time"
)

func TestSecureDefaultsAndValidation(t *testing.T) {
	client, err := New(Config{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	transport := client.Transport.(*http.Transport)
	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("TLS verification disabled by default")
	}
	if transport.TLSClientConfig.MinVersion < 0x0303 {
		t.Fatal("TLS below 1.2")
	}
	if _, err = New(Config{CACertificate: "not pem"}); err == nil {
		t.Fatal("invalid CA accepted")
	}
	if _, err = New(Config{ProxyURL: "://bad"}); err == nil {
		t.Fatal("invalid proxy accepted")
	}
}
