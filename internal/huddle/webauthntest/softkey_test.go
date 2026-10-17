package webauthntest

// The authenticator's own check: what Create answers carries the credential, and what Get signs
// verifies against that same key — the property every pairing and step-up test leans on.

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestSoftKeyAssertionVerifiesAgainstItsOwnKey(t *testing.T) {
	key := NewSoftKey(t)
	const (
		challenge = "Y2hhbGxlbmdl" // base64url of "challenge"
		origin    = "http://localhost:18760"
		rpID      = "localhost"
		handle    = "dXNlcg"
	)
	options := []byte(`{"publicKey":{"challenge":"` + challenge + `","rpId":"` + rpID + `","user":{"id":"` + handle + `"}}}`)
	created := key.Create(t, options, origin)

	var registration struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Response struct {
			ClientDataJSON    string `json:"clientDataJSON"`
			AttestationObject string `json:"attestationObject"`
		} `json:"response"`
	}
	if err := json.Unmarshal(created, &registration); err != nil {
		t.Fatalf("the registration response: %v", err)
	}
	if registration.Type != "public-key" || registration.ID == "" {
		t.Fatalf("the registration = %s", created)
	}
	var ceremony struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Origin    string `json:"origin"`
	}
	if err := json.Unmarshal(decoded(t, registration.Response.ClientDataJSON), &ceremony); err != nil {
		t.Fatalf("the client data: %v", err)
	}
	// the challenge travels the wire as it was offered: base64url text, verbatim
	if ceremony.Type != "webauthn.create" || ceremony.Challenge != challenge || ceremony.Origin != origin {
		t.Fatalf("the ceremony's client data = %+v", ceremony)
	}

	assertion := key.Get(t, challenge, rpID, origin, handle)
	var answer struct {
		ID       string `json:"id"`
		Response struct {
			AuthenticatorData string `json:"authenticatorData"`
			Signature         string `json:"signature"`
			ClientDataJSON    string `json:"clientDataJSON"`
			UserHandle        string `json:"userHandle"`
		} `json:"response"`
	}
	if err := json.Unmarshal(assertion, &answer); err != nil {
		t.Fatalf("the assertion response: %v", err)
	}
	if answer.ID != registration.ID || answer.Response.UserHandle != handle {
		t.Fatalf("the assertion = %s, want the same credential for %q", assertion, handle)
	}
	data := decoded(t, answer.Response.AuthenticatorData)
	sum := sha256.Sum256([]byte(rpID))
	if string(data[:32]) != string(sum[:]) {
		t.Fatal("the assertion's authenticator data hashes another relying party")
	}
	if data[32] != 0x05 { // UP | UV
		t.Fatalf("the assertion's flags = %#x, want user present and verified", data[32])
	}
	clientBytes := decoded(t, answer.Response.ClientDataJSON)
	signed := append(append([]byte{}, data...), hashOf(clientBytes)...)
	covered := sha256.Sum256(signed)
	sig, err := base64.RawURLEncoding.DecodeString(answer.Response.Signature)
	if err != nil {
		t.Fatalf("the signature: %v", err)
	}
	if !ecdsa.VerifyASN1(&key.key.PublicKey, covered[:], sig) {
		t.Fatal("the assertion does not verify against the key that made the credential")
	}
}

// decoded turns one base64url wire blob back into bytes.
func decoded(t *testing.T, raw string) []byte {
	t.Helper()
	out, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return out
}
