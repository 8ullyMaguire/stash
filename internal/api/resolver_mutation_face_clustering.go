package api

import (
	"context"
	"strconv"

	"github.com/stashapp/stash/internal/manager"
)

// The face-clustering mutation.
//
// # The shape of it
//
// Four lines, like every other job-enqueueing mutation here, and for the same
// reason: the decision about WHICH videos to examine, WHAT counts as the same
// person, and HOW a cluster is merged all live in the job and the domain, and a
// resolver that had a copy of any of them would be a second implementation
// that nobody tests.
//
// # The refusal is not an accident
//
// Until the observation stage has a model, FaceClustering returns an error and
// no job. That is the honest response to a button that cannot work, and it is
// deliberately the manager's error rather than a check here: the manager is
// where the missing model is discovered, and a resolver that re-implemented the
// check would be checking something else.
func (r *mutationResolver) FaceClustering(ctx context.Context) (string, error) {
	jobID, err := manager.GetInstance().FaceClustering(ctx)
	if err != nil {
		return "", err
	}

	return strconv.Itoa(jobID), nil
}
