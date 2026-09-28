package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// # WHAT THIS FILE IS
//
// The plugin's half of spec §7.1: it REQUESTS, and core DECIDES.
//
// # WHY THE REFUSAL IS HANDLED HERE AND NOT ONLY IN CORE
//
// Core's gate is the thing that actually enforces this, and
// internal/collab/locator.go is where the decision is made. So it would be
// reasonable to assume a plugin that ignores the answer causes no harm: core
// refused, nothing was stored, done.
//
// That is wrong, and the reason is what the plugin does NEXT. The gate stops
// core from storing a locator on a denied object. It does not stop the plugin
// from FETCHING the content that locator points at — the plugin holds the
// magnet in its own memory, and a refused proposal followed by a transfer is
// exactly the failure §7.1 describes, with the gate having worked perfectly.
//
// So the plugin must treat a refusal as terminal for the transfer, and this file
// is where that happens. The invariant: **no transfer starts without a granted
// proposal for that exact locator**, and a plugin that cannot reach core does
// not transfer at all. Failing closed here is not defensive programming; it is
// the only thing that makes core's refusal mean anything.

// Proposal is the request the plugin sends to core: `locator.propose`.
//
// Re-declared, like everything else here, because the plugin is a separate
// module and cannot import the core. The JSON tags are the contract, and
// TestTheProposalRoundTrips is what keeps them honest.
type Proposal struct {
	ObjectID int64  `json:"object_id"`
	Locator  string `json:"locator"`
	Scheme   string `json:"scheme,omitempty"`
}

// Refusal is what core sends back when the gate says no.
//
// `allowed` and not `permitted`: the field name has to match the one core
// actually emits, and a mismatch here does not error — it decodes as false, so a
// renamed field turns every grant into a refusal and a downloader into something
// that silently never downloads. Failing safe, but for the wrong reason, and
// impossible to diagnose from the plugin's side.
type Refusal struct {
	// Allowed is core's decision. False means the locator may not be stored.
	Allowed bool `json:"allowed"`

	// Reason is why. Populated on every refusal, and that is the contract: a
	// refusal a plugin cannot explain is a refusal a user cannot act on.
	Reason string `json:"reason,omitempty"`

	// Checked is the tier the decision was made against, so the plugin can log
	// which state it was refused at.
	Checked string `json:"checked,omitempty"`
}

// ErrRefused means core declined the locator. A distinct error rather than a
// string, because the plugin's response to a refusal and its response to a
// network failure have to be different: a refusal is final, and retrying it
// would be a plugin hammering a gate that has already answered.
var ErrRefused = fmt.Errorf("core refused the locator")

// Proposer is the seam between the plugin and core's gate.
//
// An interface, so the consent tests can drive the real Download path against a
// scripted core without an HTTP server, and so the "what if core is
// unreachable" case is one stub away rather than a firewall rule away.
type Proposer interface {
	// Propose asks core whether the locator may be attached to the object.
	//
	// It returns a Refusal, never an "approved" boolean: the reason is the part
	// the plugin has to show somebody.
	Propose(ctx context.Context, p Proposal) (*Refusal, error)
}

// httpProposer talks to the host's GraphQL API.
//
// It is the real implementation, and it is what the plugin runs. The plugin
// reaches the host through the server connection it was handed
// (StashServerConnection), authenticating with the session cookie, because that
// is the only credential a plugin process has.
type httpProposer struct {
	conn StashServerConnection
	http *http.Client
}

// newHTTPProposer returns a proposer pointed at the host.
//
// The timeout is not arbitrary. A consent check that hangs holds the download,
// and a downloader that appears to do nothing is indistinguishable from one
// waiting on a peer — so a slow core is surfaced as an error the operator can
// read, not as silence.
func newHTTPProposer(conn StashServerConnection) *httpProposer {
	return &httpProposer{
		conn: conn,
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// Propose calls the host's `locatorPropose` mutation.
//
// Over HTTP and not RPC: the RPC channel is the host calling INTO the plugin, so
// a call in this direction would deadlock the connection the host is waiting on
// a reply to. The host's own API is the outbound path, which is why the plugin
// is given a session cookie at all.
func (h *httpProposer) Propose(ctx context.Context, p Proposal) (*Refusal, error) {
	query := `mutation ProposeLocator($objectId: ID!, $locator: String!, $scheme: String) {
		locatorPropose(object_id: $objectId, locator: $locator, scheme: $scheme) {
			allowed
			reason
			checked
		}
	}`

	body, err := json.Marshal(map[string]interface{}{
		"query": query,
		"variables": map[string]interface{}{
			"objectId": p.ObjectID,
			"locator":  p.Locator,
			"scheme":   p.Scheme,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("encoding the proposal: %w", err)
	}

	endpoint := &url.URL{
		Scheme: h.conn.Scheme,
		Host:   fmt.Sprintf("%s:%d", h.conn.Host, h.conn.Port),
		Path:   "/graphql",
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(),
		strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("building the proposal request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if h.conn.SessionCookie != nil {
		req.AddCookie(h.conn.SessionCookie)
	}

	resp, err := h.http.Do(req)
	if err != nil {
		// NOT a refusal, and the distinction is the whole point: core did not
		// answer, so there is no decision to honour. The plugin fails closed
		// either way, but the error has to say which happened, because "core
		// refused" sends an operator looking at their consent tiers and "core
		// was unreachable" does not.
		return nil, fmt.Errorf("asking core about the locator: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("core answered %s. A proposal that core did not "+
			"accept is not a permission to transfer", resp.Status)
	}

	var envelope struct {
		Data struct {
			LocatorPropose Refusal `json:"locatorPropose"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decoding core's answer: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return nil, fmt.Errorf("core reported %s", envelope.Errors[0].Message)
	}

	return &envelope.Data.LocatorPropose, nil
}

// LocatorScheme is a locator's protocol, as a closed set.
//
// DUPLICATED from core's internal/collab/locator_scheme.go, and the duplication
// is the seam's price rather than an oversight: the plugin is a separate module,
// so importing the core to share five constants would put a `require` in the
// core's go.mod and make the downloader core code — the exact outcome M5 exists
// to prevent.
//
// A closed set rather than a string, because a fallback is what makes a
// downloader read a local file. `file://` and `ftp://` are not merely
// unrecognised here: they are refused BY NAME, so the error can say what the
// scheme would have done, and a scheme this build has never heard of is refused
// too rather than passed through for a protocol handler to interpret.
type LocatorScheme string

const (
	SchemeMagnet LocatorScheme = "magnet"
	SchemeHTTP   LocatorScheme = "http"
	SchemeHTTPS  LocatorScheme = "https"
	SchemeED2K   LocatorScheme = "ed2k"
)

// knownSchemes is the set, in one place.
//
// NO `torrent`, deliberately. A .torrent is a METAINFO file fetched over http or
// https, and `torrent:` is not a scheme any protocol handler speaks — so listing
// it here would be advertising a scheme this code refuses, which makes the error
// message ("it handles ...") a lie and gives the next reader something to
// "fix" by adding a handler. Core keeps the name in its vocabulary because there
// it describes a KIND of locator; here it would be a transport, and it is
// neither.
//
// The first version of this list included it, and
// TestThePluginAndCoreAgreeOnLocatorSchemes is what caught the divergence — the
// test compares the two modules' sets on every run precisely so that a name in
// one and not the other cannot sit there unnoticed.
var knownSchemes = []LocatorScheme{
	SchemeMagnet, SchemeHTTP, SchemeHTTPS, SchemeED2K,
}

// LocatorSchemeNames lists the schemes, for error messages.
func LocatorSchemeNames() []string {
	names := make([]string, 0, len(knownSchemes))
	for _, s := range knownSchemes {
		names = append(names, string(s))
	}
	return names
}

// LocatorSchemeOf classifies a locator string, refusing anything unknown.
//
// The ORDER is the security-relevant part and mirrors core's parser, because
// the two have to agree and a divergence is a locator one accepts and the other
// refuses — or worse, one that neither handles the way the other would:
//
//   - `file`, `ftp` and `gopher` are refused BY NAME. An unrecognised scheme is
//     rejected anyway; naming the dangerous ones turns "unknown scheme" into an
//     explanation.
//   - `magnet:` and `ed2k://` are matched by PREFIX, before any URL parsing,
//     because neither is a URL. `url.Parse` accepts `magnet:?xt=...` and hands
//     back a URL whose Path is the whole string, and a downloader that trusted
//     that Path would be rebuilding a locator from a field that was never one.
//   - Only then does it fall through to URL parsing, and the http/https test is
//     on the scheme the PARSER reports, not on a prefix — so `httpx://` is not
//     mistaken for https.
//
// There is no `torrent` scheme, and its absence is deliberate — see the comment
// on knownSchemes. `torrent:whatever` is refused, which is what should happen to
// a string no handler understands.
func LocatorSchemeOf(raw string) (LocatorScheme, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("the locator is empty, so it has no scheme")
	}

	lower := strings.ToLower(value)

	for _, refused := range []struct{ prefix, effect string }{
		{"file:", "a locator with this scheme makes a downloader read a local " +
			"file, which turns fetching material into reading anything the user " +
			"can read"},
		{"ftp:", "this build speaks only BitTorrent, ed2k and HTTP"},
		{"gopher:", "gopher has been dead since 2009, and nothing here speaks it"},
	} {
		if strings.HasPrefix(lower, refused.prefix) {
			return "", fmt.Errorf("the locator's scheme is %q, which is refused: %s",
				strings.TrimSuffix(refused.prefix, ":"), refused.effect)
		}
	}

	// Non-URL schemes, by prefix and before URL parsing.
	if strings.HasPrefix(lower, "magnet:") {
		return SchemeMagnet, nil
	}
	if strings.HasPrefix(lower, "ed2k://") {
		return SchemeED2K, nil
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("the locator does not parse as a URL: %w", err)
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http":
		return SchemeHTTP, nil
	case "https":
		return SchemeHTTPS, nil
	}

	// An empty scheme is the case worth naming: `example.com/file.torrent` is
	// what people paste, and treating it as https would be a transport decision
	// made by a guess.
	if parsed.Scheme == "" {
		return "", fmt.Errorf("the locator %q has no scheme. A bare host or path "+
			"is refused rather than assumed to be https, because guessing the "+
			"transport is how content gets fetched over the wrong one. Write the "+
			"scheme explicitly: https://, magnet:, or ed2k://", value)
	}

	return "", fmt.Errorf("the locator's scheme %q is not one this build handles. "+
		"It handles %s", parsed.Scheme, strings.Join(LocatorSchemeNames(), ", "))
}

// isED2KLocator reports whether a locator is an ed2k link.
//
// It exists so the pre-gate ed2k validation in downloadWithGate can ask that
// one question WITHOUT calling LocatorSchemeOf, because gateDownload already
// calls it and a second call is a second copy of the same decision — see the
// comment on that branch for what duplication cost us once already.
//
// The test is deliberately the SAME prefix test LocatorSchemeOf uses, case
// insensitively, so the two cannot drift: an ed2k link that LocatorSchemeOf
// calls ed2k is an ed2k link this calls ed2k. If LocatorSchemeOf's own
// prefix ever changes, TestTheED2KPrefixIsRecognisedTheSameWayTwice fails
// rather than the ed2k validation quietly stopping to apply.
//
// A false negative here is not a security hole: a locator this misses is one
// LocatorSchemeOf also refuses as unrecognised, so the gate still refuses it
// before anything is fetched. A false POSITIVE would be — an http URL would be
// run through the ed2k parser and refused for being http — so the prefix is
// matched exactly and not loosely, and `ed2k:foo` without the slashes is left
// to LocatorSchemeOf's own URL branch.
func isED2KLocator(raw string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "ed2k://")
}

// gateDownload is the consent check, called before anything is fetched.
//
// # THE ORDER OF THE CHECKS, AND WHY IT IS THIS ORDER
//
//  1. The locator must be well-formed, and that is checked HERE rather than
//     being left to core. A malformed locator — `file:///etc/passwd` — is
//     refused locally, because asking core's gate about it spends an
//     operator's trust on a question with an obvious answer, and because a
//     downloader that forwards a `file://` URL to a protocol handler is asking
//     that handler to read a local file.
//
//     The scheme set is duplicated from core's internal/collab/locator_scheme.go
//     on purpose. The plugin cannot import it — separate module, and importing
//     the core to share an enum would defeat the entire milestone — so the list
//     is written out again and the duplication is the price of the seam. The
//     test that follows is what keeps the two agreeing; a scheme added to core
//     and not here is refused by the plugin, which is the safe direction.
//
//  2. Core must grant. No grant, no transfer — including when core is
//     unreachable, which is the case that makes a plugin "helpful" about
//     fallbacks.
//
//  3. Only then does a transfer start, and the transfer is the ONLY thing
//     gated. A run that proposes and is refused writes nothing, fetches
//     nothing, and tells the operator why.
func gateDownload(ctx context.Context, p Proposer, objectID int64, locator string) error {
	if strings.TrimSpace(locator) == "" {
		return fmt.Errorf("%w: no locator was given, so there is nothing to "+
			"propose and nothing to fetch", ErrRefused)
	}

	if _, err := LocatorSchemeOf(locator); err != nil {
		return fmt.Errorf("%w: %v", ErrRefused, err)
	}

	if p == nil {
		// Fails closed. A plugin built without a way to ask core is a plugin
		// that would otherwise transfer unasked, and the temptation to treat
		// "no gate" as "no restriction" is the exact failure this milestone
		// exists to prevent.
		return fmt.Errorf("%w: this build cannot reach core's consent gate, so "+
			"nothing may be fetched. A downloader with no gate is a downloader "+
			"that ignores the gate", ErrRefused)
	}

	refusal, err := p.Propose(ctx, Proposal{ObjectID: objectID, Locator: locator})
	if err != nil {
		// Core did not answer. Not a permission.
		return fmt.Errorf("%w: core did not answer, so the gate has not "+
			"permitted anything: %v", ErrRefused, err)
	}

	if refusal == nil {
		// A nil answer is not consent. It is what a decode mismatch produces,
		// and treating it as approval would make a renamed field in core into
		// an open door.
		return fmt.Errorf("%w: core sent no decision, which is not a "+
			"permission", ErrRefused)
	}

	if !refusal.Allowed {
		reason := strings.TrimSpace(refusal.Reason)
		if reason == "" {
			// Core should never do this, and the plugin says so rather than
			// inventing a reason — an invented reason is a fact nobody checked.
			reason = "core gave no reason"
		}
		return fmt.Errorf("%w: %s", ErrRefused, reason)
	}

	return nil
}
