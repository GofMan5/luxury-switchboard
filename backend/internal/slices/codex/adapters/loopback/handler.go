package loopback

import (
	"io"
	"net/http"
)

// ServeHTTP routes the redirect server's two routes: the OAuth callback
// OpenAI redirects the browser to, and the cancel route the login page
// links for giving up. The method check runs first on every path —
// anything but GET has no place in a flow the browser drives with
// navigations — and unknown paths get the plain 404 they would get
// anywhere else. Error answers carry no body: the page is the browser's
// to render, not the flow's to explain.
func (s *listenerSession) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case "/auth/callback":
		s.serveCallback(w, r)
	case "/cancel":
		s.serveCancel(w)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// serveCallback handles the OAuth redirect. The state parameter must
// match the one the authorize request minted: a request that guessed the
// callback route without riding OpenAI's redirect does not carry it.
// A rejected request delivers nothing — the flow keeps waiting for the
// real redirect — and the code must be present: OpenAI redirects
// error answers to this same route without a code, and those answer
// plain text rather than a login page.
func (s *listenerSession) serveCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if query.Get("state") != s.state {
		writePlain(w, http.StatusBadRequest, "State mismatch")
		return
	}
	code := query.Get("code")
	if code == "" {
		writePlain(w, http.StatusBadRequest, "Missing code")
		return
	}

	// The delivery is non-blocking: exactly one code fits the buffered
	// channel, so the first callback wins and a late duplicate — a
	// browser that reloaded, a second tab the redirect opened — can
	// never wedge the handler or displace the winner.
	select {
	case s.code <- code:
	default:
	}

	writePlain(w, http.StatusOK, "Login completed. You can close this tab.")
}

// serveCancel handles the cancel route: any number of hits ends the flow
// once. The verdict goes to AwaitCode over the cancelled channel; the
// browser gets a page it can read after the tab outlives the wait.
func (s *listenerSession) serveCancel(w http.ResponseWriter) {
	s.cancelOnce.Do(func() {
		close(s.cancelled)
	})
	writePlain(w, http.StatusOK, "Login cancelled")
}

// writePlain answers with an exact plain-text body. The content type is
// exactly text/plain — no charset parameter — and the body is written
// verbatim, never through http.Error, whose trailing newline and
// text/html would both change what the tests and the browser see.
func writePlain(w http.ResponseWriter, statusCode int, body string) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(statusCode)
	_, _ = io.WriteString(w, body)
}
