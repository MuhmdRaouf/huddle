package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// quotaHome is one test's radar state dir: quota.json lands there and nowhere else.
func quotaHome(t *testing.T) domain.EnvLookup {
	t.Helper()
	return domain.EnvLookup{"RADAR_HOME": t.TempDir()}
}

// quotaBody decodes one POST body the way the write route hands it over.
func quotaBody(t *testing.T, raw string) *jsfmt.Value {
	t.Helper()
	value, err := jsfmt.DecodeValue([]byte(raw))
	if err != nil {
		t.Fatalf("decode the quota body %s: %v", raw, err)
	}
	return value
}

func TestQuotaRouteStoresAndAnswersTheNewest(t *testing.T) {
	quotas := CreateQuotas(quotaHome(t))
	answer := HandleQuotaWrite(context.Background(), quotas, quotaBody(t,
		`{"session_id":"s1","five_hour":42,"five_hour_resets_at":2000000000,"seven_day":18,"at":1000}`))
	if answer.Status != 200 {
		t.Fatalf("the write = %d, want 200", answer.Status)
	}
	// a second session, newer: the newest overall is the one the read answers
	if got := HandleQuotaWrite(context.Background(), quotas, quotaBody(t,
		`{"session_id":"s2","five_hour":91,"at":2000}`)); got.Status != 200 {
		t.Fatalf("the second write = %d, want 200", got.Status)
	}
	read := HandleQuotaRead(quotas)
	if read.Status != 200 {
		t.Fatalf("the read = %d, want 200", read.Status)
	}
	var snapshot QuotaSnapshot
	if err := json.Unmarshal(marshalBody(t, read), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.SessionID != "s2" || snapshot.FiveHour == nil || *snapshot.FiveHour != 91 || snapshot.At != 2000 {
		t.Fatalf("the newest snapshot = %+v, want s2's", snapshot)
	}
}

func TestQuotaRouteKeepsTheLatestPerSession(t *testing.T) {
	quotas := CreateQuotas(quotaHome(t))
	post := func(raw string) {
		if got := HandleQuotaWrite(context.Background(), quotas, quotaBody(t, raw)); got.Status != 200 {
			t.Fatalf("the write %s = %d, want 200", raw, got.Status)
		}
	}
	post(`{"session_id":"s1","five_hour":10,"at":100}`)
	post(`{"session_id":"s1","five_hour":20,"at":200}`)
	// an older report of the same session never replaces the newer one it holds
	post(`{"session_id":"s1","five_hour":5,"at":150}`)
	read := HandleQuotaRead(quotas)
	var snapshot QuotaSnapshot
	if err := json.Unmarshal(marshalBody(t, read), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.FiveHour == nil || *snapshot.FiveHour != 20 || snapshot.At != 200 {
		t.Fatalf("the latest snapshot = %+v, want the at-200 one", snapshot)
	}
}

func TestQuotaRouteSurvivesARestartOnQuotaDotJSON(t *testing.T) {
	home := quotaHome(t)
	quotas := CreateQuotas(home)
	if got := HandleQuotaWrite(context.Background(), quotas, quotaBody(t,
		`{"session_id":"s1","five_hour":42,"seven_day":18,"at":1000}`)); got.Status != 200 {
		t.Fatalf("the write = %d, want 200", got.Status)
	}
	raw, err := os.ReadFile(filepath.Join(home["RADAR_HOME"], "quota.json"))
	if err != nil {
		t.Fatalf("no quota.json in the state dir: %v", err)
	}
	if !strings.Contains(string(raw), `"session_id"`) || !strings.Contains(string(raw), `"at": 1000`) {
		t.Fatalf("quota.json holds %s, want the stored snapshot", raw)
	}
	info, err := os.Stat(filepath.Join(home["RADAR_HOME"], "quota.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("quota.json mode %o, want 0600", info.Mode().Perm())
	}
	// a fresh server (a restart) reads the file, so the card does not go dark
	again := CreateQuotas(home)
	read := HandleQuotaRead(again)
	var snapshot QuotaSnapshot
	if err := json.Unmarshal(marshalBody(t, read), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.SessionID != "s1" || snapshot.At != 1000 {
		t.Fatalf("the restarted read = %+v, want the stored snapshot", snapshot)
	}
}

func TestQuotaRouteReadsWithNothingStored(t *testing.T) {
	quotas := CreateQuotas(quotaHome(t))
	read := HandleQuotaRead(quotas)
	if read.Status != 200 {
		t.Fatalf("the read = %d, want 200", read.Status)
	}
	if body := marshalBody(t, read); string(body) != "{}" {
		t.Fatalf("the empty read = %s, want {}", body)
	}
}

func TestQuotaRouteShapesItsAnswerToTheBody(t *testing.T) {
	quotas := CreateQuotas(quotaHome(t))
	cases := []struct {
		name string
		body string
		want string
	}{
		{"nulls stay null and leave the fields out", `{"session_id":"s1","five_hour":null,"seven_day":null,"at":5}`, `{"session_id":"s1","at":5}`},
		{"a non-number window reads as null", `{"session_id":"s2","five_hour":"high","at":6}`, `{"session_id":"s2","at":6}`},
		{"no at names the server's now", `{"session_id":"s3"}`, `{"session_id":"s3","at":7000}`},
	}
	quotas.now = func() int64 { return 7000 }
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HandleQuotaWrite(context.Background(), quotas, quotaBody(t, tc.body)); got.Status != 200 {
				t.Fatalf("the write = %d, want 200", got.Status)
			}
			if body := string(marshalBody(t, HandleQuotaRead(quotas))); body != tc.want {
				t.Fatalf("the stored snapshot = %s, want %s", body, tc.want)
			}
		})
	}
}

func TestQuotaRouteRefusesABadBody(t *testing.T) {
	quotas := CreateQuotas(quotaHome(t))
	answer := HandleQuotaWrite(context.Background(), quotas, quotaBody(t, `[1,2]`))
	if answer.Status != 400 {
		t.Fatalf("an array body = %d, want 400", answer.Status)
	}
	if got := HandleQuotaWrite(context.Background(), nil, quotaBody(t, `{}`)); got.Status != 503 {
		t.Fatalf("a write with no quotas = %d, want 503", got.Status)
	}
	if got := HandleQuotaRead(nil); got.Status != 503 {
		t.Fatalf("a read with no quotas = %d, want 503", got.Status)
	}
}

func TestQuotaRouteRidesTheHTTPGuard(t *testing.T) {
	home := quotaHome(t)
	_, port := serve(t, HttpOptions{Store: store.CreateStore(clock), Version: "t", StartedAt: 1, Quotas: CreateQuotas(home)})
	post := func(headers map[string]string) reply {
		return call(t, port, "POST", "/api/quota",
			`{"session_id":"s1","five_hour":42,"at":1}`, headers)
	}
	good := post(map[string]string{"Content-Type": "application/json"})
	if good.status != 200 {
		t.Fatalf("a json write = %d %s, want 200", good.status, good.body)
	}
	if got := post(map[string]string{"Content-Type": "text/plain"}); got.status != 403 {
		t.Fatalf("a non-json write = %d, want 403", got.status)
	}
	read := call(t, port, "GET", "/api/quota", "", nil)
	if read.status != 200 {
		t.Fatalf("the read = %d, want 200", read.status)
	}
	if !strings.Contains(read.body, `"session_id":"s1"`) {
		t.Fatalf("the read = %s, want the stored snapshot", read.body)
	}
	if got := call(t, port, "PUT", "/api/quota", "{}", map[string]string{"Content-Type": "application/json"}); got.status != 405 {
		t.Fatalf("a PUT = %d, want 405", got.status)
	}
	if got := call(t, port, "GET", "/api/nope", "", nil); got.status != 404 {
		t.Fatalf("an unknown route = %d, want 404", got.status)
	}
}
