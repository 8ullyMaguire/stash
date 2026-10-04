package sqlite

import (
	"context"
	"fmt"
	"strings"
)

// Batched per-studio counts.
//
// WHY THIS EXISTS
// ===============
// internal/api/resolver_model_studio.go:86-165 has seven count field resolvers that each open their
// own read transaction and issue their own query. gqlgen invokes a field resolver once per object, so
// a page of N studios costs one query per field per studio. Measured with docs/qcount.sh on 12
// studios, using the fragment the studios page actually sends
// (ui/v2.5/graphql/data/studio.graphql, which asks for scene_count AND
// scene_count_all: scene_count(depth: -1) for each of six counts):
//
//	219 SQL statements, 18.2 per studio returned
//	 72x WITH RECURSIVE items AS (...)     <- 12 studios x 6 *_all fields
//	 24x each of scenes / galleries / groups / images / scene_markers counts
//
// The recursive CTE dominates because pkg/sqlite/criterion_handlers.go:692 already short-circuits
// depth==0 with an inline VALUES() literal, so those 72 walks are exactly the six depth=-1 fields
// across 12 studios.
//
// WHAT IS HERE
// ============
// One statement per count field for the WHOLE page instead of one per field per studio.
//
// Depth MUST be part of the batching key. Within a single request the client asks for the same
// resolver at two depths (scene_count and scene_count_all = scene_count(depth: -1)), so a cache
// keyed on studio ID alone would hand the depth=-1 answer to the depth=0 field -- fast and wrong.
// These helpers are therefore called once per distinct depth by the caller.
//
// Returned slices are indexed by input position via idToIndexMap, matching
// viewHistoryTable.getManyCount (pkg/sqlite/table.go:1049). A studio with no matching rows yields 0
// rather than shortening the slice, which would shift every later result onto the wrong studio and
// produce plausible numbers belonging to other rows. That misalignment is the failure mode to fear in
// this change: not a crash, wrong data.

// studioChildCount is one count field's SQL shape.
//
// The shape is expressed as a JOIN suffix rather than a whole FROM clause, because the batched query
// always does `FROM items LEFT JOIN <spec.join>`. Writing it the other way round produces invalid SQL
// for the multi-table shapes -- SQLite parses
//
//	LEFT JOIN performers_scenes AS ps INNER JOIN performers ON ... ON ps.studio_id = ...
//
// as a single join expression with two ON clauses, which is a syntax error. Keeping the root table in
// `from` and the rest in `joinSuffix` makes both shapes correct by construction.
type studioChildCount struct {
	// from is the root table, joined directly against the hierarchy.
	from string
	// withSuffix is an extra CTE for shapes that need one. It is emitted after `items` so it can
	// reference it. Empty for every shape whose studio link is a plain FK.
	withSuffix string
	// joinSuffix is any additional JOIN, including its own ON clause. Empty for most fields.
	joinSuffix string
	// idExpr is the DISTINCT-qualified id column being counted.
	idExpr string
	// studioExpr resolves to the studio id the counted row belongs to.
	studioExpr string
}

var studioSceneCount = studioChildCount{
	from:       sceneTable,
	idExpr:     "scenes.id",
	studioExpr: "scenes.studio_id",
}

var studioImageCount = studioChildCount{
	from:       imageTable,
	idExpr:     "images.id",
	studioExpr: "images.studio_id",
}

var studioGalleryCount = studioChildCount{
	from:       galleryTable,
	idExpr:     "galleries.id",
	studioExpr: "galleries.studio_id",
}

var studioGroupCount = studioChildCount{
	from:       groupTable,
	idExpr:     "groups.id",
	studioExpr: "groups.studio_id",
}

// studioPerformerCount reaches studios through SCENES, and needs its own CTE.
//
// There is no performer-to-studio FK at all. PRAGMA table_info confirms it: `performers` carries only
// an id, and the link is performers_scenes(performer_id, scene_id) -> scenes.studio_id. Two hops.
//
// The old per-studio query built this as a derived table named performer_studio -- which is why the
// first two attempts here failed with "no such table: performer_studio": that name is an alias created
// by a WITH clause in pkg/sqlite/performer_filter.go:636-654, not a table. This shape keeps the same
// structure, with `items` standing in for the old studio(root_id, item_id) CTE.
// The performer shape is the odd one out: it joins FROM the derived table rather than from the
// primary table, because the studio link lives on performer_studio and not on performers. That is not
// cosmetic -- SQLite rejects the other order with "ON clause references tables to its right", since
// the template emits `LEFT JOIN <from> ON <studioExpr> = items.item_id <joinSuffix>` and an ON clause
// may only mention tables already joined.
//
// So the shape overrides which table the studio is joined against, and the template puts the derived
// table first:
//
//	FROM items LEFT JOIN performer_studio ON performer_studio.studio_id = items.item_id
//	     INNER JOIN performers ON performers.id = performer_studio.performer_id
var studioPerformerCount = studioChildCount{
	// withSuffix is emitted after `items` in the CTE list, comma-separated. Without that comma SQLite
	// reports `near "performer_studio": syntax error`, pointing at the second CTE rather than at the
	// missing punctuation.
	withSuffix: `,
        performer_studio AS (
            SELECT DISTINCT ps.performer_id AS performer_id, s.studio_id AS studio_id
            FROM performers_scenes ps
            INNER JOIN scenes s ON ps.scene_id = s.id
        )`,
	from:       "performer_studio",
	joinSuffix: " INNER JOIN performers ON performers.id = performer_studio.performer_id",
	idExpr:     "performers.id",
	studioExpr: "performer_studio.studio_id",
}

// studioSceneMarkerCount reaches markers through scenes. Measured baseline:
//
//	SELECT DISTINCT scene_markers.id FROM scene_markers
//	INNER JOIN scenes ON scene_markers.scene_id = scenes.id
var studioSceneMarkerCount = studioChildCount{
	from:       "scene_markers",
	joinSuffix: " INNER JOIN scenes ON scene_markers.scene_id = scenes.id",
	idExpr:     "scene_markers.id",
	studioExpr: "scenes.studio_id",
}

// countStudioChildren returns the per-studio count of child rows for every id, indexed by input
// position.
//
// The hierarchy is resolved ONCE for the whole page (studioHierarchyDepth), then every studio's
// descendants become a VALUES(root_id, item_id) list in the same statement. That single VALUES list is
// the whole trick: one recursive walk covers every studio, and GROUP BY root_id hands each studio its
// own count back.
//
// DISTINCT matches the per-studio queries being replaced (each is SELECT COUNT(DISTINCT ...)), which
// matters for the join-shaped specs -- a performer in three scenes of one studio must count once.
func countStudioChildren(ctx context.Context, ids []int, spec studioChildCount, depth *int) ([]int, error) {
	ret := make([]int, len(ids))
	if len(ids) == 0 {
		return ret, nil
	}

	hierarchy, err := studioHierarchyDepth(ctx, ids, depth)
	if err != nil {
		return nil, err
	}

	idToIndex := idToIndexMap(ids)

	values := make([]string, 0, len(hierarchy))
	args := make([]interface{}, 0, 2*len(hierarchy))
	for _, id := range ids {
		for _, itemID := range hierarchy[id] {
			values = append(values, "(?,?)")
			args = append(args, id, itemID)
		}
	}
	if len(values) == 0 {
		return ret, nil
	}

	// The comma matters: `items` is the first CTE in the list, so anything following it has to be
	// comma-separated. Without it SQLite reports `near "performer_studio": syntax error`, which points
	// at the second CTE rather than at the missing punctuation.
	q := fmt.Sprintf(`
WITH items(root_id, item_id) AS (VALUES %s)%s
SELECT items.root_id AS root_id, COUNT(DISTINCT %s) AS c
FROM items
LEFT JOIN %s ON %s = items.item_id%s
GROUP BY items.root_id`,
		strings.Join(values, ", "),
		spec.withSuffix,
		spec.idExpr,
		spec.from,
		spec.studioExpr,
		spec.joinSuffix,
	)

	rows, err := dbWrapper.QueryxContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("querying %s count by studio: %w", spec.from, err)
	}
	defer rows.Close()

	for rows.Next() {
		var rootID, c int
		if err := rows.Scan(&rootID, &c); err != nil {
			return nil, fmt.Errorf("scanning %s count by studio: %w", spec.from, err)
		}
		if idx, ok := idToIndex[rootID]; ok {
			ret[idx] = c
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating %s count by studio: %w", spec.from, err)
	}

	return ret, nil
}

// studioHierarchyDepth resolves each studio id to the set of studio ids whose rows count toward it.
//
// Depth semantics are copied from getHierarchicalValues (criterion_handlers.go:679) so a batched
// count is identical to the per-studio one it replaces:
//
//	nil or 0 : the studio itself only
//	n > 0    : n levels of descendants
//	n < 0    : every level (depth: -1, which the *_all client aliases request)
//
// Cycle safety is inherited from the original formulation: UNION (not UNION ALL) plus the depth bound
// means a parent_id loop terminates. A studio in a cycle still resolves to its own row, which is what
// the per-studio query would have produced.
func studioHierarchyDepth(ctx context.Context, ids []int, depth *int) (map[int][]int, error) {
	hierarchy := make(map[int][]int, len(ids))
	if len(ids) == 0 {
		return hierarchy, nil
	}

	depthVal := 0
	if depth != nil {
		depthVal = *depth
	}

	if depthVal == 0 {
		// Same fast path the filter builder takes at criterion_handlers.go:692 -- no recursion at all.
		for _, id := range ids {
			hierarchy[id] = []int{id}
		}
		return hierarchy, nil
	}

	// A negative depth means unbounded, so it gets no WHERE clause -- which is exactly how depth=-1
	// is expressed upstream.
	var depthCondition string
	if depthVal > 0 {
		depthCondition = fmt.Sprintf("WHERE depth < %d", depthVal)
	}

	inList := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		inList[i] = "?"
		args[i] = id
	}

	q := fmt.Sprintf(`
WITH RECURSIVE items AS (
    SELECT id as root_id, id as item_id, 0 as depth FROM %s
    WHERE id IN (%s)
    UNION
    SELECT p.root_id, c.id, depth + 1 FROM %s c
    INNER JOIN items p ON c.parent_id = p.item_id
    %s
)
SELECT root_id, item_id FROM items`,
		studioTable,
		strings.Join(inList, ", "),
		studioTable,
		depthCondition,
	)

	rows, err := dbWrapper.QueryxContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("querying studio hierarchy: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var rootID, itemID int
		if err := rows.Scan(&rootID, &itemID); err != nil {
			return nil, fmt.Errorf("scanning studio hierarchy: %w", err)
		}
		hierarchy[rootID] = append(hierarchy[rootID], itemID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating studio hierarchy: %w", err)
	}

	// A studio that produced no row (filtered out of the page, or deleted mid-request) still needs an
	// entry, so the caller never has to tell "no descendants" apart from "not in the result".
	for _, id := range ids {
		if _, ok := hierarchy[id]; !ok {
			hierarchy[id] = []int{id}
		}
	}

	return hierarchy, nil
}
