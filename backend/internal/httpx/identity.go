package httpx

import (
	"net/http"
	"strconv"
)

// The gateway authenticates the caller and tells the internal services who it is with
// these headers. Those services are only reachable from the private network, so they
// trust them; the gateway strips any copies a client tries to send.
const (
	HeaderUserID   = "X-User-ID"
	HeaderUserRole = "X-User-Role"
	HeaderClientIP = "X-Client-IP"
)

// Caller returns the signed-in user the gateway vouches for. When there is none it
// writes the 401 itself and returns ok=false.
func Caller(w http.ResponseWriter, r *http.Request) (id int64, role string, ok bool) {
	id, err := strconv.ParseInt(r.Header.Get(HeaderUserID), 10, 64)
	if err != nil || id <= 0 {
		WriteError(w, r, http.StatusUnauthorized, "unauthenticated", "sign in to do that")
		return 0, "", false
	}
	return id, r.Header.Get(HeaderUserRole), true
}
