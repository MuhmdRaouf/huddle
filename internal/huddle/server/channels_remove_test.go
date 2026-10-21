package server

// DELETE /api/channels/<name> takes a channel off the hub: the creator only, never one with a
// session online, the kept file's path in the answer.
import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

func TestChannelsRemove(t *testing.T) {
	srv := startServer(t, nil)
	root := map[string]string{"x-huddle-token": srv.token}
	del := func(path string, headers map[string]string) (int, string) {
		t.Helper()
		code, _, out, err := srv.request(bg, http.MethodDelete, path, "", headers)
		if err != nil {
			t.Fatalf("DELETE %s: %v", path, err)
		}
		return code, out
	}

	// a member's credential: the invite story's own steps
	if code, out := srv.op(t, "team", "join", "lead", "{}"); code != 200 {
		t.Fatalf("owner join = %d %s", code, out)
	}
	code, out := srv.post(t, "/api/tokens", "{}", root)
	if code != 200 {
		t.Fatalf("create invite = %d %s", code, out)
	}
	invite := bodyMap(t, out)
	token, _ := invite["token"].(string)
	code, out = srv.post(t, "/api/join", `{"token": "`+token+`", "name": "dev"}`, nil)
	if code != 200 {
		t.Fatalf("join = %d %s", code, out)
	}
	cred, _ := bodyMap(t, out)["credential"].(string)
	if cred == "" {
		t.Fatalf("join answer = %s", out)
	}
	dev := map[string]string{"x-huddle-token": cred}

	t.Run("a member is refused", func(t *testing.T) {
		if code, out := del("/api/channels/team", dev); code != 403 {
			t.Errorf("member remove = %d %s, want 403", code, out)
		}
	})

	t.Run("an unknown channel is not found", func(t *testing.T) {
		if code, out := del("/api/channels/ghost", root); code != 404 {
			t.Errorf("remove(ghost) = %d %s, want 404", code, out)
		}
	})

	t.Run("a channel with a session online is refused", func(t *testing.T) {
		if code, out := srv.op(t, "busy", "join", "keeper", "{}"); code != 200 {
			t.Fatalf("join busy = %d %s", code, out)
		}
		code, out := del("/api/channels/busy", root)
		if code != 409 {
			t.Fatalf("remove(busy) = %d %s, want 409", code, out)
		}
		if !strings.Contains(out, "busy has 1 session(s) online") {
			t.Errorf("the refusal = %s, want the online count", out)
		}
		if _, err := os.Stat(srv.dataDir + "/channels/busy.db"); err != nil {
			t.Errorf("busy.db: %v", err)
		}
	})

	t.Run("a session silent past the stale mark does not hold the channel", func(t *testing.T) {
		// a session that crashed or was kicked never leaves: only its silence says it is gone
		if code, out := srv.op(t, "stale", "join", "ghost", "{}"); code != 200 {
			t.Fatalf("join stale = %d %s", code, out)
		}
		db, err := store.Open(bg, srv.dataDir+"/channels/stale.db")
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Run(bg, "UPDATE sessions SET last_seen = '2000-01-01T00:00:00Z' WHERE name = 'ghost'"); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()
		if code, out := del("/api/channels/stale", root); code != 200 {
			t.Errorf("remove(stale) = %d %s, want 200", code, out)
		}
	})

	t.Run("a quiet channel is removed and its file kept", func(t *testing.T) {
		if code, out := srv.post(t, "/api/channels", `{"name": "gone"}`, root); code != 200 {
			t.Fatalf("create gone = %d %s", code, out)
		}
		code, out := del("/api/channels/gone", root)
		if code != 200 {
			t.Fatalf("remove(gone) = %d %s, want 200", code, out)
		}
		body := bodyMap(t, out)
		if body["removed"] != "gone" {
			t.Errorf("removed = %v, want gone", body["removed"])
		}
		kept, _ := body["kept"].(string)
		if !strings.Contains(kept, "/channels/.removed/gone-") || !strings.HasSuffix(kept, ".db") {
			t.Errorf("kept = %q, want a .removed path", kept)
		}
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("the kept file: %v", err)
		}
		if _, _, list := srv.get(t, "/api/channels", root); strings.Contains(list, `"gone"`) {
			t.Errorf("the channel list still has gone: %s", list)
		}
	})
}
