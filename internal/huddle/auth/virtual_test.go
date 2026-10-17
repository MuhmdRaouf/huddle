package auth

// A software ES256 authenticator for the pairing tests: it holds one P-256 key pair, mints one
// credential id, and speaks the two ceremonies the browser would (attestation "none", user
// present and verified, resident key). It never reads the filesystem or the network, so the
// tests that drive it stay hermetic.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol"
)

// softToken is one virtual authenticator: a key pair, a credential id and a signature counter.
type softToken struct {
	key    *ecdsa.PrivateKey
	credID []byte
	count  uint32
}

// newSoftToken draws a fresh key pair, so every test signs as its own device.
func newSoftToken(t *testing.T) *softToken {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	credID := make([]byte, 32)
	if _, err := rand.Read(credID); err != nil {
		t.Fatalf("draw credential id: %v", err)
	}
	return &softToken{key: key, credID: credID}
}

// b64 answers the base64url encoding the WebAuthn wire format uses, without padding.
func b64(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }

// wireText reads one field the options carry (the challenge, the user handle) as the base64url
// text the wire form speaks: the client data JSON carries exactly this string.
func wireText(t *testing.T, v any) string {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("read a base64url field: %v", err)
	}
	var text string
	if err := json.Unmarshal(encoded, &text); err != nil {
		t.Fatalf("read a base64url field: %v", err)
	}
	return text
}

// coseKey encodes the public key as the COSE map an attested credential carries: EC2, ES256,
// P-256, the two coordinates as byte strings.
func (a *softToken) coseKey() []byte {
	// the uncompressed 0x04 ‖ X ‖ Y encoding, each half 32 bytes
	point, err := a.key.PublicKey.Bytes()
	if err != nil {
		panic(err)
	}
	encoded, err := cbor.Marshal(map[int64]any{1: 2, 3: -7, -1: 1, -2: point[1:33], -3: point[33:]})
	if err != nil {
		panic(err)
	}
	return encoded
}

// authData builds one authenticator data blob: the RP ID's hash, the flags (user present and
// verified, plus the attested credential on registration), the counter, and on registration the
// attested credential data (a zero AAGUID, the credential id, the COSE key).
func (a *softToken) authData(rpID string, attested bool, flags byte) []byte {
	sum := sha256.Sum256([]byte(rpID))
	out := append([]byte{}, sum[:]...)
	out = append(out, flags)
	var counter [4]byte
	a.count++
	binary.BigEndian.PutUint32(counter[:], a.count)
	out = append(out, counter[:]...)
	if attested {
		out = append(out, make([]byte, 16)...) // a zero AAGUID: no model to name
		var len16 [2]byte
		binary.BigEndian.PutUint16(len16[:], uint16(len(a.credID)))
		out = append(out, len16[:]...)
		out = append(out, a.credID...)
		out = append(out, a.coseKey()...)
	}
	return out
}

// sign draws one ES256 signature over what an assertion signs: the authenticator data followed
// by the client data's hash, DER encoded the way the verifier expects.
func (a *softToken) sign(data []byte) []byte {
	sum := sha256.Sum256(data)
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, sum[:])
	if err != nil {
		panic(err)
	}
	return sig
}

// clientData is the client data JSON of one ceremony; challenge is the base64url string the
// options carried.
func clientData(ceremony, challenge, origin string) []byte {
	raw, err := json.Marshal(struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Origin    string `json:"origin"`
	}{ceremony, challenge, origin})
	if err != nil {
		panic(err)
	}
	return raw
}

// createOptions holds what the tests need from creation options: the challenge, the RP ID and
// the user handle the ceremony enrolled, both challenges in the base64url text the wire speaks.
type createOptions struct {
	challenge     string
	rpID          string
	userHandleB64 string
}

// creationOf reads one server's creation options into the shape the authenticator signs against.
func creationOf(t *testing.T, creation *protocol.CredentialCreation) createOptions {
	t.Helper()
	return createOptions{
		challenge:     wireText(t, creation.Response.Challenge),
		rpID:          creation.Response.RelyingParty.ID,
		userHandleB64: wireText(t, creation.Response.User.ID),
	}
}

// create answers the registration body a browser would post for these options. A challenge of
// the caller's choosing signs what the caller wants signed, which is how the refusal tests
// build their wrong answers.
func (a *softToken) create(t *testing.T, o createOptions, origin, challengeOverride string) []byte {
	t.Helper()
	challenge := o.challenge
	if challengeOverride != "" {
		challenge = challengeOverride
	}
	client := clientData("webauthn.create", challenge, origin)
	data := a.authData(o.rpID, true, 0x01|0x04|0x40) // UP | UV | AT
	attestation, err := cbor.Marshal(map[string]any{
		"fmt": "none", "attStmt": map[string]any{}, "authData": data,
	})
	if err != nil {
		t.Fatalf("encode the attestation object: %v", err)
	}
	return marshalJSON(t, map[string]any{
		"id":       b64(a.credID),
		"rawId":    b64(a.credID),
		"type":     "public-key",
		"response": map[string]any{"clientDataJSON": b64(client), "attestationObject": b64(attestation)},
	})
}

// get answers the assertion body a browser would post for these options. brokenSignature signs
// bytes no public key verifies.
func (a *softToken) get(t *testing.T, challenge, rpID, origin, userHandleB64 string, brokenSignature bool) []byte {
	t.Helper()
	client := clientData("webauthn.get", challenge, origin)
	data := a.authData(rpID, false, 0x01|0x04) // UP | UV
	sig := a.sign(append(append([]byte{}, data...), hashOf(client)...))
	if brokenSignature {
		sig[0] ^= 0xff // a signature no public key verifies
	}
	return marshalJSON(t, map[string]any{
		"id":    b64(a.credID),
		"rawId": b64(a.credID),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON": b64(client), "authenticatorData": b64(data), "signature": b64(sig),
			"userHandle": userHandleB64,
		},
	})
}

// hashOf is the client data's SHA-256, what an assertion's signature covers beside the
// authenticator data.
func hashOf(client []byte) []byte {
	sum := sha256.Sum256(client)
	return sum[:]
}

// marshalJSON encodes one body the way the server reads it, failing the test on a shape it
// cannot encode.
func marshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode body: %v", err)
	}
	return raw
}
