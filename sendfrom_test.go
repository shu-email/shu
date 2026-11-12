package main

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// editorTester opens the editor of an account over the mail window.
func editorTester(t *testing.T, a *app, id string) *ui.Tester {
	t.Helper()
	tt := ui.NewTester(a.view, 1220, 900)
	a.openAccountForm(a.account(id))
	tt.Frame()
	if !tt.HasText(locales.EditAccount()) {
		t.Fatalf("the editor did not open: %q", tt.Texts())
	}
	return tt
}

// reveal scrolls the account editor until the element showing text is
// at the top of its scroll area, where top, the text of its first field,
// was: a click reaches it then, however the fonts lay the editor out.
func reveal(t *testing.T, tt *ui.Tester, top, text string) {
	t.Helper()
	first, ok := tt.Find(top)
	target, found := tt.Find(text)
	if !ok || !found {
		t.Fatalf("the editor lacks %q or %q: %q", top, text, tt.Texts())
	}
	tt.Scroll(first.X+first.W/2, first.Y+first.H/2, 0, target.Y-first.Y)
	tt.Frame()
}

// switchOn reports whether the switch named label is on.
func switchOn(t *testing.T, tt *ui.Tester, label string) bool {
	t.Helper()
	info, ok := tt.AccessByRole(ui.RoleSwitch, label)
	if !ok {
		t.Fatalf("no switch %q: %q", label, tt.Texts())
	}
	return info.Checked
}

// The account editor has the account's signature, saved as it is
// written, with a switch for each kind of message it goes into, all on
// for a new account; compose signs with what was saved. A save that
// fails says so. The editor of an account that signs in with a provider
// has it too.
func TestAccountEditorSignature(t *testing.T) {
	fs := inMessageView(composeService())
	delete(fs.signatures, "a1")
	a := openApp(fs)
	tt := editorTester(t, a, "a1")
	reveal(t, tt, locales.Provider(), locales.SignatureSection())
	if field, ok := tt.AccessByRole(ui.RoleTextField, locales.SignatureText()); !ok || field.Value != "" {
		t.Fatalf("the signature field = %+v, found %v", field, ok)
	}
	for _, label := range []string{locales.SignatureNew(), locales.SignatureReply(), locales.SignatureForward()} {
		if !switchOn(t, tt, label) {
			t.Errorf("%q is off for a new account", label)
		}
	}
	typeIn(t, tt, locales.SignatureText(), "Ann Lee\nExample Lab")
	if sig := fs.signatures["a1"]; sig != (mail.Signature{Text: "Ann Lee\nExample Lab", New: true, Reply: true, Forward: true}) {
		t.Fatalf("saved %+v", sig)
	}
	click(t, tt, locales.SignatureReply())
	if sig := fs.signatures["a1"]; sig.Reply || !sig.New || !sig.Forward || switchOn(t, tt, locales.SignatureReply()) {
		t.Fatalf("replies switched off: saved %+v", sig)
	}

	// Compose signs a new message with it, and not a reply. Closing the
	// editor without saving the account keeps the signature: it was
	// saved as it was written.
	click(t, tt, locales.Cancel())
	if cs := a.openCompose(nil); cs.body != "\n\n-- \nAnn Lee\nExample Lab" {
		t.Errorf("a new message = %q", cs.body)
	}
	openDesign(t, tt)
	click(t, tt, locales.Reply())
	if cs, _ := lastCompose(t, a); cs.body != "\n\n"+designQuote(t, fs) {
		t.Errorf("a reply = %q", cs.body)
	}

	// The editor opens with what was saved, and a save that fails says
	// so.
	tt = editorTester(t, a, "a1")
	reveal(t, tt, locales.Provider(), locales.SignatureSection())
	if field, _ := tt.AccessByRole(ui.RoleTextField, locales.SignatureText()); field.Value != "Ann Lee\nExample Lab" || switchOn(t, tt, locales.SignatureReply()) {
		t.Errorf("the editor opened again with %q", field.Value)
	}
	fs.signatureErr = errFake
	typeIn(t, tt, locales.SignatureText(), "!")
	if !tt.HasText(locales.SignatureSaveFailed(errFake.Error())) || fs.signatures["a1"].Text != "Ann Lee\nExample Lab" {
		t.Errorf("a save that failed: %q", tt.Texts())
	}
	fs.signatureErr = nil
	typeIn(t, tt, locales.SignatureText(), "!")
	if tt.HasText(locales.SignatureSaveFailed(errFake.Error())) || fs.signatures["a1"].Text != "Ann Lee\nExample Lab!!" {
		t.Errorf("saved again: %+v: %q", fs.signatures["a1"], tt.Texts())
	}

	// An account that signs in with Google.
	gs := newFakeService()
	ga, _ := googleApp(t, gs)
	gt := editorTester(t, ga, "a1")
	reveal(t, gt, locales.OauthSignsInWith("Google"), locales.SignatureSection())
	typeIn(t, gt, locales.SignatureText(), "Ann at Work")
	if gs.signatures["a1"].Text != "Ann at Work" || !gt.HasText(locales.IdentitiesSection()) {
		t.Errorf("the editor of a Google account saved %+v: %q", gs.signatures["a1"], gt.Texts())
	}
}

// The account editor lists the addresses the account sends from: its
// own, which is the account's, and those added, which can be added,
// edited and removed. What the engine refuses it says in the user's
// words, and compose offers the addresses there are.
func TestAccountEditorIdentities(t *testing.T) {
	fs := inMessageView(composeService())
	delete(fs.identities, "a1")
	a := openApp(fs)
	tt := editorTester(t, a, "a1")
	reveal(t, tt, locales.Provider(), locales.IdentitiesSection())
	if !tt.HasText(locales.IdentityOwn()) || !tt.HasText("ann@example.com") {
		t.Fatalf("the account's own address is not listed: %q", tt.Texts())
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, locales.EditIdentity("ann@example.com")); ok {
		t.Error("the account's own address is offered for editing")
	}

	// Added: the name field takes the keyboard.
	click(t, tt, locales.AddIdentity())
	tt.Type("Ann Lee (Lab)")
	typeIn(t, tt, locales.IdentityEmail(), "the lab")
	click(t, tt, locales.IdentitySave())
	if !tt.HasText(locales.IdentityNotAddress()) || len(fs.identities["a1"]) != 0 || !a.idForm.open {
		t.Fatalf("not an address: %q", tt.Texts())
	}
	a.idForm.email = "ANN@example.com"
	tt.Frame()
	click(t, tt, locales.IdentitySave())
	if !tt.HasText(locales.IdentityExists("ANN@example.com")) || len(fs.identities["a1"]) != 0 {
		t.Fatalf("the account's own address: %q", tt.Texts())
	}
	a.idForm.email = "ann@lab.example.org"
	tt.Frame()
	click(t, tt, locales.IdentitySave())
	if added := fs.identities["a1"]; len(added) != 1 || added[0].Name != "Ann Lee (Lab)" || added[0].Email != "ann@lab.example.org" {
		t.Fatalf("added %+v", added)
	}
	if a.idForm.open || tt.HasText(locales.IdentityExists("ANN@example.com")) || !tt.HasText("ann@lab.example.org") {
		t.Fatalf("after adding: %q", tt.Texts())
	}
	offered := a.openCompose(nil)
	ot := ui.NewTester(offered.view, 720, 620)
	click(t, ot, ownLabel)
	if !ot.HasText(labLabel) {
		t.Errorf("compose does not offer the address added: %q", ot.Texts())
	}
	offered.close()

	// Added twice, and edited.
	click(t, tt, locales.AddIdentity())
	typeIn(t, tt, locales.IdentityEmail(), "Ann@Lab.example.org")
	click(t, tt, locales.IdentitySave())
	if !tt.HasText(locales.IdentityExists("Ann@Lab.example.org")) || len(fs.identities["a1"]) != 1 {
		t.Fatalf("the same address twice: %q", tt.Texts())
	}
	click(t, tt, locales.IdentityCancel())
	click(t, tt, locales.EditIdentity("ann@lab.example.org"))
	if !a.idForm.open || a.idForm.name != "Ann Lee (Lab)" || a.idForm.email != "ann@lab.example.org" {
		t.Fatalf("the form of the address = %+v", a.idForm)
	}
	typeIn(t, tt, locales.IdentityName(), ", Room 4")
	click(t, tt, locales.IdentitySave())
	if added := fs.identities["a1"]; len(added) != 1 || added[0].Name != "Ann Lee (Lab), Room 4" || added[0].ID == "" {
		t.Fatalf("edited %+v", added)
	}
	click(t, tt, locales.EditIdentity("ann@lab.example.org"))
	a.idForm.email = "ann@example.com"
	tt.Frame()
	click(t, tt, locales.IdentitySave())
	if !tt.HasText(locales.IdentityExists("ann@example.com")) || fs.identities["a1"][0].Email != "ann@lab.example.org" {
		t.Fatalf("edited into the account's own address: %q", tt.Texts())
	}
	fs.identityErr = errFake
	a.idForm.email = "ann@lab2.example.org"
	tt.Frame()
	click(t, tt, locales.IdentitySave())
	if !tt.HasText(locales.IdentitySaveFailed(errFake.Error())) {
		t.Fatalf("a save the store refused: %q", tt.Texts())
	}
	click(t, tt, locales.IdentityCancel())

	// Removed: one the store refuses says so; then it goes, and compose
	// offers no choice.
	click(t, tt, locales.RemoveIdentity("ann@lab.example.org"))
	if !tt.HasText(locales.IdentityRemoveFailed(errFake.Error())) || len(fs.identities["a1"]) != 1 {
		t.Fatalf("a removal the store refused: %q", tt.Texts())
	}
	fs.identityErr = nil
	click(t, tt, locales.RemoveIdentity("ann@lab.example.org"))
	if len(fs.identities["a1"]) != 0 || tt.HasText("ann@lab.example.org") || tt.HasText(locales.IdentityRemoveFailed(errFake.Error())) {
		t.Fatalf("after removing: %+v: %q", fs.identities["a1"], tt.Texts())
	}
	cs := a.openCompose(nil)
	if ct := ui.NewTester(cs.view, 720, 620); ct.HasText(ownLabel) {
		t.Errorf("compose offers a choice of one address: %q", ct.Texts())
	}

	// The editor of an account that signs in with Google has them too.
	gs := newFakeService()
	ga, _ := googleApp(t, gs)
	gt := editorTester(t, ga, "a1")
	reveal(t, gt, locales.OauthSignsInWith("Google"), locales.IdentitiesSection())
	click(t, gt, locales.AddIdentity())
	gt.Type("Ann")
	typeIn(t, gt, locales.IdentityEmail(), "ann@work.example.com")
	click(t, gt, locales.IdentitySave())
	if added := gs.identities["a1"]; len(added) != 1 || added[0].Email != "ann@work.example.com" {
		t.Errorf("added to a Google account: %+v", added)
	}
}
