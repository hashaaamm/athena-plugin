package security

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The hashing module, on its own: no database, no router. It is the only part of the auth path that
// can be tested like this, which is the reason it is written first when you add one.
//
// The tests that make a claim about production use DefaultArgon2Params; the rest use cheap ones,
// because a digest at 64 MiB under the race detector costs most of a second.

const passphrase = "correct horse battery staple"

var cheapParams = Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}

func TestADigestVerifiesAndRevealsNothing(t *testing.T) {
	hasher := NewHasher(DefaultArgon2Params, 0)

	stored, err := hasher.Hash(context.Background(), passphrase)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(stored, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("digest %q is not Argon2id at the production parameters", stored)
	}
	if strings.Contains(stored, "correct horse") {
		t.Fatal("the digest contains the password")
	}
	matched, upgraded, err := hasher.Verify(context.Background(), passphrase, stored)
	if err != nil || !matched || upgraded != "" {
		t.Fatalf("Verify = %v, %q, %v; want true, \"\", nil", matched, upgraded, err)
	}
}

func TestVerificationIsCaseSensitive(t *testing.T) {
	hasher := NewHasher(cheapParams, 0)
	stored, _ := hasher.Hash(context.Background(), passphrase)

	matched, _, err := hasher.Verify(context.Background(), "Correct horse battery staple", stored)

	if err != nil || matched {
		t.Fatalf("Verify = %v, %v; want false, nil", matched, err)
	}
}

// Salted: two users with the same password must not have the same row.
func TestTheSamePasswordHashesDifferentlyEveryTime(t *testing.T) {
	hasher := NewHasher(cheapParams, 0)
	first, _ := hasher.Hash(context.Background(), passphrase)
	second, _ := hasher.Hash(context.Background(), passphrase)

	if first == second {
		t.Fatal("two digests of one password are identical: the salt is not random")
	}
}

// The property that stops login being a user-enumeration oracle. Asserted structurally rather than
// by timing: a wall-clock comparison is the flakiest test in any suite, and what matters is that a
// miss runs a real key derivation at all.
func TestAnUnknownAccountStillPaysForADerivation(t *testing.T) {
	hasher := NewHasher(cheapParams, 0)
	_, _ = hasher.absent() // computed once, lazily; not what this test counts
	var derivations atomic.Int32
	hasher.onDerive = func() { derivations.Add(1) }

	matched, upgraded, err := hasher.Verify(context.Background(), "anything", "")

	if err != nil || matched || upgraded != "" {
		t.Fatalf("Verify = %v, %q, %v; want false, \"\", nil", matched, upgraded, err)
	}
	if got := derivations.Load(); got != 1 {
		t.Fatalf("a miss ran %d derivations, want 1", got)
	}
}

// A broken row fails the login, not the request — and costs what a real verification costs.
func TestADigestThisBuildCannotReadFailsTheLoginNotTheRequest(t *testing.T) {
	hasher := NewHasher(cheapParams, 0)
	_, _ = hasher.absent()
	var derivations atomic.Int32
	hasher.onDerive = func() { derivations.Add(1) }

	for _, stored := range []string{"not-a-hash", "$2b$12$abcdefghijklmnopqrstuv", "$argon2i$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5a2V5"} {
		matched, upgraded, err := hasher.Verify(context.Background(), "anything", stored)
		if err != nil || matched || upgraded != "" {
			t.Fatalf("Verify(%q) = %v, %q, %v; want false, \"\", nil", stored, matched, upgraded, err)
		}
	}
	if got := derivations.Load(); got != 3 {
		t.Fatalf("%d derivations for 3 unreadable digests, want 3", got)
	}
}

// A digest is data. One that asks for 4 GiB per verification is refused rather than obeyed.
func TestADigestAskingForHostileParametersIsRefused(t *testing.T) {
	hasher := NewHasher(cheapParams, 0)
	salt := base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef"))
	key := base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	hostile := fmt.Sprintf("$argon2id$v=19$m=%d,t=3,p=4$%s$%s", 4*1024*1024, salt, key)

	start := time.Now()
	matched, _, err := hasher.Verify(context.Background(), "anything", hostile)

	if err != nil || matched {
		t.Fatalf("Verify = %v, %v; want false, nil", matched, err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("verification took %v: the hostile parameters were obeyed", elapsed)
	}
}

// Login is the only moment the plaintext is in hand, so it is when old digests are migrated.
func TestADigestMadeWithOtherParametersIsUpgradedOnASuccessfulVerify(t *testing.T) {
	old, _ := NewHasher(cheapParams, 0).Hash(context.Background(), passphrase)
	hasher := NewHasher(DefaultArgon2Params, 0)

	matched, upgraded, err := hasher.Verify(context.Background(), passphrase, old)
	if err != nil || !matched {
		t.Fatalf("Verify = %v, %v; want true, nil", matched, err)
	}
	if !strings.HasPrefix(upgraded, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("upgraded digest %q is not at the current parameters", upgraded)
	}
	if matched, again, _ := hasher.Verify(context.Background(), passphrase, upgraded); !matched || again != "" {
		t.Fatal("the upgraded digest does not verify, or asks to be upgraded again")
	}

	wrong, upgradedWrong, _ := hasher.Verify(context.Background(), "not the password", old)
	if wrong || upgradedWrong != "" {
		t.Fatal("a wrong password produced a match or an upgrade")
	}
}

// Each hash holds 64 MiB for its duration. The semaphore is what stops a burst of logins becoming an
// out-of-memory kill — and a request whose client has gone stops waiting for a slot.
func TestHashingWaitsForASlotAndGivesUpWithItsContext(t *testing.T) {
	hasher := NewHasher(cheapParams, 1)
	if err := hasher.slots.Acquire(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	defer hasher.slots.Release(1)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := hasher.Hash(ctx, passphrase)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Hash with every slot taken = %v, want context.DeadlineExceeded", err)
	}
	_, _, err = hasher.Verify(ctx, passphrase, "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Verify with every slot taken = %v, want context.DeadlineExceeded", err)
	}
}
