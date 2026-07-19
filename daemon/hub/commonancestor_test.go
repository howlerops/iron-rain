package hub

import "testing"

func TestCommonAncestor(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"/Users/jacob/projects/a", "/Users/jacob/projects/b"}, "/Users/jacob/projects"},
		{[]string{"/Users/jacob/a", "/Users/jacob/a/sub"}, "/Users/jacob/a"},
		{[]string{"/Users/a", "/opt/b"}, "/"},
		{[]string{"/only/one"}, "/only/one"},
	}
	for _, c := range cases {
		if got := commonAncestor(c.in); got != c.want {
			t.Errorf("commonAncestor(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
