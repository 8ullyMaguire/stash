package api

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/asticode/go-astisub"
)

// stash#4771 -- caption offsets.
//
// THE DESIGN DECISION THAT MATTERS HERE: the offset is a query parameter, not stored state.
//
// It is a property of one viewing session on one device, not of the scene. The same scene watched in
// another browser, or on a cast receiver that cannot pass the parameter, should keep the original
// timings. Storing it would mean a mutation, a GraphQL field and a UI control for something that is a
// display concern. The tests below pin the parsing contract, and the clamping behaviour is pinned where
// it actually lives -- in astisub's Subtitles.Add, which is what makes this cheap and correct.

func TestParseCaptionOffset(t *testing.T) {
	tests := []struct {
		name   string
		query  string
		wantMS int64
		wantOK bool
	}{
		{name: "absent means no offset", query: "lang=en&type=vtt", wantMS: 0, wantOK: false},
		{name: "positive", query: "lang=en&type=vtt&offset=1500", wantMS: 1500, wantOK: true},
		{name: "negative", query: "lang=en&type=vtt&offset=-2500", wantMS: -2500, wantOK: true},
		{name: "zero is a real offset that happens to be a no-op", query: "lang=en&type=vtt&offset=0", wantMS: 0, wantOK: true},
		{name: "negative zero parses to zero", query: "lang=en&type=vtt&offset=-0", wantMS: 0, wantOK: true},

		// A caption track is still useful when the client sent nonsense, so these degrade to no offset
		// rather than failing the request. 400-ing here would let a cosmetic parameter take down
		// subtitle playback entirely.
		{name: "not a number", query: "lang=en&type=vtt&offset=soon", wantOK: false},
		{name: "float", query: "lang=en&type=vtt&offset=1.5", wantOK: false},
		{name: "empty", query: "lang=en&type=vtt&offset=", wantOK: false},
		{name: "overflows int64", query: "lang=en&type=vtt&offset=99999999999999999999999", wantOK: false},

		// The bound is what stops an untrusted query string overflowing the time.Duration
		// multiplication downstream.
		{name: "absurdly positive is rejected", query: "lang=en&type=vtt&offset=86400001", wantOK: false},
		{name: "absurdly negative is rejected", query: "lang=en&type=vtt&offset=-86400001", wantOK: false},
		{name: "exactly a day positive is accepted", query: "lang=en&type=vtt&offset=86400000", wantMS: 86400000, wantOK: true},
		{name: "exactly a day negative is accepted", query: "lang=en&type=vtt&offset=-86400000", wantMS: -86400000, wantOK: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/scene/1/caption?"+tt.query, nil)

			gotMS, gotOK := parseCaptionOffset(r)
			if gotOK != tt.wantOK {
				t.Fatalf("ok = %v, want %v", gotOK, tt.wantOK)
			}
			if gotOK && gotMS != tt.wantMS {
				t.Errorf("offset = %dms, want %dms", gotMS, tt.wantMS)
			}
		})
	}
}

// TestCaptionOffsetClampsAndDrops pins the behaviour that makes the offset safe, which lives in
// astisub's Subtitles.Add rather than in our code. If a future dependency bump changes it, this fails --
// which is the point: a negative timestamp in a WebVTT file is invalid, and browsers reject or silently
// truncate the whole track, so a viewer nudging subtitles earlier must not lose the rest of them.
func TestCaptionOffsetClampsAndDrops(t *testing.T) {
	mk := func(startMS, endMS int) astisub.Subtitles {
		return astisub.Subtitles{
			Items: []*astisub.Item{
				{StartAt: time.Duration(startMS) * time.Millisecond, EndAt: time.Duration(endMS) * time.Millisecond},
			},
		}
	}

	tests := []struct {
		name        string
		subs        astisub.Subtitles
		offsetMS    int64
		wantStartMS int
		wantEndMS   int
		wantItems   int
	}{
		{
			name:        "shifted later by 500ms",
			subs:        mk(1000, 2000),
			offsetMS:    500,
			wantStartMS: 1500,
			wantEndMS:   2500,
			wantItems:   1,
		},
		{
			name:        "shifted earlier by 500ms, staying positive",
			subs:        mk(1000, 2000),
			offsetMS:    -500,
			wantStartMS: 500,
			wantEndMS:   1500,
			wantItems:   1,
		},
		{
			// The interesting case: the cue straddles zero. Clamping the start to 0 keeps the line
			// visible; dropping it would silently lose text.
			name:        "straddling zero clamps rather than being dropped",
			subs:        mk(200, 2000),
			offsetMS:    -1000,
			wantStartMS: 0,
			wantEndMS:   1000,
			wantItems:   1,
		},
		{
			// Pushed entirely before the start of the video: cannot be shown, so it is dropped.
			name:      "entirely before zero is dropped",
			subs:      mk(100, 900),
			offsetMS:  -1000,
			wantItems: 0,
		},
		{
			name:        "no offset leaves the cue untouched",
			subs:        mk(1000, 2000),
			offsetMS:    0,
			wantStartMS: 1000,
			wantEndMS:   2000,
			wantItems:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subs := tt.subs
			subs.Add(time.Duration(tt.offsetMS) * time.Millisecond)

			if len(subs.Items) != tt.wantItems {
				t.Fatalf("items = %d, want %d", len(subs.Items), tt.wantItems)
			}
			if tt.wantItems == 0 {
				return
			}

			got := subs.Items[0]
			if got.StartAt != time.Duration(tt.wantStartMS)*time.Millisecond {
				t.Errorf("start = %v, want %dms", got.StartAt, tt.wantStartMS)
			}
			if got.EndAt != time.Duration(tt.wantEndMS)*time.Millisecond {
				t.Errorf("end = %v, want %dms", got.EndAt, tt.wantEndMS)
			}
		})
	}
}

// TestCaptionOffsetNeverProducesNegativeTimestamps is the invariant behind the clamp: no cue the
// offset produces may start before zero. WebVTT has no representation for a negative timestamp, so a
// single un-clamped cue makes the file invalid and browsers discard the track -- a viewer nudging
// subtitles 2s earlier would lose every line, not shift them.
func TestCaptionOffsetNeverProducesNegativeTimestamps(t *testing.T) {
	subs := astisub.Subtitles{
		Items: []*astisub.Item{
			{StartAt: 0, EndAt: 500 * time.Millisecond},
			{StartAt: 100 * time.Millisecond, EndAt: 900 * time.Millisecond},
			{StartAt: 5 * time.Second, EndAt: 6 * time.Second},
		},
	}

	subs.Add(-2 * time.Second)

	for i, item := range subs.Items {
		if item.StartAt < 0 {
			t.Errorf("item %d has negative start %v", i, item.StartAt)
		}
		if item.EndAt < item.StartAt {
			t.Errorf("item %d ends (%v) before it starts (%v)", i, item.EndAt, item.StartAt)
		}
	}
}

// TestShiftCaptionsIsTheWiring is the load-bearing test for #4771.
//
// The parse and clamp tests above all pass with the offset removed from the request path entirely — they
// exercise parseCaptionOffset and Subtitles.Add in isolation and know nothing about whether the handler
// applies either. That was verified by mutation, not assumed: deleting the offset logic from the handler
// left the suite green. So this test goes through the function the handler actually calls, and asserts on
// the mutated subtitle track.
func TestShiftCaptionsIsTheWiring(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		wantStartMS int
	}{
		{name: "the handler applies a positive offset", query: "lang=en&type=vtt&offset=2000", wantStartMS: 3000},
		{name: "the handler applies a negative offset", query: "lang=en&type=vtt&offset=-500", wantStartMS: 500},
		{name: "no offset leaves the track alone", query: "lang=en&type=vtt", wantStartMS: 1000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/scene/1/caption?"+tt.query, nil)
			sub := &astisub.Subtitles{
				Items: []*astisub.Item{
					{StartAt: time.Second, EndAt: 2 * time.Second},
				},
			}

			shiftCaptions(r, sub)

			if len(sub.Items) != 1 {
				t.Fatalf("items = %d, want 1", len(sub.Items))
			}
			if got := sub.Items[0].StartAt; got != time.Duration(tt.wantStartMS)*time.Millisecond {
				t.Errorf("start = %v, want %dms", got, tt.wantStartMS)
			}
		})
	}
}
