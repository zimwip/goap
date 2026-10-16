package modeltoken

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSignVerify(t *testing.T) {
	secret, now := []byte("s3cret"), time.Unix(1_800_000_000, 0)
	c := Claims{Subject: "system:claude-coding-agent", EndUser: "alice", Change: "C1", Process: "P1", Source: "claude-coding-agent"}
	tok, err := Sign(secret, c, now, time.Hour)
	if err != nil || strings.ContainsAny(tok, "/+= ") {
		t.Fatalf("sign: %q %v", tok, err)
	}
	got, err := Verify(secret, tok, now.Add(30*time.Minute))
	if err != nil || got.EndUser != "alice" || got.Change != "C1" || got.Process != "P1" {
		t.Fatalf("verify: %+v %v", got, err)
	}
	if _, err := Verify(secret, tok, now.Add(2*time.Hour)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
	if _, err := Verify([]byte("other"), tok, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("other secret: %v", err)
	}
	payload, sig, _ := strings.Cut(tok, ".")
	forged := payload[:len(payload)-2] + "xx." + sig
	if _, err := Verify(secret, forged, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("forged: %v", err)
	}
	if _, err := Sign(nil, c, now, time.Hour); err == nil {
		t.Fatal("no secret must refuse")
	}
}
