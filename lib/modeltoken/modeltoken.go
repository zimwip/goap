// Package modeltoken is the token a tool that calls a model by itself (the Claude coding agent, ADR 0123) carries to
// the model gateway's Anthropic endpoint, so its calls are rows of the ledger (ADR 0089) attributed to the person, the
// change and the process they ran for (ADR 0114). It is minted by the MCP hub for each call of a connector that calls a
// model by itself and checked by the gateway, both holding the same secret (GOAP_MODEL_PROXY_SECRET): the connector
// holds no secret and can name no one else. It is short-lived and names what it may be used for, never more: a
// principal of its own (a system subject acting for the end user), no roles.
package modeltoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Claims are what a token says: who the calls are made by and for, and what they run for.
type Claims struct {
	// Subject is the system subject making the calls (system:<tool>).
	Subject string `json:"sub"`
	// EndUser is the person the calls are made for (authz.Principal.OnBehalfOf).
	EndUser string `json:"eu,omitempty"`
	Org     string `json:"org,omitempty"`
	Project string `json:"prj,omitempty"`
	Change  string `json:"chg,omitempty"`
	Process string `json:"prc,omitempty"`
	// Source is the source of the calls in the ledger (llm.CallMeta.Source).
	Source string `json:"src,omitempty"`
	// Expires is the Unix time after which the token is refused.
	Expires int64 `json:"exp"`
}

// Errors of Verify.
var (
	ErrInvalid = errors.New("invalid model token")
	ErrExpired = errors.New("model token expired")
)

var enc = base64.RawURLEncoding

// Sign returns the token of c, valid for ttl from now. The token is URL-safe (it may be a path segment).
func Sign(secret []byte, c Claims, now time.Time, ttl time.Duration) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("model token: no secret")
	}
	c.Expires = now.Add(ttl).Unix()
	body, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	payload := enc.EncodeToString(body)
	return payload + "." + enc.EncodeToString(mac(secret, payload)), nil
}

// Verify checks the signature and the expiry of a token and returns its claims.
func Verify(secret []byte, token string, now time.Time) (Claims, error) {
	var c Claims
	if len(secret) == 0 {
		return c, ErrInvalid
	}
	payload, sig, ok := strings.Cut(token, ".")
	if !ok {
		return c, ErrInvalid
	}
	got, err := enc.DecodeString(sig)
	if err != nil || !hmac.Equal(got, mac(secret, payload)) {
		return c, ErrInvalid
	}
	body, err := enc.DecodeString(payload)
	if err != nil || json.Unmarshal(body, &c) != nil || c.Subject == "" {
		return Claims{}, ErrInvalid
	}
	if now.Unix() > c.Expires {
		return Claims{}, ErrExpired
	}
	return c, nil
}

func mac(secret []byte, payload string) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(payload))
	return h.Sum(nil)
}
