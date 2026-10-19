package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// The reserve words: the shell face of the channel's reservations. reserve resolves the paths
// against the session's directory and answers what the server said per path; release takes the
// paths or all of yours; reservations lists them.
func TestReserveWords(t *testing.T) {
	t.Run("reserve sends the resolved absolute paths, the ttl and the note", func(t *testing.T) {
		dir := t.TempDir()
		fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
			return 200, `{"result":{"paths":[{"path":"` + filepath.Join(dir, "src", "a.ts") + `","held":true}]},"text":"` + filepath.Join(dir, "src", "a.ts") + ` held"}`
		})
		w := huddleWorld(t, fake.server.URL, nil)
		w.Dir = dir
		code, out, errOut := runH(t, w, "reserve", "src/a.ts", "--ttl", "30", "--note", "rewriting")
		if code != 0 || errOut != "" {
			t.Fatalf("reserve = (%d, %q, %q)", code, out, errOut)
		}
		call := fake.lastCall()
		if !strings.HasSuffix(call.Path, "/api/c/c/op/reserve") {
			t.Errorf("path = %q", call.Path)
		}
		body := call.Body
		for _, want := range []string{
			`"paths":["` + filepath.Join(dir, "src", "a.ts") + `"]`,
			`"ttl_minutes":30`,
			`"note":"rewriting"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("body = %s, want %s in it", body, want)
			}
		}
		if out != filepath.Join(dir, "src", "a.ts")+" held\n" {
			t.Errorf("out = %q, want the server's line", out)
		}
	})

	t.Run("reserve without a path is a usage refusal", func(t *testing.T) {
		fake := startFakeHuddle(t, func(fakeCall) (int, string) { return 200, `{}` })
		w := huddleWorld(t, fake.server.URL, nil)
		if code, _, errOut := runH(t, w, "reserve"); code != 2 || !strings.Contains(errOut, "reserve <path") {
			t.Errorf("reserve no paths = (%d, %q), want 2 and the usage", code, errOut)
		}
	})

	t.Run("release takes the paths named, or none for all of yours", func(t *testing.T) {
		dir := t.TempDir()
		fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
			return 200, `{"result":{"released":[]},"text":"nothing released"}`
		})
		w := huddleWorld(t, fake.server.URL, nil)
		w.Dir = dir
		if code, out, _ := runH(t, w, "release", "src/a.ts", "/r/b.ts"); code != 0 || out != "nothing released\n" {
			t.Errorf("release paths = (%d, %q)", code, out)
		}
		// the relative path reads against the session's directory, the way reserve resolves it
		if body := fake.lastCall().Body; body != `{"paths":["`+filepath.Join(dir, "src", "a.ts")+`","/r/b.ts"]}` {
			t.Errorf("body = %s", body)
		}
		if code, _, _ := runH(t, w, "release"); code != 0 {
			t.Errorf("release all = %d", code)
		}
		if body := fake.lastCall().Body; body != `{}` {
			t.Errorf("release-all body = %s, want no paths key", body)
		}
	})

	t.Run("reservations lists, --mine keeps yours", func(t *testing.T) {
		fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
			return 200, `{"result":{"reservations":[]},"text":"(no reservations)"}`
		})
		w := huddleWorld(t, fake.server.URL, nil)
		if code, out, _ := runH(t, w, "reservations"); code != 0 || out != "(no reservations)\n" {
			t.Errorf("reservations = (%d, %q)", code, out)
		}
		if body := fake.lastCall().Body; body != `{"mine":false}` {
			t.Errorf("body = %s", body)
		}
		if code, _, _ := runH(t, w, "reservations", "--mine"); code != 0 {
			t.Errorf("reservations --mine = %d", code)
		}
		if body := fake.lastCall().Body; body != `{"mine":true}` {
			t.Errorf("body = %s", body)
		}
	})

	t.Run("the words are in help", func(t *testing.T) {
		fake := startFakeHuddle(t, func(fakeCall) (int, string) { return 200, `{}` })
		w := huddleWorld(t, fake.server.URL, nil)
		_, out, _ := runH(t, w, "help")
		for _, want := range []string{"huddle reserve <path", "huddle release [path", "huddle reservations [--mine]"} {
			if !strings.Contains(out, want) {
				t.Errorf("help lacks %q", want)
			}
		}
	})
}
