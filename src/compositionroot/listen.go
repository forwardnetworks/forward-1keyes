package compositionroot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/forwardnetworks/forward-1keyes/src/adapters/webhook"
	"github.com/forwardnetworks/forward-1keyes/src/domain"
	"github.com/forwardnetworks/forward-1keyes/src/ports"
	"github.com/forwardnetworks/forward-1keyes/src/usecases"
)

// ShutdownGrace is how long a stopping listener lets the alert in work finish.
const ShutdownGrace = 30 * time.Second

// AlertDeadline bounds the work on one alert, so that a service that never
// answers cannot hold the one worker for good.
const AlertDeadline = 5 * time.Minute

// minAlertToken is the shortest bearer token an alert may be protected by.
const minAlertToken = 32

// ListenHooks are what a test needs to drive the listen verb.
type ListenHooks struct {
	Stop    <-chan struct{}                           // closing it stops the listener; nil: interrupt or terminate does
	Serving func(address string, server *http.Server) // called once the listener accepts requests
	Grace   time.Duration                             // zero means ShutdownGrace
	// Deadline bounds the work on one alert; zero means AlertDeadline.
	Deadline time.Duration
}

// redacting removes the secrets from everything written through it.
type redacting struct {
	mu      *sync.Mutex
	w       io.Writer
	secrets []string
}

func (r redacting) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := io.WriteString(r.w, ports.Redact(string(p), r.secrets)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// listenConfig checks the address and the environment, all before anything is
// built, read or listened on.
func (r Runner) listenConfig(o options) (Config, string, error) {
	address, err := ports.ValidateListenAddress(o.address)
	if err != nil {
		return Config{}, "", err
	}
	token := r.Env("ALERT_TOKEN")
	if len(token) < minAlertToken {
		return Config{}, "", fmt.Errorf("ALERT_TOKEN must be set (at least %d characters)", minAlertToken)
	}
	// ThousandEyes stores the token, so it must open nothing else.
	for _, other := range []string{"TE_API_TOKEN", "FWD_PASS", "NOTIFY_SECRET", "NOTIFY_URL"} {
		if token == r.Env(other) {
			return Config{}, "", fmt.Errorf("ALERT_TOKEN must differ from %s", other)
		}
	}
	notifyURL, err := ports.ValidateNotifyURL(r.Env("NOTIFY_URL"))
	if err != nil {
		return Config{}, "", fmt.Errorf("listen needs NOTIFY_URL: %w", err)
	}
	cfg, err := r.config("listen", true, o)
	if err != nil {
		return Config{}, "", err
	}
	cfg.NotifyURL, cfg.NotifySecret, cfg.AlertToken = notifyURL, r.Env("NOTIFY_SECRET"), token
	return cfg, address, nil
}

func (r Runner) listen(o options) (int, error) {
	ignored, err := ignoredSubnets(o)
	if err != nil {
		return 0, err
	}
	if o.window < time.Second {
		return 0, usageError{"--window must be at least one second"}
	}
	cfg, address, err := r.listenConfig(o)
	if err != nil {
		return 0, err
	}
	secrets := cfg.secrets()
	mu := &sync.Mutex{}
	out, errs := redacting{mu, r.IO.Out, secrets}, redacting{mu, r.IO.Err, secrets}
	deps, err := r.Build(cfg)
	if err != nil {
		return 0, err
	}
	params := usecases.ReportParams{
		Window: o.window,
		Plan: usecases.PlanParams{
			AccountGroup: o.group, NetworkName: o.network, MaxFlows: 500, Persistent: true,
			UpdateStrategy: "U1", ToolVersion: ToolVersion, ResolverSetting: cfg.ResolverSetting, IgnoreAgentSubnets: ignored,
			Selection: domain.Selection{TestIDs: o.testIDs, IncludeLabels: o.includeLabels, ExcludeLabels: o.excludeLabels, IncludeNames: o.includeNames, ExcludeNames: o.excludeNames},
		},
	}
	deadline := r.Listen.Deadline
	if deadline == 0 {
		deadline = AlertDeadline
	}
	answer := func(ctx context.Context, alert ports.Alert, received time.Time) {
		// Running out of time is a failure like any other, and is announced:
		// only the parent's end means the listener is stopping.
		working, release := context.WithTimeout(ctx, deadline)
		defer release()
		a, err := usecases.AnswerAlert(working, usecases.PlanDeps{TE: deps.TE, Forward: deps.Reader, Resolver: deps.Resolver}, params, alert, received)
		if ctx.Err() != nil {
			return // stopping: the alert is listed as unanswered
		}
		if err != nil {
			fmt.Fprintf(errs, "error: alert %s for test %s: %v\n", alert.ID, alert.TestID, err)
			a = domain.UnansweredAlert(alert, received)
		}
		fmt.Fprintf(out, "\n%s\n", a.Text)
		body, err := json.Marshal(a)
		if err == nil {
			err = deps.Notifier.Notify(ctx, body)
		}
		if err != nil {
			fmt.Fprintf(errs, "error: the answer to alert %s was not delivered: %v\n", alert.ID, err)
		}
	}
	return r.serve(address, webhook.New(cfg.AlertToken, r.now, answer), out)
}

// serve listens on a loopback address until told to stop.
func (r Runner) serve(address string, listener *webhook.Listener, out io.Writer) (int, error) {
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return 0, fmt.Errorf("cannot listen: %w", err)
	}
	stop := r.Listen.Stop
	if stop == nil {
		ctx, release := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer release()
		// After the first signal the next one ends the program at once.
		context.AfterFunc(ctx, release)
		stop = ctx.Done()
	}
	grace := r.Listen.Grace
	if grace == 0 {
		grace = ShutdownGrace
	}
	server := listener.Server()
	bound := ln.Addr().String()
	fmt.Fprintf(out, "listening on %s for POST %s\n", bound, webhook.AlertPath)
	if r.Listen.Serving != nil {
		r.Listen.Serving(bound, server)
	}
	left, err := listener.Run(server, ln, stop, grace)
	if len(left) > 0 {
		fmt.Fprintln(out, "stopped; accepted and not answered:")
		for _, a := range left {
			fmt.Fprintf(out, "  %s (%s, test %s)\n", a.ID, a.Kind(), a.TestID)
		}
	} else {
		fmt.Fprintln(out, "stopped; every accepted alert was answered")
	}
	if err != nil {
		return 0, fmt.Errorf("serving: %w", err)
	}
	return 0, nil
}
