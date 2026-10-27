// Package security holds the cross-cutting security primitives: password hashing, token signing,
// and the caller a token resolves to.
//
// Nothing here knows a request, a session row or a repository, which is why each piece has unit
// tests that need no database and no router. Nothing here is reversible either: the database holds
// an Argon2id digest, and a leaked backup yields no credential.
package security

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
	"golang.org/x/sync/semaphore"
)

// Argon2Params are the cost factors of an Argon2id digest. They are written into every digest, so
// a stored hash says how it was made and can be verified after the defaults change.
type Argon2Params struct {
	// Memory in KiB.
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultArgon2Params is Argon2id at m=64 MiB, t=3, p=4 — RFC 9106's second recommended option,
// for when 2 GiB per hash is not available. One place for the cost factors, which makes raising
// them a one-line change; Verify is what migrates the digests already in the table when they are.
var DefaultArgon2Params = Argon2Params{
	Memory:      64 * 1024,
	Iterations:  3,
	Parallelism: 4,
	SaltLength:  16,
	KeyLength:   32,
}

// Upper bounds on what a *stored* digest may ask for. A digest is data, and a row edited by hand —
// or written by an attacker with database access — that claims m=4 GiB would otherwise make every
// login for that account allocate it. Anything past these is treated as unreadable.
const (
	maxMemoryKiB  = 1024 * 1024
	maxIterations = 16
	minKeyLength  = 16
	maxKeyLength  = 64
)

// Hasher hashes and verifies passwords with Argon2id.
//
// **Bounded concurrency.** Each hash allocates Params.Memory — 64 MiB by default — for its whole
// duration. Unbounded, a burst of fifty logins on a 512 MiB Cloud Run instance is an out-of-memory
// kill, and an instance that dies under load is a denial of service with a login form on it. The
// semaphore admits as many hashes at once as there are CPUs to run them (more would only queue in
// the scheduler while holding their memory); the rest wait, and a request whose client gives up
// stops waiting with it.
//
// **A miss costs what a hit costs.** See Verify.
type Hasher struct {
	params Params
	slots  *semaphore.Weighted
	// absent is a real digest of a value no account has, computed once, on first use. Lazy because
	// the migration job builds no Hasher at all and the server should not pay for it before a login.
	absent func() (string, error)
	// onDerive is called with every key derivation. Nil outside tests; the enumeration test uses it
	// to prove a miss runs a real derivation.
	onDerive func()
}

// Params is an alias so callers read security.Params rather than the algorithm's name.
type Params = Argon2Params

// NewHasher builds a Hasher with the given cost factors. concurrency <= 0 means GOMAXPROCS.
func NewHasher(params Params, concurrency int) *Hasher {
	if concurrency <= 0 {
		concurrency = runtime.GOMAXPROCS(0)
	}
	h := &Hasher{params: params, slots: semaphore.NewWeighted(int64(concurrency))}
	h.absent = sync.OnceValues(func() (string, error) {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return "", fmt.Errorf("generate absent-user secret: %w", err)
		}
		return h.derive(base64.RawStdEncoding.EncodeToString(secret), h.params)
	})
	return h
}

// Hash returns the digest to store, in PHC string format:
// `$argon2id$v=19$m=65536,t=3,p=4$<salt>$<key>`. Salt and parameters travel inside it.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	if err := h.slots.Acquire(ctx, 1); err != nil {
		return "", fmt.Errorf("wait for a hashing slot: %w", err)
	}
	defer h.slots.Release(1)
	return h.derive(password, h.params)
}

// Verify checks a password against a stored digest. It returns whether it matched and, when the
// digest was made with different parameters than the current ones, a replacement digest to store.
//
// `stored == ""` means there is no such account — and a full derivation runs anyway, against a
// digest of nothing. Returning early on a miss answers in microseconds where a hit pays Argon2's
// whole cost, and that difference is stable enough to enumerate a user base through a load balancer
// over a lunch break. The caller must also answer both with the same body and status; the auth
// service does.
//
// A digest this build cannot read — another algorithm, a hand-edited column, parameters past the
// bounds above — fails the login rather than the request: an error here would turn one broken row
// into a page. It still pays for a derivation, for the same reason a miss does.
//
// Login is the only moment the plaintext is in hand, so it is the only moment a digest can be
// migrated to new parameters; the caller persists the replacement, which keeps that write
// conditional rather than a write on every login.
func (h *Hasher) Verify(ctx context.Context, password, stored string) (matched bool, upgraded string, err error) {
	if err := h.slots.Acquire(ctx, 1); err != nil {
		return false, "", fmt.Errorf("wait for a hashing slot: %w", err)
	}
	defer h.slots.Release(1)

	params, salt, key, parseErr := parseDigest(stored)
	if parseErr != nil {
		absent, err := h.absent()
		if err != nil {
			return false, "", err
		}
		params, salt, key, _ = parseDigest(absent)
	}
	candidate := argon2.IDKey([]byte(password), salt, params.Iterations, params.Memory, params.Parallelism, params.KeyLength)
	if h.onDerive != nil {
		h.onDerive()
	}
	// Constant time, so the comparison does not leak how many leading bytes were right.
	if parseErr != nil || subtle.ConstantTimeCompare(candidate, key) != 1 {
		return false, "", nil
	}
	if params == h.params {
		return true, "", nil
	}
	replacement, err := h.derive(password, h.params)
	if err != nil {
		return true, "", err
	}
	return true, replacement, nil
}

// derive computes a digest. Callers hold a slot.
func (h *Hasher) derive(password string, params Params) (string, error) {
	salt := make([]byte, params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, params.Iterations, params.Memory, params.Parallelism, params.KeyLength)
	if h.onDerive != nil {
		h.onDerive()
	}
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, params.Memory, params.Iterations, params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

var errUnreadableDigest = errors.New("unreadable password digest")

// parseDigest reads a PHC-format Argon2id digest. Anything else is errUnreadableDigest.
func parseDigest(digest string) (Params, []byte, []byte, error) {
	// "", "argon2id", "v=19", "m=…,t=…,p=…", salt, key
	parts := strings.Split(digest, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return Params{}, nil, nil, errUnreadableDigest
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return Params{}, nil, nil, errUnreadableDigest
	}
	var params Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &params.Memory, &params.Iterations, &params.Parallelism); err != nil {
		return Params{}, nil, nil, errUnreadableDigest
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return Params{}, nil, nil, errUnreadableDigest
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(key) < minKeyLength || len(key) > maxKeyLength {
		return Params{}, nil, nil, errUnreadableDigest
	}
	if params.Memory == 0 || params.Memory > maxMemoryKiB || params.Iterations == 0 ||
		params.Iterations > maxIterations || params.Parallelism == 0 || params.Memory < 8*uint32(params.Parallelism) {
		return Params{}, nil, nil, errUnreadableDigest
	}
	params.SaltLength = uint32(len(salt)) //nolint:gosec // bounded by the digest length checked above
	params.KeyLength = uint32(len(key))   //nolint:gosec // bounded by maxKeyLength
	return params, salt, key, nil
}
