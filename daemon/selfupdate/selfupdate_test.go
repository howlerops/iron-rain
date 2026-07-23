package selfupdate

import "testing"

func TestIsNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.2.43", "0.2.42", true},
		{"0.2.42", "0.2.42", false},
		{"0.2.42", "0.2.43", false},
		{"0.3.0", "0.2.99", true},
		{"1.0.0", "0.9.9", true},
		{"v0.2.43", "0.2.42", true}, // tolerate a leading v
		{"0.2.43-rc1", "0.2.42", true},
	}
	for _, c := range cases {
		if got := isNewer(c.a, c.b); got != c.want {
			t.Errorf("isNewer(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestUpdatableSkipsDev(t *testing.T) {
	for _, v := range []string{"", "0.0.0-dev", "0.2.43-dev", "dev"} {
		if updatable(v) {
			t.Errorf("updatable(%q) should be false (dev/placeholder must never self-update)", v)
		}
	}
	if !updatable("0.2.43") {
		t.Error("updatable(0.2.43) should be true")
	}
}

func TestIsRealInstallRejectsDevPaths(t *testing.T) {
	// The scratchpad/dev daemon must never be self-updated.
	for _, p := range []string{
		"/private/tmp/claude-501/x/scratchpad/oculusd-new",
		"/Users/jacob/projects/oculus/daemon/oculusd",
		"/var/folders/xx/T/go-build123/b001/exe/oculusd",
	} {
		if isRealInstall(p) {
			t.Errorf("isRealInstall(%q) should be false (dev/temp path)", p)
		}
	}
}
