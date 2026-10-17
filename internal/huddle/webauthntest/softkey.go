// Package webauthntest holds the virtual authenticator the huddle tests pair devices with: the
// answers a browser's platform authenticator would give, without the platform. The pairing core
// verifies them like the real ones.
package webauthntest

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
)

// SoftKey is one virtual authenticator: a P-256 key, one credential id, user present and
// verified, attestation none.
type SoftKey struct {
	key    *ecdsa.PrivateKey
	credID []byte
	count  uint32
}

// NewSoftKey draws the key and the credential id.
func NewSoftKey(t testing.TB) *SoftKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, err)
	credID := make([]byte, 32)
	_, err = rand.Read(credID)
	must(t, err)
	return &SoftKey{key: key, credID: credID}
}

// Create answers the registration response for the creation options a begin route served: the
// JSON navigator.credentials.create turns into one credential. The origin is the page's own,
// the one the ceremony ran in.
func (k *SoftKey) Create(t testing.TB, options json.RawMessage, origin string) json.RawMessage {
	t.Helper()
	var parsed struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			RPID      string `json:"rpId"`
			RP        struct {
				ID string `json:"id"`
			} `json:"rp"`
		} `json:"publicKey"`
	}
	mustDecode(t, options, &parsed)
	rpID := parsed.PublicKey.RPID
	if rpID == "" {
		// a pairing begin names the relying party one level down, the way the
		// ceremony's own options carry it
		rpID = parsed.PublicKey.RP.ID
	}
	if parsed.PublicKey.Challenge == "" || rpID == "" {
		t.Fatalf("webauthntest: the creation options name no challenge or relying party: %s", options)
	}
	client := clientData(t, "webauthn.create", parsed.PublicKey.Challenge, origin)
	data := k.authData(rpID, 0x01|0x04|0x40, k.attested(t)) // UP | UV | AT
	attestation := mustCBOR(t, map[string]any{
		"fmt": "none", "attStmt": map[string]any{}, "authData": data,
	})
	return mustJSON(t, map[string]any{
		"id": b64(k.credID), "rawId": b64(k.credID), "type": "public-key",
		"response": map[string]any{"clientDataJSON": b64(client), "attestationObject": b64(attestation)},
	})
}

// Get answers the assertion response for one sign-in or step-up ceremony: challenge, relying
// party and origin as they were offered, and the user handle the pairing enrolled.
func (k *SoftKey) Get(t testing.TB, challenge, rpID, origin, userHandle string) json.RawMessage {
	t.Helper()
	client := clientData(t, "webauthn.get", challenge, origin)
	data := k.authData(rpID, 0x01|0x04, nil) // UP | UV
	signed := append(append([]byte{}, data...), hashOf(client)...)
	sum := sha256.Sum256(signed)
	sig, err := ecdsa.SignASN1(rand.Reader, k.key, sum[:])
	must(t, err)
	return mustJSON(t, map[string]any{
		"id": b64(k.credID), "rawId": b64(k.credID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON": b64(client), "authenticatorData": b64(data),
			"signature": b64(sig), "userHandle": userHandle,
		},
	})
}

// b64 is the base64url text without padding the WebAuthn wire speaks.
func b64(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }

// authData builds one authenticator data blob for the RP, with the attested credential data on
// registration.
func (k *SoftKey) authData(rpID string, flags byte, extra []byte) []byte {
	sum := sha256.Sum256([]byte(rpID))
	out := append([]byte{}, sum[:]...)
	out = append(out, flags)
	var counter [4]byte
	k.count++
	binary.BigEndian.PutUint32(counter[:], k.count)
	out = append(out, counter[:]...)
	return append(out, extra...)
}

// attested is the attested credential data: a zero AAGUID, the credential id, the COSE key.
func (k *SoftKey) attested(t testing.TB) []byte {
	// the uncompressed 0x04 ‖ X ‖ Y encoding, each half 32 bytes
	point, err := k.key.PublicKey.Bytes()
	must(t, err)
	cose := mustCBOR(t, map[int64]any{1: 2, 3: -7, -1: 1, -2: point[1:33], -3: point[33:]})
	out := append([]byte{}, make([]byte, 16)...)
	var len16 [2]byte
	binary.BigEndian.PutUint16(len16[:], uint16(len(k.credID)))
	out = append(out, len16[:]...)
	out = append(out, k.credID...)
	return append(out, cose...)
}

// clientData is the client data JSON of one ceremony.
func clientData(t testing.TB, ceremony, challenge, origin string) []byte {
	return mustJSON(t, struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Origin    string `json:"origin"`
	}{ceremony, challenge, origin})
}

func hashOf(client []byte) []byte {
	sum := sha256.Sum256(client)
	return sum[:]
}

// must stops on an error drawing a key or reading random bytes cannot meet but a torn machine
// must not pass silently.
func must(t testing.TB, err error) {
	if err != nil {
		t.Fatalf("webauthntest: %v", err)
	}
}

func mustDecode(t testing.TB, raw []byte, into any) {
	err := json.Unmarshal(raw, into)
	must(t, err)
}

func mustJSON(t testing.TB, v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("webauthntest: encode: %v", err)
	}
	return raw
}

func mustCBOR(t testing.TB, v any) []byte {
	raw, err := cbor.Marshal(v)
	if err != nil {
		t.Fatalf("webauthntest: encode cbor: %v", err)
	}
	return raw
}
