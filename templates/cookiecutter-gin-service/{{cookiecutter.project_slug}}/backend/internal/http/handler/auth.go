package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"

	"{{ cookiecutter.go_module }}/internal/http/middleware"
	"{{ cookiecutter.go_module }}/internal/service"
)

// Auth is the authentication routes. The routes both transports share are in this file; where the
// refresh token travels is the only thing that differs between them, and it lives in auth_body.go
// (REFRESH_TRANSPORT=body, the default: the token in the JSON body, both ways) and auth_cookie.go
// (REFRESH_TRANSPORT=cookie: an httpOnly cookie, for a browser client). The router mounts one set or
// the other; the service never learns which.
//
// Which route group a route is on is the security decision, and it is made in router.go: `public`
// is mounted bare, `private` behind RequireActor, so a route added there is authenticated without
// saying anything.
type Auth struct {
	Service *service.AuthService
	Cookie  CookieSettings
}

// Register creates an account.
//
// @Summary		Register
// @Description	Create an account. Returns the user and no session — log in next.
// @ID			register_api_v1_auth_register_post
// @Tags		auth
// @Accept		json
// @Produce		json
// @Param		body	body		RegisterRequest			true	"The account to create"
// @Success		201		{object}	UserRead				"Successful Response"
// @Failure		409		{object}	apperr.ErrorResponse	"Conflict"
// @Failure		422		{object}	apperr.ErrorResponse	"Unprocessable Entity"
// @Router		/api/v1/auth/register [post]
func (h *Auth) Register(c *gin.Context) {
	var req RegisterRequest
	if !BindJSON(c, &req) {
		return
	}
	user, err := h.Service.Register(c.Request.Context(), normaliseEmail(req.Email), req.Password)
	if err != nil {
		Fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, userRead(user))
}

// Me returns the signed-in user.
//
// @Summary		Me
// @ID			me_api_v1_auth_me_get
// @Tags		auth
// @Produce		json
// @Security	BearerAuth
// @Success		200	{object}	UserRead				"Successful Response"
// @Failure		401	{object}	apperr.ErrorResponse	"Unauthorized"
// @Router		/api/v1/auth/me [get]
func (h *Auth) Me(c *gin.Context) {
	actor, ok := middleware.ActorFrom(c)
	if !ok {
		Fail(c, errNoActor)
		return
	}
	user, err := h.Service.Me(c.Request.Context(), actor.ID)
	if err != nil {
		Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, userRead(user))
}

// errNoActor is a route that reads the caller but is not behind RequireActor — mounted on `public`
// by mistake. A 500, loudly, rather than a guess: the fix is one line in router.go.
var errNoActor = errors.New("handler needs the caller but RequireActor did not run: mount it on the private group")

// changePassword binds the request and reads the caller, the half both transports share.
func (h *Auth) changePassword(c *gin.Context) (service.TokenPair, bool) {
	var req ChangePasswordRequest
	if !BindJSON(c, &req) {
		return service.TokenPair{}, false
	}
	actor, ok := middleware.ActorFrom(c)
	if !ok {
		Fail(c, errNoActor)
		return service.TokenPair{}, false
	}
	pair, err := h.Service.ChangePassword(c.Request.Context(), actor.ID, *req.CurrentPassword, req.NewPassword)
	if err != nil {
		Fail(c, err)
		return service.TokenPair{}, false
	}
	return pair, true
}

// login binds credentials and starts a session, the half both transports share.
func (h *Auth) login(c *gin.Context) (service.TokenPair, bool) {
	var req LoginRequest
	if !BindJSON(c, &req) {
		return service.TokenPair{}, false
	}
	pair, err := h.Service.Login(c.Request.Context(), normaliseEmail(req.Email), req.Password)
	if err != nil {
		Fail(c, err)
		return service.TokenPair{}, false
	}
	return pair, true
}

func userRead(u service.User) UserRead {
	return UserRead{ID: u.ID, Email: u.Email, IsActive: u.IsActive, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt}
}

// emailShape is normalisation's precondition, not address validation: it rejects a value with no
// `@`, or nothing either side of it, and nothing else. Real validation means sending mail.
func emailShape(fl validator.FieldLevel) bool {
	local, domain, found := strings.Cut(strings.TrimSpace(fl.Field().String()), "@")
	return found && local != "" && domain != ""
}

// normaliseEmail trims and lower-cases an address, which is what the unique index on users.email
// assumes. Two rows differing only in case is an account-takeover shape — whichever one a lookup
// finds first wins — so the fix belongs at the boundary rather than in a case-insensitive comparison
// somebody later forgets to write.
func normaliseEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
