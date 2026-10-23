package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"{{ cookiecutter.go_module }}/internal/service"
)

// REFRESH_TRANSPORT=body, the default. The refresh token travels in the JSON body, both ways:
// login, refresh and change-password return it, and refresh and logout take it. Right for a
// server-to-server or mobile client. A browser must not keep this token in script-readable storage;
// a browser client runs this service with REFRESH_TRANSPORT=cookie instead (auth_cookie.go).
//
// The annotations in this file describe docs/openapi.json; `just gen` documents the two transports
// separately (scripts/gen.sh), so each document describes exactly the API that is served.

// LoginBody exchanges credentials for an access token and a refresh token.
//
// @Summary		Login
// @Description	Exchange an email and a password for an access token and a refresh token.
// @Description
// @Description	A wrong password and an unknown address answer identically, in body, status and timing.
// @ID			login_api_v1_auth_login_post
// @Tags		auth,transport-body
// @Accept		json
// @Produce		json
// @Param		body	body		LoginRequest			true	"Credentials"
// @Success		200		{object}	TokenPairRead			"Successful Response"
// @Failure		401		{object}	apperr.ErrorResponse	"Unauthorized"
// @Failure		422		{object}	apperr.ErrorResponse	"Unprocessable Entity"
// @Router		/api/v1/auth/login [post]
func (h *Auth) LoginBody(c *gin.Context) {
	if pair, ok := h.login(c); ok {
		c.JSON(http.StatusOK, tokenPairRead(pair))
	}
}

// RefreshBody spends a refresh token for a new pair.
//
// @Summary		Refresh
// @Description	Spend a refresh token for a new pair.
// @Description
// @Description	Single-use: the token presented is revoked, and presenting it again revokes every token descended from the same sign-in. Store the new refresh token before using the new access token.
// @ID			refresh_api_v1_auth_refresh_post
// @Tags		auth,transport-body
// @Accept		json
// @Produce		json
// @Param		body	body		RefreshRequest			true	"The refresh token to spend"
// @Success		200		{object}	TokenPairRead			"Successful Response"
// @Failure		401		{object}	apperr.ErrorResponse	"Unauthorized"
// @Failure		422		{object}	apperr.ErrorResponse	"Unprocessable Entity"
// @Router		/api/v1/auth/refresh [post]
func (h *Auth) RefreshBody(c *gin.Context) {
	var req RefreshRequest
	if !BindStrictJSON(c, &req) {
		return
	}
	pair, err := h.Service.Refresh(c.Request.Context(), *req.RefreshToken)
	if err != nil {
		Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, tokenPairRead(pair))
}

// LogoutBody ends the session a refresh token belongs to.
//
// @Summary		Logout
// @Description	End the session a refresh token belongs to. 204 whether or not there was one to end.
// @ID			logout_api_v1_auth_logout_post
// @Tags		auth,transport-body
// @Accept		json
// @Param		body	body		LogoutRequest			true	"A refresh token from the session to end"
// @Success		204		"Successful Response"
// @Failure		422		{object}	apperr.ErrorResponse	"Unprocessable Entity"
// @Router		/api/v1/auth/logout [post]
func (h *Auth) LogoutBody(c *gin.Context) {
	var req LogoutRequest
	if !BindStrictJSON(c, &req) {
		return
	}
	if err := h.Service.Logout(c.Request.Context(), *req.RefreshToken); err != nil {
		Fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ChangePasswordBody replaces the caller's password and returns the session that replaces theirs.
//
// @Summary		Change Password
// @Description	Replace the caller's password. A valid token is not enough; the current password is.
// @Description
// @Description	Ends every session the account has, and starts a new one for the caller: the pair returned here replaces the one the caller was holding.
// @ID			change_password_api_v1_auth_change_password_post
// @Tags		auth,transport-body
// @Accept		json
// @Produce		json
// @Security	BearerAuth
// @Param		body	body		ChangePasswordRequest	true	"The current and the new password"
// @Success		200		{object}	TokenPairRead			"Successful Response"
// @Failure		401		{object}	apperr.ErrorResponse	"Unauthorized"
// @Failure		403		{object}	apperr.ErrorResponse	"Forbidden"
// @Failure		422		{object}	apperr.ErrorResponse	"Unprocessable Entity"
// @Router		/api/v1/auth/change-password [post]
func (h *Auth) ChangePasswordBody(c *gin.Context) {
	if pair, ok := h.changePassword(c); ok {
		c.JSON(http.StatusOK, tokenPairRead(pair))
	}
}

func tokenPairRead(pair service.TokenPair) TokenPairRead {
	return TokenPairRead{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		TokenType:    "bearer",
		ExpiresIn:    pair.ExpiresIn,
	}
}
