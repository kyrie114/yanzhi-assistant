package account

import (
	"errors"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"yanzhi/server/internal/platform/clock"
)

// These tests deliberately keep time inside one JWT second. Advancing the
// clock would hide the immediate-revocation requirement in AC-01-07.
func TestChangePasswordInvalidatesAllTokensInSameSecond(t *testing.T) {
	clk := clock.NewManual(time.Date(2026, 10, 20, 9, 0, 0, 750000000, time.UTC))
	svc := newSvc(clk)
	mustRegisterSessionUser(t, svc, "a@example.edu.cn")
	mustRegisterSessionUser(t, svc, "b@example.edu.cn")
	first := mustAccountSession(t, svc, "a@example.edu.cn", "Abc12345")
	second := mustAccountSession(t, svc, "a@example.edu.cn", "Abc12345")
	other := mustAccountSession(t, svc, "b@example.edu.cn", "Abc12345")

	if err := svc.ChangePassword(first, "Abc12345", "Abc123456"); err != nil {
		t.Fatal(err)
	}
	for i, token := range []string{first, second} {
		if _, err := svc.Authenticate(token); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("pre-change token %d remains usable in the same second: %v", i+1, err)
		}
	}
	if _, err := svc.Authenticate(other); err != nil {
		t.Errorf("another user's session was revoked: %v", err)
	}
	if _, err := svc.Login("a@example.edu.cn", "Abc12345"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("old password still accepted: %v", err)
	}
	current := mustAccountSession(t, svc, "a@example.edu.cn", "Abc123456")
	if _, err := svc.Authenticate(current); err != nil {
		t.Errorf("new password's immediate session was rejected: %v", err)
	}
}

func TestRepeatedPasswordChangesInSameSecond(t *testing.T) {
	svc := newSvc(clock.NewManual(time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)))
	mustRegisterSessionUser(t, svc, "a@example.edu.cn")
	password := "Abc12345"
	current := mustAccountSession(t, svc, "a@example.edu.cn", password)
	var issued []string
	for _, nextPassword := range []string{"Next12345", "Next23456", "Next34567"} {
		issued = append(issued, current)
		if err := svc.ChangePassword(current, password, nextPassword); err != nil {
			t.Fatalf("change to %q: %v", nextPassword, err)
		}
		for i, token := range issued {
			if _, err := svc.Authenticate(token); !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("previous token %d is usable after change to %q: %v", i+1, nextPassword, err)
			}
		}
		current = mustAccountSession(t, svc, "a@example.edu.cn", nextPassword)
		if _, err := svc.Authenticate(current); err != nil {
			t.Fatalf("latest session should be valid without advancing time: %v", err)
		}
		password = nextPassword
	}
}

func TestRejectedPasswordChangesPreserveCurrentSessions(t *testing.T) {
	svc := newSvc(clock.NewManual(time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)))
	mustRegisterSessionUser(t, svc, "a@example.edu.cn")
	initial := mustAccountSession(t, svc, "a@example.edu.cn", "Abc12345")
	if err := svc.ChangePassword(initial, "Abc12345", "Abc123456"); err != nil {
		t.Fatal(err)
	}
	first := mustAccountSession(t, svc, "a@example.edu.cn", "Abc123456")
	second := mustAccountSession(t, svc, "a@example.edu.cn", "Abc123456")
	before, _ := svc.store.FindByEmail("a@example.edu.cn")
	for _, tc := range []struct {
		name string
		old  string
		next string
		want error
	}{
		{"incorrect old password", "Wrong1234", "Next12345", ErrInvalidCredentials},
		{"weak new password", "Abc123456", "abc", ErrWeakPassword},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := svc.ChangePassword(first, tc.old, tc.next); !errors.Is(err, tc.want) {
				t.Fatalf("change error = %v, want %v", err, tc.want)
			}
			after, _ := svc.store.FindByEmail("a@example.edu.cn")
			if after.PasswordHash != before.PasswordHash || after.TokensInvalidBefore == nil ||
				before.TokensInvalidBefore == nil || !after.TokensInvalidBefore.Equal(*before.TokensInvalidBefore) {
				t.Error("rejected change modified the password or revocation timestamp")
			}
			for i, token := range []string{first, second} {
				if _, err := svc.Authenticate(token); err != nil {
					t.Errorf("rejected change revoked current session %d: %v", i+1, err)
				}
			}
			mustAccountSession(t, svc, "a@example.edu.cn", "Abc123456")
		})
	}
}

// Both requests authenticate against the same password before either writes.
// Exactly one change may commit; a delayed stale request must not overwrite it.
func TestConcurrentPasswordChangesRejectStaleCredentials(t *testing.T) {
	clk := &passwordChangeBarrierClock{now: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)}
	svc := NewService(NewMemoryStore(), clk, []byte("test-secret"), bcrypt.MinCost)
	mustRegisterSessionUser(t, svc, "a@example.edu.cn")
	initial := mustAccountSession(t, svc, "a@example.edu.cn", "Abc12345")
	clk.arm()
	defer clk.disarm()
	type result struct {
		password string
		err      error
	}
	results := make(chan result, 2)
	startChange := func(password string) {
		go func() {
			results <- result{password, svc.ChangePassword(initial, "Abc12345", password)}
		}()
	}
	startChange("First12345")
	awaitPasswordChangeBarrier(t, clk.ready)
	startChange("Second12345")
	awaitPasswordChangeBarrier(t, clk.ready)
	clk.disarm()

	successes := 0
	var winner, loser string
	for i := 0; i < 2; i++ {
		select {
		case got := <-results:
			if got.err == nil {
				successes++
				winner = got.password
			} else if errors.Is(got.err, ErrUnauthenticated) || errors.Is(got.err, ErrInvalidCredentials) {
				loser = got.password
			} else {
				t.Errorf("unexpected concurrent change error: %v", got.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent password changes did not finish")
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent changes = %d, want exactly 1", successes)
	}
	current := mustAccountSession(t, svc, "a@example.edu.cn", winner)
	if _, err := svc.Authenticate(current); err != nil {
		t.Errorf("winning password's session is invalid: %v", err)
	}
	if _, err := svc.Login("a@example.edu.cn", loser); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("losing password was stored: %v", err)
	}
	if _, err := svc.Authenticate(initial); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("original session still valid after concurrent changes: %v", err)
	}
}

func mustRegisterSessionUser(t *testing.T, svc *Service, email string) {
	t.Helper()
	if _, err := svc.Register(email, "Session test", "Abc12345"); err != nil {
		t.Fatal(err)
	}
}

func mustAccountSession(t *testing.T, svc *Service, email, password string) string {
	t.Helper()
	login, err := svc.Login(email, password)
	if err != nil {
		t.Fatalf("login %s: %v", email, err)
	}
	return login.AccessToken
}

// While armed, each change calls Now once to validate its JWT and once just
// before committing its password. The latter call waits for explicit release.
type passwordChangeBarrierClock struct {
	mu      sync.Mutex
	now     time.Time
	armed   bool
	calls   int
	ready   chan struct{}
	release chan struct{}
}

func (c *passwordChangeBarrierClock) Now() time.Time {
	c.mu.Lock()
	block := false
	if c.armed {
		c.calls++
		block = c.calls%2 == 0
	}
	ready, release, now := c.ready, c.release, c.now
	c.mu.Unlock()
	if block {
		ready <- struct{}{}
		<-release
	}
	return now
}

func (c *passwordChangeBarrierClock) arm() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.armed = true
	c.calls = 0
	c.ready = make(chan struct{}, 2)
	c.release = make(chan struct{})
}

func (c *passwordChangeBarrierClock) disarm() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.armed {
		c.armed = false
		close(c.release)
	}
}

func awaitPasswordChangeBarrier(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("password change did not reach the pre-commit barrier")
	}
}
