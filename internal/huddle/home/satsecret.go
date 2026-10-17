// The satellite's hub credential at rest (SPEC 4.1): the OS keystore first, under the one
// account a hub's address names; the home's keys/ files hold it where no keystore answers —
// the fallback every secret takes. A join replaces the credential whole, so a leaked home names
// the one credential it carries and a `huddle join-hub` over it replaces it again.

package home

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// SatelliteSecretName answers the account a hub's credential sits under: satellite: plus the
// hub's host, lowercased with its port's ":" spelled "-" — the account shape the store takes.
func SatelliteSecretName(hubURL string) string {
	host := hubURL
	if parsed, err := url.Parse(hubURL); err == nil && parsed.Host != "" {
		host = parsed.Host
	}
	return "satellite:" + strings.ReplaceAll(strings.ToLower(host), ":", "-")
}

// SaveSatelliteSecret stores one hub's credential in the store over any earlier one.
func SaveSatelliteSecret(ctx context.Context, store SecretStore, hubURL, credential string) error {
	if hubURL == "" {
		return fmt.Errorf("no hub url to store the credential under")
	}
	return store.Set(ctx, SatelliteSecretName(hubURL), strings.TrimSpace(credential))
}

// LoadSatelliteSecret answers the credential the store holds for one hub, trimmed; a missing or
// blank value answers an error, because a link with no credential must not start.
func LoadSatelliteSecret(ctx context.Context, store SecretStore, hubURL string) (string, error) {
	raw, ok := store.Get(ctx, SatelliteSecretName(hubURL))
	if !ok {
		return "", fmt.Errorf("no hub credential for %s: join with `huddle join-hub %s <invite>`", hubURL, hubURL)
	}
	credential := strings.TrimSpace(raw)
	if credential == "" {
		return "", fmt.Errorf("the hub credential for %s is empty: join again with `huddle join-hub`", hubURL)
	}
	return credential, nil
}
