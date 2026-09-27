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

	result, err := Download(ctx, input)
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
// It BLOCKS on the context, and that is a contract rather than an
// implementation detail: the host calls Run asynchronously and closes the
// client the moment Run returns, so a Download that returned immediately would
// give the host a finished task that fetched nothing. The wait here stands in
// for a real transfer until step 5.3 replaces it, and it waits on the CONTEXT
// rather than a fixed duration so a Stop ends it promptly — which is what
// TestStopUnblocksRun measures.
func Download(ctx context.Context, input PluginInput) (*DownloadResult, error) {
	locator, err := LocatorFrom(input.Args)
	if err != nil {
		// Validated BEFORE the wait. A missing locator is a refusal, and
		// refusing instantly is the right behaviour: there is no transfer to
		// wait for, and a client that is told immediately can show the error
		// while the operator is still looking at the form.
		return nil, err
	}

	// Stand-in for the transfer. See the comment above on why it waits on the
	// context rather than a timer.
	<-ctx.Done()
	return nil, fmt.Errorf("%w: %s", ErrTransferNotImplemented, locator)
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
