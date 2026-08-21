package handlers

import (
	"net/http"
	"time"
)

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
