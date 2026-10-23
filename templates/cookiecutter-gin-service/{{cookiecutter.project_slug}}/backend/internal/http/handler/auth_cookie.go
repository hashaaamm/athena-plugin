package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"{{ cookiecutter.go_module }}/internal/service"
)

// REFRESH_TRANSPORT=cookie: the refresh token as an httpOnly cookie — set, read, cleared — for a
// browser client.
//
// A browser has nowhere to keep a credential that script in the page cannot read, except a cookie
// marked HttpOnly. So the refresh token never appears in a response body here. Login, refresh and
// change-password set it as a cookie and return only the access token, which the web client holds
// in memory; refresh and logout read it back from the cookie. Ask Athena about browser token storage
// for why this shape and not sessionStorage.
//
// The attributes, and what each one is for:
//
//   - HttpOnly — invisible to document.cookie and to every script, so an XSS payload can use the
//     session while the page is open but cannot carry the token off to replay later.
//   - Secure — never sent over plain HTTP. REFRESH_COOKIE_SECURE, refused as false when deployed.
//   - SameSite — strict by default: not attached to any request a third-party page starts. See
//     RefreshCookieSameSite in internal/config.
//   - Path — the auth routes only. No other endpoint reads the cookie, so no other request carries it.
//   - No Domain — host-only, so no other subdomain ever receives it.
//
// CSRF: a cookie is attached to requests the browser makes whoever started them, which is the whole
// difference between it and a bearer header. Refresh and logout are the only routes that read it, and
// the router puts middleware.RequireBrowserCaller in front of both.

// RefreshCookieName is namespaced by project. Cookies are scoped by host, not by port, so two
// services generated from this template and run on localhost would otherwise overwrite each other's
// session.
const RefreshCookieName = "{{ cookiecutter.project_slug }}.refresh_token"

// CookieSettings are the refresh cookie's attributes, from configuration.
type CookieSettings struct {
	Path     string
	MaxAge   int
	Secure   bool
	SameSite http.SameSite
}

// LoginCookie exchanges credentials for an access token, and sets the refresh cookie.
//
// @Summary		Login
// @Description	Exchange an email and a password for an access token, and a refresh cookie.
// @Description
// @Description	A wrong password and an unknown address answer identically, in body, status and timing.
// @ID			login_api_v1_auth_login_post
// @Tags		auth,transport-cookie
// @Accept		json
// @Produce		json
// @Param		body	body		LoginRequest			true	"Credentials"
// @Success		200		{object}	AccessTokenRead			"Successful Response"
// @Failure		401		{object}	apperr.ErrorResponse	"Unauthorized"
// @Failure		422		{object}	apperr.ErrorResponse	"Unprocessable Entity"
// @Router		/api/v1/auth/login [post]
func (h *Auth) LoginCookie(c *gin.Context) {
	if pair, ok := h.login(c); ok {
		c.JSON(http.StatusOK, h.deliverSession(c, pair))
	}
}

// RefreshCookie spends the refresh cookie for a new access token and a new cookie.
//
// @Summary				Refresh
// @Description			Spend the refresh cookie for a new access token and a new cookie.
// @Description
// @Description			Single-use: the cookie this replaces is revoked, and presenting it again revokes every token descended from the same sign-in.
// @ID					refresh_api_v1_auth_refresh_post
// @Tags				auth,transport-cookie
// @Produce				json
// @x-cookie-parameter	{"name": "{{ cookiecutter.project_slug }}.refresh_token", "description": "The refresh token, set by login, refresh and change-password."}
// @Success				200	{object}	AccessTokenRead			"Successful Response"
// @Failure				401	{object}	apperr.ErrorResponse	"Unauthorized"
// @Failure				403	{object}	apperr.ErrorResponse	"Forbidden"
// @Router				/api/v1/auth/refresh [post]
func (h *Auth) RefreshCookie(c *gin.Context) {
	// Absent is not an error here: the service refuses "" with the same 401 as a bad token.
	token, _ := c.Cookie(RefreshCookieName)
	pair, err := h.Service.Refresh(c.Request.Context(), token)
	if err != nil {
		Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, h.deliverSession(c, pair))
}

// LogoutCookie ends this session and clears its cookie.
//
// @Summary				Logout
// @Description			End this session and clear its cookie. 204 whether or not there was a session to end.
// @ID					logout_api_v1_auth_logout_post
// @Tags				auth,transport-cookie
// @x-cookie-parameter	{"name": "{{ cookiecutter.project_slug }}.refresh_token", "description": "The refresh token, set by login, refresh and change-password."}
// @Success				204	"Successful Response"
// @Failure				403	{object}	apperr.ErrorResponse	"Forbidden"
// @Router				/api/v1/auth/logout [post]
func (h *Auth) LogoutCookie(c *gin.Context) {
	token, _ := c.Cookie(RefreshCookieName)
	if err := h.Service.Logout(c.Request.Context(), token); err != nil {
		Fail(c, err)
		return
	}
	h.clearSession(c)
	c.Status(http.StatusNoContent)
}

// ChangePasswordCookie replaces the caller's password, and sets a new refresh cookie.
//
// @Summary		Change Password
// @Description	Replace the caller's password. A valid token is not enough; the current password is.
// @Description
// @Description	Ends every session the account has, and starts a new one for the caller: a new access token, and a new refresh cookie.
// @ID			change_password_api_v1_auth_change_password_post
// @Tags		auth,transport-cookie
// @Accept		json
// @Produce		json
// @Security	BearerAuth
// @Param		body	body		ChangePasswordRequest	true	"The current and the new password"
// @Success		200		{object}	AccessTokenRead			"Successful Response"
// @Failure		401		{object}	apperr.ErrorResponse	"Unauthorized"
// @Failure		403		{object}	apperr.ErrorResponse	"Forbidden"
// @Failure		422		{object}	apperr.ErrorResponse	"Unprocessable Entity"
// @Router		/api/v1/auth/change-password [post]
func (h *Auth) ChangePasswordCookie(c *gin.Context) {
	if pair, ok := h.changePassword(c); ok {
		c.JSON(http.StatusOK, h.deliverSession(c, pair))
	}
}

// deliverSession puts the refresh token in the cookie and returns the rest as the body. Never both:
// a body carrying the refresh token as well would hand it to any script that can call the endpoint,
// which is exactly the read the cookie exists to remove.
func (h *Auth) deliverSession(c *gin.Context, pair service.TokenPair) AccessTokenRead {
	http.SetCookie(c.Writer, &http.Cookie{ //nolint:gosec // Secure is REFRESH_COOKIE_SECURE, refused as false when deployed
		Name:     RefreshCookieName,
		Value:    pair.RefreshToken,
		Path:     h.Cookie.Path,
		MaxAge:   h.Cookie.MaxAge,
		Secure:   h.Cookie.Secure,
		HttpOnly: true,
		SameSite: h.Cookie.SameSite,
	})
	return AccessTokenRead{AccessToken: pair.AccessToken, TokenType: "bearer", ExpiresIn: pair.ExpiresIn}
}

// clearSession tells the browser to drop the cookie. Same name, path and attributes as when it was
// set, or the browser keeps the original.
func (h *Auth) clearSession(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{ //nolint:gosec // Secure is REFRESH_COOKIE_SECURE, refused as false when deployed
		Name:  RefreshCookieName,
		Value: "",
		Path:  h.Cookie.Path,
		// Negative is Go's spelling of `Max-Age=0`; Expires covers clients that predate Max-Age.
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		Secure:   h.Cookie.Secure,
		HttpOnly: true,
		SameSite: h.Cookie.SameSite,
	})
}
