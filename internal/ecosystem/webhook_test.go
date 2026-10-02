package ecosystem

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// R054 — §6a.18's webhooks, and the one property the whole surface exists to have:
//
//	AN OBSERVATION MUST NOT OUTLIVE THE CONSENT THAT PERMITTED IT.
//
// The hazard is specific and it is not solvable by stopping future sends: a payload that has
// left cannot be recalled, so the only lever is the window. What IS in this process's control is
// (a) never queueing a payload whose consent proof is not an opt-in, and (b) dropping queued
// payloads the moment consent is revoked, including while the transport is down and nothing is
// being sent at all. (b) is the one that gets missed, because it does nothing visible.

// THE POSITIVE CONTROL. A suite where every delivery is refused would pass every refusal test.
func TestASubscribedOptedInEntityIsDelivered(t *testing.T) {
	s := NewSubscription()
	require.NoError(t, s.Subscribe(Subscriber{ID: "sub-1", URL: "https://example.invalid/hook", Events: []string{"entity.updated"}}))

	got, err := s.Dispatch(Delivery{
		ID: "d1", Event: "entity.updated",
		Entity:          PublicEntity{PublicID: "scene-1", Title: "T"},
		ShareAtDelivery: OptedIn,
	})
	require.NoError(t, err)
	require.Len(t, got, 1, "a subscribed, opted-in, matching event must be delivered")
	assert.Equal(t, OptedIn, got[0].ShareAtDelivery,
		"and the delivery must carry the consent proof, so a receiver can tell what it was sent under")
	assert.Len(t, s.Pending(), 1, "and it is queued for transport")
}

// §6a.2: the consent proof is checked ON THE DELIVERY, so a payload assembled elsewhere cannot
// bypass it by arriving already-built.
func TestADeliveryWithoutAnOptInProofIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, share string }{
		{"opted out", "opted-out"},
		{"unknown", "maybe"},
		{"empty", ""},
		{"trailing space", "opted-in "},
		{"different case", "OPTED-IN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSubscription()
			require.NoError(t, s.Subscribe(Subscriber{
				ID: "sub-1", URL: "https://example.invalid/hook", Events: []string{"entity.updated"},
			}))

			_, err := s.Dispatch(Delivery{
				ID: "d1", Event: "entity.updated",
				Entity:          PublicEntity{PublicID: "scene-1"},
				ShareAtDelivery: tc.share,
			})
			assert.Error(t, err,
				"a payload that leaves this process cannot be recalled, so it is refused at the "+
					"boundary rather than filtered after it is built")
			assert.Empty(t, s.Pending(),
				"and NOTHING may be queued -- a filter that discarded it later would still have "+
					"built it and put it in a queue, and a queue is a store")
		})
	}
	// THE POSITIVE CONTROL for the loop above, or it could pass as blanket rejection. A
	// subscriber who IS eligible and a delivery that IS proved must still go through.
	ok := NewSubscription()
	require.NoError(t, ok.Subscribe(Subscriber{
		ID: "sub-ok", URL: "https://example.invalid/hook", Events: []string{"entity.updated"},
	}))
	got, err := ok.Dispatch(Delivery{
		ID: "ok", Event: "entity.updated",
		Entity: PublicEntity{PublicID: "x"}, ShareAtDelivery: OptedIn,
	})
	require.NoError(t, err)
	require.Len(t, got, 1, "the same shape with a valid proof must be delivered")
}

// A malformed delivery is a client bug, refused like any other.
func TestAMalformedDeliveryIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    Delivery
	}{
		{"no id", Delivery{Event: "e", ShareAtDelivery: OptedIn}},
		{"no event", Delivery{ID: "d", ShareAtDelivery: OptedIn}},
		{"no proof", Delivery{ID: "d", Event: "e"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSubscription()
			_, err := s.Dispatch(tc.d)
			assert.Error(t, err)
			assert.Empty(t, s.Pending())
		})
	}
}

// THE POINT OF THE FILE. A payload queued under an opt-in, then consent revoked, then the
// transport comes back: the payload must be GONE, not merely unsent.
//
// A "stop future deliveries" implementation passes every other test in this file and fails this
// one, because nothing is being sent here at all.
func TestRevokingConsentDropsAlreadyQueuedPayloads(t *testing.T) {
	s := NewSubscription()
	require.NoError(t, s.Subscribe(Subscriber{
		ID: "sub-1", URL: "https://example.invalid/hook", Events: []string{"entity.updated"},
	}))
	_, err := s.Dispatch(Delivery{
		ID: "d1", Event: "entity.updated",
		Entity: PublicEntity{PublicID: "scene-1"}, ShareAtDelivery: OptedIn,
	})
	require.NoError(t, err)
	require.Len(t, s.Pending(), 1)

	// The owner opts out. The delivery's proof still says OptedIn, because it was true when the
	// payload was made -- which is exactly why the proof alone cannot decide this.
	dropped, err := s.RevokeConsent(func(string) string { return "opted-out" })
	require.NoError(t, err)
	assert.Equal(t, 1, dropped, "the queued payload must be counted as dropped")
	assert.Empty(t, s.Pending(),
		"a payload for an entity whose owner has since opted out must not remain queued; "+
			"stopping FUTURE deliveries does not help, because this one was already built")
}

// Revocation is PER ENTITY, not global. One opt-out must not clear the whole queue.
func TestRevokingConsentOnlyDropsTheAffectedEntity(t *testing.T) {
	s := NewSubscription()
	require.NoError(t, s.Subscribe(Subscriber{
		ID: "sub-1", URL: "https://example.invalid/hook", Events: []string{"entity.updated"},
	}))
	for _, id := range []string{"scene-1", "scene-2"} {
		_, err := s.Dispatch(Delivery{
			ID: "d-" + id, Event: "entity.updated",
			Entity: PublicEntity{PublicID: id}, ShareAtDelivery: OptedIn,
		})
		require.NoError(t, err)
	}
	require.Len(t, s.Pending(), 2)

	dropped, err := s.RevokeConsent(func(id string) string {
		if id == "scene-1" {
			return "opted-out"
		}
		return OptedIn
	})
	require.NoError(t, err)
	assert.Equal(t, 1, dropped)

	pending := s.Pending()
	require.Len(t, pending, 1, "the still-opted-in entity's payload must survive")
	assert.Equal(t, "scene-2", pending[0].Entity.PublicID,
		"and it must be the RIGHT one; a revocation that dropped the wrong payload is worse than "+
			"one that dropped none")
}

// A dropped payload must not merely be unreachable -- its bytes must go. A slice that keeps
// them alive in the backing array is still a store, just an inconvenient one.
func TestRevokingConsentActuallyReleasesThePayload(t *testing.T) {
	s := NewSubscription()
	require.NoError(t, s.Subscribe(Subscriber{
		ID: "sub-1", URL: "https://example.invalid/hook", Events: []string{"entity.updated"},
	}))
	for _, id := range []string{"a", "b", "c"} {
		_, err := s.Dispatch(Delivery{
			ID: "d-" + id, Event: "entity.updated",
			Entity: PublicEntity{PublicID: id, Title: "secret-" + id}, ShareAtDelivery: OptedIn,
		})
		require.NoError(t, err)
	}
	require.Len(t, s.Pending(), 3)

	_, err := s.RevokeConsent(func(id string) string {
		if id == "b" {
			return "opted-out"
		}
		return OptedIn
	})
	require.NoError(t, err)

	// "b" must not appear anywhere reachable, including in the part of the backing array past
	// the new length -- which is exactly where a filter-in-place leaves it.
	pending := s.Pending()
	for _, d := range pending {
		assert.NotEqual(t, "b", d.Entity.PublicID, "the revoked entity must be gone from the queue")
		assert.NotContains(t, d.Entity.Title, "secret-b",
			"and its payload must not survive anywhere the queue can reach")
	}
	assert.Len(t, pending, 2)
}

// The queue is bounded. An unbounded queue on a consent-gated surface is a store with no expiry.
func TestTheQueueIsBounded(t *testing.T) {
	s := NewSubscription()
	s.maxQueue = 3 // small enough to observe
	require.NoError(t, s.Subscribe(Subscriber{
		ID: "sub-1", URL: "https://example.invalid/hook", Events: []string{"entity.updated"},
	}))

	for i := 0; i < 10; i++ {
		_, err := s.Dispatch(Delivery{
			ID: "d" + string(rune('0'+i)), Event: "entity.updated",
			Entity: PublicEntity{PublicID: "scene-1"}, ShareAtDelivery: OptedIn,
		})
		require.NoError(t, err)
	}

	pending := s.Pending()
	assert.Len(t, pending, 3, "the queue must be bounded; an unbounded one is where a revoked "+
		"payload goes to wait")
	// And it drops the OLDEST, because a webhook consumer wants recency and a queue that
	// refused new deliveries would turn a backlog into a permanent outage.
	assert.Equal(t, "d7", pending[0].ID, "the oldest deliveries are the ones dropped")
	assert.Equal(t, "d9", pending[2].ID, "and the newest is kept")
}

// An empty event set means NOTHING, not everything. A "default to all" on a broadcast surface
// turns a misconfigured subscriber into a bulk feed.
func TestAnEmptyEventSetMatchesNothing(t *testing.T) {
	s := NewSubscription()
	require.NoError(t, s.Subscribe(Subscriber{ID: "sub-1", URL: "https://example.invalid/hook"}))

	got, err := s.Dispatch(Delivery{
		ID: "d1", Event: "entity.updated",
		Entity: PublicEntity{PublicID: "scene-1"}, ShareAtDelivery: OptedIn,
	})
	require.NoError(t, err, "dispatch itself is valid; there is simply nobody to send it to")
	assert.Empty(t, got, "a subscriber that named no events must receive none, not all of them")
	assert.Empty(t, s.Pending(), "and nothing is queued when there is no eligible subscriber")
}

// A subscriber's own withdrawal is separate from unsubscribing, and it stops delivery even
// while the subscription stands.
func TestOptingOutStopsDeliveryWithoutUnsubscribing(t *testing.T) {
	s := NewSubscription()
	require.NoError(t, s.Subscribe(Subscriber{
		ID: "sub-1", URL: "https://example.invalid/hook", Events: []string{"entity.updated"},
	}))
	require.NoError(t, s.OptOut("sub-1"))

	got, err := s.Dispatch(Delivery{
		ID: "d1", Event: "entity.updated",
		Entity: PublicEntity{PublicID: "scene-1"}, ShareAtDelivery: OptedIn,
	})
	require.NoError(t, err)
	assert.Empty(t, got, "an opted-out subscriber receives nothing, even though it is still "+
		"subscribed -- collapsing the two states is how a revoke gets implemented as an "+
		"unsubscribe")
	require.Len(t, s.Subscribers(), 1, "and the subscription itself still stands")
}

// Events are matched exactly. A subscriber for entity.updated must not receive entity.deleted.
func TestEventsAreMatchedExactly(t *testing.T) {
	s := NewSubscription()
	require.NoError(t, s.Subscribe(Subscriber{
		ID: "sub-1", URL: "https://example.invalid/hook", Events: []string{"entity.updated"},
	}))

	got, err := s.Dispatch(Delivery{
		ID: "d1", Event: "entity.deleted",
		Entity: PublicEntity{PublicID: "scene-1"}, ShareAtDelivery: OptedIn,
	})
	require.NoError(t, err)
	assert.Empty(t, got, "a different event is not a match, however similar the string")

	// And a prefix is not a match either.
	got, err = s.Dispatch(Delivery{
		ID: "d2", Event: "entity.updated.v2",
		Entity: PublicEntity{PublicID: "scene-1"}, ShareAtDelivery: OptedIn,
	})
	require.NoError(t, err)
	assert.Empty(t, got, "a prefix of a subscribed event is not a match")
}

// The surface has no transport of its own, and that is deliberate -- but it also must have no
// way to SEND anything, or "no transport" would be a claim rather than a boundary.
func TestTheWebhookSurfaceHasNoSendPath(t *testing.T) {
	s := NewSubscription()
	for _, m := range methodNamesOf(s) {
		switch m {
		case "Subscribe", "Unsubscribe", "OptOut", "Dispatch", "Pending", "RevokeConsent", "Subscribers":
		default:
			t.Errorf("Subscription exposes %q; this package does not send webhooks. An outbound "+
				"call from a consent-gated surface is where a payload actually leaves, and "+
				"coupling it to the consent proof makes the rule untestable without a network", m)
		}
	}
}

// A subscription is not a trust grant. §6a.10: reward never grants access, and the same holds
// for a subscriber -- being fed the public surface is not authority over anything.
func TestSubscribingGrantsNothing(t *testing.T) {
	s := NewSubscription()
	require.NoError(t, s.Subscribe(Subscriber{
		ID: "sub-1", URL: "https://example.invalid/hook", Events: []string{"entity.updated"},
	}))

	subs := s.Subscribers()
	require.Len(t, subs, 1)

	// The struct carries no trust level, no scope elevation, no access field at all: there is
	// nothing on Subscriber that COULD hold one, which is the enforcement rather than a
	// convention. This is the positive-allowlist idea from R057's Syncer guard, applied to
	// FIELDS -- and reading the declaration means it cannot drift from the type.
	// fieldNamesOf includes UNEXPORTED fields, which is deliberate and is the point: the
	// question this guard asks is "is there anything on this type that could hold authority",
	// and an unexported trust level would be just as much a bypass as an exported one. The
	// list below is therefore the WHOLE struct, not its public face.
	fields := fieldNamesOf(Subscriber{})
	assert.ElementsMatch(t, []string{"ID", "URL", "Events", "OptedOut"}, fields,
		"Subscriber's fields are exactly these; a new one is a decision, not a convenience. A "+
			"trust level or a scope here would be a feed that grants authority (6a.10)")
}

// THE SEVEN FIRST-PASS SURVIVORS. Two were equivalent mutants; five were real.

// (1) REAL. A revoked payload left alive in the queue's BACKING ARRAY. My test asserted the
// dropped entity was absent from Pending(), which a filter-in-place satisfies -- Pending() only
// reads up to the new length. The bytes are still there past it, and a queue is a store, so
// "unreachable through the accessor" is not "released".
func TestARevokedPayloadIsReleasedFromTheBackingArray(t *testing.T) {
	s := NewSubscription()
	require.NoError(t, s.Subscribe(Subscriber{
		ID: "sub-1", URL: "https://example.invalid/hook", Events: []string{"entity.updated"},
	}))
	for _, id := range []string{"a", "b", "c"} {
		_, err := s.Dispatch(Delivery{
			ID: "d-" + id, Event: "entity.updated",
			Entity: PublicEntity{PublicID: id, Title: "secret-" + id}, ShareAtDelivery: OptedIn,
		})
		require.NoError(t, err)
	}
	require.Len(t, s.Pending(), 3)

	_, err := s.RevokeConsent(func(id string) string {
		if id == "b" {
			return "opted-out"
		}
		return OptedIn
	})
	require.NoError(t, err)

	// The whole backing array, past the new length. This is the only place a filter-in-place
	// leak is visible, and it is the assertion that makes the zeroing loop load-bearing.
	s.mu.Lock()
	whole := append([]Delivery(nil), s.queued[:cap(s.queued)]...)
	s.mu.Unlock()
	for _, d := range whole {
		assert.NotEqual(t, "b", d.Entity.PublicID,
			"the revoked entity must not survive ANYWHERE in the queue's backing array, "+
				"including past the new length where a filter-in-place leaves it")
		assert.NotContains(t, d.Entity.Title, "secret-b",
			"and neither must its payload")
	}
}

// (2) REAL. RevokeConsent(nil) was unchecked: it panicked on the first lookup, which in a
// revoke path is the worst possible failure -- a crash instead of a revocation.
func TestRevokingWithoutAKnownShareStateIsRefusedNotPanicked(t *testing.T) {
	s := NewSubscription()
	_, err := s.RevokeConsent(nil)
	assert.Error(t, err,
		"a revocation that cannot read the current share state must refuse, not dereference "+
			"nil: this is the consent path, and a panic here is a crash instead of a revoke")
	assert.Empty(t, s.Pending(), "and it must not have touched the queue")
}

// (3) AND (4) REAL. Subscribe's own validation was untested, so a subscriber with no id and no
// URL could be registered -- an entry nothing can ever be delivered to, holding a queue slot.
func TestAMalformedSubscriberIsRefused(t *testing.T) {
	s := NewSubscription()
	for _, tc := range []struct {
		name string
		sub  Subscriber
	}{
		{"no id", Subscriber{URL: "https://example.invalid/hook", Events: []string{"e"}}},
		{"no URL", Subscriber{ID: "sub-1", Events: []string{"e"}}},
		{"neither", Subscriber{Events: []string{"e"}}},
		{"blank id", Subscriber{ID: "   ", URL: "https://example.invalid/hook"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Error(t, s.Subscribe(tc.sub))
		})
	}
	assert.Empty(t, s.Subscribers(), "no malformed subscriber may be registered at all")
	// The positive control.
	require.NoError(t, s.Subscribe(Subscriber{ID: "ok", URL: "https://example.invalid/hook"}))
	assert.Len(t, s.Subscribers(), 1)
}

// (5) REAL. Subscribe stored the caller's Events slice by reference, so a caller that reused or
// mutated its slice afterwards could rewrite a registered subscription's event set -- including
// widening it to events it never asked for.
func TestSubscribingCopiesTheEventSet(t *testing.T) {
	s := NewSubscription()
	events := []string{"entity.updated"}
	require.NoError(t, s.Subscribe(Subscriber{ID: "sub-1", URL: "https://example.invalid/hook", Events: events}))

	// The caller keeps its slice and widens it.
	events[0] = "entity.deleted"
	events = append(events, "entity.created")

	subs := s.Subscribers()
	require.Len(t, subs, 1)
	assert.Equal(t, []string{"entity.updated"}, subs[0].Events,
		"a subscriber's event set must be a snapshot; storing the caller's slice lets a later "+
			"mutation rewrite what this instance will send it")

	// And the ORIGINAL event still matches, the new one does not.
	got, err := s.Dispatch(Delivery{
		ID: "d1", Event: "entity.updated",
		Entity: PublicEntity{PublicID: "x"}, ShareAtDelivery: OptedIn,
	})
	require.NoError(t, err)
	assert.Len(t, got, 1, "the event that WAS subscribed for must still match")

	got, err = s.Dispatch(Delivery{
		ID: "d2", Event: "entity.deleted",
		Entity: PublicEntity{PublicID: "x"}, ShareAtDelivery: OptedIn,
	})
	require.NoError(t, err)
	assert.Empty(t, got, "and the one the caller mutated in must not match")
}

// (6) REAL, AND IT WAS A DESIGN BUG RATHER THAN ONLY A MISSING TEST. Dispatch returned one copy
// per eligible subscriber, all carrying the SAME Delivery.ID, so the caller got N payloads it
// could not route and the fan-out sort by ID was a no-op -- a mutation removing it survived
// because it did nothing either way.
//
// Each copy now names its SubscriberID, and the order is by subscriber.
func TestEachDeliveredCopyNamesItsSubscriber(t *testing.T) {
	s := NewSubscription()
	for _, id := range []string{"sub-c", "sub-a", "sub-b"} {
		require.NoError(t, s.Subscribe(Subscriber{
			ID: id, URL: "https://example.invalid/hook", Events: []string{"entity.updated"},
		}))
	}

	got, err := s.Dispatch(Delivery{
		ID: "d1", Event: "entity.updated",
		Entity: PublicEntity{PublicID: "scene-1"}, ShareAtDelivery: OptedIn,
	})
	require.NoError(t, err)
	require.Len(t, got, 3, "every eligible subscriber gets a copy")

	ids := make([]string, len(got))
	for i, d := range got {
		ids[i] = d.SubscriberID
		assert.Equal(t, "d1", d.ID, "and every copy still describes the same event")
	}
	assert.Equal(t, []string{"sub-a", "sub-b", "sub-c"}, ids,
		"copies are ordered by SUBSCRIBER, which is the only ordering that means anything when "+
			"every copy shares one Delivery.ID -- and it makes a test and an operator see the "+
			"same sequence")
}

// (7) EQUIVALENT MUTANT, NOT A GAP. Swapping the opt-out check after the event match changed
// nothing: both branches `continue`, neither has a side effect, and there is no observable that
// distinguishes them. Recorded here so the next person does not re-derive it -- and the ordering
// stays as it is because "an opted-out subscriber is not even told an event occurred" is a
// property worth having in the code even when this implementation cannot express it.
func TestAnOptedOutSubscriberIsNotEvenToldAnEventHappened(t *testing.T) {
	s := NewSubscription()
	require.NoError(t, s.Subscribe(Subscriber{
		ID: "withdrawn", URL: "https://example.invalid/hook", Events: []string{"entity.updated"},
	}))
	require.NoError(t, s.Subscribe(Subscriber{
		ID: "active", URL: "https://example.invalid/hook", Events: []string{"entity.updated"},
	}))
	require.NoError(t, s.OptOut("withdrawn"))

	got, err := s.Dispatch(Delivery{
		ID: "d1", Event: "entity.updated",
		Entity: PublicEntity{PublicID: "scene-1"}, ShareAtDelivery: OptedIn,
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "active", got[0].SubscriberID,
		"only the active subscriber is told; the withdrawn one gets nothing, so nothing about the "+
			"event reaches it at all")
}
