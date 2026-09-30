package api

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// The websocket transport has no test coverage anywhere in the tree, and this
// file exists because of that.
//
// #7181 bumped gorilla/websocket 1.5.0 -> 1.5.3, which is NOT a no-op for us. The
// one substantive change on our path is in the handshake:
//
//	-challengeKey := r.Header.Get("Sec-Websocket-Key")
//	-if challengeKey == "" { ...400... }
//	+if !isValidChallengeKey(challengeKey) { ...400... }
//
// 1.5.0 accepted ANY non-empty value; 1.5.3 requires base64 decoding to 16 bytes.
// "It compiles" says nothing about whether that is safe here, and the whole usage
// surface is `Upgrader{CheckOrigin: func(*http.Request) bool { return true }}` with
// no subprotocols -- small enough to pin exactly.
//
// So: a real upgrader, real handshakes, and assertions on what the dependency now
// does rather than on what it is supposed to do.

// testUpgrader mirrors the configuration in server.go. A literal rather than a
// reference to the server's own construction, because the point is to pin the
// SHAPE that matters: permissive CheckOrigin, no subprotocols, default buffers.
func testUpgrader() websocket.Upgrader {
	return websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
}

// dial performs a real handshake against an httptest server whose handler
// upgrades. It returns the client's observed status and the dial error.
//
// The status source matters, and the obvious choice is wrong. The client's `resp`
// is nil on the rejection path -- gorilla writes the 400 from inside Upgrade and
// Dial has already given up -- so `resp.StatusCode` reads 0 there. Capturing
// WriteHeader on a wrapped ResponseWriter does not help either: the handler runs
// on another goroutine, so reading its variable straight after Dial returns is a
// data race, and guarding it with a mutex still leaves the value unset when Dial
// returns before the handler has run.
//
// What IS deterministic is `err != nil`: accepted versus rejected. So the
// rejection tests assert on the error and log the status, and the numeric status
// is asserted only on the 101 path, where the client genuinely observes it.
// dial performs a real handshake against an httptest server whose handler
// upgrades. It returns the client's observed status and any error.
//
// THE CLIENT GENERATES ITS OWN Sec-WebSocket-Key. Passing one in a header map
// produces a duplicate header, and the dial then fails before the server's
// validation is reached at all. The first draft of these tests did exactly that,
// so every rejection test was passing for the WRONG REASON -- and the control
// test is what caught it, because a VALID key was refused too, which cannot
// happen if the server is genuinely validating.
//
// So when a caller wants to control the key, the handshake is written on a raw
// socket with exactly one key of the chosen value. Everything else goes through
// the normal Dialer.
//
// Status: the client's `resp` is nil on the rejection path, because gorilla writes
// the 400 from inside Upgrade and the dial has already given up -- so
// `resp.StatusCode` reads 0 there. Wrapping the ResponseWriter to capture
// WriteHeader does not help either: the handler is on another goroutine, so
// reading its variable after the dial returns is a data race. What IS
// deterministic is whether the handshake succeeded, so rejection tests assert on
// that and log the status; the numeric status is asserted only on the 101 path,
// where the client genuinely observes it.
func dial(t *testing.T, u websocket.Upgrader, headers map[string]string) (int, error) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := u.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn.Close()
	}))
	defer srv.Close()

	if key, ok := headers["Sec-Websocket-Key"]; ok {
		return rawHandshake(srv.Listener.Addr().String(), key, headers)
	}

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/"
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, h)
	if conn != nil {
		conn.Close()
	}
	status := 0
	if resp != nil {
		resp.Body.Close()
		status = resp.StatusCode
	}
	return status, err
}

// rawHandshake writes an RFC 6455 opening handshake by hand, with exactly one
// Sec-WebSocket-Key of the caller's choosing, and reports the response status.
//
// This is the only way to exercise the server's key validation, because the
// Dialer will not let you supply the header -- and a duplicate is rejected by the
// client before the request is even sent.
func rawHandshake(addr, key string, extra map[string]string) (int, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	var b strings.Builder
	fmt.Fprintf(&b, "GET / HTTP/1.1\r\n")
	fmt.Fprintf(&b, "Host: %s\r\n", addr)
	b.WriteString("Upgrade: websocket\r\n")
	b.WriteString("Connection: Upgrade\r\n")
	// A real key needs a real Version; Sec-WebSocket-Version is mandatory.
	b.WriteString("Sec-WebSocket-Version: 13\r\n")
	fmt.Fprintf(&b, "Sec-WebSocket-Key: %s\r\n", key)
	for k, v := range extra {
		if k == "Sec-Websocket-Key" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\r\n", k, v)
	}
	b.WriteString("\r\n")

	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, b.String()); err != nil {
		return 0, err
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// A correct handshake succeeds. This is the control: without it, every rejection
// test below would pass even if all handshakes failed.
func TestWebsocketHandshakeSucceeds(t *testing.T) {
	status, err := dial(t, testUpgrader(), nil)
	if err != nil {
		t.Fatalf("a correct handshake failed with %d: %v", status, err)
	}
	if status != http.StatusSwitchingProtocols {
		t.Errorf("status = %d, want 101", status)
	}
}

// The change #7181 brings: the challenge key must be base64 decoding to 16 bytes.
//
// On 1.5.0 every one of these was ACCEPTED, because the check was only for a
// non-empty header. On 1.5.3 each is refused. Asserting the refusal is what makes
// the bump observable: if a future dependency change silently loosened the
// validation again, this fails.
//
// The values are wrong SHAPES -- not base64 at all, and valid base64 of the wrong
// decoded length. A single example would not distinguish "validates length" from
// "validates alphabet", so both are present.
func TestAnInvalidChallengeKeyIsRejected(t *testing.T) {
	for name, key := range map[string]string{
		"not base64 at all":  "!!!!!!!!!!!!!!!!!!!!!!!!",
		"base64 of 8 bytes":  "c2hvcnQga2V5IQ==",
		"base64 of 10 bytes": "c2hvcnQga2V5",
		"a plain word":       "hello",
		"hex digits":         "0123456789abcdef0123456789abcdef",
	} {
		t.Run(name, func(t *testing.T) {
			status, err := dial(t, testUpgrader(), map[string]string{
				"Sec-Websocket-Key": key,
			})
			if err != nil {
				t.Fatalf("the raw handshake itself failed for key %q (%v); the test is measuring nothing", key, err)
			}
			// 1.5.0 returned 101 for every one of these.
			if status != http.StatusBadRequest {
				t.Errorf("challenge key %q: status = %d, want 400 -- the dependency is not validating it", key, status)
			}
		})
	}
}

// A correct base64 key of the right length is ACCEPTED -- the control for the
// rejection test above, so that test cannot pass merely because everything fails.
func TestAValidChallengeKeyIsAccepted(t *testing.T) {
	// The RFC 6455 example key: base64 of exactly 16 bytes.
	const key = "dGhlIHNhbXBsZSBub25jZQ=="
	status, err := dial(t, testUpgrader(), map[string]string{"Sec-Websocket-Key": key})
	if err != nil {
		t.Fatalf("the raw handshake failed for a VALID key (%v); the test is measuring nothing", err)
	}
	if status != http.StatusSwitchingProtocols {
		t.Errorf("a VALID challenge key was refused: status = %d, want 101", status)
	}
}

// A missing header entirely: 1.5.0 rejected this and 1.5.3 still does. Pinned so
// the two rejection cases stay distinguishable.
func TestAMissingChallengeKeyIsRejected(t *testing.T) {
	status, err := dial(t, testUpgrader(), map[string]string{
		"Sec-Websocket-Key": "",
	})
	if err != nil {
		t.Fatalf("the raw handshake failed for an empty key (%v); the test is measuring nothing", err)
	}
	if status != http.StatusBadRequest {
		t.Errorf("an empty challenge key: status = %d, want 400", status)
	}
}

// CheckOrigin is `return true` in server.go, so a cross-origin handshake is
// ACCEPTED. If that ever changes this fails -- deliberately: the permissive origin
// check is a property of the app's configuration and should not be altered by a
// dependency bump without a decision being made about it.
func TestCrossOriginHandshakeIsAccepted(t *testing.T) {
	u := testUpgrader()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := u.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn.Close()
	}))
	defer srv.Close()

	h := http.Header{}
	h.Set("Origin", "http://a-different-host.example.com")

	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/", h)
	if resp != nil {
		resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("a cross-origin handshake was refused (%d): %v", resp.StatusCode, err)
	}
	conn.Close()
}

// With no Subprotocols configured on the Upgrader, a client offering
// `Sec-WebSocket-Protocol` gets none negotiated. v1.5.3's headline change list
// mentions "fixes subprotocol selection (aligning with rfc6455)", and that is
// the part most likely to shift behaviour for an app that configures none -- so it
// is pinned explicitly rather than assumed.
func TestNoSubprotocolIsNegotiatedWhenNoneAreConfigured(t *testing.T) {
	u := testUpgrader()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := u.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// Hold the connection briefly so the client can complete its read.
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()

	h := http.Header{}
	h.Set("Sec-Websocket-Protocol", "graphql-ws")

	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/", h)
	if resp != nil {
		resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("handshake failed: %v", err)
	}
	defer conn.Close()

	if got := conn.Subprotocol(); got != "" {
		t.Errorf("negotiated subprotocol = %q, want none (the Upgrader configures no Subprotocols)", got)
	}
}

// echoServer upgrades and echoes every message back, for the data-path tests.
func echoServer(t *testing.T) *httptest.Server {
	t.Helper()
	u := testUpgrader()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := u.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, msg); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A real message round-trip. The bump touched conn.go, so the data path -- not
// just the handshake -- is worth one end-to-end assertion.
func TestWebsocketCarriesAMessage(t *testing.T) {
	srv := echoServer(t)
	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/", nil)
	if resp != nil {
		resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("handshake failed: %v", err)
	}
	defer conn.Close()

	const payload = `{"type":"connection_init"}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
		t.Fatalf("writing: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, got, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if string(got) != payload {
		t.Errorf("echo = %q, want %q", got, payload)
	}
}

// A large frame, crossing the default 4096 write buffer, since v1.5.3's
// changelog mentions the buffer sizes.
func TestWebsocketCarriesALargeFrame(t *testing.T) {
	srv := echoServer(t)
	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/", nil)
	if resp != nil {
		resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("handshake failed: %v", err)
	}
	defer conn.Close()

	payload := []byte(strings.Repeat("x", 200_000))
	if err := conn.WriteMessage(websocket.BinaryMessage, payload); err != nil {
		t.Fatalf("writing a large frame: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, got, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("reading a large frame: %v", err)
	}
	if len(got) != len(payload) {
		t.Errorf("echoed %d bytes, want %d", len(got), len(payload))
	}
}
