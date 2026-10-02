package ecosystem

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// R054 — §6a.18, "Public API over GraphQL (existing §10) plus REST, SDKs and webhooks".
//
// The spec names three surfaces and constrains none of their mechanisms, so the design question
// is what they all have in common. The answer is that each is a way for something OUTSIDE this
// process to observe the library, and §6a.2's consent is per-instance and REVOCABLE. So the
// property every surface must have is the same one, and it is the hard part:
//
//	AN OBSERVATION MUST NOT OUTLIVE THE CONSENT THAT PERMITTED IT.
//
// That is what this file builds, and it is built first and centrally rather than per-surface
// because the alternative is three handlers each with their own copy of a revocation check, and
// one of them would eventually not do it.
//
// THE HAZARD, STATED PRECISELY. A webhook subscriber receives a payload. Consent is later
// revoked. The subscriber now holds a copy of an entity it must no longer see, and no mechanism
// can take it back -- the bytes left the building. Webhook systems that get this wrong deliver
// anyway ("it was already subscribed"), and the usual mitigation is to stop FUTURE deliveries,
// which does not address the copy already in the other party's log.
//
// SO THE HONEST POSITION, WHICH IS NOT "WE SOLVED IT": a delivery cannot be un-delivered. What
// the system can do is make the WINDOW as small as the protocol allows and never widen it, and
// say so in the type rather than in a README. `Delivery` carries a consent PROOF -- the share
// state observed at the moment of delivery -- so a receiver can tell, for every payload, whether
// it was delivered under an opt-in that was current then. A receiver that keeps payloads can
// honour that proof; one that cannot has a problem this side cannot fix, and the doc comment
// says so rather than implying otherwise.
//
// Everything else here follows from making that window and that proof real.

// Delivery is one webhook payload.
//
// It carries the CONSENT PROOF rather than the consent state, deliberately. §6a.2's rule is that
// consent is revocable and must be re-checked, so a receiver cannot treat "this was published
// when you got it" as current fact -- and this type must not invite that mistake. What it can
// truthfully say is "at the instant this was produced, the local share state was X", which is a
// fact about the past and is the only kind a delivered copy can carry.
type Delivery struct {
	// ID identifies the delivery, so a receiver can deduplicate a retried send.
	ID string

	Event string

	// Entity is the payload. It is a PublicEntity and NOT a database record: a webhook that
	// could carry an internal id or a storage path would be a way around §6a.6's (instance,
	// local) identity and around every redaction rule above it.
	Entity PublicEntity

	// ShareAtDelivery is the LOCAL share state observed when this payload was produced. It is
	// the proof, and it is a copy of a decision that may since have been revoked.
	ShareAtDelivery string

	// SubscriberID is who this copy is FOR. One event fans out to N subscribers, so without it
	// the caller receives N indistinguishable payloads and cannot route them -- and the
	// fan-out order is not something a caller should have to reconstruct.
	SubscriberID string

	// ProducedAt is when the payload was produced, for the receiver's own retention maths.
	ProducedAt time.Time
}

// Valid reports whether the delivery may be sent AT ALL.
//
// A delivery whose proof is not an opt-in is refused at the boundary rather than filtered
// downstream. This is the same ordering R057's preservation contribution uses, and for the same
// reason: a filter that discarded the payload later would still have built it, counted it, and
// queued it -- and a queue is a store, and a store is somewhere a revoked payload survives.
func (d Delivery) Valid() error {
	if strings.TrimSpace(d.ID) == "" {
		return fmt.Errorf("ecosystem: a delivery needs an id")
	}
	if strings.TrimSpace(d.Event) == "" {
		return fmt.Errorf("ecosystem: a delivery needs an event")
	}
	// The consent proof, checked on the DELIVERY and not only where it is produced, so a
	// payload assembled elsewhere cannot bypass it. §6a.2: opt-out is not a default to be
	// overridden, and §6a.21 says the same of the indexed surfaces.
	if d.ShareAtDelivery != OptedIn {
		return fmt.Errorf("ecosystem: refusing to deliver %q: the local share state was %q, not "+
			"%q. A payload that leaves this process cannot be recalled, so it is refused at the "+
			"boundary rather than filtered after it is built", d.ID, d.ShareAtDelivery, OptedIn)
	}
	return nil
}

// Subscriber is one registered endpoint.
type Subscriber struct {
	ID  string
	URL string
	// Events is the set this subscriber asked for. Empty means none, NOT all: a default of
	// "everything" turns a misconfigured subscriber into a bulk feed of a library it may not be
	// entitled to, and the safe default for a broadcast surface is silence.
	Events []string
	// OptedOut is the subscriber's OWN consent for this delivery. A subscriber can withdraw
	// without unsubscribing, and both are honoured.
	OptedOut bool
}

// Subscription is the webhook surface's own state.
type Subscription struct {
	mu          sync.Mutex
	subscribers map[string]Subscriber

	// queued is what Dispatch produced and nothing has transported yet. It is BOUNDED, and that
	// bound is the reason this type can say anything honest about revocation: a queue is a
	// store, so it is capped, and a capped queue cannot quietly become the place a revoked
	// payload lives.
	queued []Delivery
	// maxQueue bounds it. A webhook surface with an unbounded queue is a memory leak with a
	// payload-shaped hole in it.
	maxQueue int

	// now is the clock, so revocation's effect on queued deliveries is testable without sleep.
	now func() time.Time
}

// NewSubscription returns an empty webhook surface.
func NewSubscription() *Subscription {
	t := TestNow
	return &Subscription{
		subscribers: map[string]Subscriber{},
		maxQueue:    DefaultWebhookQueue,
		now:         func() time.Time { return t },
	}
}

// DefaultWebhookQueue bounds the pending-delivery queue.
const DefaultWebhookQueue = 256

// Subscribe registers an endpoint.
//
// The URL is stored but NEVER FETCHED by this package. That is a decision with a reason: an
// outbound HTTP call from a consent-gated surface is the place where a payload actually leaves,
// and doing it here would mean the consent proof and the transport are coupled in a way that
// cannot be tested without a network. The dispatcher below hands the validated deliveries to a
// caller-supplied transport, so the consent rule stays testable and the transport stays the
// deployment's business.
func (s *Subscription) Subscribe(sub Subscriber) error {
	if strings.TrimSpace(sub.ID) == "" {
		return fmt.Errorf("ecosystem: a subscriber needs an id")
	}
	if strings.TrimSpace(sub.URL) == "" {
		return fmt.Errorf("ecosystem: a subscriber needs a URL")
	}
	// No event is normalised to "all". See the field's comment.
	sub.Events = append([]string(nil), sub.Events...)
	for i, e := range sub.Events {
		sub.Events[i] = strings.TrimSpace(e)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscribers[sub.ID] = sub
	return nil
}

// Unsubscribe removes a subscriber. A payload already delivered to it cannot be recalled, and
// this method does not pretend otherwise -- see the Delivery comment.
func (s *Subscription) Unsubscribe(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subscribers[id]; !ok {
		return fmt.Errorf("ecosystem: no subscriber %q", id)
	}
	delete(s.subscribers, id)
	return nil
}

// OptOut is a subscriber withdrawing consent WITHOUT unsubscribing.
//
// The two are separate because a user who stops wanting a feed and a user who revokes consent
// are different acts with different consequences, and collapsing them means a revoke can be
// implemented as an unsubscribe and the "still subscribed, still opted in" state becomes
// reachable.
func (s *Subscription) OptOut(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.subscribers[id]
	if !ok {
		return fmt.Errorf("ecosystem: no subscriber %q", id)
	}
	sub.OptedOut = true
	s.subscribers[id] = sub
	return nil
}

// Dispatch validates a candidate delivery and queues it for every eligible subscriber.
//
// It RETURNS what it would send rather than sending, for the reason on Subscribe: the consent
// rule is the part that must be provable, and it is provable without a network.
func (s *Subscription) Dispatch(d Delivery) ([]Delivery, error) {
	if err := d.Valid(); err != nil {
		return nil, err
	}
	if d.ProducedAt.IsZero() {
		d.ProducedAt = s.now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Delivery, 0, len(s.subscribers))
	for _, sub := range s.subscribers {
		if sub.OptedOut {
			// The subscriber's own withdrawal, checked before the event match so an opted-out
			// subscriber is not even told whether an event occurred.
			continue
		}
		if !subscribedTo(sub, d.Event) {
			continue
		}
		one := d
		one.SubscriberID = sub.ID
		out = append(out, one)
	}
	if len(out) == 0 {
		return []Delivery{}, nil
	}

	// Deterministic order BY SUBSCRIBER, which is now meaningful because each copy names its
	// recipient. Sorting by Delivery.ID would be a no-op -- every copy shares one -- and I only
	// noticed that because a mutation removing this sort survived.
	sort.Slice(out, func(i, j int) bool { return out[i].SubscriberID < out[j].SubscriberID })

	// The queue is bounded. Dropping the OLDEST is the right end to drop: a webhook consumer
	// wants recency, and a bounded queue that refused new deliveries would turn a backlog into
	// a permanent outage.
	s.queued = append(s.queued, out...)
	if len(s.queued) > s.maxQueue {
		s.queued = s.queued[len(s.queued)-s.maxQueue:]
	}
	return out, nil
}

// subscribedTo matches an event against a subscriber's set. An empty set matches NOTHING.
func subscribedTo(sub Subscriber, event string) bool {
	for _, e := range sub.Events {
		if e == event {
			return true
		}
	}
	return false
}

// Pending is the queued, not-yet-transported deliveries.
//
// Revocation's whole effect is visible here: a delivery queued under an opt-in that has since
// been revoked is GONE, not merely undelivered, because the queue is where a revoked payload
// would otherwise survive.
func (s *Subscription) Pending() []Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Delivery(nil), s.queued...)
}

// RevokeConsent drops every queued delivery for entities whose LOCAL share state is no longer an
// opt-in.
//
// THIS IS THE POINT OF THE WHOLE FILE. It runs at revoke time rather than at send time because
// send time may never come: if the transport is down, a payload queued now would sit until it
// came back, and a revoke that only affects future sends leaves it there. The check is on the
// entity's CURRENT share state, not on the proof the delivery carries -- a delivery that was
// valid when it was made can be invalid now, and only the current state decides.
func (s *Subscription) RevokeConsent(shareNow func(publicID string) string) (int, error) {
	if shareNow == nil {
		return 0, fmt.Errorf("ecosystem: RevokeConsent needs to know the current share state")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	kept := s.queued[:0]
	dropped := 0
	for _, d := range s.queued {
		if shareNow(d.Entity.PublicID) != OptedIn {
			dropped++
			continue
		}
		kept = append(kept, d)
	}
	// Clear the tail so dropped payloads are not merely unreachable through Pending -- a slice
	// still holding them keeps the bytes alive in the backing array.
	for i := len(kept); i < len(s.queued); i++ {
		s.queued[i] = Delivery{}
	}
	s.queued = kept
	return dropped, nil
}

// Subscribers is the registered set, for the surface's own introspection and tests.
func (s *Subscription) Subscribers() []Subscriber {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Subscriber, 0, len(s.subscribers))
	for _, sub := range s.subscribers {
		out = append(out, sub)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
