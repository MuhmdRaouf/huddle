package auth

// Step-up (SPEC 7.4): a passkey assertion at most five minutes old, held on the device
// session's own row, covers the dangerous routes. The window's edges, the binding to one
// session, and the refusals all run against the real pairing core with the clock in the test's
// hands: the pairing core never reads time.Now itself.

import (
	"context"
	"testing"
)

const stepOrigin = "http://localhost:18860"

// stepUpAssertion opens one ceremony and answers it with the harness's authenticator.
func (h *pairHarness) stepUpAssertion(t *testing.T, cookie, handle string) error {
	t.Helper()
	assertion, err := h.p.BeginStepUp(context.Background())
	if err != nil {
		t.Fatalf("begin the step-up: %v", err)
	}
	_, err = h.p.FinishStepUp(context.Background(), cookie,
		h.auth.get(t, wireText(t, assertion.Response.Challenge), "localhost", stepOrigin, handle, false))
	return err
}

// TestStepUpWindowIsFiveMinutes walks the window's edges with the clock in hand: the ceremony
// that minted the session covers it, exactly five minutes still covers it, one second past does
// not, and a step-up renews the window from its own moment.
func TestStepUpWindowIsFiveMinutes(t *testing.T) {
	h := newPairHarness(t)
	ctx := context.Background()
	_, session := h.pair(t, "iPhone", stepOrigin)

	if h.p.StepUpDue(ctx, session.Cookie, h.p.nowMS()) {
		t.Error("a session the pairing just minted reads due: its ceremony verified the user")
	}
	h.add(300)
	if h.p.StepUpDue(ctx, session.Cookie, h.p.nowMS()) {
		t.Error("an assertion exactly five minutes old reads due: the window is at most five minutes")
	}
	h.add(1)
	if !h.p.StepUpDue(ctx, session.Cookie, h.p.nowMS()) {
		t.Error("an assertion five minutes and one second old still covers the session")
	}

	if err := h.stepUpAssertion(t, session.Cookie, h.pairHandle(t, "iPhone")); err != nil {
		t.Fatalf("the step-up finish: %v", err)
	}
	if h.p.StepUpDue(ctx, session.Cookie, h.p.nowMS()) {
		t.Error("the stepped-up session reads due at once")
	}
	h.add(300)
	if h.p.StepUpDue(ctx, session.Cookie, h.p.nowMS()) {
		t.Error("the renewed window ends a second early")
	}
	h.add(1)
	if !h.p.StepUpDue(ctx, session.Cookie, h.p.nowMS()) {
		t.Error("the renewed window outlasted five minutes")
	}
}

// TestStepUpCoversOneSessionAlone proves the assertion is bound to the browser session that
// earned it: a second session of the same device, aged past its own sign-in, stays due.
func TestStepUpCoversOneSessionAlone(t *testing.T) {
	h := newPairHarness(t)
	ctx := context.Background()
	_, first := h.pair(t, "iPhone", stepOrigin)

	assertion, err := h.p.BeginLogin(ctx)
	if err != nil {
		t.Fatalf("login begin: %v", err)
	}
	second, err := h.p.FinishLogin(ctx, h.auth.get(t, wireText(t, assertion.Response.Challenge),
		"localhost", stepOrigin, h.pairHandle(t, "iPhone"), false))
	if err != nil {
		t.Fatalf("login finish: %v", err)
	}

	h.add(400) // both windows pass
	if !h.p.StepUpDue(ctx, first.Cookie, h.p.nowMS()) || !h.p.StepUpDue(ctx, second.Cookie, h.p.nowMS()) {
		t.Fatal("both aged sessions should read due")
	}
	if err := h.stepUpAssertion(t, first.Cookie, h.pairHandle(t, "iPhone")); err != nil {
		t.Fatalf("step-up the first session: %v", err)
	}
	if h.p.StepUpDue(ctx, first.Cookie, h.p.nowMS()) {
		t.Error("the stepped-up session reads due")
	}
	if !h.p.StepUpDue(ctx, second.Cookie, h.p.nowMS()) {
		t.Error("the other browser session shares an assertion it never made")
	}
	// no cookie, and a cookie no live session answers, read due: neither is a stepped-up session
	if !h.p.StepUpDue(ctx, "", h.p.nowMS()) {
		t.Error("a request with no device cookie reads covered")
	}
	if !h.p.StepUpDue(ctx, "hdev_nobody", h.p.nowMS()) {
		t.Error("a cookie no session answers reads covered")
	}
}

// TestStepUpFinishRefusals covers what the finish refuses: a cookie that names no session, a
// replayed body whose challenge is spent, a signature no key of ours verifies, and another
// device's passkey — none of them stamps the session.
func TestStepUpFinishRefusals(t *testing.T) {
	h := newPairHarness(t)
	ctx := context.Background()
	_, session := h.pair(t, "iPhone", stepOrigin)
	handle := h.pairHandle(t, "iPhone")
	h.add(400) // the pairing's own coverage is past; a refused finish must leave it that way

	stranger, err := h.p.BeginStepUp(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if code := statusOf(t, mustErr(t, func() error {
		_, err := h.p.FinishStepUp(ctx, "hdev_nobody", h.auth.get(t,
			wireText(t, stranger.Response.Challenge), "localhost", stepOrigin, handle, false))
		return err
	})); code != 401 {
		t.Errorf("a finish on no session = %d, want 401", code)
	}

	spent, err := h.p.BeginStepUp(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	body := h.auth.get(t, wireText(t, spent.Response.Challenge), "localhost", stepOrigin, handle, false)
	if _, err := h.p.FinishStepUp(ctx, session.Cookie, body); err != nil {
		t.Fatalf("the first finish on a good body: %v", err)
	}
	if code := statusOf(t, mustErr(t, func() error {
		_, err := h.p.FinishStepUp(ctx, session.Cookie, body)
		return err
	})); code != 400 {
		t.Errorf("a replayed finish = %d, want 400", code)
	}

	// the spent challenge renewed the session; age it out again for the rest
	h.add(400)

	broken, err := h.p.BeginStepUp(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if code := statusOf(t, mustErr(t, func() error {
		_, err := h.p.FinishStepUp(ctx, session.Cookie, h.auth.get(t,
			wireText(t, broken.Response.Challenge), "localhost", stepOrigin, handle, true))
		return err
	})); code != 400 {
		t.Errorf("a broken signature = %d, want 400", code)
	}

	// a second device, paired beside the first, may not step the first one's session up
	other := newSoftToken(t)
	invite, err := h.p.Mint(ctx)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	creation, err := h.p.BeginPair(ctx, invite.Token, "Pixel")
	if err != nil {
		t.Fatalf("begin pair: %v", err)
	}
	if _, err := h.p.FinishPair(ctx, invite.Token,
		other.create(t, creationOf(t, creation), stepOrigin, "")); err != nil {
		t.Fatalf("pair Pixel: %v", err)
	}
	foreign, err := h.p.BeginStepUp(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if code := statusOf(t, mustErr(t, func() error {
		_, err := h.p.FinishStepUp(ctx, session.Cookie, other.get(t,
			wireText(t, foreign.Response.Challenge), "localhost", stepOrigin, h.pairHandle(t, "Pixel"), false))
		return err
	})); code != 403 {
		t.Errorf("another device's passkey = %d, want 403", code)
	}

	if !h.p.StepUpDue(ctx, session.Cookie, h.p.nowMS()) {
		t.Error("a refused finish stamped the session anyway")
	}
}

// TestStepUpAsksOnlyOnceADeviceIsPaired: until the home holds a passkey there is nothing to
// assert, and the check must not ask.
func TestStepUpAsksOnlyOnceADeviceIsPaired(t *testing.T) {
	h := newPairHarness(t)
	ctx := context.Background()
	if h.p.AnyDevicePaired(ctx) {
		t.Error("a home with no device reads as paired")
	}
	h.pair(t, "iPhone", stepOrigin)
	if !h.p.AnyDevicePaired(ctx) {
		t.Error("a paired device is not seen")
	}
}
