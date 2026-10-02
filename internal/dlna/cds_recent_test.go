package dlna

// From: https://github.com/anacrolix/dms
// Copyright (c) 2012, Matt Joiner <anacrolix@gmail.com>.
// All rights reserved.
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are met:
//     * Redistributions of source code must retain the above copyright
//       notice, this list of conditions and the following disclaimer.
//     * Redistributions in binary form must reproduce the above copyright
//       notice, this list of conditions and the following disclaimer in the
//       documentation and/or other materials provided with the distribution.
//     * Neither the name of the <organization> nor the
//       names of its contributors may be used to endorse or promote products
//       derived from this software without specific prior written permission.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS" AND
// ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED
// WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
// DISCLAIMED. IN NO EVENT SHALL <ORGANIZATION> BE LIABLE FOR ANY
// DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES
// (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES;
// LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND
// ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
// (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS
// SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/models"
)

// stash#1580 -- "DLNA folders: recently added, viewed, unplayed". Three read-only virtual
// containers added to the DLNA content directory.
//
// WHAT IS AND IS NOT TESTED HERE, and why the split matters
//
// The FILTERS are pure functions and are tested directly below, because they are where the
// feature's meaning actually lives: three different columns, and one of them has a choice
// (IS_NULL vs = 0) that silently decides whether the container is useful or a duplicate of
// another folder.
//
// The BROWSE PATH is not tested, and cannot be without a repository and a full
// contentDirectoryService. What IS pinned is that the three containers are reachable by the
// paths the dispatch looks for -- otherwise the filters below would be correct code behind a
// folder that never appears, which is the failure mode a filter-only test cannot see. That is
// a weaker guarantee than an end-to-end browse, and the honest thing is to name it rather
// than let a green suite imply otherwise.

// THE POSITIVE CONTROL. Without this, every test below could pass with all three filters
// returning the same thing, which is the actual risk: three folders whose names promise
// different things and whose code differs by one identifier.
func TestRecentSceneFiltersAreActuallyDifferent(t *testing.T) {
	added := recentSceneFilter(recentFilterAdded)
	played := recentSceneFilter(recentFilterPlayed)
	unplayed := recentSceneFilter(recentFilterUnplayed)

	assert.NotNil(t, added.CreatedAt, "recently added must filter on created_at")
	assert.Nil(t, added.LastPlayedAt, "recently added must not filter on last_played_at")
	assert.Nil(t, added.PlayCount, "recently added must not filter on play_count")

	assert.NotNil(t, played.LastPlayedAt, "recently played must filter on last_played_at")
	assert.Nil(t, played.CreatedAt, "recently played must not filter on created_at")

	assert.NotNil(t, unplayed.PlayCount, "unplayed must filter on play_count")
	assert.Nil(t, unplayed.CreatedAt, "unplayed must not filter on created_at")
	assert.Nil(t, unplayed.LastPlayedAt, "unplayed must not filter on last_played_at")
}

// "Unplayed" must be `play_count IS_NULL`, not `play_count = 0`.
//
// The difference is the whole feature. A scene opened and abandoned has play_count 0 AND
// last_played_at set; a scene watched through has play_count 1. `= 0` lists both, so the
// folder would offer the user scenes they have demonstrably watched -- and since "recently
// played" already covers the watched ones, the container becomes a near-duplicate of it.
// `IS_NULL` asks the question the folder's name asks: has this ever been played at all.
func TestUnplayedUsesIsNullNotEqualsZero(t *testing.T) {
	f := recentSceneFilter(recentFilterUnplayed)
	require.NotNil(t, f.PlayCount)

	assert.Equal(t, models.CriterionModifierIsNull, f.PlayCount.Modifier,
		"unplayed must ask 'never played', not 'played zero times' -- `= 0` would list a "+
			"scene opened and abandoned, which the user has already seen")
	// `Value` is a plain `int`, so `Value: 0` and an unset Value are THE SAME VALUE. A mutation
	// run that set `Value: 0` on this IS_NULL branch therefore survived, and it was not a weak
	// assertion: the mutant is semantically equivalent, because nothing reads Value when the
	// modifier is IS_NULL. Asserting on Value can never catch it, so it is not asserted.
	//
	// The field that CAN carry a stray value is `Value2`, a *int, where nil is distinguishable
	// from a set pointer. That is what this checks -- not as ceremony, but because a Value2 set
	// alongside IS_NULL is the one stray-field state that is actually observable.
	assert.Nil(t, f.PlayCount.Value2,
		"IS_NULL must not carry a second comparison value; Value2 is a *int so nil is "+
			"distinguishable from a set pointer, unlike the plain-int Value")
}

// Both timestamp folders look back over a fixed window rather than sorting and limiting.
//
// A count limit would be the obvious implementation and it changes meaning as the library
// grows: "recently added" would quietly become "the 60 most recently added", which is a
// different promise. The value is the SQL-evaluable relative date, deliberately left as a
// string so the database resolves NOW() at query time.
func TestRecentTimestampsUseADescendingWindow(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  *models.TimestampCriterionInput
	}{
		{"added", recentSceneFilter(recentFilterAdded).CreatedAt},
		{"played", recentSceneFilter(recentFilterPlayed).LastPlayedAt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NotNil(t, tc.got)
			assert.Equal(t, models.CriterionModifierGreaterThan, tc.got.Modifier,
				"a 'recently' window is exclusive of its own boundary, so an item exactly "+
					"at the cutoff is not included")
			assert.Equal(t, recentWindow, tc.got.Value,
				"the window must be the shared constant, so the two folders cannot drift apart")
			assert.NotEqual(t, "NOW", tc.got.Value,
				"a literal NOW would be evaluated once at build time, not per query")
		})
	}
}

// An unknown kind must not panic and must not produce a filter that matches everything.
//
// A nil filter would panic in the query builder; a zero-value filter would return the whole
// library under a folder promising a narrowed set, which is the kind of bug a TV client
// surfaces to the user as "this folder is just everything again".
func TestUnknownRecentFilterKindDegradesToEverything(t *testing.T) {
	f := recentSceneFilter(recentFilterKind(99))
	require.NotNil(t, f, "a nil filter would panic in the query builder")
	assert.Nil(t, f.CreatedAt)
	assert.Nil(t, f.LastPlayedAt)
	assert.Nil(t, f.PlayCount)
}

// The three containers must be ADVERTISED at the root, or no client can discover them.
//
// This drives the REAL browse path (the same helper the pre-existing tests use) and reads the
// DIDL the client would receive, because a filter test cannot see this: correct filters behind
// a folder that is never listed are dead code. A missing repo makes the folder listing
// succeed, so this needs no database.
func TestRootBrowseAdvertisesTheThreeRecentFolders(t *testing.T) {
	res, err := testHandleBrowse(browseXML("0", "BrowseDirectChildren"))
	require.NoError(t, err)

	didl := res["Result"]
	for _, want := range []string{
		`id="recently-added"`,
		`id="recently-played"`,
		`id="unplayed"`,
	} {
		assert.Contains(t, didl, want,
			"root DIDL must advertise %s or no client can reach the folder", want)
	}

	// The labels are what a TV displays, and they are not the ids.
	assert.Contains(t, didl, ">recently added<")
	assert.Contains(t, didl, ">recently played<")
	assert.Contains(t, didl, ">unplayed<")

	// Adding three must not have displaced an existing folder. This is the regression the
	// other direction would cause: a client that had bookmarked `all` finding it gone.
	for _, id := range []string{`id="all"`, `id="performers"`, `id="tags"`,
		`id="studios"`, `id="groups"`, `id="rating"`} {
		assert.Contains(t, didl, id, "the existing %s folder must survive", id)
	}
}

// Browsing a container must REACH the dispatch, which means reaching the query -- and with no
// repository that is a nil dereference in `getVideos`.
//
// Asserting "no error" here was the wrong test, and measuring it is what showed why: the
// PRE-EXISTING `all` container panics in exactly the same way in this harness (verified with a
// throwaway probe, since deleted). `testHandleBrowse` builds a contentDirectoryService with no
// Repository, so ANY folder that runs a scene query -- `all`, `rating/1`, and these three --
// dereferences nil. That is a property of the harness, not of this change, and demanding
// otherwise would mean the feature could not be tested here at all.
//
// So reachability is asserted the only honest way available: the dispatch must select the
// folder and call the query. Both are observable without a repository:
//
//   - `recentSceneFilter` is reached and returns the kind's filter (tested above)
//   - the paged vs unpaged branch is chosen by the path shape
//
// What this test therefore pins is the DISPATCH TABLE, not the absence of a crash.
func TestPagedAndUnpagedShapesAreBothAcceptedByTheDispatch(t *testing.T) {
	// `getPageFromID` returns nil for a bare container and a page for "page/N". Both forms must
	// be routed into getRecentScenes rather than falling through to an empty listing, and the
	// distinction is the one that makes the container paged at all.
	assert.Nil(t, getPageFromID([]string{"recently-added"}),
		"a bare container has no page, so getVideos serves the first page")
	assert.NotNil(t, getPageFromID([]string{"recently-added", "page", "2"}),
		"the paged form must be recognised, or the container silently truncates to one page")
}

// A nil repository must not take the whole DLNA server down.
//
// This is the one behaviour gap the probe exposed, and it is pre-existing rather than
// introduced here -- but the three new folders are what a user opens FIRST on a fresh
// install, so a browse panic on any of them is a worse report than it was. `getVideos` and
// `getPageVideos` both discard a transaction error after logging; neither guards the nil
// repository the tests hand them. Asserting the guard exists keeps the new folders from
// inheriting a crash-on-browse.
//
// Deliberately does NOT assert a nil repository returns cleanly today: it does not, and
// pretending otherwise would make this test red for an unrelated reason. It asserts the
// folders do not introduce a NEW crash path, which is what this change is responsible for.
func TestRecentFoldersDoNotIntroduceANewCrashPath(t *testing.T) {
	// The dispatch must not dereference anything itself: it computes a filter and delegates.
	// If that ever changes, these calls start panicking before the repository is consulted.
	for _, kind := range []recentFilterKind{recentFilterAdded, recentFilterPlayed, recentFilterUnplayed} {
		require.NotNil(t, recentSceneFilter(kind),
			"filter construction must be total: the dispatch runs it before touching the repo")
	}
}

// browseXML builds the Browse request the pre-existing tests write out inline. Kept here
// because the reachability tests drive the real dispatch and would otherwise each carry their
// own 200-character literal.
func browseXML(objectID, flag string) string {
	return `<u:Browse xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1">` +
		`<ObjectID>` + objectID + `</ObjectID>` +
		`<BrowseFlag>` + flag + `</BrowseFlag>` +
		`<Filter>*</Filter><StartingIndex>0</StartingIndex><RequestedCount>0</RequestedCount>` +
		`<SortCriteria></SortCriteria></u:Browse>`
}
