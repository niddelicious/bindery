package auth

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestDummyPasswordHashEqualizesTiming asserts the dummy hash is a real
// argon2id hash with the CURRENT parameters, so verifying against it costs the
// same as verifying a genuine user's hash. If the argon parameters ever change
// without the dummy following, the username-not-found path would run a
// cheaper/absent KDF and reintroduce the login timing side-channel.
func TestDummyPasswordHashEqualizesTiming(t *testing.T) {
	dummy := DummyPasswordHash()

	// Structurally valid and rejected for attacker-supplied passwords, so the
	// missing-user branch always runs the KDF and fails. (The handler's u == nil
	// guard means even a match here still returns 401, so this is belt-and-braces.)
	for _, pw := range []string{"", "password", "admin", "hunter2"} {
		if VerifyPassword(pw, dummy) {
			t.Fatalf("dummy hash unexpectedly matched password %q", pw)
		}
	}

	// Its parameters must match what HashPassword produces today, otherwise the
	// KDF cost differs from a real verify and login timing diverges again.
	real, err := HashPassword("whatever")
	if err != nil {
		t.Fatal(err)
	}
	if d, r := paramField(t, dummy), paramField(t, real); d != r {
		t.Fatalf("dummy params %q != real params %q", d, r)
	}
}

// paramField returns the "m=..,t=..,p=.." segment (4th field) of a PHC hash.
func paramField(t *testing.T, phc string) string {
	t.Helper()
	parts := strings.Split(phc, "$")
	if len(parts) != 6 {
		t.Fatalf("malformed PHC hash: %q", phc)
	}
	return parts[3]
}

// holdAllKDFSlots takes every KDF slot so that a verification that would run
// the KDF has to wait, and returns the release. A verification refused before
// the KDF never touches the slots.
func holdAllKDFSlots(t *testing.T) func() {
	t.Helper()
	n := cap(kdfSlots)
	for i := 0; i < n; i++ {
		kdfSlots <- struct{}{}
	}
	return func() {
		for i := 0; i < n; i++ {
			<-kdfSlots
		}
	}
}

// S8: the m, t and p in a stored hash drive the allocation and CPU of the
// verify. A hash from a tampered database or restore must not be able to ask
// for gigabytes, hours of iterations or a wild thread count; it is refused
// (false, nil) before a KDF slot is taken or any memory is allocated. The slots
// are held here, so a hash that was NOT refused would wait on them and return
// the context error instead.
func TestVerifyPassword_OutOfRangeParamsRefusedBeforeKDF(t *testing.T) {
	salt := "c29tZXNhbHRzb21lc2FsdA"
	hash := "c29tZWhhc2hzb21laGFzaHNvbWVoYXNoc29tZWhhc2g"
	cases := []struct{ name, params string }{
		{"oversized memory 4 GiB", "m=4194304,t=1,p=4"},
		{"memory just over the cap", "m=262145,t=1,p=4"},
		{"max memory", "m=4294967295,t=1,p=4"},
		{"too many iterations", "m=65536,t=11,p=4"},
		{"huge iterations", "m=65536,t=4294967295,p=4"},
		{"too many threads", "m=65536,t=1,p=9"},
		{"zero iterations would panic argon2", "m=65536,t=0,p=4"},
		{"zero threads would panic argon2", "m=65536,t=1,p=0"},
		{"zero memory", "m=0,t=1,p=4"},
	}
	release := holdAllKDFSlots(t)
	defer release()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			phc := "$argon2id$v=19$" + c.params + "$" + salt + "$" + hash
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			ok, err := VerifyPasswordContext(ctx, "anything", phc)
			if ok || err != nil {
				t.Fatalf("VerifyPasswordContext = %v, %v; want false, nil (refused before the KDF)", ok, err)
			}
		})
	}
}

// The bounds sit above what Bindery itself writes, so every hash it ever
// produced still verifies: m=65536 (64 MiB), t=1, p=4.
func TestArgonParamsAcceptable(t *testing.T) {
	if !argonParamsAcceptable(argonMemory, argonTime, argonThreads) {
		t.Fatal("the parameters HashPassword writes must be accepted")
	}
	for _, c := range []struct {
		m, t uint32
		p    uint8
		want bool
	}{
		{65536, 1, 4, true},
		{262144, 1, 4, true}, // exactly 256 MiB
		{262145, 1, 4, false},
		{19456, 2, 1, true}, // the OWASP minimum profile other tools write
		{65536, 10, 4, true},
		{65536, 11, 4, false},
		{65536, 1, 8, true},
		{65536, 1, 9, false},
		{65536, 0, 4, false},
		{65536, 1, 0, false},
		{0, 1, 4, false},
	} {
		if got := argonParamsAcceptable(c.m, c.t, c.p); got != c.want {
			t.Errorf("argonParamsAcceptable(m=%d,t=%d,p=%d) = %v, want %v", c.m, c.t, c.p, got, c.want)
		}
	}
}

// A hash made with Bindery's own defaults still verifies end to end.
func TestVerifyPassword_DefaultHashStillVerifies(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("correct horse battery", h) {
		t.Fatal("a hash written with the default parameters must verify")
	}
	if VerifyPassword("wrong", h) {
		t.Fatal("wrong password verified")
	}
}
