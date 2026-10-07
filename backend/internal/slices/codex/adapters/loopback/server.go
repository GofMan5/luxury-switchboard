// Package loopback implements the loopback redirect server for the
// OpenAI OAuth login flow: the adapter behind the application layer's
// RedirectServer port. It binds a listener on the local machine that
// OpenAI's authorize page redirects the system browser back to, catches
// that redirect, and hands the authorization code to the login flow.
//
// The listener is 127.0.0.1 only, never a wildcard address: anyone on
// the LAN could otherwise race the redirect and harvest a code. The port
// is pinned to the one registered with OpenAI for this client, so the
// address stays stable across logins.
package loopback

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
)

// The adapter must satisfy the port as declared, not as remembered.
var _ application.RedirectServer = (*RedirectServer)(nil)

const (
	// loopbackAddr is the address registered with OpenAI for this
	// client: local host, fixed port. It must stay in lockstep with the
	// oauth adapter's redirect URI — OpenAI rejects authorize requests
	// whose redirect_uri does not match the registered address, and the
	// fixed port is what makes the browser land here. The host part must
	// be 127.0.0.1, never 0.0.0.0: a wildcard bind would let any machine
	// on the LAN answer instead.
	loopbackAddr = "127.0.0.1:1455"

	// readHeaderTimeout and idleTimeout are hardcoded safety caps, not
	// configuration: a browser that connects and stalls must not hold a
	// file descriptor or a goroutine, and an idle keep-alive must not
	// outlive the login. Bounded to well under any login flow's natural
	// length.
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 30 * time.Second
)

var (
	// errAlreadyRunning answers a Start on a server whose listener is
	// already serving a flow.
	errAlreadyRunning = errors.New("codex oauth server already running")

	// errServerStopped answers an AwaitCode that has nothing to wait
	// for: the server was stopped, or never started.
	errServerStopped = errors.New("codex oauth server stopped")

	// errLoginCancelled answers an AwaitCode that ended because the
	// browser hit the cancel route.
	errLoginCancelled = errors.New("codex login cancelled")
)

// RedirectServer owns the loopback listener for OAuth logins. It
// implements the application layer's RedirectServer port. A fresh
// RedirectServer is stopped, not running; the zero value is not usable,
// take one from NewRedirectServer.
//
// Start/Stop/AwaitCode are safe for concurrent use. Each Start mints an
// independent session: the code from one login is never delivered to the
// next one, and a Stop leaves the server ready to serve again.
type RedirectServer struct {
	laddr string

	// mu guards active: it decides who is the current session, so the
	// lifecycles of Start, Stop and AwaitCode synchronize on it before
	// anything else.
	mu sync.Mutex

	active *listenerSession
}

// NewRedirectServer builds the RedirectServer bound to the pinned
// production address. The listener only opens when Start is called.
func NewRedirectServer() *RedirectServer {
	return newRedirectServerAt(loopbackAddr)
}

// newRedirectServerAt builds a RedirectServer for an explicit loopback
// address. It exists for tests, which take an ephemeral port on the same
// host so the address stays loopback; production takes NewRedirectServer.
func newRedirectServerAt(laddr string) *RedirectServer {
	return &RedirectServer{laddr: laddr}
}

// Start binds the loopback listener for one login flow and remembers the
// state the authorize request minted. expectedState is compared against
// the redirect's state parameter: only the browser OpenAI redirected is
// carrying it, and the check rejects anything that guessed the callback
// route.
//
// Start returns an error if a flow is already running or the port cannot
// be bound — most often another process (or a leftover of this one) on
// the pinned port, which names the port in the error so the failure is
// actionable. Calling Start again after Stop is supported. Concurrent
// Starts race fair: exactly one wins, and the loser closes the listener
// it bound and reports the already-running error.
func (r *RedirectServer) Start(expectedState string) error {
	r.mu.Lock()
	if r.active != nil {
		r.mu.Unlock()
		return errAlreadyRunning
	}
	r.mu.Unlock()

	// The bind runs outside mu: a slow bind (firewall interaction, a
	// stack hiccup) must not hold the lock that Stop and AwaitCode take
	// just to read the current session.
	listener, err := net.Listen("tcp", r.laddr)
	if err != nil {
		return fmt.Errorf("codex oauth port %s in use: %w", portOf(r.laddr), err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// Another Start may have won while this one was binding: only one
	// session owns the server, and the loser closes the listener it bound
	// so the port is not held by a session nobody can reach.
	if r.active != nil {
		_ = listener.Close()
		return errAlreadyRunning
	}

	session := newListenerSession(expectedState, listener)
	r.active = session
	go func() {
		// Serve returns ErrServerClosed on Stop and a listener error only
		// after the listener is already closed; either way stop() has
		// taken care of the session and the error carries no information
		// worth surfacing.
		_ = session.server.Serve(listener)
	}()
	return nil
}

// Stop tears down the listener and the current flow, if one is running.
// Stop on a stopped server is a no-op; any pending AwaitCode resolves
// immediately with errServerStopped, and the port is released so the
// next Start can bind it.
func (r *RedirectServer) Stop() {
	r.mu.Lock()
	session := r.active
	r.active = nil
	r.mu.Unlock()

	if session != nil {
		session.stop()
	}
}

// AwaitCode blocks until the login flow's authorization code arrives,
// the caller gives up, or the flow ends without one. The code is
// delivered once — the first callback that passed the state check wins
// and the channel is closed to no one else.
//
// It resolves with errLoginCancelled when the browser hit the cancel
// route, errServerStopped when the flow was stopped (including before it
// ever started or before this call), and the caller's context error —
// wrapped with a codex prefix — when the caller's own cancellation wins.
// ctx must not be nil; a nil context is read as context.Background.
func (r *RedirectServer) AwaitCode(ctx context.Context) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	r.mu.Lock()
	session := r.active
	r.mu.Unlock()
	if session == nil {
		return "", errServerStopped
	}

	// A code may already be waiting before anything selects. The code,
	// and nothing else, takes precedence: a flow that finished cannot
	// end as cancelled or stopped.
	select {
	case code := <-session.code:
		return code, nil
	default:
	}

	select {
	case code := <-session.code:
		return code, nil
	case <-ctx.Done():
		return "", fmt.Errorf("codex login await: %w", ctx.Err())
	case <-session.cancelled:
		return "", errLoginCancelled
	case <-session.stopped:
		return "", errServerStopped
	}
}

// portOf renders a listen address's port for an error message: the port
// when the address splits, the whole address when it does not, so the
// message is never empty.
func portOf(laddr string) string {
	if _, port, err := net.SplitHostPort(laddr); err == nil {
		return port
	}
	return laddr
}

// listenerSession is one Start's worth of listener state. session serves
// one flow and is then discarded; the next Start mints a fresh session,
// so no code, channel or once can leak across logins.
type listenerSession struct {
	state string

	// listener and server live for the duration of this session's
	// Serve goroutine.
	listener net.Listener
	server   *http.Server

	// code carries the authorization code to AwaitCode; buffered so the
	// handler never blocks on delivering it.
	code chan string

	// cancelled closes when the browser hits the cancel route; stopped
	// closes when the session is being torn down. cancelOnce and
	// stopOnce make both events one-shot and idempotent however many
	// times they are triggered.
	cancelled  chan struct{}
	stopped    chan struct{}
	cancelOnce sync.Once
	stopOnce   sync.Once
}

// newListenerSession mints a session serving the callback routes for
// one flow's expected state.
func newListenerSession(state string, listener net.Listener) *listenerSession {
	session := &listenerSession{
		state:     state,
		listener:  listener,
		code:      make(chan string, 1),
		cancelled: make(chan struct{}),
		stopped:   make(chan struct{}),
	}
	// The session is its own handler: the routes it serves are the whole
	// server, one ServeMux would only re-dispatch what ServeHTTP already
	// switches on.
	session.server = &http.Server{
		Handler:           session,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}
	return session
}

// stop tears the session down exactly once: the terminal events fire
// before the listener closes, so a pending AwaitCode resolves instead of
// blocking on a socket no one will ever deliver to. Safe to call
// concurrently and from a context that has already been through Serve's
// exit path.
func (s *listenerSession) stop() {
	s.stopOnce.Do(func() {
		close(s.stopped)
		_ = s.listener.Close()
	})
}
