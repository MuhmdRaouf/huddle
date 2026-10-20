package server

// The VAPID keys every push send signs with (SPEC 7.6, 4.1): the private key prefers the OS
// keystore (service huddle, account vapid) and falls back to a 0600 file in the home after a
// second, so a keystore that hangs holds no push back — and never changes the pair under the
// browsers' subscriptions, because the file answers with the same key the keystore holds.

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"fmt"
	"math/big"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/fsutil"
	"github.com/MuhmdRaouf/huddle/internal/adapters/keystore"
	"github.com/SherClockHolmes/webpush-go"
)

// vapidKeys is the pair one push send signs with: the private key as the keystore and the file
// hold it, and the public one the browsers subscribe with.
type vapidKeys struct {
	Private string
	Public  string
}

// vapidStore is where the private key prefers to live: the OS keystore through its port. Only
// the read and the write travel here — the keystore's kind and label are its own business.
type vapidStore interface {
	Get(ctx context.Context) (string, bool)
	Set(ctx context.Context, key string) error
}

// vapidStoreWait is how long a keystore read may hold its answer before the key file takes over.
const vapidStoreWait = time.Second

// vapidSource answers the push keys, making the pair the first time it is asked and keeping it
// for the source's life.
type vapidSource struct {
	mu    sync.Mutex
	keys  *vapidKeys
	store vapidStore
	file  string
	log   func(string, ...any)
}

// newVapidSource builds the source over its keystore (nil: the key file alone) and key file.
func newVapidSource(store vapidStore, file string, log func(string, ...any)) *vapidSource {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &vapidSource{store: store, file: file, log: log}
}

// Keys answers the pair: the keystore's when it answers within its second, else the key file's,
// else a freshly made pair written to the file first. One pair per source, whatever asks.
func (v *vapidSource) Keys(ctx context.Context) (vapidKeys, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.keys != nil {
		return *v.keys, nil
	}
	keys, err := v.read(ctx)
	if err != nil {
		return vapidKeys{}, err
	}
	v.keys = &keys
	return keys, nil
}

// read is Keys past its cache.
func (v *vapidSource) read(ctx context.Context) (vapidKeys, error) {
	if v.store == nil {
		return v.fromFile(ctx)
	}
	type stored struct {
		key   string
		found bool
	}
	box := make(chan stored, 1)
	wait, cancel := context.WithTimeout(ctx, vapidStoreWait)
	defer cancel()
	go func() {
		key, found := v.store.Get(wait)
		box <- stored{key, found}
	}()
	var unanswered bool
	select {
	case a := <-box:
		if a.found {
			if keys, err := vapidKeysOf(a.key); err == nil {
				v.keepFile(a.key)
				return keys, nil
			}
			// a keystore value that names no private key cannot sign: the file answers instead
			v.log("vapid: the keystore's value is not a private key; the key file answers")
		} else {
			unanswered = true
		}
	case <-wait.Done():
		v.log("vapid: the keystore did not answer in %s; the key file answers", vapidStoreWait)
	case <-ctx.Done():
		return vapidKeys{}, ctx.Err()
	}
	keys, err := v.fromFile(ctx)
	if err != nil {
		return vapidKeys{}, err
	}
	if unanswered {
		// the keystore answered but held no key: this fresh one belongs there too, so the file
		// stays the fallback it is meant to be. Off to the side — a slow store never holds the
		// push that asked for the keys.
		go v.keepStore(keys.Private)
	}
	return keys, nil
}

// fromFile answers the key file's pair, making and writing one when the file holds none.
func (v *vapidSource) fromFile(ctx context.Context) (vapidKeys, error) {
	if raw, err := os.ReadFile(v.file); err == nil {
		if keys, err := vapidKeysOf(string(raw)); err == nil {
			return keys, nil
		}
		// a file that holds no private key is remade: no subscription ever held what it named
		v.log("vapid: the key file holds no private key; making a new pair")
	} else if !os.IsNotExist(err) {
		v.log("vapid: the key file: %v", err)
	}
	private, public, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return vapidKeys{}, fmt.Errorf("make the vapid keys: %w", err)
	}
	if _, err := fsutil.WriteFileAtomic(ctx, v.file, []byte(private), fsutil.WriteOptions{
		Mode: fsutil.Perm(0o600), Mkdir: true,
	}); err != nil {
		return vapidKeys{}, fmt.Errorf("write the vapid key file: %w", err)
	}
	return vapidKeys{Private: private, Public: public}, nil
}

// keepFile lays the keystore's key down as the file too, so a keystore that hangs one day
// answers with the same key the file holds. Best effort either way.
func (v *vapidSource) keepFile(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := fsutil.WriteFileAtomic(ctx, v.file, []byte(key), fsutil.WriteOptions{
		Mode: fsutil.Perm(0o600), Mkdir: true,
	}); err != nil {
		v.log("vapid: the key file keep: %v", err)
	}
}

// keepStore hands one private key to the keystore; its failure only names itself in the log.
func (v *vapidSource) keepStore(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := v.store.Set(ctx, key); err != nil {
		v.log("vapid: the keystore keep: %v", err)
	}
}

// vapidKeysOf answers the pair one stored private key names: the public half is its own P-256
// point, so the key read back anywhere — keystore or file — signs with the one same identity.
func vapidKeysOf(private string) (vapidKeys, error) {
	private = strings.TrimSpace(private)
	raw, err := base64.RawURLEncoding.DecodeString(private)
	if err != nil || len(raw) == 0 || len(raw) > 66 {
		return vapidKeys{}, fmt.Errorf("not a vapid private key")
	}
	d := new(big.Int).SetBytes(raw)
	// a scalar wider than the key size names no P-256 key, and the fixed-size key form needs it
	// to fit; the key's own range check (zero, and nothing at or past the curve order) runs next
	if d.Sign() == 0 || d.BitLen() > 256 {
		return vapidKeys{}, fmt.Errorf("not a vapid private key")
	}
	key, err := ecdh.P256().NewPrivateKey(d.FillBytes(make([]byte, 32)))
	if err != nil {
		return vapidKeys{}, fmt.Errorf("not a vapid private key")
	}
	return vapidKeys{
		Private: private,
		Public:  base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
	}, nil
}

// vapidKeystoreStore is the OS keystore's hold of the private key, nil where this process has
// none: a HOME that is not the login account's home — a sandbox, a test harness — is another
// profile, the rule the providers' own keys follow. The platform stores answer the vapidStore
// port as they are.
func vapidKeystoreStore() vapidStore {
	login, err := user.Current()
	home := ""
	if err == nil {
		home = login.HomeDir
	} else {
		home, _ = os.UserHomeDir()
	}
	if home == "" {
		return nil
	}
	if env := os.Getenv("HOME"); env != "" && filepath.Clean(env) != filepath.Clean(home) {
		return nil
	}
	id := keystore.KeyIdentity{Service: "huddle", Account: "vapid", Display: "Huddle push", AnyValue: true}
	switch runtime.GOOS {
	case "darwin":
		return keystore.MacosKeyStore(id, keystore.SpawnRunner)
	case "linux":
		return keystore.LinuxKeyStore(id, keystore.SpawnRunner)
	}
	return nil
}
