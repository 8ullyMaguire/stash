//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/sqlite"
)

// TestInviteStore_SingleUseUnderConcurrency is the race the plan calls out as
// the one that matters in this milestone.
//
// A read-then-write redemption has a window: two registrations both read
// uses=0, both see a valid key, both write uses=1, and one invite key mints two
// accounts. That is exactly what the single atomic UPDATE in RedeemInvite
// exists to prevent, and a sequential test cannot see it — it needs two
// goroutines hitting the same row at once.
//
// SQLite serialises writes, so the second UPDATE waits and then re-evaluates its
// WHERE clause against the committed value. That is the behaviour under test:
// exactly one redemption must win, and the losers must get an error rather than
// a silent success.
func TestInviteStore_SingleUseUnderConcurrency(t *testing.T) {
	// Real concurrency against the real database, so NOT inside withRollbackTxn:
	// a rollback transaction would serialise the writers and the race could not
	// occur. The rows are given unique names per run so a persistent dev
	// database cannot make a second run collide.
	suffix := time.Now().UnixNano()
	creatorName := "conc-inviter-" + itoa(suffix)

	users := sqlite.NewUserStore()
	creator := &models.User{Username: creatorName}
	require.NoError(t, inCommittedTxn(t, func(ctx context.Context) error {
		return users.Create(ctx, creator, []byte("hash"))
	}))

	invites := sqlite.NewInviteStore()
	keyHash := []byte("concurrent-key-" + itoa(suffix))
	// max_uses 1: the strongest form of the race. Two simultaneous
	// registrations, one key, one of them must lose.
	require.NoError(t, inCommittedTxn(t, func(ctx context.Context) error {
		return invites.CreateInvite(ctx, keyHash, creator.ID, nil, 1)
	}))

	const racers = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		success int
		errs    []error
	)

	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release all goroutines at once

			// A real, separate, COMMITTED transaction per racer. Sharing a
			// transaction would serialise the writers and the race could not
			// occur at all, so a green test here would prove nothing.
			err := inCommittedTxnNoT(func(ctx context.Context) error {
				_, err := invites.RedeemInvite(ctx, keyHash, time.Now())
				return err
			})

			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				success++
			} else {
				errs = append(errs, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.Equal(t, 1, success,
		"exactly one redemption of a single-use key may succeed; %d did, which means a leaked invite key can mint multiple accounts", success)
	assert.Len(t, errs, racers-1, "every loser must get an error, not a silent no-op")

	// And the row must show exactly one use consumed, not a lost update.
	// Redeeming again must fail, which is the same statement from the other side.
	err := inCommittedTxnNoT(func(ctx context.Context) error {
		_, err := invites.RedeemInvite(ctx, keyHash, time.Now())
		return err
	})
	assert.Error(t, err, "the key must be spent after its single use")
}

// TestUserSessionStore_ConcurrentCreateForSameUserKeepsBothSessions checks the
// other concurrency property the login path depends on: two logins from the same
// user are two independent sessions, and neither clobbers the other.
func TestUserSessionStore_ConcurrentCreateForSameUser(t *testing.T) {
	suffix := time.Now().UnixNano()
	users := sqlite.NewUserStore()
	u := &models.User{Username: "conc-user-" + itoa(suffix)}
	require.NoError(t, inCommittedTxn(t, func(ctx context.Context) error {
		return users.Create(ctx, u, []byte("hash"))
	}))

	store := sqlite.NewUserSessionStore()
	ids := [][]byte{
		[]byte("s1-" + itoa(suffix)), []byte("s2-" + itoa(suffix)),
		[]byte("s3-" + itoa(suffix)), []byte("s4-" + itoa(suffix)),
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(ids))
	for _, id := range ids {
		wg.Add(1)
		go func(id []byte) {
			defer wg.Done()
			errCh <- inCommittedTxnNoT(func(ctx context.Context) error {
				return store.Create(ctx, id, u.ID, time.Now().Add(time.Hour), "", "")
			})
		}(id)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err, "each session id is distinct, so no insert may collide")
	}

	// All four must resolve. A lost update here would log a user out of a
	// device they had just signed in on.
	for _, id := range ids {
		var (
			got   int
			found bool
		)
		require.NoError(t, inCommittedTxn(t, func(ctx context.Context) error {
			var err error
			got, found, err = store.FindSession(ctx, id, time.Now())
			return err
		}))
		require.True(t, found, "every concurrently created session must resolve")
		assert.Equal(t, u.ID, got)
	}
}

// inCommittedTxn runs f inside a real transaction that COMMITS, so its writes
// are visible to other transactions. The package's own withRollbackTxn helper is
// the opposite: it always rolls back, which is right for isolation between tests
// and useless for a race that needs committed state.
func inCommittedTxn(t *testing.T, f func(ctx context.Context) error) error {
	t.Helper()
	return inCommittedTxnNoT(f)
}

func inCommittedTxnNoT(f func(ctx context.Context) error) error {
	ctx, err := db.Begin(context.Background(), true)
	if err != nil {
		return err
	}

	if err := f(ctx); err != nil {
		// Roll back so a failing racer does not leave the key spent and make a
		// later assertion in this test meaningless.
		_ = db.Rollback(ctx)
		return err
	}
	return db.Commit(ctx)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
