package rpc

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/rpc"
	"net/rpc/jsonrpc"
	"reflect"
	"strings"
	"testing"
	"time"
)

// # WHY THESE TALK TO A REAL net/rpc CLIENT
//
// The host is `rpcPluginTask` in pkg/plugin/rpc.go, and the two things most
// likely to be wrong in a plugin are (a) the SERVICE NAME the host dials and
// (b) whether Run blocks. Neither is visible from this package's own code: a
// server that registers itself under the wrong name compiles, vets, starts, and
// then fails every call with "can't find service", and a Run that returns
// immediately looks identical to a Run that is merely fast.
//
// So every test here drives the same path the host does — a net/rpc client over
// a jsonrpc codec on a pipe — rather than calling the methods directly. Calling
// r.Run(...) directly would pass for a server the host can never reach.

// testSocket returns a connected client/server pair over a REAL TCP socket.
//
// NOT net.Pipe. net.Pipe is fully synchronous with no buffering, so a codec that
// writes a reply before reading the next request deadlocks -- and it does so
// with no output at all, which is what the first version of this file did: it
// hung until the test binary's own timeout, and the only clue was that nothing
// had printed. A real socket has kernel buffering on both sides, which is also
// what the host's `pie` package gives the plugin.
//
// A deadlocking test harness is worse than no harness: it looks like a hang in
// the code under test, and the next person debugs the plugin.
func testSocket(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	type accepted struct {
		conn net.Conn
		err  error
	}
	ch := make(chan accepted, 1)
	go func() {
		c, aErr := ln.Accept()
		ch <- accepted{c, aErr}
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	got := <-ch
	if got.err != nil {
		t.Fatalf("accept: %v", got.err)
	}
	t.Cleanup(func() { _ = got.conn.Close() })
	return client, got.conn
}

// dialAsHost starts the server and returns a client configured the way
// pkg/plugin/rpc.go configures its own.
func dialAsHost(t *testing.T) *rpc.Client {
	t.Helper()
	return dialAsHostWithGate(t, grantingProposer())
}

// dialAsHostWithGate starts a server whose Runner consults the given gate.
//
// The gate goes on the RUNNER, not in the PluginInput the test sends: the input
// crosses the jsonrpc wire, where an unexported field does not survive. A test
// that put its stub in the input it sent would get a server-side input with no
// gate, the consent check would refuse, and Run would return at once — which
// satisfies "Run blocks" by failing rather than by blocking.
func dialAsHostWithGate(t *testing.T, gate Proposer) *rpc.Client {
	t.Helper()

	clientConn, serverConn := testSocket(t)

	go func() {
		srv := rpc.NewServer()
		runner := NewRunner()
		runner.gate = gate
		if err := srv.RegisterName(serviceName, runner); err != nil {
			t.Errorf("registering %s: %v", serviceName, err)
			return
		}
		srv.ServeCodec(jsonrpc.NewServerCodec(&stdio{
			in:  serverConn,
			out: serverConn,
		}))
	}()

	return rpc.NewClientWithCodec(jsonrpc.NewClientCodec(clientConn))
}

// TestTheServiceNameIsTheOneTheHostDials pins "RPCRunner".
//
// This is the single most expensive mistake available to a plugin. net/rpc
// derives a service name from the receiver's TYPE, so a receiver called Runner
// registers as "Runner" — and the host, which dials the literal string
// "RPCRunner.Run", gets "can't find service RPCRunner" for every call. The
// plugin starts cleanly, the host records a task, and nothing works. It is
// pinned here as a literal rather than derived from serviceName, because a test
// asserting `serviceName == serviceName` is a test that cannot fail.
func TestTheServiceNameIsTheOneTheHostDials(t *testing.T) {
	if serviceName != "RPCRunner" {
		t.Fatalf("serviceName is %q, but pkg/plugin/rpc.go dials %q. A different "+
			"name means every call from the host fails with \"can't find service\" "+
			"while the plugin looks healthy", serviceName, "RPCRunner")
	}

	// And the name is not derivable from the type, which is the trap: if a
	// future edit renames Runner, RegisterName keeps working and this assertion
	// is what catches the type moving away from the constant.
	if got := reflect.TypeOf(NewRunner()).Name(); got == serviceName {
		t.Logf("note: the type is itself named %q, so Register and RegisterName "+
			"would agree. The constant is still correct and still deliberate — it "+
			"is the only thing tying the two modules together", got)
	}
}

// TestRunBlocksUntilTheTransferFinishes is the property the host depends on and
// cannot check.
//
// pkg/plugin/rpc.go calls Run ASYNCHRONOUSLY at Start and then closes the
// client as soon as the call returns. A Run that returns immediately therefore
// produces a plugin the host has already finished with: `t.done` is closed with
// a zero PluginOutput, so the task shows as finished having done nothing, and
// the exit is clean, so nothing is logged.
//
// The test asserts the call has NOT returned after a short interval. A Run that
// blocks forever is the correct shape here — the real Run blocks for the length
// of a transfer — so the assertion is about the SHAPE, not about finishing.
func TestRunBlocksUntilTheTransferFinishes(t *testing.T) {
	client := dialAsHost(t)
	defer client.Close()

	// An object id and a GRANTING gate. Without both, the consent check refuses
	// and Run returns immediately -- which would make this test pass for entirely
	// the wrong reason: it is asserting that Run BLOCKS, and a refusal also
	// returns. The gate tests in consent_test.go cover the refusal path.
	input := PluginInput{
		Args: ArgsMap{
			"url":       "magnet:?xt=urn:btih:0000000000000000000000000000000000000000",
			"object_id": float64(7),
		},
	}

	type result struct {
		err error
	}
	done := make(chan result, 1)

	go func() {
		out := PluginOutput{}
		err := client.Call("RPCRunner.Run", input, &out)
		done <- result{err: err}
	}()

	select {
	case r := <-done:
		t.Fatalf("Run returned immediately (err=%v). The host calls Run "+
			"asynchronously at Start and closes the client the moment it returns, "+
			"so a non-blocking Run is a plugin that reports a finished task having "+
			"done nothing -- and exits cleanly, so nothing is logged", r.err)
	case <-time.After(300 * time.Millisecond):
		// Still running, which is the correct shape.
	}
}

// TestStopUnblocksRun is the other half of the contract, and the one whose
// absence is a resource leak rather than an error.
//
// The host calls Stop and does NOT wait. So a Run that Stop cannot unblock keeps
// the transfer running -- a real BitTorrent client, still connected to peers,
// writing files -- after the operator asked it to stop, and the only thing that
// eventually kills it is the host giving up on the process.
func TestStopUnblocksRun(t *testing.T) {
	client := dialAsHost(t)
	defer client.Close()

	runDone := make(chan error, 1)
	go func() {
		out := PluginOutput{}
		runDone <- client.Call("RPCRunner.Run", PluginInput{
			Args: ArgsMap{
				"url":       "magnet:?xt=urn:btih:0000000000000000000000000000000000000000",
				"object_id": float64(7),
			},
		}, &out)
	}()

	// Let Run get as far as it is going to.
	time.Sleep(150 * time.Millisecond)

	// The host's exact call: `t.client.Call("RPCRunner.Stop", nil, &resp)`.
	var resp StopReply
	if err := client.Call("RPCRunner.Stop", nil, &resp); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run returned %v after Stop. The host treats an error here as "+
				"a protocol failure and logs it, when the honest answer is that the "+
				"run was cancelled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not unblock Run. The host does not wait for Stop, so a " +
			"transfer that survives it keeps running after the operator asked it " +
			"to stop")
	}
}

// TestStopBeforeRunIsNotAnError covers the teardown path.
//
// The host calls Stop on shutdown whether or not a Run ever started, and a
// refusal here turns a clean shutdown into a reported failure.
func TestStopBeforeRunIsNotAnError(t *testing.T) {
	r := NewRunner()

	var resp StopReply
	if err := r.Stop(nil, &resp); err != nil {
		t.Fatalf("Stop before Run returned %v. The host calls Stop on a teardown "+
			"path where Run may never have started, so refusing makes a clean "+
			"shutdown look like a failure", err)
	}
}

// TestARefusedLocatorIsReportedInOutputNotAsAnRPCError is the difference between
// "the download failed" and "the plugin is broken", and it is easy to get
// backwards.
//
// PluginOutput.Error is how a FAILED OPERATION is reported. Returning the error
// as the second return value of Run is a PROTOCOL failure, which the host
// records with no message — so a user who typed a bad magnet sees an empty
// error rather than "that is not a locator".
func TestARefusedLocatorIsReportedInOutputNotAsAnRPCError(t *testing.T) {
	r := NewRunner()
	out := PluginOutput{}

	err := r.Run(PluginInput{Args: ArgsMap{}}, &out)
	if err != nil {
		t.Fatalf("Run returned a protocol error (%v) for a missing locator. A "+
			"refusal belongs in PluginOutput.Error; returning it here means the "+
			"host logs a failure with no message", err)
	}
	if out.Error == nil {
		t.Fatal("Run succeeded with no error for a request carrying no locator")
	}
	if !strings.Contains(*out.Error, "locator") {
		t.Errorf("the refusal is %q, which does not say what was wrong. The "+
			"operator needs to know a locator was expected", *out.Error)
	}
}

// TestLocatorFromRefusesRatherThanGuessing is the security-relevant one.
//
// The tempting implementation is "try it as a magnet, then as a torrent path,
// then as an ed2k link". That is how a downloader ends up FETCHING FROM A PATH
// somebody chose for it, and why the argument set is a fixed list of names with
// no fallthrough.
func TestLocatorFromRefusesRatherThanGuessing(t *testing.T) {
	// The happy paths, so the refusal is not the only thing tested.
	for _, key := range []string{"url", "locator", "magnet", "torrent"} {
		got, err := LocatorFrom(ArgsMap{key: "magnet:?xt=urn:btih:abc"})
		if err != nil {
			t.Errorf("%s: %v", key, err)
			continue
		}
		if got != "magnet:?xt=urn:btih:abc" {
			t.Errorf("%s: got %q", key, got)
		}
	}

	// A present but empty value is NOT a locator, and must not be passed on.
	if _, err := LocatorFrom(ArgsMap{"url": ""}); err == nil {
		t.Error(`LocatorFrom accepted url: "" — an empty locator handed to a ` +
			`protocol handler is how a client fetches from a path nobody chose`)
	}

	// A non-string is refused rather than formatted into a locator.
	if _, err := LocatorFrom(ArgsMap{"url": 42}); err == nil {
		t.Error("LocatorFrom accepted a number as a locator")
	}

	// Nothing at all is refused.
	if _, err := LocatorFrom(ArgsMap{}); err == nil {
		t.Error("LocatorFrom accepted an empty argument map")
	}

	// An argument that is NOT one of the four names is refused even though it
	// is a perfectly good string.
	//
	// This is the case the mutation "iterate every argument" passes, and it is
	// the security-relevant one: a fallthrough implementation takes whatever
	// string it finds first, so an argument named `path` or `url_backup` or
	// anything else a future operation definition adds becomes a locator. The
	// mutation harness found this survivor, which is why the case exists -- the
	// original four-key loop was not wrong, but the test did not pin the
	// property that makes it right.
	for _, key := range []string{"path", "output", "url_backup", "URL", "Url"} {
		if _, err := LocatorFrom(ArgsMap{key: "/etc/passwd"}); err == nil {
			t.Errorf("LocatorFrom accepted %q, an argument outside the fixed set. A "+
				"fallthrough implementation turns ANY string argument into a "+
				"locator, which is how a downloader ends up fetching from a path "+
				"the operator never named", key)
		}
	}
}

// TestTheWireTypesRoundTrip is the module-boundary check.
//
// The plugin redeclares the host's types because it cannot import them, so the
// only thing keeping the two in step is that the JSON agrees. A host that
// renames server_connection would deliver an empty StashServerConnection here,
// and the plugin would then try to reach "" — with no error anywhere, because a
// zero value is a valid struct.
func TestTheWireTypesRoundTrip(t *testing.T) {
	cookie := &http.Cookie{Name: "session", Value: "abc123"}

	// NO proposer here on purpose: the field is unexported, so json cannot see
	// it, and the round trip is what proves the wire format is unchanged by a
	// test hook existing in the struct at all.
	in := PluginInput{
		ServerConnection: StashServerConnection{
			Scheme:        "https",
			Host:          "localhost",
			Port:          9999,
			SessionCookie: cookie,
			Dir:           "/config",
			PluginDir:     "/plugins/p2p-downloader",
		},
		Args: ArgsMap{"url": "magnet:?xt=urn:btih:abc"},
	}

	out, err := jsonRoundTrip(in)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}

	// Compared field by field rather than with reflect.DeepEqual on the whole
	// struct, because a whole-struct comparison says "equal" when both sides
	// are zero — and the failure being guarded against IS both sides zero.
	if out.ServerConnection.Host != in.ServerConnection.Host {
		t.Errorf("Host came back as %q, was %q", out.ServerConnection.Host, in.ServerConnection.Host)
	}
	if out.ServerConnection.Port != in.ServerConnection.Port {
		t.Errorf("Port came back as %d, was %d", out.ServerConnection.Port, in.ServerConnection.Port)
	}
	if out.ServerConnection.SessionCookie == nil ||
		out.ServerConnection.SessionCookie.Value != "abc123" {
		t.Errorf("the session cookie did not survive: %+v", out.ServerConnection.SessionCookie)
	}
	if out.Args["url"] != in.Args["url"] {
		t.Errorf("the locator came back as %v", out.Args["url"])
	}
}

// TestTheServerRefusesAMalformedCallRatherThanCrashing matters because the
// plugin runs for the lifetime of the host and a panic takes the whole task
// with it.
func TestTheServerRefusesAMalformedCallRatherThanCrashing(t *testing.T) {
	client := dialAsHost(t)
	defer client.Close()

	var resp StopReply
	if err := client.Call("RPCRunner.NoSuchMethod", nil, &resp); err == nil {
		t.Error("calling a method that does not exist succeeded. The host would " +
			"then believe a task ran when nothing did")
	}

	// And the server is still usable afterwards: a panic would have taken the
	// connection with it.
	if err := client.Call("RPCRunner.Stop", nil, &resp); err != nil {
		t.Errorf("the server stopped answering after an unknown method: %v", err)
	}
}

// TestDownloadReportsThatItIsAStub keeps the next milestone honest: a caller
// must be able to tell "not implemented" from "failed", and the error says which
// locator it was refused so a log line is actionable.
func TestDownloadReportsThatItIsAStub(t *testing.T) {
	// A cancelled context, because Download blocks until one arrives -- a test
	// passing context.Background() would hang rather than fail, and a hanging
	// test is indistinguishable from a hang in the code.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// An object id AND a granting proposer, because the consent gate runs first
	// and refuses without both. That refusal is correct -- spec 7.1 puts the gate
	// ahead of the transfer -- so the test has to get past it to reach the stub
	// it is actually about. TestTheGateRefusesBeforeAnyTransfer covers the
	// other direction.
	_, err := Download(ctx, PluginInput{
		Args: ArgsMap{
			"url":       "magnet:?xt=urn:btih:abc",
			"object_id": float64(7),
		},
		proposer: grantingProposer(),
	})
	if err == nil {
		t.Fatal("Download succeeded before any transfer code exists")
	}
	if !errors.Is(err, ErrTransferNotImplemented) {
		t.Errorf("the error is %v, which does not wrap ErrTransferNotImplemented, "+
			"so a caller cannot tell a stub from a failed download", err)
	}
	if !strings.Contains(err.Error(), "btih:abc") {
		t.Errorf("the error %q does not name the locator, so a log line does not "+
			"say which download failed", err)
	}
}

// TestServeReturnsWhenTheHostGoesAway is the shutdown path.
//
// The host sends no signal, so the only clean exit is the codec's reader hitting
// EOF. A Serve that waited for something else would hang a process the host has
// already finished with.
func TestServeReturnsWhenTheHostGoesAway(t *testing.T) {
	clientConn, serverConn := testSocket(t)

	done := make(chan error, 1)
	go func() {
		done <- Serve(serverConn, serverConn)
	}()

	// The host going away.
	_ = clientConn.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned %v after the host closed the pipe. A close is "+
				"a normal shutdown and is not a failure", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after the host closed the connection. The " +
			"host sends no shutdown signal, so EOF on the codec is the only exit")
	}
}

// unusedIo keeps the io import honest if the stdio helper is ever moved: it
// documents that the pipes are io, not net.Conn.
var _ io.Reader = (*stdio)(nil)
