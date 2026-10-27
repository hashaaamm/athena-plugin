package security

import (
	"strings"

	"github.com/google/uuid"

	"{{ cookiecutter.go_module }}/internal/apperr"
)

// Actor is who is calling, rebuilt from the access token's claims. Not a database row.
//
// Nothing was loaded to produce it, which is the property that makes a JWT worth having and is also
// its cost: a deactivated account keeps calling until its token expires — fifteen minutes with the
// default lifetime. An endpoint that must not tolerate that window loads the row itself; /auth/me
// does, and says why.
//
// Roles and permissions belong on this type when they arrive. Ask Athena for the authorization and
// RBAC rules before inventing a shape for them.
type Actor struct {
	ID uuid.UUID
}

// ActorFromAuthorization verifies an `Authorization` header value and returns the caller.
// The only code in the service that decides a request is unauthenticated; RequireActor in
// internal/http/middleware is its only caller.
func ActorFromAuthorization(header string, tokens *Tokens) (Actor, error) {
	scheme, token, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "bearer") || strings.TrimSpace(token) == "" {
		return Actor{}, apperr.Unauthorized("Authentication required")
	}
	// The token alone, never the whole header. `Access` is what refuses a refresh token presented
	// here: same key, same claims, thirty days instead of fifteen minutes.
	verified, err := tokens.Decode(strings.TrimSpace(token), Access)
	if err != nil {
		return Actor{}, err
	}
	return Actor{ID: verified.Subject}, nil
}
