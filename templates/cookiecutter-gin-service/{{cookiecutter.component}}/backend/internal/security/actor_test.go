package security

import (
	"testing"

	"github.com/google/uuid"

	"{{ cookiecutter.go_module }}/internal/apperr"
)

func TestTheCallerIsReadFromABearerAccessToken(t *testing.T) {
	tokens := defaultTokens(t)
	userID := uuid.New()
	token, _, _ := tokens.IssueAccess(userID)

	for _, header := range []string{"Bearer " + token, "bearer " + token, "  Bearer   " + token + "  "} {
		actor, err := ActorFromAuthorization(header, tokens)
		if err != nil || actor.ID != userID {
			t.Fatalf("ActorFromAuthorization(%q) = %v, %v; want %s", header, actor, err, userID)
		}
	}
}

// Every one of these is a 401, never a 403: a client that sees 403 for a missing credential reads it
// as a permissions problem and never tries to authenticate.
func TestAnythingButABearerAccessTokenIsUnauthenticated(t *testing.T) {
	tokens := defaultTokens(t)
	refresh, _ := tokens.IssueRefresh(uuid.New())

	for _, header := range []string{
		"",
		"Bearer",
		"Bearer ",
		"Basic YWRhOnNlY3JldA==",
		"Bearer not-a-token",
		"Bearer " + refresh.Token,
	} {
		_, err := ActorFromAuthorization(header, tokens)
		if appErr, ok := apperr.As(err); !ok || appErr.Status != 401 {
			t.Fatalf("ActorFromAuthorization(%q) = %v; want a 401", header, err)
		}
	}
}
