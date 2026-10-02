//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/sqlite"
)

// TestTheSQLFragmentAgreesWithTheGoImplementationOnEveryCase — the SQL aggregate and
// scene.go's Go reduction are the SAME RULE written twice, so the only question that matters
// is whether they can ever disagree.
//
// They are used for different things and a disagreement is silent in both directions: the Go
// one decides what a user SEES on a scene card, the SQL one decides what the library total
// and the `scenes_duration` sorts SAY. A library totalling 2h while every card reads 1h is
// the kind of bug nobody reports, because neither number looks broken.
//
// One table drives both assertions, so a disagreement names its own case. Each case is a
// (start, end, fileDuration) triple with the duration both sides must produce.
func TestTheSQLFragmentAgreesWithTheGoImplementationOnEveryCase(t *testing.T) {
	type tc struct {
		name       string
		start, end interface{}
		fileDur    float64
		want       float64
	}

	cases := []tc{
		{"unranged is the whole file", nil, nil, 2700, 2700},
		{"bounded window", 10.0, 40.0, 2700, 30},
		{"open tail runs to the end", 2400.0, nil, 2700, 300},
		{"open head starts at the beginning", nil, 600.0, 2700, 600},
		{"zero start is the first second", 0.0, 600.0, 2700, 600},
		{"start overruns the file, no end", 2800.0, nil, 2700, 0},
		{"end overruns the file", 2600.0, 2900.0, 2700, 100},
		{"start exactly at the file's end", 2700.0, nil, 2700, 0},
		{"whole file spelled out", 0.0, 2700.0, 2700, 2700},
		{"bounded window, unknown file duration", 10.0, 40.0, 0, 30},
		{"open tail, unknown file duration", 10.0, nil, 0, 0},
		// This case exists for ONE reason: it is the only shape that reaches the second arm
		// of MIN, the COALESCE(NULLIF(duration,0), 999999999). With end NULL and the file's
		// duration 0, the first arm is 0 and the second arm decides -- remove the NULLIF and
		// it becomes 0, collapsing the range. Every other case either sets end (first arm
		// wins outright) or has a real duration (NULLIF is a no-op), which is why the mutant
		// "treat unknown duration as 0" survived until this row was added.
		{"open tail, unknown duration, start 0", 0.0, nil, 0, 0},
	}

	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sfTxn(t, func(ctx context.Context) {
				id := mkRangeVideo(t, ctx, fmt.Sprintf("agg%02d.mp4", i), c.fileDur)
				setRange(t, ctx, id, c.start, c.end)

				// The Go reduction, as the UI sees it.
				assert.Equal(t, c.want, rangeVideoDuration(t, ctx, id),
					"the GO reduction disagrees with the case table")

				// The SQL fragment, as the aggregates compute it.
				sqlDur := scalar(t, ctx,
					"SELECT "+sqlite.SceneRangeDurationSQL+
						" FROM scenes_files"+
						" JOIN video_files ON video_files.file_id = scenes_files.file_id"+
						" WHERE scenes_files.scene_id = ?", id)

				require.NotNil(t, sqlDur, "the SQL fragment returned NULL, which is never a duration")
				assert.Equal(t, c.want, toFloat(sqlDur),
					"the SQL fragment disagrees with the Go reduction: same rule, two "+
						"implementations, and a library total that contradicts the cards")
			})
		})
	}
}

// toFloat coerces whatever the driver returned into a float64, so the assertion above reads
// as a number rather than as a type switch.
func toFloat(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int64:
		return float64(n)
	case int:
		return float64(n)
	default:
		return -1
	}
}
