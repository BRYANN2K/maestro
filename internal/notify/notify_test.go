package notify

import "testing"

func TestModes(t *testing.T) {
	for _, m := range []Mode{ModeAuto, ModeNative, ModeOSC, ModeBell, ModeDisabled} {
		if !m.Valid() {
			t.Errorf("%s should be valid", m)
		}
	}
	if Mode("bogus").Valid() {
		t.Error("bogus mode should be invalid")
	}
}

func TestEscape(t *testing.T) {
	if got := escape(`a"b\c`); got != `a\"b\\c` {
		t.Errorf("escape = %q", got)
	}
}

func TestDisabledNoop(t *testing.T) {
	New(ModeDisabled).Notify("t", "b") // must not panic or print
}

func TestSSHDetection(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "1.2.3.4")
	if !isSSH() {
		t.Error("SSH_CONNECTION should be detected")
	}
	t.Setenv("SSH_CONNECTION", "")
	if isSSH() {
		t.Error("no SSH env should not be detected")
	}
}
