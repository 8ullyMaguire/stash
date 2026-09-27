// Package rpc serves the Stash plugin RPC interface.
//
// # THE CONTRACT, AS THE HOST ACTUALLY IMPLEMENTS IT
//
// The host is `rpcPluginTask` in pkg/plugin/rpc.go, and reading it rather than
// assuming is the only way to get this right — three of the details below are
// documented nowhere else, and each one is a plugin that starts and then does
// nothing:
//
//  1. It calls `RPCRunner.Run` IMMEDIATELY on Start, asynchronously, and does not
//     wait for it. So Run must BLOCK: a Run that returns straight away produces
//     a plugin the host has already finished with, and `t.done` closes with an
//     empty result.
//  2. The codec is jsonrpc over the plugin's stdin/stdout, and the host reads
//     the plugin's STDERR as its log stream. So stdout belongs to the protocol
//     alone — a stray Println corrupts the stream, and the failure surfaces as a
//     JSON parse error in the HOST, nowhere in this process.
//  3. `Stop` is a separate call on the same connection, and the host does not
//     wait for it. It has to unblock Run, or a stopped downloader keeps running
//     until the host kills the process.
//
// # WHY THE ARGUMENT TYPES ARE REDECLARED HERE
//
// The types in pkg/plugin/common are not imported. The plugin is a separate Go
// module, and importing the core to share three struct definitions would defeat
// the entire milestone: the core's go.mod would gain a require, and a plugin
// that needs the core in order to build is core code.
//
// The wire format is therefore the interface, and these declarations ARE that
// interface. `TestWireFormatMatchesTheHost` checks them against the host's by
// decoding a real `common.PluginInput` and re-encoding it here, so a field the
// host renames fails a test rather than silently arriving as a zero value.
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/rpc"
	"net/rpc/jsonrpc"
	"sync"
)

// serviceName is what the host dials: `t.client.Call("RPCRunner.Run", ...)`.
//
// It is a CONSTANT rather than the receiver's type name because net/rpc derives
// the name from the type, and a type named `Runner` registers as "Runner" — so
// every call from the host fails with "can't find service RPCRunner", after the
// plugin has started successfully and looks fine from the outside. Registering
// with RegisterName is what makes the two independent.
const serviceName = "RPCRunner"

// ArgsMap is the host's argument map: whatever JSON the operation's inputs
// decoded to, so the value types are whatever the schema produced.
type ArgsMap map[string]interface{}

// StashServerConnection is how a plugin reaches the host's API.
type StashServerConnection struct {
	Scheme string
	Host   string
	Port   int

	// Cookie is the session cookie for authenticating to the host. It is a
	// *http.Cookie on the wire, and the distinction matters on the way back
	// out: a request built with http.Cookie{Name, Value} and nothing else
	// serialises to exactly what the host expects.
	SessionCookie *http.Cookie

	// Dir is the directory containing the stash configuration file.
	Dir string

	// PluginDir is the directory containing this plugin's configuration.
	PluginDir string
}

// PluginInput is the host's Run argument.
type PluginInput struct {
	// proposer is the consent gate, injected by tests. Unexported, and
	// therefore not part of the wire format: a test hook that were exported
	// would be a field the host could set, which is a gate a plugin could
	// override -- the exact property 7.1 denies it.
	proposer Proposer

	ServerConnection StashServerConnection `json:"server_connection"`
	Args             ArgsMap               `json:"args"`
}

// PluginOutput is the host's Run reply.
//
// Error is a *string rather than an error because that is what the host
// unmarshals, and a failed task is reported by a non-nil Error with a NIL Go
// error: the RPC call itself succeeded and the operation inside it did not.
// Getting this backwards — returning the error as the second value — makes the
// host record a protocol failure with no message, which reads as a crash.
type PluginOutput struct {
	Error  *string     `json:"error"`
	Output interface{} `json:"output"`
}

// SetError records a failure the way the host reads it.
func (o *PluginOutput) SetError(err error) {
	if err == nil {
		o.Error = nil
		return
	}
	s := err.Error()
	o.Error = &s
}

// Runner is the receiver the host looks for.
//
// It is registered by NAME (serviceName) and not by type, because the host's
// dial string is a constant in a different module and the two must not drift.
type Runner struct {
	// mu guards cancel and cause. Stop is called from the host's goroutine
	// while Run is blocked in the transfer, so without it this is a data race
	// that Go's race detector reports and a production build reports as
	// whatever it likes.
	mu     sync.Mutex
	cancel context.CancelFunc

	// cause is why Run was cancelled. A context cause rather than a bare flag,
	// because "the operator pressed Stop" and "the locator was refused" are
	// different things to write in a task list, and a download that ends
	// because someone stopped it must not read like a policy failure.
	cause error

	// gate overrides the consent gate for this Runner.
	//
	// ON THE RUNNER AND NOT ONLY ON THE INPUT, because the input crosses the
	// wire: an unexported field is invisible to json, so a PluginInput carrying
	// a test proposer arrives at the server as a PluginInput without one. A test
	// that injects a gate into the input it SENDS would then be testing a run
	// that refuses immediately -- which returns fast, and so silently satisfies
	// any assertion that Run merely has to return.
	//
	// Nil in production, and nil means "ask core over the host API", so this
	// field cannot weaken the gate: leaving it unset is the real path.
	gate Proposer
}

// NewRunner returns a Runner with its fields initialised.
//
// Constructed rather than zero-valued so a Runner used directly in a test is in
// the same state as one Serve built — the zero value works, and only because
// every method tolerates a nil cancel, which is a subtlety a test should not
// have to rediscover.
func NewRunner() *Runner {
	return &Runner{cause: errStopped}
}

// errStopped is the default cancellation cause: stopped before anything
// happened, which is what a Stop before Run means.
var errStopped = errors.New("stopped by the host")

// Run performs the download. It BLOCKS until the transfer finishes or Stop is
// called — see the package comment, point 1.
func (r *Runner) Run(input PluginInput, output *PluginOutput) error {
	ctx, cancel := context.WithCancel(context.Background())
	// Always released, on every path. A Run that returns without cancelling
	// leaks a context, and a leak in a process the host expects to be
	// restartable is a timer that outlives the task that created it.
	defer cancel()

	r.mu.Lock()
	r.cancel = cancel
	cause := r.cause
	r.mu.Unlock()

	result, err := downloadWithGate(ctx, input, r.gate)
	if err == nil {
		output.Output = result
		return nil
	}

	// Whether the run was cancelled decides which error the operator sees.
	// Reporting "context canceled" for a Stop is indistinguishable from a
	// network failure in the host's task list, and "did it fail or did I stop
	// it" is the operator's next question every time.
	if ctx.Err() != nil {
		output.SetError(cause)
		return nil
	}
	output.SetError(err)
	return nil
}

// StopReply is the body of a Stop acknowledgement.
//
// A CONCRETE type, and this is not a style choice. The host calls
// `t.client.Call("RPCRunner.Stop", nil, &resp)` with `var resp interface{}`, so
// the reply parameter is a `*interface{}` — and a method whose reply argument is
// `*interface{}` cannot be called over jsonrpc at all. It returns
// `invalid error <nil>`: the codec writes a reply body of `null`, the client
// tries to decode it into a pointer to an empty interface, and fails with a
// message that names neither the plugin nor the method.
//
// Measured, with a method of each reply type behind an otherwise identical
// server:
//
//	Stop(_ interface{}, _ *interface{}) error -> invalid error <nil>
//	Stop(_ interface{}, _ *struct{})    error -> ok
//	Stop(_ interface{}, _ *bool)        error -> ok
//
// So the reply type is chosen for what the DECODER can hold, and `struct{}` is
// the honest choice: a Stop acknowledges and returns nothing. The host discards
// the value into an `interface{}`, which accepts whatever arrives.
type StopReply struct{}

// Stop ends a running Run. The host calls it and does not wait.
func (r *Runner) Stop(_ interface{}, _ *StopReply) error {
	r.mu.Lock()
	cancel := r.cancel
	r.cause = errStopped
	r.cancel = nil
	r.mu.Unlock()

	if cancel == nil {
		// Stop before Run, or twice. Not an error: the host calls Stop on a
		// teardown path where Run may never have started, and refusing it
		// turns a clean shutdown into a reported failure.
		return nil
	}
	cancel()
	return nil
}

// Serve runs the RPC server until the host closes the connection.
//
// The server's lifetime IS the connection's: net/rpc's ServeCodec returns when
// the codec's reader hits EOF, which happens when the host closes the pipe or
// dies. There is no signal to wait for, because the host sends none — so a
// plugin that waited for one would hang forever after the host was gone.
func Serve(in io.Reader, out io.Writer) error {
	server := rpc.NewServer()
	// RegisterName, not Register: see serviceName.
	if err := server.RegisterName(serviceName, NewRunner()); err != nil {
		return fmt.Errorf("registering %s: %w", serviceName, err)
	}

	// jsonrpc over the same two streams the host is reading, because that is
	// the codec it starts the plugin with
	// (pie.StartProviderCodec(jsonrpc.NewClientCodec, ...)). jsonrpc's SERVER
	// codec takes ONE io.ReadWriteCloser rather than a reader and a writer
	// separately, which is why the two pipes are combined here instead of
	// being passed through: the first version tried to hand it a two-field
	// struct, and the compiler's complaint ("missing method Close") is the
	// whole reason this note exists.
	server.ServeCodec(jsonrpc.NewServerCodec(&stdio{in: in, out: out}))
	return nil
}

// stdio presents the plugin's stdin and stdout as the single ReadWriteCloser
// that jsonrpc's server codec takes.
//
// It is NOT a net.Conn, and does not need to be: net/rpc's ServeCodec only
// reads and writes through the codec, and the codec only needs Read/Write plus
// a Close. Close is a no-op because the plugin does not own the pipes — the
// host created them and the host closes them, so a Close here that actually
// closed anything would be the plugin shutting down its own stdin.
type stdio struct {
	in  io.Reader
	out io.Writer
}

func (s *stdio) Read(p []byte) (int, error)  { return s.in.Read(p) }
func (s *stdio) Write(p []byte) (int, error) { return s.out.Write(p) }
func (s *stdio) Close() error                { return nil }

// DownloadResult is what a completed transfer reports back.
type DownloadResult struct {
	// Path is the file as it landed, relative to the library root where
	// possible. A path here is NOT a library path yet: linking it to a scene is
	// step 5.5's job, through the plugin API, and a downloader that inserts
	// scenes itself would be a core store reachable from a plugin.
	Path string `json:"path"`
	// Bytes is the size written, so the host's UI can show progress after the
	// fact rather than "done".
	Bytes int64 `json:"bytes"`
	// Locator is the locator that was fetched, echoed so a task list can be
	// read without keeping the plugin's arguments around.
	Locator string `json:"locator"`
}

// Download is the work Run does.
//
// A function rather than an inline body so the RPC layer is testable without a
// BitTorrent client, and so step 5.2's path sanitisation has somewhere to sit
// that is not buried inside a protocol library.
//
// # THE CONSENT GATE COMES FIRST, AND THAT IS THE WHOLE POINT
//
// The gate is the first thing that happens, before any transfer machinery is
// touched, and the ordering is the design. A downloader built transfer-first
// and gated afterwards is a downloader that already holds every permission the
// gate could have withheld, and §7.1 exists precisely to prevent that.
//
// It BLOCKS on the context once past the gate, and that is a contract rather
// than an implementation detail: the host calls Run asynchronously and closes
// the client the moment Run returns, so a Download that returned immediately
// would give the host a finished task that fetched nothing. The wait here
// stands in for a real transfer until step 5.3 replaces it, and it waits on the
// CONTEXT rather than a fixed duration so a Stop ends it promptly — which is
// what TestStopUnblocksRun measures.
func Download(ctx context.Context, input PluginInput) (*DownloadResult, error) {
	return downloadWithGate(ctx, input, input.proposer)
}

// downloadWithGate is Download with the consent gate supplied explicitly, which
// is how Run injects one that survives the wire.
func downloadWithGate(ctx context.Context, input PluginInput, override Proposer) (*DownloadResult, error) {
	locator, err := LocatorFrom(input.Args)
	if err != nil {
		// Validated BEFORE the gate. A malformed locator is refused locally,
		// because asking core's gate about `file:///etc/passwd` spends an
		// operator's trust on a question with an obvious answer.
		return nil, err
	}

	// Core decides. See consent.go for why a refusal here has to end the run
	// even though core's own gate already refused to store anything: the plugin
	// holds the magnet either way, and a refused proposal followed by a transfer
	// is the failure mode with the gate working perfectly.
	objectID, err := ObjectIDFrom(input.Args)
	if err != nil {
		return nil, err
	}

	gate := override
	if gate == nil {
		gate = proposerFor(input)
	}
	if err := gateDownload(ctx, gate, objectID, locator); err != nil {
		return nil, err
	}

	// Stand-in for the transfer. See the comment above on why it waits on the
	// context rather than a timer.
	<-ctx.Done()
	return nil, fmt.Errorf("%w: %s", ErrTransferNotImplemented, locator)
}

// ObjectIDFrom reads the object a locator is being proposed for.
//
// REQUIRED, and not defaulted. A proposal with no object would leave core to
// decide about something unspecified, and the gate's own answer to that is a
// refusal — so the plugin asks first and gets a specific error rather than a
// round trip whose outcome is already decided.
//
// Read from Args rather than from a struct field because PluginInput is the
// HOST's type, redeclared here to match its wire format. A field the host does
// not send is a field that always arrives as zero, and a plugin that reads an
// object id from one would propose against object 0 forever — which core
// refuses, silently, for a reason that names neither the plugin nor the bug.
func ObjectIDFrom(args ArgsMap) (int64, error) {
	for _, key := range []string{"object_id", "objectId"} {
		v, ok := args[key]
		if !ok {
			continue
		}
		switch n := v.(type) {
		case float64: // every JSON number decodes as float64
			if n != float64(int64(n)) {
				return 0, fmt.Errorf("%s is %v, which is not a whole object id. "+
					"A fractional id means the caller sent something that is not "+
					"an object", key, n)
			}
			return int64(n), nil
		case int64:
			return n, nil
		case int:
			return int64(n), nil
		}
		return 0, fmt.Errorf("%s is %T, which is not an object id", key, v)
	}
	return 0, fmt.Errorf("no object id was given, so there is no object to " +
		"propose a locator for. A locator is a pointer TO something; without an " +
		"object it points at nothing and core would refuse it anyway")
}

// proposerFor returns the consent gate for this run.
//
// A test-only `proposer` field on PluginInput is honoured first, and it is
// deliberately a field on the input rather than a package-level variable: a
// package-level test hook is shared state that leaks between tests, so a
// parallel test and a serial one race on the same gate and one of them ends up
// consulting a stub that belongs to the other. A field travels with the value it
// applies to.
//
// It is a function rather than a field so the Runner stays a value with no
// hidden dependency, and so a test can drive the whole Download path with a
// scripted core. Right now it reads the proposer out of the input's server
// connection, which means a run launched with no connection to the host has no
// gate — and gateDownload refuses in that case, by design.
func proposerFor(input PluginInput) Proposer {
	if input.proposer != nil {
		return input.proposer
	}
	if input.ServerConnection.Host == "" {
		return nil
	}
	return newHTTPProposer(input.ServerConnection)
}

// ErrTransferNotImplemented is what the unimplemented transfer path returns.
//
// Named so a caller can tell "the plugin is a stub" from "the download failed",
// and so the next milestone has something to replace rather than a string to
// grep for in a log.
var ErrTransferNotImplemented = errors.New("the transfer path is not implemented yet")

// LocatorFrom reads the locator out of the host's argument map.
//
// The argument names are what the `source.json`-style operation definition
// declares, and they are validated here rather than cast: an unrecognised
// locator is a REFUSAL, because the alternative is passing an arbitrary string
// to a protocol handler that will try to parse it as a magnet, a torrent file
// path, or an ed2k link, and "try each" is how a downloader ends up fetching
// from a path somebody chose for it.
func LocatorFrom(args ArgsMap) (string, error) {
	for _, key := range []string{"url", "locator", "magnet", "torrent"} {
		if v, ok := args[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s, nil
			}
		}
	}
	return "", errors.New("no locator: pass one of url, locator, magnet or torrent")
}

// jsonRoundTrip proves the redeclared wire types agree with the host's.
//
// It is a test helper rather than a runtime check, and it exists because the
// types CANNOT be compared by importing the host: that is the module boundary
// this milestone exists to keep. The check is that a value round-trips through
// this package's JSON and every field survives — a host that renames a field
// produces a zero value here, and the round trip is where that shows up.
func jsonRoundTrip(in PluginInput) (PluginInput, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return PluginInput{}, err
	}
	var out PluginInput
	if err := json.Unmarshal(raw, &out); err != nil {
		return PluginInput{}, err
	}
	return out, nil
}

// ServiceNameForTest exposes the service name to code in a DIFFERENT package.
//
// It exists only for the seam mutation harness in internal/api/mutate_seam.py,
// which has to make the core legally import the plugin in order to check that
// the seam test notices when the downloader is bundled. It cannot import
// internal/rpc itself -- Go's internal/ rule forbids it -- so a public name has
// to exist for the harness's anchor package to reach.
//
// If this is deleted, the "the core binary links the downloader in" mutation
// stops compiling, and that mutation silently becomes `broken`, which the
// harness counts as a hole rather than a kill.
func ServiceNameForTest() string { return serviceName }
