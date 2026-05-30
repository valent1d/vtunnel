package cli

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func suggestKey(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	default:
		return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
	}
}

func advance(m sshSuggestModel, keys ...string) sshSuggestModel {
	for _, k := range keys {
		updated, _ := m.Update(suggestKey(k))
		m = updated.(sshSuggestModel)
	}
	return m
}

func TestSSHSuggestChooseSSHCollectsEmail(t *testing.T) {
	m := newSSHSuggestModel("box")
	// toggle remember, then accept SSH, type an email, confirm.
	m = advance(m, "space", "y")
	if m.phase != suggestEmail {
		t.Fatalf("phase = %v, want email after choosing ssh", m.phase)
	}
	m.email.SetValue("me@example.com")
	m = advance(m, "enter")

	if !m.done {
		t.Fatal("expected the prompt to finish")
	}
	if !m.result.useSSH {
		t.Fatal("result.useSSH = false, want true")
	}
	if m.result.allow != "me@example.com" {
		t.Fatalf("result.allow = %q", m.result.allow)
	}
	if !m.result.remember {
		t.Fatal("result.remember = false, want true (space toggled it)")
	}
}

func TestSSHSuggestEmptyEmailStays(t *testing.T) {
	m := newSSHSuggestModel("box")
	m = advance(m, "y") // into email phase
	m = advance(m, "enter")
	if m.done {
		t.Fatal("empty email should not confirm")
	}
	if m.phase != suggestEmail {
		t.Fatalf("phase = %v, want still email", m.phase)
	}
}

func TestSSHSuggestKeepTCP(t *testing.T) {
	m := newSSHSuggestModel("box")
	m = advance(m, "n")
	if !m.done {
		t.Fatal("expected finish on 'n'")
	}
	if m.result.useSSH {
		t.Fatal("useSSH should be false when keeping TCP")
	}
	if m.result.cancelled {
		t.Fatal("keeping TCP is not a cancel")
	}
}

func TestSSHSuggestRememberKeepTCP(t *testing.T) {
	m := newSSHSuggestModel("box")
	m = advance(m, "space", "n")
	if !m.result.remember {
		t.Fatal("remember should persist even when keeping TCP")
	}
	if m.result.useSSH {
		t.Fatal("useSSH should be false")
	}
}

func TestSSHSuggestCancel(t *testing.T) {
	m := newSSHSuggestModel("box")
	m = advance(m, "esc")
	if !m.done || !m.result.cancelled {
		t.Fatalf("esc should cancel; done=%v cancelled=%v", m.done, m.result.cancelled)
	}
}

func TestSSHSuggestEscFromEmailGoesBack(t *testing.T) {
	m := newSSHSuggestModel("box")
	m = advance(m, "y", "esc")
	if m.phase != suggestChoice {
		t.Fatalf("esc in email phase should return to choice, got %v", m.phase)
	}
	if m.done {
		t.Fatal("esc from email should not finish the prompt")
	}
}

func TestSSHTargetNormalisation(t *testing.T) {
	cases := map[string]string{
		"":                  "localhost:22",
		"ssh://10.0.0.1:22": "10.0.0.1:22",
		"box:2222":          "box:2222",
		"  localhost:22 ":   "localhost:22",
	}
	for in, want := range cases {
		if got := sshTarget(in); got != want {
			t.Errorf("sshTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSSHModeFromIdP(t *testing.T) {
	if sshMode("") != "otp" {
		t.Error("no idp should be otp")
	}
	if sshMode("Authentik") != "sso" {
		t.Error("an idp should be sso")
	}
}
