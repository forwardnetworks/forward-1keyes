// Package compositionroot wires the tool: it parses the command line, reads
// the environment, builds the adapters and runs the use cases. It is the only
// package that imports adapters.
package compositionroot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/forwardnetworks/forward-1keyes/src/domain"
	"github.com/forwardnetworks/forward-1keyes/src/ports"
	"github.com/forwardnetworks/forward-1keyes/src/usecases"
)

// IO is where output goes.
type IO struct{ Out, Err io.Writer }

// FS is the files the tool reads and writes: the plan file, and the results.
type FS interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte) error
}

// Config is what the adapters are built from. It is read from the environment,
// never from flags.
type Config struct {
	Verb            string // "plan", "apply" or "status"
	ToolVersion     string
	NeedTE          bool // false for status and apply: no ThousandEyes token is read
	TEToken         string
	ForwardURL      string
	ForwardUser     string
	ForwardPass     string
	ResolverSetting string // "system" or "server:<ip:port>"
	Debug           bool
	DebugBodies     bool
}

// Deps are the ports the use cases run on.
type Deps struct {
	TE       ports.TeClient
	Reader   ports.ForwardReader
	Writer   ports.ForwardClient
	Resolver ports.Resolver
}

// Build makes the adapters for a Config.
type Build func(cfg Config) (Deps, error)

// ToolVersion is the version written into every plan.
const ToolVersion = "0.1.0"

const defaultPlanFile = "forward-1keyes.plan.json"

// Runner runs one command line.
type Runner struct {
	Env   func(string) string
	IO    IO
	FS    FS
	Now   func() time.Time
	Build Build
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// options holds the flags of every verb; each verb defines the ones it takes.
type options struct {
	group, network, out, resolver, planFile, digest, results, snapshot string
	maxFlows, batchSize                                                int
	window                                                             time.Duration
	// Each verb's defaults are written here when its flags are registered, so
	// a flag whose default differs between verbs needs its own field.
	reportOut                                                         string
	nonPersistent, asJSON, debug, debugBodies                         bool
	testIDs, includeLabels, excludeLabels, includeNames, excludeNames []string
	ignoreAgentSubnets                                                []string
}

func addFlags(verb string, fs *pflag.FlagSet, o *options) {
	debug := func() {
		fs.BoolVar(&o.debug, "debug", false, "log method, path, status and duration of each call")
		fs.BoolVar(&o.debugBodies, "debug-bodies", false, "also log request and response bodies, redacted")
	}
	selection := func() {
		fs.StringVar(&o.resolver, "resolver", "", "DNS server as ip:port (default: the system resolver)")
		fs.StringArrayVar(&o.testIDs, "test-id", nil, "select only this test ID (repeatable)")
		fs.StringArrayVar(&o.includeLabels, "include-label", nil, "select only tests with this label (repeatable)")
		fs.StringArrayVar(&o.excludeLabels, "exclude-label", nil, "skip tests with this label (repeatable)")
		fs.StringArrayVar(&o.includeNames, "include-name", nil, "select only tests whose name matches this regexp (repeatable)")
		fs.StringArrayVar(&o.excludeNames, "exclude-name", nil, "skip tests whose name matches this regexp (repeatable)")
		fs.StringArrayVar(&o.ignoreAgentSubnets, "ignore-agent-subnet", nil, "ignore agent addresses in this CIDR, for example a management subnet (repeatable)")
	}
	switch verb {
	case "report":
		fs.StringVar(&o.group, "te-account-group", "", "ThousandEyes account group name (required)")
		fs.StringVar(&o.network, "network", "", "Forward network name (required)")
		fs.DurationVar(&o.window, "window", time.Hour, "how far back to read measurements")
		fs.StringVar(&o.reportOut, "out", "", "also write the report to this file as JSON")
		fs.BoolVar(&o.asJSON, "json", false, "print JSON instead of a table")
		selection()
		debug()
	case "plan":
		fs.StringVar(&o.group, "te-account-group", "", "ThousandEyes account group name (required)")
		fs.StringVar(&o.network, "network", "", "Forward network name (required)")
		fs.StringVar(&o.out, "out", defaultPlanFile, "where to write the plan file")
		fs.BoolVar(&o.asJSON, "json", false, "print the plan file instead of a table")
		fs.IntVar(&o.maxFlows, "max-flows", 500, "flow cap")
		fs.BoolVar(&o.nonPersistent, "non-persistent", false, "plan non-persistent checks (this snapshot only)")
		selection()
		debug()
	case "apply":
		fs.StringVar(&o.planFile, "plan", "", "the plan file (required)")
		fs.StringVar(&o.digest, "digest", "", "the digest you approved, sha256:<hex> (required)")
		fs.IntVar(&o.batchSize, "batch-size", 25, "rows written per batch")
		fs.StringVar(&o.results, "results", "", "also write the results to this file as JSON")
		fs.BoolVar(&o.asJSON, "json", false, "print the results as JSON")
		debug()
	case "status":
		fs.StringVar(&o.network, "network", "", "Forward network name (required)")
		fs.StringVar(&o.snapshot, "snapshot", "", "snapshot ID (default: the latest)")
		fs.BoolVar(&o.asJSON, "json", false, "print JSON instead of a table")
		debug()
	}
}

// FlagNames lists the flags of a verb, for tests that check none takes a secret.
func FlagNames(verb string) []string {
	var o options
	fs := pflag.NewFlagSet(verb, pflag.ContinueOnError)
	addFlags(verb, fs, &o)
	var names []string
	fs.VisitAll(func(f *pflag.Flag) { names = append(names, f.Name) })
	return names
}

var verbHelp = map[string]string{
	"report": "compare what ThousandEyes measured with what Forward concluded; reads only",
	"plan":   "read ThousandEyes and Forward and write a plan; changes nothing",
	"apply":  "write an approved plan to Forward, after checking its digest and the world",
	"status": "list the checks this tool manages and flag bad tuples; reads only Forward",
}

// command builds the cobra tree. The exit code of the verb that ran goes to *code.
func (r Runner) command(code *int) *cobra.Command {
	flagErr := func(_ *cobra.Command, err error) error { return usageError{err.Error()} }
	root := &cobra.Command{
		Use:           "forward-1keyes",
		Short:         "turn ThousandEyes tests into Forward intent checks",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(*cobra.Command, []string) error {
			return usageError{"a verb is required: plan, apply, status or report"}
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetOut(r.IO.Out)
	root.SetErr(r.IO.Err)
	root.SetFlagErrorFunc(flagErr)
	var o options
	for _, verb := range []string{"plan", "apply", "status", "report"} {
		verb := verb
		sub := &cobra.Command{
			Use:   verb,
			Short: verbHelp[verb],
			Args:  cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				c, err := r.exec(verb, o)
				*code = c
				return err
			},
		}
		sub.SetFlagErrorFunc(flagErr)
		addFlags(verb, sub.Flags(), &o)
		root.AddCommand(sub)
	}
	return root
}

// Run runs a command line (without the program name) and returns the exit code:
// 1 for a usage error or any other error, otherwise the verb's own code.
func (r Runner) Run(args []string) int {
	if args == nil {
		args = []string{}
	}
	code := 0
	root := r.command(&code)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		var u usageError
		if errors.As(err, &u) {
			fmt.Fprintf(r.IO.Err, "usage error: %s\nusage: forward-1keyes plan|apply|status|report [flags]  (--help lists the flags)\n", u.msg)
		} else {
			fmt.Fprintf(r.IO.Err, "error: %v\n", err)
		}
		return 1
	}
	return code
}

func (r Runner) exec(verb string, o options) (int, error) {
	switch verb {
	case "plan":
		if o.group == "" || o.network == "" {
			return 0, usageError{"plan needs --te-account-group and --network"}
		}
		return r.plan(o)
	case "apply":
		if o.planFile == "" || o.digest == "" {
			return 0, usageError{"apply needs --plan and --digest"}
		}
		return r.apply(o)
	case "report":
		if o.group == "" || o.network == "" {
			return 0, usageError{"report needs --te-account-group and --network"}
		}
		return r.report(o)
	default:
		if o.network == "" {
			return 0, usageError{"status needs --network"}
		}
		return r.status(o)
	}
}

// config reads the environment. Secrets come only from here, never from flags.
func (r Runner) config(verb string, needTE bool, o options) (Config, error) {
	cfg := Config{Verb: verb, ToolVersion: ToolVersion, NeedTE: needTE, Debug: o.debug, DebugBodies: o.debugBodies, ResolverSetting: "system"}
	url, err := ports.ValidateForwardURL(r.Env("FWD_URL"))
	if err != nil {
		return cfg, fmt.Errorf("FWD_URL: %w", err)
	}
	cfg.ForwardURL, cfg.ForwardUser, cfg.ForwardPass = url, r.Env("FWD_USER"), r.Env("FWD_PASS")
	if cfg.ForwardUser == "" || len(cfg.ForwardPass) < ports.MinSecretLength {
		return cfg, fmt.Errorf("FWD_USER and FWD_PASS must be set (the password at least %d characters)", ports.MinSecretLength)
	}
	if needTE {
		if cfg.TEToken = r.Env("TE_API_TOKEN"); len(cfg.TEToken) < ports.MinSecretLength {
			return cfg, fmt.Errorf("TE_API_TOKEN must be set (at least %d characters)", ports.MinSecretLength)
		}
	}
	if o.resolver != "" {
		cfg.ResolverSetting = "server:" + o.resolver
	}
	return cfg, nil
}

func (r Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r Runner) plan(o options) (int, error) {
	// Checked before anything is built or read.
	ignored, err := ignoredSubnets(o)
	if err != nil {
		return 0, err
	}
	cfg, err := r.config("plan", true, o)
	if err != nil {
		return 0, err
	}
	deps, err := r.Build(cfg)
	if err != nil {
		return 0, err
	}
	body, err := usecases.Plan(context.Background(), usecases.PlanDeps{TE: deps.TE, Forward: deps.Reader, Resolver: deps.Resolver}, usecases.PlanParams{
		AccountGroup: o.group, NetworkName: o.network, MaxFlows: o.maxFlows, Persistent: !o.nonPersistent,
		UpdateStrategy: "U1", ToolVersion: ToolVersion, ResolverSetting: cfg.ResolverSetting,
		IgnoreAgentSubnets: ignored,
		Selection:          domain.Selection{TestIDs: o.testIDs, IncludeLabels: o.includeLabels, ExcludeLabels: o.excludeLabels, IncludeNames: o.includeNames, ExcludeNames: o.excludeNames},
	})
	if err != nil {
		return 0, err
	}
	raw, err := domain.MarshalPlan(body, domain.Advisory{GeneratedAt: r.now().UTC().Format(time.RFC3339)})
	if err != nil {
		return 0, err
	}
	if err := r.FS.WriteFile(o.out, raw); err != nil {
		return 0, fmt.Errorf("writing the plan: %w", err)
	}
	digest, err := domain.PlanDigest(body)
	if err != nil {
		return 0, err
	}
	if o.asJSON {
		_, _ = r.IO.Out.Write(raw)
		return body.ExitCode(), nil
	}
	printPlan(r.IO.Out, body, o.out, digest)
	return body.ExitCode(), nil
}

// ignoredSubnets reads the --ignore-agent-subnet values.
func ignoredSubnets(o options) ([]netip.Prefix, error) {
	var ignored []netip.Prefix
	for _, v := range o.ignoreAgentSubnets {
		p, err := netip.ParsePrefix(v)
		if err != nil {
			return nil, fmt.Errorf("--ignore-agent-subnet %q is not a CIDR prefix such as 172.28.28.0/24", v)
		}
		ignored = append(ignored, p)
	}
	return ignored, nil
}

func (r Runner) report(o options) (int, error) {
	ignored, err := ignoredSubnets(o)
	if err != nil {
		return 0, err
	}
	if o.window < time.Second {
		return 0, usageError{"--window must be at least one second"}
	}
	cfg, err := r.config("report", true, o)
	if err != nil {
		return 0, err
	}
	deps, err := r.Build(cfg)
	if err != nil {
		return 0, err
	}
	rep, err := usecases.Report(context.Background(), usecases.PlanDeps{TE: deps.TE, Forward: deps.Reader, Resolver: deps.Resolver}, usecases.ReportParams{
		Window: o.window,
		Plan: usecases.PlanParams{
			AccountGroup: o.group, NetworkName: o.network, MaxFlows: 500, Persistent: true,
			UpdateStrategy: "U1", ToolVersion: ToolVersion, ResolverSetting: cfg.ResolverSetting, IgnoreAgentSubnets: ignored,
			Selection: domain.Selection{TestIDs: o.testIDs, IncludeLabels: o.includeLabels, ExcludeLabels: o.excludeLabels, IncludeNames: o.includeNames, ExcludeNames: o.excludeNames},
		},
	})
	if err != nil {
		return 0, err
	}
	raw, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return 0, err
	}
	if o.reportOut != "" {
		if err := r.FS.WriteFile(o.reportOut, append(raw, '\n')); err != nil {
			return 0, fmt.Errorf("writing the report: %w", err)
		}
	}
	if o.asJSON {
		fmt.Fprintln(r.IO.Out, string(raw))
	} else {
		printReport(r.IO.Out, rep)
	}
	return rep.ExitCode(), nil
}

func printReport(w io.Writer, rep domain.Report) {
	fmt.Fprintf(w, "report on snapshot %s: measured by ThousandEyes beside modelled by Forward\n", rep.SnapshotID)
	for _, row := range rep.Rows {
		if !row.Compared {
			fmt.Fprintf(w, "\nnot compared  %s\n    %s\n", row.Name, row.Reason)
			continue
		}
		note := ""
		if row.DiffersFromPlan {
			note = "  (the check differs from what plan would write)"
		}
		fmt.Fprintf(w, "\n%s  %s%s\n    measured %s, Forward %s (snapshot processed %s)\n", row.Verdict, row.Name, note, row.Measured, row.ForwardStatus, row.SnapshotProcessedAt)
		for _, m := range row.Records {
			fmt.Fprintf(w, "      %s  %s  agent %s, test %s, to %s\n", m.MeasuredAt.Format(time.RFC3339), m.Reading, m.AgentID, m.TestID, m.ServerIP)
		}
		if row.OlderThanSnapshot {
			fmt.Fprintln(w, "    a measurement is older than the snapshot: the network may have changed between them")
		}
		fmt.Fprintf(w, "    look first: %s\n", row.Guidance)
	}
}

func printPlan(w io.Writer, b domain.PlanBody, out, digest string) {
	fmt.Fprintf(w, "plan for %s (aid %s) on %s, snapshot %s: written to %s\n", b.TE.AccountGroupName, b.TE.AID, b.Forward.NetworkName, b.Forward.SnapshotID, out)
	c := b.Counts
	fmt.Fprintf(w, "create %d  update %d  unchanged %d  stale %d  collision %d  not importable %d  (cap %d, appliable %v)\n",
		c.Create, c.Update, c.Unchanged, c.Stale, c.Collision, c.NotImportable, b.MaxFlows, b.Appliable)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "OUTCOME\tKEY\tNAME\tDETAIL")
	for _, row := range b.Rows {
		key := strings.TrimPrefix(row.SourceKey, "sha256:")
		if len(key) > 12 {
			key = key[:12]
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", row.Outcome, key, row.Name, row.Reason)
	}
	_ = tw.Flush()
	fmt.Fprintf(w, "digest %s\n", digest)
}

func (r Runner) apply(o options) (int, error) {
	cfg, err := r.config("apply", true, o)
	if err != nil {
		return 0, err
	}
	raw, err := r.FS.ReadFile(o.planFile)
	if err != nil {
		return 0, fmt.Errorf("reading the plan: %w", err)
	}
	deps, err := r.Build(cfg)
	if err != nil {
		return 0, err
	}
	rep := usecases.ApplyWithTE(context.Background(), usecases.ApplyDeps{Forward: deps.Writer, TE: deps.TE}, raw, usecases.ApplyOptions{Digest: o.digest, ToolVersion: ToolVersion, BatchSize: o.batchSize})
	out, _ := json.MarshalIndent(rep, "", "  ")
	if o.results != "" {
		if err := r.FS.WriteFile(o.results, append(out, '\n')); err != nil {
			return 0, fmt.Errorf("writing the results: %w", err)
		}
	}
	if o.asJSON {
		fmt.Fprintln(r.IO.Out, string(out))
		return rep.ExitCode, nil
	}
	if rep.Refusal != "" {
		fmt.Fprintf(r.IO.Out, "refused: %s\n", rep.Refusal)
	}
	for _, w := range rep.Warnings {
		fmt.Fprintf(r.IO.Out, "warning: %s\n", w)
	}
	for _, d := range rep.Differences {
		fmt.Fprintf(r.IO.Out, "drift: %s %s %s: planned %q, now %q\n", d.Kind, d.ID, d.Field, d.Planned, d.Now)
	}
	for _, res := range rep.Results {
		fmt.Fprintf(r.IO.Out, "%s  %s  %s %s\n", res.Status, res.Name, strings.Join(res.CheckIDs, ","), res.Reason)
	}
	for key, ids := range rep.Duplicates {
		slices.Sort(ids)
		fmt.Fprintf(r.IO.Out, "duplicate after apply: %s held by %s\n", key, strings.Join(ids, ", "))
	}
	return rep.ExitCode, nil
}

func (r Runner) status(o options) (int, error) {
	cfg, err := r.config("status", false, o)
	if err != nil {
		return 0, err
	}
	deps, err := r.Build(cfg)
	if err != nil {
		return 0, err
	}
	rep, err := usecases.Status(context.Background(), deps.Reader, o.network, o.snapshot)
	if err != nil {
		return 0, err
	}
	if o.asJSON {
		out, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Fprintln(r.IO.Out, string(out))
	} else {
		fmt.Fprintf(r.IO.Out, "managed checks on %s, snapshot %s\n", rep.Network, rep.Snapshot)
		tw := tabwriter.NewWriter(r.IO.Out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tACTIVE\tINSTANCE\tKEY\tNAME\tPROBLEM")
		for _, c := range rep.Checks {
			key := strings.TrimPrefix(c.SourceKey, "sha256:")
			if len(key) > 12 {
				key = key[:12]
			}
			fmt.Fprintf(tw, "%s\t%v\t%s\t%s\t%s\t%s\n", c.ID, c.Active, c.SourceInstance, key, c.Name, c.Problem)
		}
		_ = tw.Flush()
	}
	if rep.Problems() > 0 {
		return 3, nil
	}
	return 0, nil
}
