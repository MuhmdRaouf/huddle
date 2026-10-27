package ledger

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// The body store's key. One key per machine, created once and read back after its first
// store, then answered from the store on every later open. Losing it loses only the
// stored bodies: nothing else in router.db is sealed with it. Errors never carry the key
// or its encoding.

// KeySource holds the machine's body key as its encoding. The store's owner supplies one;
// the ledger never looks behind the interface.
type KeySource interface {
	Get(ctx context.Context) (string, bool)
	Set(ctx context.Context, value string) error
}

// bodyKeyLen is the key's length in bytes: an AES-256 key.
const bodyKeyLen = 32

// LoadBodyKey answers the machine's body key, drawing and storing a first one when the
// store holds none. A stored value decodes as base64 to exactly 32 bytes, or the load is
// an error and nothing is written over it. A drawn key is stored as its encoding only
// after the read-back decodes to the same 32 bytes; a refused store or a changed read-back
// is an error, and no key is answered.
func LoadBodyKey(ctx context.Context, store KeySource, random io.Reader) ([]byte, error) {
	if value, ok := store.Get(ctx); ok {
		return decodeBodyKey(value)
	}
	fresh := make([]byte, bodyKeyLen)
	if _, err := io.ReadFull(random, fresh); err != nil {
		return nil, fmt.Errorf("draw a body key: %w", err)
	}
	if err := store.Set(ctx, base64.RawStdEncoding.EncodeToString(fresh)); err != nil {
		return nil, fmt.Errorf("store the body key: %w", err)
	}
	value, ok := store.Get(ctx)
	if !ok {
		return nil, errors.New("the body key stored is not there on the read-back")
	}
	readBack, err := decodeBodyKey(value)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(readBack, fresh) {
		return nil, errors.New("the body key read back differs from the one stored")
	}
	return readBack, nil
}

// ReadBodyKey answers the machine's body key from the store, reading only: a reader never
// draws or stores a key, so a store with no value is bodies off, not a first key. A stored
// value goes through the same decode as a load — the key and true, or the decode's error,
// which never carries the value.
func ReadBodyKey(ctx context.Context, store KeySource) ([]byte, bool, error) {
	value, ok := store.Get(ctx)
	if !ok {
		return nil, false, nil
	}
	key, err := decodeBodyKey(value)
	if err != nil {
		return nil, false, err
	}
	return key, true, nil
}

// decodeBodyKey answers the 32 bytes a stored encoding holds. The error says what is
// wrong with the value, never what the value is.
func decodeBodyKey(value string) ([]byte, error) {
	key, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		return nil, errors.New("the stored body key is not a base64 encoding")
	}
	if len(key) != bodyKeyLen {
		return nil, fmt.Errorf("the stored body key decodes to %d bytes, want %d", len(key), bodyKeyLen)
	}
	return key, nil
}
