package collab

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Federation tests. M3 step 3.3.
//
// The plan names three: TestFederation_OptOutPeerReceivesNothing,
// TestFederation_SignedIdRejectsTampering, and TestCommons_UnauthenticatedReadIs404.
// All three are here, plus the property that makes the feature coherent -- that
// publishing and consuming are INDEPENDENT, which is what §6.5 says ("may
// consume a peer's commons for identification without publishing to it") and
// which a single "federated: true" flag would make inexpressible.

func testPeer(id int64, name string) Peer {
	return Peer{
		ID: id, Name: name,
		Endpoint: fmt.Sprintf("https://%s.example/graphql", name),
		Key:      []byte("key-for-" + name),
	}
}

func testPayload(t *testing.T) Payload {
	t.Helper()
	src := &fakeSource{
		ids:    []int64{1, 2},
		scenes: map[int64]map[string]string{1: {"title": "One"}, 2: {"title": "Two"}},
	}
	p, err := BuildPayload(context.Background(), src, "inst", testLib())
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	return p
}

// TestFederation_OptOutPeerReceivesNothing is the first of the plan's three.
//
// The peer is configured but NOT enabled for publishing, and the assertion is
// that no signed artefact is produced at all -- not that the send is suppressed
// later. A submission that exists is a submission that can be sent, so the
// refusal belongs where the signature is made.
func TestFederation_OptOutPeerReceivesNothing(t *testing.T) {
	p := testPayload(t)
	peer := testPeer(1, "alice")
	peer.PublishTo = false
	peer.ConsumeFrom = false

	// No signed submission can be made.
	_, err := MakeSubmission(p, peer)
	if !errors.Is(err, ErrPeerPublishNotEnabled) {
		t.Fatalf("MakeSubmission on a peer not enabled for publishing returned %v, want ErrPeerPublishNotEnabled", err)
	}

	// And the fan-out does not call Send for it.
	reg := &fakeRegistry{peers: []Peer{peer}}
	var sent []int64
	fed := &FedPublisher{Registry: reg, Send: func(ctx context.Context, p Peer, s SignedSubmission) error {
		sent = append(sent, p.ID)
		return nil
	}}
	res, err := fed.PublishToPeers(context.Background(), p)
	if err != nil {
		t.Fatalf("PublishToPeers: %v", err)
	}
	if len(sent) != 0 {
		t.Fatalf("Send was called for peers %v on an opt-out peer", sent)
	}
	if len(res.Sent) != 0 {
		t.Fatalf("result reports %v as sent", res.Sent)
	}
	if res.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1: an opt-out peer must be counted, not silently dropped", res.Skipped)
	}
}

// TestFederation_ReceiveRefusesWhenNotEnabledForConsume is the inbound mirror.
// A peer you publish to is not thereby a peer you accept from.
func TestFederation_ReceiveRefusesWhenNotEnabledForConsume(t *testing.T) {
	p := testPayload(t)
	sender := testPeer(1, "alice")
	sender.PublishTo = true
	sender.ConsumeFrom = false

	signed, err := MakeSubmission(p, sender)
	if err != nil {
		t.Fatalf("MakeSubmission: %v", err)
	}

	receiver := &FedReceiver{
		Registry: &fakeRegistry{peers: []Peer{sender}},
		Accept:   func(ctx context.Context, p Peer, pld Payload) error { return nil },
	}
	err = receiver.Receive(context.Background(), sender.ID, 3, signed.Payload, signed.Signature)
	if !errors.Is(err, ErrPeerConsumeNotEnabled) {
		t.Fatalf("Receive from a peer not enabled for consume returned %v, want ErrPeerConsumeNotEnabled", err)
	}
}

// TestFederation_SignedIdRejectsTampering is the second of the plan's three,
// and the security centre of the milestone.
//
// It tampers in every way a payload could be altered and asserts each is
// refused. Tampering with only the entries and not with the id would be the
// obvious test and would pass for the wrong reason -- the signature covers the
// id, and the id covers the content, so changing the content without changing
// the id is exactly the case the CONTENT check exists for. Both are tested.
func TestFederation_SignedIdRejectsTampering(t *testing.T) {
	p := testPayload(t)
	peer := testPeer(1, "alice")
	peer.PublishTo = true
	peer.ConsumeFrom = true

	signed, err := MakeSubmission(p, peer)
	if err != nil {
		t.Fatalf("MakeSubmission: %v", err)
	}
	if signed.Signature == "" {
		t.Fatal("MakeSubmission produced no signature")
	}

	receiver := &FedReceiver{
		Registry: &fakeRegistry{peers: []Peer{peer}},
		Accept: func(ctx context.Context, p Peer, pld Payload) error {
			t.Error("a tampered submission reached Accept")
			return nil
		},
		Audit: func(ctx context.Context, p Peer, reason string, cause error) error {
			if reason == "" {
				t.Error("a rejection was not audited")
			}
			return nil
		},
	}

	t.Run("the untampered submission is accepted", func(t *testing.T) {
		accepted := false
		ok := &FedReceiver{
			Registry: &fakeRegistry{peers: []Peer{peer}},
			Accept:   func(ctx context.Context, p Peer, pld Payload) error { accepted = true; return nil },
		}
		if err := ok.Receive(context.Background(), peer.ID, 3, signed.Payload, signed.Signature); err != nil {
			t.Fatalf("the genuine submission was refused: %v", err)
		}
		if !accepted {
			t.Fatal("the genuine submission did not reach Accept")
		}
	})

	t.Run("a flipped signature byte", func(t *testing.T) {
		bad := flipOneByte(signed.Signature)
		err := receiver.Receive(context.Background(), peer.ID, 3, signed.Payload, bad)
		if !errors.Is(err, ErrBadSignature) {
			t.Fatalf("flipped signature returned %v, want ErrBadSignature", err)
		}
	})

	t.Run("an empty signature", func(t *testing.T) {
		err := receiver.Receive(context.Background(), peer.ID, 3, signed.Payload, "")
		if !errors.Is(err, ErrUnsignedSubmission) {
			t.Fatalf("empty signature returned %v, want ErrUnsignedSubmission", err)
		}
	})

	t.Run("a signature from a different peer's key", func(t *testing.T) {
		// The realistic attack: Alice signs, the submission is replayed at Bob.
		bob := testPeer(2, "bob")
		bob.PublishTo = true
		bob.ConsumeFrom = true
		rec := &FedReceiver{
			Registry: &fakeRegistry{peers: []Peer{bob}},
			Accept: func(ctx context.Context, p Peer, pld Payload) error {
				t.Error("a cross-peer replay was accepted")
				return nil
			},
		}
		err := rec.Receive(context.Background(), bob.ID, 3, signed.Payload, signed.Signature)
		if !errors.Is(err, ErrBadSignature) {
			t.Fatalf("cross-peer replay returned %v, want ErrBadSignature: a per-peer key must not verify at another peer", err)
		}
	})

	t.Run("entries changed but the signed id left alone", func(t *testing.T) {
		// The signature still verifies -- it covers the id, and the id was not
		// changed. This is precisely the hole VerifySubmissionContent closes.
		tampered := signed.Payload
		tampered.Entries = []ExportEntry{{TargetType: "scene", TargetID: 99, Fields: map[string]string{"title": "Injected"}}}

		if err := VerifySubmission(tampered, signed.Signature, peer); err != nil {
			t.Logf("note: the signature check alone already caught this (%v)", err)
		}
		err := VerifySubmissionContent(tampered, 3)
		if err == nil {
			t.Fatal("content changed while the signed id stayed the same, and the content check passed it")
		}
		if !strings.Contains(err.Error(), "does not match its content") {
			t.Errorf("error %q should say the content does not match the signed id", err)
		}
	})

	t.Run("a full receive of content-tampered payload is refused", func(t *testing.T) {
		tampered := signed.Payload
		tampered.Entries = []ExportEntry{{TargetType: "scene", TargetID: 99, Fields: map[string]string{"title": "Injected"}}}
		err := receiver.Receive(context.Background(), peer.ID, 3, tampered, signed.Signature)
		if err == nil {
			t.Fatal("Receive accepted a payload whose content does not match its signed id")
		}
	})

	t.Run("a payload signed for a different library", func(t *testing.T) {
		// The library id is part of the hash, so a submission for library 3
		// cannot be replayed as library 4.
		err := VerifySubmissionContent(signed.Payload, 4)
		if err == nil {
			t.Fatal("a submission for library 3 verified as library 4: the library id is not bound into the id")
		}
	})
}

func flipOneByte(s string) string {
	if s == "" {
		return "x"
	}
	b := []byte(s)
	if b[0] == 'a' {
		b[0] = 'b'
	} else {
		b[0] = 'a'
	}
	return string(b)
}

// TestFederation_PublishAndConsumeAreIndependent walks all four combinations.
//
// §6.5 says an instance may consume a peer's commons "without publishing to it",
// which means the two are separate grants. A single `Federated bool` would make
// the inbound-only case inexpressible, and the fix for that afterwards is always
// to publish to people you did not mean to.
func TestFederation_PublishAndConsumeAreIndependent(t *testing.T) {
	p := testPayload(t)

	for _, tc := range []struct {
		name        string
		publishTo   bool
		consumeFrom bool
		wantSend    bool
		wantReceive bool
	}{
		{"neither: a peer nobody talks to", false, false, false, false},
		{"outbound only: publish but do not accept", true, false, true, false},
		{"inbound only: identify without publishing", false, true, false, true},
		{"both: a full peer", true, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := testPeer(1, "alice")
			peer.PublishTo = tc.publishTo
			peer.ConsumeFrom = tc.consumeFrom

			// Outbound.
			sent := 0
			fed := &FedPublisher{
				Registry: &fakeRegistry{peers: []Peer{peer}},
				Send:     func(ctx context.Context, p Peer, s SignedSubmission) error { sent++; return nil },
			}
			if _, err := fed.PublishToPeers(context.Background(), p); err != nil {
				t.Fatalf("PublishToPeers: %v", err)
			}
			if tc.wantSend && sent != 1 {
				t.Errorf("Send called %d times, want 1", sent)
			}
			if !tc.wantSend && sent != 0 {
				t.Errorf("Send called %d times, want 0: this peer is not enabled for publishing", sent)
			}

			// Inbound.
			received := 0
			rec := &FedReceiver{
				Registry: &fakeRegistry{peers: []Peer{peer}},
				Accept:   func(ctx context.Context, p Peer, pld Payload) error { received++; return nil },
			}
			sig := ""
			if tc.publishTo {
				// Sign with the peer's key; whether the receiver will take it is
				// the thing under test.
				sig = SignSubmission(p.SubmissionID, peer.Key)
			} else {
				sig = SignSubmission(p.SubmissionID, peer.Key)
			}
			err := rec.Receive(context.Background(), peer.ID, 3, p, sig)
			if tc.wantReceive {
				if err != nil {
					t.Errorf("Receive refused a peer enabled for consume: %v", err)
				} else if received != 1 {
					t.Errorf("Accept called %d times, want 1", received)
				}
			} else {
				if err == nil {
					t.Error("Receive accepted from a peer not enabled for consume")
				}
				if received != 0 {
					t.Errorf("Accept called %d times, want 0", received)
				}
			}
		})
	}
}

// TestFederation_OneFailingPeerDoesNotStopTheOthers: "Alice is down" must not
// prevent publishing to Bob, and the operator needs to know two of three were
// missed.
func TestFederation_OneFailingPeerDoesNotStopTheOthers(t *testing.T) {
	p := testPayload(t)
	alice, bob, carol := testPeer(1, "alice"), testPeer(2, "bob"), testPeer(3, "carol")
	alice.PublishTo, bob.PublishTo, carol.PublishTo = true, true, true
	// Carol has no key: a misconfiguration, not a network failure.
	carol.Key = nil

	fed := &FedPublisher{
		Registry: &fakeRegistry{peers: []Peer{alice, bob, carol}},
		Send: func(ctx context.Context, p Peer, s SignedSubmission) error {
			if p.ID == alice.ID {
				return errors.New("connection refused")
			}
			return nil
		},
	}
	res, err := fed.PublishToPeers(context.Background(), p)
	if err != nil {
		t.Fatalf("PublishToPeers: %v", err)
	}
	if len(res.Sent) != 1 || res.Sent[0] != bob.ID {
		t.Errorf("Sent = %v, want just bob: one failure must not abort the run", res.Sent)
	}
	if len(res.Failed) != 2 {
		t.Errorf("Failed has %d entries, want 2 (alice unreachable, carol unkeyed): %v", len(res.Failed), res.Failed)
	}
	if _, ok := res.Failed[carol.ID]; !ok {
		t.Error("carol's missing key was not reported as a per-peer failure")
	}
}

// TestFederation_ReceiveRefusesAnUnknownPeer: an unconfigured peer is a
// configuration question, not an attack, and the two must be distinguishable.
func TestFederation_ReceiveRefusesAnUnknownPeer(t *testing.T) {
	p := testPayload(t)
	rec := &FedReceiver{
		Registry: &fakeRegistry{peers: nil},
		Accept: func(ctx context.Context, p Peer, pld Payload) error {
			t.Error("an unknown peer was accepted")
			return nil
		},
	}
	err := rec.Receive(context.Background(), 999, 3, p, "deadbeef")
	if !errors.Is(err, ErrPeerConsumeNotEnabled) {
		t.Fatalf("Receive from an unknown peer returned %v, want ErrPeerConsumeNotEnabled", err)
	}
	if errors.Is(err, ErrBadSignature) {
		t.Error("an unconfigured peer was reported as a signature failure: that sends an operator hunting a breach that did not happen")
	}
}

// TestCommons_UnauthenticatedReadIs404 is the third of the plan's three.
//
// The load-bearing part is that an UNAUTHORISED read and a NONEXISTENT one
// return the SAME error. If they differed, the endpoint would confirm which
// libraries exist, and for a private library that confirmation is itself the
// disclosure §6.4 forbids.
func TestCommons_UnauthenticatedReadIs404(t *testing.T) {
	p := testPayload(t)

	t.Run("an unauthorised caller gets not-found, not forbidden", func(t *testing.T) {
		c := &CommonsRead{
			Lookup: func(ctx context.Context, instance string, libID int64) (Payload, bool, error) {
				return p, true, nil
			},
			Authorize: func(ctx context.Context, callerID int64, instance string, libID int64) (bool, error) {
				return false, nil
			},
		}
		_, err := c.Read(context.Background(), 12345, "inst", 3)
		if !errors.Is(err, ErrCommonsNotFound) {
			t.Fatalf("unauthorised read returned %v, want ErrCommonsNotFound (404)", err)
		}
		if strings.Contains(err.Error(), "forbidden") || strings.Contains(err.Error(), "403") {
			t.Errorf("error %q must not distinguish forbidden from missing", err)
		}
	})

	t.Run("an authorised caller gets the payload", func(t *testing.T) {
		c := &CommonsRead{
			Lookup: func(ctx context.Context, instance string, libID int64) (Payload, bool, error) { return p, true, nil },
			Authorize: func(ctx context.Context, callerID int64, instance string, libID int64) (bool, error) {
				return true, nil
			},
		}
		got, err := c.Read(context.Background(), 7, "inst", 3)
		if err != nil {
			t.Fatalf("authorised read: %v", err)
		}
		if got.SubmissionID != p.SubmissionID {
			t.Error("the wrong payload was returned")
		}
	})

	t.Run("a missing payload and a denied one are indistinguishable", func(t *testing.T) {
		denied := &CommonsRead{
			Lookup: func(ctx context.Context, instance string, libID int64) (Payload, bool, error) { return p, true, nil },
			Authorize: func(ctx context.Context, callerID int64, instance string, libID int64) (bool, error) {
				return false, nil
			},
		}
		missing := &CommonsRead{
			Lookup: func(ctx context.Context, instance string, libID int64) (Payload, bool, error) {
				return Payload{}, false, nil
			},
			Authorize: func(ctx context.Context, callerID int64, instance string, libID int64) (bool, error) {
				return true, nil
			},
		}
		_, deniedErr := denied.Read(context.Background(), 1, "inst", 3)
		_, missingErr := missing.Read(context.Background(), 1, "inst", 999)
		if deniedErr.Error() != missingErr.Error() {
			t.Errorf("denied returned %q and missing returned %q: a caller can probe which libraries exist", deniedErr, missingErr)
		}
	})

	t.Run("a missing authorizer is treated as denied, not as allowed", func(t *testing.T) {
		c := &CommonsRead{
			Lookup: func(ctx context.Context, instance string, libID int64) (Payload, bool, error) {
				t.Error("the payload was read with no authorizer configured")
				return p, true, nil
			},
		}
		if _, err := c.Read(context.Background(), 1, "inst", 3); !errors.Is(err, ErrCommonsNotFound) {
			t.Fatalf("a read with no authorizer returned %v, want not-found: absent authorization is not authorization", err)
		}
	})

	t.Run("an authorization error does not leak that the credentials were valid", func(t *testing.T) {
		c := &CommonsRead{
			Lookup: func(ctx context.Context, instance string, libID int64) (Payload, bool, error) {
				return p, true, nil
			},
			Authorize: func(ctx context.Context, callerID int64, instance string, libID int64) (bool, error) {
				return false, errors.New("auth backend down")
			},
		}
		_, err := c.Read(context.Background(), 1, "inst", 3)
		if !errors.Is(err, ErrCommonsNotFound) {
			t.Fatalf("an auth failure surfaced as %v: that tells a prober their token was valid but their grant was not", err)
		}
	})
}

// TestSignSubmission_IsKeyedPerPeerAndDomainSeparated pins the two properties
// that make a per-peer key safe.
func TestSignSubmission_IsKeyedPerPeerAndDomainSeparated(t *testing.T) {
	id := "abc123"
	a := SignSubmission(id, []byte("key-a"))
	b := SignSubmission(id, []byte("key-b"))
	if a == b {
		t.Fatal("two peers' keys produced the same signature: the key is not being used")
	}
	if SignSubmission(id, []byte("key-a")) != a {
		t.Fatal("signing is not deterministic")
	}
	if SignSubmission("different", []byte("key-a")) == a {
		t.Fatal("a different submission id produced the same signature")
	}
}

// TestMakeSubmission_RefusesAnUnkeyedPeer: an unkeyed peer cannot sign, and an
// unsigned submission would be refused by the peer anyway -- so failing here
// gives a message the operator can act on.
func TestMakeSubmission_RefusesAnUnkeyedPeer(t *testing.T) {
	p := testPayload(t)
	peer := testPeer(1, "alice")
	peer.PublishTo = true
	peer.Key = nil

	_, err := MakeSubmission(p, peer)
	if err == nil {
		t.Fatal("MakeSubmission signed a submission for a peer with no key")
	}
	if errors.Is(err, ErrPeerPublishNotEnabled) {
		t.Error("the error blamed the enable flag; the actual problem is the missing key")
	}
	if !strings.Contains(err.Error(), "no key") {
		t.Errorf("error %q should name the missing key", err)
	}
}

// TestSummarizePeers_ReportsDirectionAndKeyState: the settings screen's answer
// must include the misconfiguration, not hide it behind a cheerful label.
func TestSummarizePeers_ReportsDirectionAndKeyState(t *testing.T) {
	outbound := testPeer(1, "alice")
	outbound.PublishTo = true
	inbound := testPeer(2, "bob")
	inbound.ConsumeFrom = true
	both := testPeer(3, "carol")
	both.PublishTo, both.ConsumeFrom = true, true
	// Neither direction, but still holding a key: direction and key state are
	// independent, and conflating them in the summary would misreport this peer.
	none := testPeer(4, "dave")
	unkeyed := testPeer(5, "erin")
	unkeyed.PublishTo = true
	unkeyed.Key = nil

	got := SummarizePeers([]Peer{outbound, inbound, both, none, unkeyed})
	want := []struct {
		dir string
		key bool
	}{
		{"outbound", true},
		{"inbound", true},
		{"both", true},
		{"none", true},      // keyed, but neither direction enabled
		{"outbound", false}, // configured outbound with NO key: the misconfiguration
	}
	if len(got) != len(want) {
		t.Fatalf("%d summaries, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Direction != w.dir {
			t.Errorf("peer %d direction = %q, want %q", i, got[i].Direction, w.dir)
		}
		if got[i].KeyConfigured != w.key {
			t.Errorf("peer %d KeyConfigured = %v, want %v", i, got[i].KeyConfigured, w.key)
		}
	}
}

// TestRedactPeerKey: a key must never be renderable. Showing its LENGTH is
// enough to confirm one is configured and nothing more.
func TestRedactPeerKey(t *testing.T) {
	if got := RedactPeerKey(testPeer(1, "alice")); strings.Contains(got, "key-for-alice") {
		t.Fatalf("RedactPeerKey leaked the key: %q", got)
	}
	if got := RedactPeerKey(Peer{}); got != "" {
		t.Errorf("RedactPeerKey on a peer with no key = %q, want empty", got)
	}
}

// fakeRegistry serves a fixed peer set.
type fakeRegistry struct {
	peers []Peer
	err   error
}

func (f *fakeRegistry) all() []Peer { return f.peers }

func (f *fakeRegistry) PeersForPublish(ctx context.Context) ([]Peer, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.all(), nil
}

func (f *fakeRegistry) PeersForConsume(ctx context.Context) ([]Peer, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.all(), nil
}

func (f *fakeRegistry) PeerByID(ctx context.Context, id int64) (Peer, bool, error) {
	if f.err != nil {
		return Peer{}, false, f.err
	}
	for _, p := range f.peers {
		if p.ID == id {
			return p, true, nil
		}
	}
	return Peer{}, false, nil
}
