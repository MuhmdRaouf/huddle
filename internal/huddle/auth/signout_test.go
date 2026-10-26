package auth

// Signing out: the credential a browser presents ends where it lives, one browser alone or every
// browser at once, while the root token, a member's own credential and the invites stay.

import (
	"path/filepath"
	"testing"
)

// browserOf signs one browser in through a login code of by (nil for the owner) and answers the
// credential its cookie will carry.
func browserOf(t *testing.T, a *Auth, by *string) string {
	t.Helper()
	cred, ok := a.Redeem(a.LoginCode(by))
	if !ok {
		t.Fatal("the login code did not redeem")
	}
	return cred
}

// refused says whether Who turns the credential away.
func refused(a *Auth, cred string) bool {
	_, ok := a.Who(cred)
	return !ok
}

func TestSignOutEndsThePresentedCredential(t *testing.T) {
	a := New("root-cred", nil, nil, "")
	ownerBrowser := browserOf(t, a, nil)
	inv := a.Create(InviteOptions{By: "owner"})
	join := a.Join(inv.Token, "cea")
	if !joined(join) {
		t.Fatalf("join = %q", join.Err)
	}
	memberBrowser := browserOf(t, a, str("cea"))

	// a member's own credential is not a browser: the sign-out leaves it
	if a.SignOut(join.Credential) {
		t.Error("signing out answered true for a member's own credential")
	}
	if refused(a, join.Credential) {
		t.Error("the member's own credential died with the sign-out")
	}
	// one browser goes, and only that one
	if !a.SignOut(ownerBrowser) {
		t.Fatal("signing out the owner's browser answered false")
	}
	if !refused(a, ownerBrowser) {
		t.Error("the signed-out browser still answers Who")
	}
	if refused(a, "root-cred") {
		t.Error("the root token died with the browser's sign-out")
	}
	if refused(a, memberBrowser) {
		t.Error("the member's browser died with the owner's sign-out")
	}
	// a root goes too, and an unknown credential and the empty one answer false
	if !a.SignOut("root-cred") {
		t.Error("signing out the root token answered false")
	}
	if !refused(a, "root-cred") {
		t.Error("the signed-out root still answers Who")
	}
	if a.SignOut("hcred_nobody") {
		t.Error("an unknown credential answered true")
	}
	if a.SignOut("") {
		t.Error("the empty credential answered true")
	}
}

func TestSignOutAllEndsEveryBrowserAndKeepsRootsAndMembers(t *testing.T) {
	a := New("root-cred", nil, nil, "")
	first := browserOf(t, a, nil)
	inv := a.Create(InviteOptions{By: "owner"})
	join := a.Join(inv.Token, "cea")
	if !joined(join) {
		t.Fatalf("join = %q", join.Err)
	}
	second := browserOf(t, a, str("cea"))
	third := browserOf(t, a, nil)

	if gone := a.SignOutAll(); gone != 3 {
		t.Fatalf("SignOutAll = %d, want 3", gone)
	}
	for _, cred := range []string{first, second, third} {
		if !refused(a, cred) {
			t.Error("a browser survived the sweep")
		}
	}
	if refused(a, "root-cred") {
		t.Error("the root token died with the sweep")
	}
	if refused(a, join.Credential) {
		t.Error("the member's own credential died with the sweep")
	}
	if gone := a.SignOutAll(); gone != 0 {
		t.Errorf("a second sweep = %d, want 0", gone)
	}
}

func TestSignOutPersistsToTheStateFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "auth.json")
	a := New("root-cred", nil, nil, file)
	survivor := browserOf(t, a, nil)
	ended := browserOf(t, a, nil)
	if !a.SignOut(ended) {
		t.Fatal("signing out answered false")
	}

	b := New("root-cred", nil, nil, file)
	if !refused(b, ended) {
		t.Error("the state file kept the signed-out browser")
	}
	if refused(b, survivor) {
		t.Error("the reload lost the browser that stayed")
	}
	if gone := b.SignOutAll(); gone != 1 {
		t.Fatalf("SignOutAll after the reload = %d, want 1", gone)
	}
	c := New("root-cred", nil, nil, file)
	if !refused(c, survivor) {
		t.Error("the swept browser came back with the reload")
	}
	if refused(c, "root-cred") {
		t.Error("the sweep took the root token with it")
	}
}
