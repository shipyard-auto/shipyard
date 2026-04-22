// Package crew contains the shipyard CLI subcommands that drive the crew
// addon. The core never imports addons/crew/internal/*: the only bridges are
// the JSON-RPC 2.0 socket of the daemon and the shipyard-crew subprocess
// contract.
package crew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/shipyard-auto/shipyard/internal/app"
	"github.com/shipyard-auto/shipyard/internal/crewctl"
)

// Exit codes produced by the `shipyard crew run` command.
const (
	ExitOK              = 0
	ExitBusinessErr     = 1
	ExitInvalidArgs     = 2
	ExitTimeout         = 60
	ExitVersionMismatch = 70
	ExitInternal        = 99
)

const (
	ExecutionModeOnDemand = "on-demand"
	ExecutionModeService  = "service"
)

const (
	defaultTimeout         = 5 * time.Minute
	socketDialTimeout      = 500 * time.Millisecond
	handshakeTimeout       = 2 * time.Second
	jsonrpcVersion         = "2.0"
	errCodeVersionMismatch = -32010
	subprocessBinary       = "shipyard-crew"
)

// AgentMeta holds the minimal subset of an agent.yaml needed by the CLI to
// route a run invocation. The core deliberately avoids importing the addon's
// domain model to keep the architectural boundary clean.
type AgentMeta struct {
	Name          string
	ExecutionMode string
}

type runOutput struct {
	Text string `json:"text"`
}

// runResult is the envelope returned by the daemon (as a JSON-RPC result)
// and by the subprocess (stdout).
type runResult struct {
	Output     runOutput `json:"output"`
	TraceID    string    `json:"trace_id"`
	Status     string    `json:"status"`
	DurationMs int64     `json:"duration_ms"`
}

// runFlags captures the parsed flags of the run command.
type runFlags struct {
	Input     string
	InputFile string
	Timeout   time.Duration
	JSON      bool
}

// runDeps is the dependency injection struct used to make the command
// testable. All fields are optional — missing ones are filled by
// withDefaults.
type runDeps struct {
	Home        string
	Version     string
	Stdout      io.Writer
	Stderr      io.Writer
	ReadFile    func(string) ([]byte, error)
	LoadAgent   func(dir string) (*AgentMeta, error)
	DialSocket  func(ctx context.Context, path string) (net.Conn, error)
	LookPath    func(string) (string, error)
	MakeCommand func(ctx context.Context, name string, args ...string) *exec.Cmd
	Now         func() time.Time
}

func (d runDeps) withDefaults() runDeps {
	if d.Home == "" {
		if home, err := os.UserHomeDir(); err == nil {
			d.Home = filepath.Join(home, ".shipyard")
		}
	}
	if d.Version == "" {
		d.Version = app.Version
	}
	if d.Stdout == nil {
		d.Stdout = os.Stdout
	}
	if d.Stderr == nil {
		d.Stderr = os.Stderr
	}
	if d.ReadFile == nil {
		d.ReadFile = os.ReadFile
	}
	if d.LoadAgent == nil {
		d.LoadAgent = loadAgentMeta
	}
	if d.DialSocket == nil {
		d.DialSocket = func(ctx context.Context, path string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", path)
		}
	}
	if d.LookPath == nil {
		d.LookPath = exec.LookPath
	}
	if d.MakeCommand == nil {
		d.MakeCommand = exec.CommandContext
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return d
}

// NewRunCmd returns the cobra command for `shipyard crew run <name>`.
func NewRunCmd() *cobra.Command {
	return newRunCmdWith(runDeps{})
}

func newRunCmdWith(deps runDeps) *cobra.Command {
	flags := &runFlags{}
	cmd := &cobra.Command{
		Use:           "run <name>",
		Short:         "Run a crew member once",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			code := Run(cmd.Context(), deps, args[0], *flags)
			if code == ExitOK {
				return nil
			}
			return &ExitError{Code: code}
		},
	}
	cmd.Flags().StringVar(&flags.Input, "input", "", "JSON input inline")
	cmd.Flags().StringVar(&flags.InputFile, "input-file", "", "path to file containing JSON input")
	cmd.Flags().DurationVar(&flags.Timeout, "timeout", defaultTimeout, "total execution timeout")
	cmd.Flags().BoolVar(&flags.JSON, "json", false, "emit final result as JSON")
	cmd.MarkFlagsMutuallyExclusive("input", "input-file")
	return cmd
}

// ExitError carries a CLI exit code back through cobra. Callers that care
// about the numeric code should type-assert or use errors.As.
type ExitError struct {
	Code    int
	Message string
}

func (e *ExitError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("exit code %d", e.Code)
}

// ExitCode implements the convention consumed by shipyard's main.
func (e *ExitError) ExitCode() int { return e.Code }

// Run executes the run command end-to-end and returns the CLI exit code.
// This is the primary test surface; cobra's RunE delegates here.
func Run(ctx context.Context, deps runDeps, name string, flags runFlags) int {
	deps = deps.withDefaults()
	if flags.Timeout <= 0 {
		flags.Timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, flags.Timeout)
	defer cancel()

	inputJSON, err := resolveInput(flags.Input, flags.InputFile, deps.ReadFile)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "shipyard crew run: %s\n", err)
		return ExitInvalidArgs
	}

	agentDir := filepath.Join(deps.Home, "crew", name)
	meta, err := deps.LoadAgent(agentDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(deps.Stderr, "shipyard crew run: crew member %q not found\n", name)
			return ExitInvalidArgs
		}
		fmt.Fprintf(deps.Stderr, "shipyard crew run: crew member %q: %s\n", name, err)
		return ExitInvalidArgs
	}

	startedAt := deps.Now()
	result, code := dispatch(ctx, deps, meta, name, inputJSON, flags.Timeout)
	if result == nil {
		result = &runResult{}
	}
	if result.DurationMs == 0 {
		result.DurationMs = deps.Now().Sub(startedAt).Milliseconds()
	}
	emit(deps.Stdout, deps.Stderr, result, flags.JSON, code)
	return code
}

// dispatch picks the right backend (socket vs subprocess) and executes the
// call. It returns the envelope plus the CLI exit code.
func dispatch(ctx context.Context, deps runDeps, meta *AgentMeta, name string, input []byte, total time.Duration) (*runResult, int) {
	if meta.ExecutionMode == ExecutionModeService {
		sockPath := crewctl.AgentSocketPath(deps.Home, name)
		dialCtx, cancel := context.WithTimeout(ctx, socketDialTimeout)
		client, err := crewctl.Dial(dialCtx, crewctl.Opts{
			SocketPath:       sockPath,
			Version:          deps.Version,
			HandshakeTimeout: handshakeTimeout,
			Dial:             deps.DialSocket,
		})
		cancel()
		if err == nil {
			defer client.Close()
			result, code, callErr := runViaClient(ctx, client, input, total)
			if callErr == nil || code == ExitVersionMismatch {
				if callErr != nil {
					fmt.Fprintf(deps.Stderr, "shipyard crew run: %s\n", callErr)
				}
				return result, code
			}
			fmt.Fprintf(deps.Stderr, "shipyard crew run: daemon call failed: %s\n", callErr)
			return nil, ExitInternal
		}
		// Version mismatch is fatal — the user must upgrade before retrying.
		var vm *crewctl.ErrVersionMismatch
		if errors.As(err, &vm) {
			fmt.Fprintf(deps.Stderr, "shipyard crew run: %s\n", err)
			return nil, ExitVersionMismatch
		}
		// Only fall back to subprocess when the daemon is genuinely
		// unreachable. Other handshake failures (malformed response, protocol
		// error) surface as ExitInternal without a silent fallback.
		if errors.Is(err, crewctl.ErrDaemonNotRunning) {
			fmt.Fprintf(deps.Stderr, "warning: daemon not responding, falling back to on-demand execution\n")
			return callViaSubprocess(ctx, deps, name, input, total)
		}
		fmt.Fprintf(deps.Stderr, "shipyard crew run: %s\n", err)
		return nil, ExitInternal
	}
	return callViaSubprocess(ctx, deps, name, input, total)
}

// runViaClient invokes the "run" method on a connected crewctl.Client and
// maps the typed response into the CLI's local envelope plus exit code.
func runViaClient(ctx context.Context, client *crewctl.Client, input []byte, total time.Duration) (*runResult, int, error) {
	res, err := client.Run(ctx, json.RawMessage(input), total)
	if err != nil {
		var rpcErr *crewctl.RPCError
		if errors.As(err, &rpcErr) {
			if rpcErr.Code == crewctl.ErrCodeVersionMismatch {
				return nil, ExitVersionMismatch, errors.New("version mismatch between shipyard and shipyard-crew daemon")
			}
			// App-specific errors from the daemon (e.g. runner failure) map
			// to the business-error exit code; message is echoed on stdout
			// via the runResult.
			if rpcErr.Code == crewctl.ErrCodeAppSpecific {
				return &runResult{Status: "err", Output: runOutput{Text: rpcErr.Message}}, ExitBusinessErr, nil
			}
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil, ExitTimeout, errors.New("daemon did not respond within --timeout")
		}
		return nil, ExitInternal, err
	}
	code := ExitOK
	if res.Status == "err" {
		code = ExitBusinessErr
	}
	return &runResult{
		Output:  runOutput{Text: res.Text},
		TraceID: res.TraceID,
		Status:  res.Status,
	}, code, nil
}

// resolveInput normalises the --input / --input-file pair to a JSON byte
// slice. Returns "{}" when neither is set.
func resolveInput(inline, path string, readFile func(string) ([]byte, error)) ([]byte, error) {
	if inline != "" && path != "" {
		return nil, errors.New("--input and --input-file are mutually exclusive")
	}
	var raw []byte
	switch {
	case inline != "":
		raw = []byte(inline)
	case path != "":
		data, err := readFile(path)
		if err != nil {
			return nil, fmt.Errorf("read --input-file: %w", err)
		}
		raw = data
	default:
		return []byte("{}"), nil
	}
	if !json.Valid(raw) {
		return nil, errors.New("input is not valid JSON")
	}
	return raw, nil
}

// callViaSubprocess runs the shipyard-crew binary as a one-shot worker and
// maps its stdout envelope into a runResult.
func callViaSubprocess(ctx context.Context, deps runDeps, name string, input []byte, total time.Duration) (*runResult, int) {
	bin, err := deps.LookPath(subprocessBinary)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "shipyard crew run: %s not found in PATH; run 'shipyard crew install'\n", subprocessBinary)
		return nil, ExitInternal
	}
	cmdCtx, cancel := context.WithTimeout(ctx, total)
	defer cancel()

	cmd := deps.MakeCommand(cmdCtx, bin, "--agent", name)
	cmd.Stdin = strings.NewReader(string(input))
	stdout := &strings.Builder{}
	stderr := &strings.Builder{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err = cmd.Run()
	if cmdCtx.Err() == context.DeadlineExceeded {
		return nil, ExitTimeout
	}

	raw := strings.TrimSpace(stdout.String())
	var result runResult
	parsed := false
	if raw != "" {
		if jerr := json.Unmarshal([]byte(raw), &result); jerr == nil {
			parsed = true
		}
	}
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			fmt.Fprintf(deps.Stderr, "shipyard crew run: subprocess failed: %s\n", err)
			return nil, ExitInternal
		}
	}

	if !parsed {
		result = runResult{Output: runOutput{Text: raw}}
	}
	if stderr.Len() > 0 {
		_, _ = deps.Stderr.Write([]byte(stderr.String()))
	}

	if exitCode == 0 {
		if result.Status == "" {
			result.Status = "ok"
		}
		return &result, ExitOK
	}
	if exitCode == 1 {
		if result.Status == "" {
			result.Status = "err"
		}
		return &result, ExitBusinessErr
	}
	return &result, ExitInternal
}

// emit writes the final envelope to stdout and the one-line summary to stderr.
func emit(stdout, stderr io.Writer, result *runResult, asJSON bool, code int) {
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
	} else if result.Output.Text != "" {
		fmt.Fprint(stdout, result.Output.Text)
		if !strings.HasSuffix(result.Output.Text, "\n") {
			fmt.Fprintln(stdout)
		}
	}
	status := statusFor(result, code)
	fmt.Fprintf(stderr, "trace_id=%s status=%s duration=%dms\n", result.TraceID, status, result.DurationMs)
}

func statusFor(result *runResult, code int) string {
	if result.Status != "" {
		return result.Status
	}
	if code == ExitOK {
		return "ok"
	}
	return "err"
}

// loadAgentMeta reads the minimal subset of agent.yaml needed by the CLI to
// route a run invocation. It does NOT validate the agent — full validation
// is the responsibility of the addon. Parsing is tolerant: unknown fields
// are ignored.
func loadAgentMeta(dir string) (*AgentMeta, error) {
	yamlPath := filepath.Join(dir, "agent.yaml")
	raw, err := os.ReadFile(yamlPath)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Name      string `yaml:"name"`
		Execution struct {
			Mode string `yaml:"mode"`
		} `yaml:"execution"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse agent.yaml: %w", err)
	}
	if doc.Name == "" {
		return nil, errors.New("agent.yaml: missing name")
	}
	if doc.Execution.Mode != ExecutionModeOnDemand && doc.Execution.Mode != ExecutionModeService {
		return nil, fmt.Errorf("agent.yaml: invalid execution.mode %q", doc.Execution.Mode)
	}
	return &AgentMeta{Name: doc.Name, ExecutionMode: doc.Execution.Mode}, nil
}

// ── JSON-RPC 2.0 wire types (kept for the package's in-process test daemon) ─

// rpcError mirrors crewctl.RPCError but is kept in this package so the test
// daemon can wire responses without pulling the crewctl import into tests.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}
