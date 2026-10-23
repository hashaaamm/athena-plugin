package middleware

import (
	"github.com/gin-gonic/gin"

	"{{ cookiecutter.go_module }}/internal/security"
)

const actorKey = "{{ cookiecutter.project_slug }}.actor"

// RequireActor is the one middleware that decides 401. The router applies it to the whole `private`
// group, so every route mounted there is authenticated whether or not its handler says so — deny by
// default, enforced where routes are grouped rather than route by route. Ask Athena for the
// authorization rules; they grade that MUST.
func RequireActor(tokens *security.Tokens) gin.HandlerFunc {
	return func(c *gin.Context) {
		actor, err := security.ActorFromAuthorization(c.GetHeader("Authorization"), tokens)
		if err != nil {
			_ = c.Error(err)
			c.Abort()
			return
		}
		c.Set(actorKey, actor)
		c.Next()
	}
}

// ActorFrom is how a handler reads the verified caller. ok is false only when the route is not
// behind RequireActor, which is a wiring mistake, never a client's.
func ActorFrom(c *gin.Context) (security.Actor, bool) {
	value, exists := c.Get(actorKey)
	if !exists {
		return security.Actor{}, false
	}
	actor, ok := value.(security.Actor)
	return actor, ok
}
