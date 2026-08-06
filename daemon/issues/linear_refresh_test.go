package issues

import "testing"

// TestLinearImplementsTokenRefresher pins the gap that caused daily Linear re-auth: the Manager's
// refresh loop type-asserts each provider to TokenRefresher, and Linear did not implement it, so it
// was skipped in silence and simply expired.
func TestLinearImplementsTokenRefresher(t *testing.T) {
	var p Provider = NewLinear("tok")
	if _, ok := p.(TokenRefresher); !ok {
		t.Fatal("Linear must implement TokenRefresher or the refresh loop skips it entirely")
	}
}

// A bare (non-OAuth) token has nothing to refresh and must not report an error — a personal API key
// does not expire, and a spurious auth error would show a "reconnect" pill on a healthy connection.
func TestLinearBareTokenRefreshIsNoop(t *testing.T) {
	if err := NewLinear("personal-api-key").RefreshToken(t.Context()); err != nil {
		t.Fatalf("bare token refresh should be a no-op, got %v", err)
	}
}
