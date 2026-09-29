// Package client talks to a muxalot agent: request signing, REST API, terminal WebSocket.
package client

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"
)

// Authorization builds the agent's request signature header. The signed string
// must match agent/server.go signedMessage():
//
//	"muxalot-v1\nMETHOD\nHOST\nREQUEST_URI\nts\nnonce"
//
// signed with ECDSA P-256 / SHA-256, DER encoded, base64. The phone clock must
// be within 60 s of the server's.
func Authorization(key *ecdsa.PrivateKey, deviceID, method, host, uri string) (string, error) {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return authorizationAt(key, deviceID, method, host, uri, time.Now(), nonce)
}

func authorizationAt(key *ecdsa.PrivateKey, deviceID, method, host, uri string, now time.Time, nonce []byte) (string, error) {
	ts := fmt.Sprint(now.Unix())
	n := base64.RawURLEncoding.EncodeToString(nonce)
	sum := sha256.Sum256([]byte("muxalot-v1\n" + method + "\n" + host + "\n" + uri + "\n" + ts + "\n" + n))
	sig, err := ecdsa.SignASN1(rand.Reader, key, sum[:])
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`Sig id="%s", ts="%s", nonce="%s", sig="%s"`, deviceID, ts, n, base64.StdEncoding.EncodeToString(sig)), nil
}
