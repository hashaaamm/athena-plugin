package handler

{% if cookiecutter.use_postgres == "yes" -%}
import (
	"time"

	"github.com/google/uuid"
)

// Wire contracts: what crosses the HTTP boundary, typed in and typed out. Field names, required
// fields and the component names swag gives them (`// @name ...`) are the API's contract with every
// client, including the React app generated from /openapi.json — renaming one is a breaking change.
//
// On request types, `binding` is the validation gin runs. On response types it is read only by
// swag, which marks the field required in the document; nothing binds into a response.
//
// The password bounds are here rather than in the service on purpose. The floor because short
// passwords are guessable; the ceiling because Argon2 will cheerfully spend real CPU and memory on a
// ten-megabyte password, and an endpoint that lets an anonymous caller choose how much to spend is a
// denial of service with a login form on it. There are no composition rules: they produce
// `Password1!` and a sticky note, and they rule out the passphrases that are actually strong.

// RegisterRequest creates an account.
type RegisterRequest struct {
	Email    string `json:"email"    binding:"required,min=3,max=320,email_shape" example:"ada@example.com"`
	Password string `json:"password" binding:"required,min=12,max=1024"            format:"password"`
} // @name RegisterRequest

// LoginRequest exchanges credentials for a session.
type LoginRequest struct {
	Email    string `json:"email"    binding:"required,min=3,max=320,email_shape" example:"ada@example.com"`
	Password string `json:"password" binding:"required,min=12,max=1024"            format:"password"`
} // @name LoginRequest

// ChangePasswordRequest replaces the caller's password.
type ChangePasswordRequest struct {
	// current_password is not length-bounded: it is checked against a stored digest, not hashed into
	// a new one, and a bound would tell a caller how long the real password is not. A pointer, so ""
	// is a wrong password (403) rather than a missing field (422). (Separated by a blank line so swag
	// does not publish this note as the field's description.)

	CurrentPassword *string `json:"current_password" binding:"required"                format:"password"`
	NewPassword     string  `json:"new_password"     binding:"required,min=12,max=1024" format:"password"`
} // @name ChangePasswordRequest

// RefreshRequest spends a refresh token for a new pair. Unknown fields are refused.
type RefreshRequest struct {
	// The refresh token from the last login or refresh. Spent by this call.
	RefreshToken *string `json:"refresh_token" binding:"required" format:"password"`
} // @name RefreshRequest

// LogoutRequest ends the session a refresh token belongs to. Unknown fields are refused.
type LogoutRequest struct {
	// Any refresh token from the session to end. Its whole family is revoked.
	RefreshToken *string `json:"refresh_token" binding:"required" format:"password"`
} // @name LogoutRequest

// UserRead is what every endpoint that returns a user returns. There is no hashed_password field,
// and adding one is how a digest reaches a browser's network tab.
type UserRead struct {
	ID        uuid.UUID `json:"id"         binding:"required" swaggertype:"string" format:"uuid"`
	Email     string    `json:"email"      binding:"required"`
	IsActive  bool      `json:"is_active"  binding:"required"`
	CreatedAt time.Time `json:"created_at" binding:"required" format:"date-time"`
	UpdatedAt time.Time `json:"updated_at" binding:"required" format:"date-time"`
} // @name UserRead

// TokenPairRead is a new session, or the next step of one: an access token and the refresh token
// behind it. What login, refresh and change-password return under REFRESH_TRANSPORT=body. The
// refresh token is single-use — spending it revokes it, and presenting it a second time revokes every
// token descended from the same sign-in.
//
// expires_in is sent even though the client could read `exp` out of the token, because a client that
// has to decode a JWT to know when to refresh is a client that will decode it without verifying it.
type TokenPairRead struct {
	// Bearer credential: `Authorization: Bearer <token>`.
	AccessToken string `json:"access_token"  binding:"required"`
	// Single-use. Spend it at `POST /api/v1/auth/refresh`.
	RefreshToken string `json:"refresh_token" binding:"required"`
	TokenType    string `json:"token_type" enums:"bearer" default:"bearer"`
	// Seconds until the access token expires.
	ExpiresIn int `json:"expires_in" binding:"required" example:"900"`
} // @name TokenPairRead

// AccessTokenRead is what a browser receives under REFRESH_TRANSPORT=cookie: the access token, and
// not the refresh token, which travels as an httpOnly cookie no script in the page can read.
// Putting it in this body as well would undo that.
type AccessTokenRead struct {
	// Bearer credential: `Authorization: Bearer <token>`.
	AccessToken string `json:"access_token" binding:"required"`
	TokenType   string `json:"token_type" enums:"bearer" default:"bearer"`
	// Seconds until the access token expires.
	ExpiresIn int `json:"expires_in" binding:"required" example:"900"`
} // @name AccessTokenRead

{% endif -%}
// ReadinessStatus is the readiness probe's answer.
type ReadinessStatus struct {
	Status  string `json:"status" binding:"required" example:"ok"`
	Version string `json:"version" binding:"required"`
{%- if cookiecutter.use_postgres == "yes" %}
	// `ok` when the database answered a trivial query.
	Database string `json:"database" binding:"required"`
{%- endif %}
} // @name ReadinessStatus
