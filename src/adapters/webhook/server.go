// Package webhook is the one thing in the tool that listens: it takes
// ThousandEyes alerts over HTTP and hands them, one at a time, to the work it
// is given. TLS and a public address are somebody else's: it serves plain HTTP
// on the listener it is handed, which the caller keeps on loopback.
package webhook

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/forwardnetworks/forward-1keyes/src/ports"
)

// AlertPath is the one path that accepts an alert.
const AlertPath = "/thousandeyes/alerts"

const (
	maxBody    = 64 << 10
	queueSize  = 16
	memoryTime = 3 * time.Hour
	memorySize = 1000
	// settle bounds the wait for cancelled work to return.
	settle = 5 * time.Second
)

// Work answers one alert. It returns when it is done or ctx is cancelled.
type Work func(ctx context.Context, alert ports.Alert, received time.Time)

// accepted is an alert the listener answered 202.
type accepted struct {
	alert    ports.Alert
	received time.Time
}

type remembered struct {
	alert ports.Alert
	at    time.Time
}

// Listener holds the queue and the memory of what was accepted.
type Listener struct {
	token [sha256.Size]byte
	now   func() time.Time
	work  Work

	mu       sync.Mutex
	waiting  []accepted
	inHand   *ports.Alert // the alert being worked, or whose work was cut short
	memory   []remembered // oldest first
	stopping bool
	stop     <-chan struct{} // closed when the listener is told to stop; nil before Run
	wake     chan struct{}
}

// New makes a listener that accepts requests carrying this bearer token.
func New(token string, now func() time.Time, work Work) *Listener {
	return &Listener{token: sha256.Sum256([]byte("Bearer " + token)), now: now, work: work, wake: make(chan struct{}, 1)}
}

// authorized compares the whole header in constant time. Both sides are hashed
// first, so that not even the length shows.
func (l *Listener) authorized(r *http.Request) bool {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	got := sha256.Sum256([]byte(values[0]))
	return subtle.ConstantTimeCompare(got[:], l.token[:]) == 1
}

// ServeHTTP judges the token before anything else about a request.
func (l *Listener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case !l.authorized(r):
		// The body is thrown away unread by the tool, so that a sender still
		// writing it gets the answer and not a broken connection.
		// A sender waiting to be told to go on is not told to.
		if r.Header.Get("Expect") == "" {
			_, _ = io.CopyN(io.Discard, r.Body, 4*maxBody)
		}
		w.WriteHeader(http.StatusUnauthorized)
	case r.URL.EscapedPath() != AlertPath:
		w.WriteHeader(http.StatusNotFound)
	case r.Method != http.MethodPost:
		w.WriteHeader(http.StatusMethodNotAllowed)
	default:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		alert, err := ports.ParseAlert(body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(l.accept(alert))
	}
}

// accept remembers and queues an alert in one step, and says what to answer.
// Accepting one type forgets the other type of the same alert and test, so a
// new trigger after a clear is never taken for a repeat.
func (l *Listener) accept(alert ports.Alert) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	// The signal itself counts, not only the flag Run sets after it reads the
	// signal: nothing is accepted once it has been given.
	if l.stopping || (l.stop != nil && stopped(l.stop)) {
		return http.StatusServiceUnavailable
	}
	now := l.now()
	l.memory = slices.DeleteFunc(l.memory, func(m remembered) bool { return now.Sub(m.at) >= memoryTime })
	if slices.ContainsFunc(l.memory, func(m remembered) bool { return m.alert == alert }) {
		return http.StatusOK
	}
	if len(l.waiting) >= queueSize {
		return http.StatusServiceUnavailable
	}
	other := alert
	other.Triggered = !alert.Triggered
	l.memory = slices.DeleteFunc(l.memory, func(m remembered) bool { return m.alert == other })
	l.memory = append(l.memory, remembered{alert, now})
	if len(l.memory) > memorySize {
		l.memory = slices.Delete(l.memory, 0, len(l.memory)-memorySize)
	}
	l.waiting = append(l.waiting, accepted{alert: alert, received: now})
	select {
	case l.wake <- struct{}{}:
	default:
	}
	return http.StatusAccepted
}

// next moves the oldest waiting alert into hand, unless the listener has been
// told to stop.
func (l *Listener) next(stop <-chan struct{}) (accepted, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopping || stopped(stop) || len(l.waiting) == 0 {
		return accepted{}, false
	}
	a := l.waiting[0]
	l.waiting = l.waiting[1:]
	l.inHand = &a.alert
	return a, true
}

func stopped(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

// drain works the queue, one alert at a time, until stop is closed.
func (l *Listener) drain(ctx context.Context, stop <-chan struct{}) {
	for !stopped(stop) {
		if a, ok := l.next(stop); ok {
			l.work(ctx, a.alert, a.received)
			if ctx.Err() == nil {
				l.mu.Lock()
				l.inHand = nil
				l.mu.Unlock()
			}
			continue
		}
		select {
		case <-stop:
		case <-l.wake:
		}
	}
}

// Server is the HTTP server of the listener, with its limits on slow clients.
// Nothing it would log is kept: a request is not for the log.
func (l *Listener) Server() *http.Server {
	return &http.Server{
		Handler:           l,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
		// Otherwise Go answers OPTIONS * itself, without the token.
		DisableGeneralOptionsHandler: true,
		// Plain HTTP/1 only: there is no TLS here to negotiate anything with.
		Protocols: http1Only(),
	}
}

func http1Only() *http.Protocols {
	p := new(http.Protocols)
	p.SetHTTP1(true)
	return p
}

// Run serves on ln and works the queue until stop is closed. It then accepts
// nothing new, gives the work in hand at most grace before cancelling it, and
// returns the alerts it accepted and did not answer.
func (l *Listener) Run(server *http.Server, ln net.Listener, stop <-chan struct{}, grace time.Duration) ([]ports.Alert, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		l.drain(ctx, quit)
	}()
	l.mu.Lock()
	l.stop = stop
	l.mu.Unlock()
	failed := make(chan error, 1)
	go func() { failed <- server.Serve(ln) }()

	var serveErr error
	select {
	case <-stop:
	case serveErr = <-failed:
		cancel()
	}
	l.mu.Lock()
	l.stopping = true
	l.mu.Unlock()
	close(quit)
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	// Open requests get a moment, beside the work's grace, and are then closed.
	go func() {
		shutdown, stopShutdown := context.WithTimeout(context.Background(), min(grace, settle))
		defer stopShutdown()
		if server.Shutdown(shutdown) != nil {
			_ = server.Close()
		}
	}()
	select {
	case <-done:
	case <-deadline.C:
		cancel()
		select {
		case <-done:
		case <-time.After(settle):
		}
	}
	return l.unanswered(), serveErr
}

// unanswered lists what was accepted and not answered: the work cut short,
// then what was still waiting.
func (l *Listener) unanswered() []ports.Alert {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []ports.Alert
	if l.inHand != nil {
		out = append(out, *l.inHand)
	}
	for _, a := range l.waiting {
		out = append(out, a.alert)
	}
	return out
}
