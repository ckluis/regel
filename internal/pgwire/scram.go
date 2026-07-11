package pgwire

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// SCRAM-SHA-256 client (RFC 5802 / RFC 7677), Go stdlib only. Channel binding
// is not used ("n,," GS2 header), matching Postgres non-TLS SCRAM.

type scramClient struct {
	username    string // SCRAM n= field; empty for Postgres (startup user is used)
	password    string
	clientNonce string

	clientFirstBare string
	authMessage     string
	serverSignature []byte
}

// newSCRAMClient builds a client with a random nonce. The n= username is left
// empty, matching Postgres (which uses the startup-packet user).
func newSCRAMClient(password string) (*scramClient, error) {
	nonce, err := randomNonce()
	if err != nil {
		return nil, err
	}
	return &scramClient{password: password, clientNonce: nonce}, nil
}

// newSCRAMClientWithNonce is used by tests to pin the client nonce (and n=
// username) for RFC vector reproduction.
func newSCRAMClientWithNonce(username, password, nonce string) *scramClient {
	return &scramClient{username: username, password: password, clientNonce: nonce}
}

func randomNonce() (string, error) {
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b[:]), nil
}

// clientFirst returns the client-first-message ("n,,n=,r=<nonce>"). Postgres
// takes the username from the startup packet, so the SCRAM n= field is empty.
func (c *scramClient) clientFirst() string {
	c.clientFirstBare = "n=" + c.username + ",r=" + c.clientNonce
	return "n,," + c.clientFirstBare
}

// clientFinal consumes the server-first-message and returns the
// client-final-message with the client proof.
func (c *scramClient) clientFinal(serverFirst string) (string, error) {
	var combinedNonce, saltB64 string
	var iterations int
	for _, field := range strings.Split(serverFirst, ",") {
		if len(field) < 2 || field[1] != '=' {
			return "", fmt.Errorf("pgwire: malformed SCRAM server-first field %q", field)
		}
		val := field[2:]
		switch field[0] {
		case 'r':
			combinedNonce = val
		case 's':
			saltB64 = val
		case 'i':
			n, err := strconv.Atoi(val)
			if err != nil {
				return "", fmt.Errorf("pgwire: bad SCRAM iteration count: %w", err)
			}
			iterations = n
		}
	}
	if combinedNonce == "" || saltB64 == "" || iterations == 0 {
		return "", fmt.Errorf("pgwire: incomplete SCRAM server-first message %q", serverFirst)
	}
	if !strings.HasPrefix(combinedNonce, c.clientNonce) {
		return "", fmt.Errorf("pgwire: SCRAM server nonce does not extend client nonce")
	}
	salt, err := base64.StdEncoding.DecodeString(saltB64)
	if err != nil {
		return "", fmt.Errorf("pgwire: bad SCRAM salt: %w", err)
	}

	saltedPassword, err := pbkdf2.Key(sha256.New, c.password, salt, iterations, sha256.Size)
	if err != nil {
		return "", fmt.Errorf("pgwire: pbkdf2: %w", err)
	}
	clientKey := hmacSHA256(saltedPassword, []byte("Client Key"))
	storedKey := sha256.Sum256(clientKey)
	serverKey := hmacSHA256(saltedPassword, []byte("Server Key"))

	// channel-binding "biws" == base64("n,,")
	clientFinalNoProof := "c=biws,r=" + combinedNonce
	c.authMessage = c.clientFirstBare + "," + serverFirst + "," + clientFinalNoProof

	clientSignature := hmacSHA256(storedKey[:], []byte(c.authMessage))
	proof := make([]byte, len(clientKey))
	for i := range clientKey {
		proof[i] = clientKey[i] ^ clientSignature[i]
	}
	c.serverSignature = hmacSHA256(serverKey, []byte(c.authMessage))

	return clientFinalNoProof + ",p=" + base64.StdEncoding.EncodeToString(proof), nil
}

// verifyServerFinal checks the server-final-message ("v=<ServerSignature>").
func (c *scramClient) verifyServerFinal(serverFinal string) error {
	var sig string
	for _, field := range strings.Split(serverFinal, ",") {
		if strings.HasPrefix(field, "v=") {
			sig = field[2:]
		} else if strings.HasPrefix(field, "e=") {
			return fmt.Errorf("pgwire: SCRAM server error: %s", field[2:])
		}
	}
	if sig == "" {
		return fmt.Errorf("pgwire: SCRAM server-final missing verifier")
	}
	got, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("pgwire: bad SCRAM server signature: %w", err)
	}
	if subtle.ConstantTimeCompare(got, c.serverSignature) != 1 {
		return fmt.Errorf("pgwire: SCRAM server signature mismatch (server not authenticated)")
	}
	return nil
}

func hmacSHA256(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}
