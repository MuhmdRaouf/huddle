package ledger

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// LoadBodyKey's cases, against a fake KeySource and a fake random reader. No keystore and
// no OS store: every value here is a fake built in the test, and no failure prints one.

// drawnKey is what the fake random reader answers: 32 known bytes.
var drawnKey = []byte{
	0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
	16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31,
}

// fakeKeySource is a KeySource over one in-memory value, counting its calls. A non-nil
// setErr makes every Set fail; shuffleSet makes Set store a value one letter away from
// the one it was given, so the read-back differs.
type fakeKeySource struct {
	stored     string
	ok         bool
	setErr     error
	shuffleSet bool
	sets       int
	gets       int
}

func (f *fakeKeySource) Get(ctx context.Context) (string, bool) {
	f.gets++
	return f.stored, f.ok
}

func (f *fakeKeySource) Set(ctx context.Context, value string) error {
	f.sets++
	if f.setErr != nil {
		return f.setErr
	}
	if f.shuffleSet {
		value = oneLetterAway(value)
	}
	f.stored = value
	f.ok = true
	return nil
}

// oneLetterAway answers value with its last letter moved one step in the base64
// alphabet: still a valid encoding of 32 bytes, but different ones.
func oneLetterAway(value string) string {
	if strings.HasSuffix(value, "A") {
		return value[:len(value)-1] + "B"
	}
	return value[:len(value)-1] + "A"
}

// TestLoadBodyKeyDrawsAndStoresAFirstKey: with nothing stored, the key is drawn from
// random, stored once as its encoding and answered after the read-back confirms it.
func TestLoadBodyKeyDrawsAndStoresAFirstKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := &fakeKeySource{}
	key, err := LoadBodyKey(ctx, source, bytes.NewReader(drawnKey))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !bytes.Equal(key, drawnKey) {
		t.Fatalf("key = %d bytes, want the 32 the random reader gave", len(key))
	}
	if source.sets != 1 {
		t.Fatalf("sets = %d, want 1", source.sets)
	}
	if source.gets != 2 {
		t.Fatalf("gets = %d, want the read-back after the store", source.gets)
	}
	if source.stored != base64.RawStdEncoding.EncodeToString(drawnKey) {
		t.Fatal("the source holds something other than the key's encoding")
	}
}

// TestLoadBodyKeyAnswersTheSameKeyWithoutStoring: a second load answers the same key,
// drawing nothing (the random reader is spent) and storing nothing.
func TestLoadBodyKeyAnswersTheSameKeyWithoutStoring(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := &fakeKeySource{}
	first, err := LoadBodyKey(ctx, source, bytes.NewReader(drawnKey))
	if err != nil {
		t.Fatalf("the first load: %v", err)
	}
	second, err := LoadBodyKey(ctx, source, bytes.NewBuffer(nil))
	if err != nil {
		t.Fatalf("the second load: %v", err)
	}
	if !bytes.Equal(second, first) {
		t.Fatal("the second load answered a different key")
	}
	if source.sets != 1 {
		t.Fatalf("sets = %d, want still the first load's 1", source.sets)
	}
	if source.gets != 3 {
		t.Fatalf("gets = %d, want 3", source.gets)
	}
}

// TestLoadBodyKeyAnswersAStoredKey: a value already stored decodes to the key it came
// from, with no Set and no draw.
func TestLoadBodyKeyAnswersAStoredKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := &fakeKeySource{stored: base64.RawStdEncoding.EncodeToString(drawnKey), ok: true}
	key, err := LoadBodyKey(ctx, source, bytes.NewBuffer(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !bytes.Equal(key, drawnKey) {
		t.Fatal("the stored key decoded to other bytes")
	}
	if source.sets != 0 {
		t.Fatalf("sets = %d, want none", source.sets)
	}
	if source.gets != 1 {
		t.Fatalf("gets = %d, want 1", source.gets)
	}
}

// TestLoadBodyKeyRefusesAStoredValueItCannotUse: a stored value that is not a 32-byte
// base64 body is an error, and nothing is written over it.
func TestLoadBodyKeyRefusesAStoredValueItCannotUse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("a stored value of the wrong length", func(t *testing.T) {
		t.Parallel()
		source := &fakeKeySource{stored: base64.RawStdEncoding.EncodeToString(make([]byte, 16)), ok: true}
		key, err := LoadBodyKey(ctx, source, bytes.NewReader(drawnKey))
		if err == nil {
			t.Fatal("load a 16-byte stored value = nil error, want an error")
		}
		if key != nil {
			t.Fatal("load a 16-byte stored value answers a key")
		}
		if source.sets != 0 {
			t.Fatalf("sets = %d, want none", source.sets)
		}
	})
	t.Run("a stored value that is not base64", func(t *testing.T) {
		t.Parallel()
		source := &fakeKeySource{stored: "not base64 at all!!", ok: true}
		key, err := LoadBodyKey(ctx, source, bytes.NewReader(drawnKey))
		if err == nil {
			t.Fatal("load a non-base64 stored value = nil error, want an error")
		}
		if key != nil {
			t.Fatal("load a non-base64 stored value answers a key")
		}
		if source.sets != 0 {
			t.Fatalf("sets = %d, want none", source.sets)
		}
	})
}

// TestLoadBodyKeyRefusesAFailedSet: a store that refuses the write is an error, and no
// key is answered.
func TestLoadBodyKeyRefusesAFailedSet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := &fakeKeySource{setErr: errors.New("the store refused the write")}
	key, err := LoadBodyKey(ctx, source, bytes.NewReader(drawnKey))
	if err == nil {
		t.Fatal("load with a failing Set = nil error, want an error")
	}
	if key != nil {
		t.Fatal("load with a failing Set answers a key")
	}
	if source.sets != 1 {
		t.Fatalf("sets = %d, want 1", source.sets)
	}
}

// TestLoadBodyKeyRefusesADifferentReadBack: a store that reads back a value other than
// the one Set wrote is an error, and no key is answered.
func TestLoadBodyKeyRefusesADifferentReadBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := &fakeKeySource{shuffleSet: true}
	key, err := LoadBodyKey(ctx, source, bytes.NewReader(drawnKey))
	if err == nil {
		t.Fatal("load with a changed read-back = nil error, want an error")
	}
	if key != nil {
		t.Fatal("load with a changed read-back answers a key")
	}
	if source.sets != 1 {
		t.Fatalf("sets = %d, want 1", source.sets)
	}
}
