package library

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"testing"
)

// The helpers in this file exist so the tests above read as claims about
// behaviour rather than as HTTP plumbing, and so every one of them is in a file
// whose whole job is to be obviously correct.

// httptestServer starts a server for one test and closes it when the test ends.
//
// A helper rather than `httptest.NewServer` inline at each site, because
// `t.Cleanup` in twenty places is twenty places to forget one, and a leaked
// listener in a test binary is a port that stays bound for the run.
func httptestServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// testConn builds a Conn pointing at a test server.
//
// Parsed rather than assembled from SplitHostPort by hand, because the split is
// exactly the part that goes wrong: a malformed port produces a Conn that looks
// right and a request URL that is not.
func testConn(srv *httptest.Server) Conn {
	u, err := url.Parse(srv.URL)
	if err != nil {
		panic("a test server produced an unparseable URL: " + err.Error())
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		panic("a test server produced a URL with no port: " + u.Port())
	}
	return Conn{
		Scheme: u.Scheme,
		Host:   u.Hostname(),
		Port:   port,
		Cookie: &http.Cookie{Name: "stash-auth", Value: "test-session"},
	}
}

// newFakeHost serves one fixed body, for the cases where the RESPONSE is the
// thing under test rather than the request.
func newFakeHost(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	})
}

// regexpMatch reports whether a pattern matches a string, and whether it
// compiled at all.
//
// The compile error is RETURNED rather than treated as "no match", because a
// pattern that does not compile is a different failure from one that matches
// nothing -- and a helper that turned the first into the second would make
// `TestAPathWithRegexCharactersMatchesOnlyItself` pass for the wrong reason.
func regexpMatch(pattern, s string) (bool, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, err
	}
	return re.MatchString(s), nil
}
