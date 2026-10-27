// The body key's keystore: the one rule the router and the hub share about which secret
// store holds huddle's body key, and when a process may reach it at all.
package keystore

import (
	"github.com/MuhmdRaouf/huddle/internal/core/ports"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// BodyKey is the body key's keystore, or nil where there is none. The key lives in the login
// account's OS secret store, and a process reaches it only when the env it was handed has that
// account's HOME: a temp or missing HOME (a test, an agent, another account) is a different
// profile and gets no store, so it can neither read nor create the key. The router and the hub
// share this one rule.
func BodyKey(env domain.EnvLookup) ports.KeyStore {
	home := accountHome()
	envHome, set := env["HOME"]
	if !set || envHome == "" || resolvePath(envHome) != resolvePath(home) {
		return nil
	}
	id := KeyIdentity{
		Service:  "huddle",
		Account:  "router-body-key",
		Display:  "Huddle",
		Label:    "Huddle router body key",
		AnyValue: true,
	}
	switch nodePlatform() {
	case "darwin":
		return MacosKeyStore(id, systemRunner)
	case "linux":
		return LinuxKeyStore(id, systemRunner)
	}
	return nil
}
