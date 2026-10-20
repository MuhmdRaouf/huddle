package cli

// The authenticator words: `huddle totp setup` prints the QR and the secret, reads the
// confirming code from the terminal and answers the recovery codes once, and `huddle totp off`
// removes the enrolment with its sessions. The codes come from a hand-rolled RFC 6238, so the
// words are proven against the specification.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
)

// rfc6238 answers the code the specification draws for one secret at one step.
func rfc6238(secret string, step int64) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		panic(err)
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	at := int(sum[len(sum)-1] & 0x0f)
	code := (uint32(sum[at])&0x7f)<<24 | uint32(sum[at+1])<<16 | uint32(sum[at+2])<<8 | uint32(sum[at+3])
	return fmt.Sprintf("%06d", code%1_000_000)
}

// syncBuffer collects one stream from the goroutine that runs the command.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// runSetup answers typed words once the setup printed its secret: the terminal reads the QR and
// the secret before it types, and the words are drawn from what this very run printed.
func runTotpSetup(t *testing.T, w *World, typed func(secret string) string) (int, string, string) {
	t.Helper()
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdinR.Close()
	w.Stdin = stdinR
	out := &syncBuffer{}
	errOut := &syncBuffer{}
	type answer struct {
		code int
	}
	done := make(chan answer, 1)
	go func() {
		code := Huddle(context.Background(), w, []string{"totp", "setup"}, io.MultiWriter(stdoutW, out), errOut)
		_ = stdoutW.Close()
		done <- answer{code}
	}()
	secret := ""
	scanner := io.Reader(stdoutR)
	buf := make([]byte, 4096)
	for secret == "" {
		n, readErr := scanner.Read(buf)
		if n > 0 && regexp.MustCompile(`secret=([A-Z2-7]+)`).Match(buf[:n]) {
			found := regexp.MustCompile(`secret=([A-Z2-7]+)`).FindSubmatch(buf[:n])
			secret = string(found[1])
		}
		if readErr != nil {
			break
		}
	}
	if secret == "" {
		t.Fatalf("the setup printed no secret:\n%s", out.String())
	}
	_, _ = stdinW.WriteString(typed(secret))
	_ = stdinW.Close()
	result := <-done
	_ = stdoutR.Close()
	return result.code, out.String(), errOut.String()
}

// TestTotpSetupPrintsCodesAndOffRemoves pins the terminal path: the QR, the otpauth URI and the
// grouped secret; a wrong code asked again and the right one answered with the ten recovery
// codes that show once; a second setup refused while one lives; the off word ending it.
func TestTotpSetupPrintsCodesAndOffRemoves(t *testing.T) {
	homeDir, w := oneHome(t)

	code, out, errOut := runTotpSetup(t, w, func(secret string) string {
		return "000000\n" + rfc6238(secret, time.Now().Unix()/30) + "\n"
	})
	if code != 0 {
		t.Fatalf("huddle totp setup = %d: %s", code, errOut)
	}
	uri := regexp.MustCompile(`otpauth://totp/[^\s]+`).FindString(out)
	if uri == "" || !strings.Contains(uri, "issuer=Huddle") {
		t.Errorf("the otpauth URI is missing or wrong:\n%s", out)
	}
	secret := regexp.MustCompile(`secret=([A-Z2-7]+)`).FindStringSubmatch(uri)
	if secret == nil {
		t.Fatalf("the otpauth URI carries no secret:\n%s", out)
	}
	if !strings.Contains(out, "▀") && !strings.Contains(out, "█") {
		t.Errorf("no QR code was printed:\n%s", out)
	}
	if !strings.Contains(out, secretSpaced(secret[1])) {
		t.Errorf("the secret is not printed in groups of 4:\n%s", out)
	}
	if !strings.Contains(out, "did not work") {
		t.Errorf("the wrong code's refusal is missing:\n%s", out)
	}
	// each code sits on its own line: the otpauth label carries the hostname, which can hold
	// the same shape
	recovery := regexp.MustCompile(`(?m)^\s*[a-z2-7]{4}-[a-z2-7]{4}\s*$`).FindAllString(out, -1)
	if len(recovery) != 10 {
		t.Fatalf("the setup printed %d recovery codes, want 10:\n%s", len(recovery), out)
	}

	// the enrolment landed in the home's hub database
	db, err := hub.Open(context.Background(), filepath.Join(homeDir, "hub.db"))
	if err != nil {
		t.Fatalf("open the home's hub.db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, ok, err := db.TOTP(context.Background()); err != nil || !ok {
		t.Fatalf("the enrolment did not land: %v (ok %t)", err, ok)
	}

	// a second setup is refused while one lives
	var plain, plainErr bytes.Buffer
	w.Stdin = strings.NewReader("")
	if second := Huddle(context.Background(), w, []string{"totp", "setup"}, &plain, &plainErr); second != 2 ||
		!strings.Contains(plainErr.String(), "totp off") {
		t.Errorf("a second setup = %d (%q), want 2 with the off word", second, plainErr.String())
	}

	// the off word removes it, and a second off says there is nothing there
	plain.Reset()
	plainErr.Reset()
	if third := Huddle(context.Background(), w, []string{"totp", "off"}, &plain, &plainErr); third != 0 ||
		!strings.Contains(plain.String(), "removed") {
		t.Fatalf("huddle totp off = %d: %s (%s)", third, plain.String(), plainErr.String())
	}
	plain.Reset()
	plainErr.Reset()
	if fourth := Huddle(context.Background(), w, []string{"totp", "off"}, &plain, &plainErr); fourth != 4 {
		t.Errorf("a second off = %d (%q), want 4", fourth, plainErr.String())
	}

	// usage: the word alone names its two halves
	plain.Reset()
	plainErr.Reset()
	if fifth := Huddle(context.Background(), w, []string{"totp"}, &plain, &plainErr); fifth != 2 || plainErr.Len() == 0 {
		t.Errorf("huddle totp alone = %d (%q), want 2 with a usage line", fifth, plainErr.String())
	}
}
