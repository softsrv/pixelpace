package handlers

import (
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// concept2StateCookie holds the OAuth state between Connect and the callback.
const concept2StateCookie = "concept2_oauth_state"

// concept2StateMaxAge bounds how long a started Concept2 connect stays valid.
const concept2StateMaxAge = 10 * time.Minute

// setConcept2StateCookie stores the OAuth state, the browser's timezone and
// the id of the user who started the connect as
// state + "." + base64url(tz) + "." + userID. Neither the state (base64url)
// nor the UUID contains ".", so the value splits unambiguously; a value that
// does not split, decode or parse is a bad state. The user id binds the state
// to that user: a callback served to anyone else is rejected. The cookie is
// scoped to /profile/concept2 and short-lived.
func setConcept2StateCookie(w http.ResponseWriter, state, tz string, userID uuid.UUID, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     concept2StateCookie,
		Value:    state + "." + base64.RawURLEncoding.EncodeToString([]byte(tz)) + "." + userID.String(),
		Path:     "/profile/concept2",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(concept2StateMaxAge.Seconds()),
	})
}

// concept2StateValue is the decoded concept2_oauth_state cookie.
type concept2StateValue struct {
	State  string
	TZ     string
	UserID uuid.UUID
}

// readConcept2StateCookie decodes the value stored by setConcept2StateCookie.
// ok is false when the cookie is missing, does not split into exactly state,
// timezone and user id, has an empty state, or its timezone or user id does
// not decode; every such case is a bad state.
func readConcept2StateCookie(r *http.Request) (v concept2StateValue, ok bool) {
	c, err := r.Cookie(concept2StateCookie)
	if err != nil {
		return concept2StateValue{}, false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 3 || parts[0] == "" {
		return concept2StateValue{}, false
	}
	tz, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return concept2StateValue{}, false
	}
	userID, err := uuid.Parse(parts[2])
	if err != nil {
		return concept2StateValue{}, false
	}
	return concept2StateValue{State: parts[0], TZ: string(tz), UserID: userID}, true
}

// clearConcept2StateCookie expires the OAuth state cookie, so a state is used
// at most once whatever the callback's outcome.
func clearConcept2StateCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     concept2StateCookie,
		Value:    "",
		Path:     "/profile/concept2",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}

// clearAuthCookies expires both auth cookies on the response.
func clearAuthCookies(w http.ResponseWriter, secure bool) {
	epoch := time.Unix(0, 0)
	http.SetCookie(w, &http.Cookie{
		Name:     "access_token",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  epoch,
		MaxAge:   -1,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    "",
		Path:     "/auth",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  epoch,
		MaxAge:   -1,
	})
}
