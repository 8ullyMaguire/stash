package api

import (
	"context"
	"crypto/tls"
	"errors"
	"testing"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/pkg/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A posture store that fails whichever reads the test tells it to.
//
// The two reads fail for different reasons and the difference matters: Mode
// unreadable is "we do not know what this instance is", WizardCompleted
// unreadable is "we do not know whether the operator consented". Both refuse,
// but a test that only ever fails both reads cannot tell a check that refuses
// because the mode is public from one that refuses because the database is down,
// and those are the two things an operator has to tell apart at 3am.
type fakePostureStore struct {
	mode      collab.Mode
	completed bool

	modeErr      error
	completedErr error
}

func (f *fakePostureStore) Mode(context.Context) (collab.Mode, error) {
	if f.modeErr != nil {
		return "", f.modeErr
	}
	return f.mode, nil
}

func (f *fakePostureStore) WizardCompleted(context.Context) (bool, error) {
	if f.completedErr != nil {
		return false, f.completedErr
	}
	return f.completed, nil
}

var errPostureUnreadable = errors.New("instance_settings is locked")

// TestCheckInstancePosture_RefusesThePosturesThatAreNotSafeToServe is the
// startup gate: a public instance may not serve plain HTTP, and a public
// instance whose wizard was never completed may not serve at all.
//
// The startup check is the last point at which this is cheap. Once a listener is
// up, a public posture over plain HTTP has already put session cookies and the
// instance key in the clear, and there is no way to un-send them.
func TestCheckInstancePosture_RefusesThePosturesThatAreNotSafeToServe(t *testing.T) {
	cases := []struct {
		name      string
		store     *fakePostureStore
		scheme    string
		wantError string
	}{
		{
			name:   "a public instance may serve over TLS",
			store:  &fakePostureStore{mode: collab.ModePublic, completed: true},
			scheme: "https",
		},
		{
			name:      "a public instance may NOT serve over plain HTTP",
			store:     &fakePostureStore{mode: collab.ModePublic, completed: true},
			scheme:    "http",
			wantError: "public mode requires HTTPS",
		},
		{
			name:   "a private instance may serve over plain HTTP",
			store:  &fakePostureStore{mode: collab.ModePrivate, completed: true},
			scheme: "http",
		},
		{
			name:   "a contribute instance may serve over plain HTTP",
			store:  &fakePostureStore{mode: collab.ModeContribute, completed: true},
			scheme: "http",
		},
		{
			// The reason the wizard gate exists: a public posture must be
			// chosen, not defaulted into. An instance whose wizard never ran
			// has expressed no consent at all, so public + not completed is
			// refused even over TLS.
			name:      "a public instance whose wizard never completed is refused outright",
			store:     &fakePostureStore{mode: collab.ModePublic, completed: false},
			scheme:    "https",
			wantError: "wizard",
		},
		{
			name:   "a private instance whose wizard never completed is merely undecided",
			store:  &fakePostureStore{mode: collab.ModePrivate, completed: false},
			scheme: "http",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkInstancePosture(context.Background(), tc.store, tc.scheme)
			if tc.wantError == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantError)
			assert.Contains(t, err.Error(), "refusing to start",
				"an operator has to be told this is a refusal, not a crash")
		})
	}
}

// TestCheckInstancePosture_FailsClosedOnAnUnreadableStore: a database that
// cannot be read must stop the boot, not downgrade the posture.
//
// The alternative -- treat an unreadable mode as "private" and carry on -- looks
// safe and is not: it means a public instance whose database hiccups comes back
// serving over plain HTTP, and a contribute instance's consent decision silently
// becomes "no". The failure is invisible until someone notices the posture, which
// is the worst time.
//
// Both reads are exercised separately, because both must refuse and a test that
// only fails one of them passes just as well if the other is dropped.
func TestCheckInstancePosture_FailsClosedOnAnUnreadableStore(t *testing.T) {
	cases := []struct {
		name      string
		store     *fakePostureStore
		wantError string
	}{
		{
			name: "an unreadable mode",
			store: &fakePostureStore{
				// The mode reads as PUBLIC here, and it still must not start:
				// the point is that the read failed, not what it would have said.
				mode:      collab.ModePublic,
				completed: true,
				modeErr:   errPostureUnreadable,
			},
			wantError: "cannot determine the instance mode",
		},
		{
			name: "an unreadable wizard flag",
			store: &fakePostureStore{
				mode:         collab.ModePublic,
				completed:    true,
				completedErr: errPostureUnreadable,
			},
			wantError: "cannot determine whether the first-run wizard completed",
		},
		{
			// The subtle one. Mode says public, over https, wizard says
			// complete -- the one configuration that is legal -- and the
			// wizard read fails. If the check consulted only the mode it
			// would start. It must not, because the one thing this gate
			// exists to establish is that public was CONSENTED to, and a
			// failed read establishes nothing.
			name: "an unreadable wizard flag refuses even a public instance over TLS",
			store: &fakePostureStore{
				mode:         collab.ModePublic,
				completed:    true,
				completedErr: errPostureUnreadable,
			},
			wantError: "cannot determine whether the first-run wizard completed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkInstancePosture(context.Background(), tc.store, "https")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantError)
			// The underlying cause has to survive: an operator reading only
			// the refusal learns nothing about which query failed.
			assert.ErrorIs(t, err, errPostureUnreadable,
				"the wrapped cause must be recoverable with errors.Is")
		})
	}
}

// TestCheckInstancePosture_DoesNotConsultTheModeTwice: one read each.
//
// A check that re-reads the mode to confirm what it just read is a check whose
// two reads can disagree, and the disagreement is resolved by whichever answer
// arrives last -- not by the more conservative of the two.
func TestCheckInstancePosture_DoesNotConsultTheModeTwice(t *testing.T) {
	store := &countingPostureStore{mode: collab.ModePublic, completed: true}
	require.NoError(t, checkInstancePosture(context.Background(), store, "https"))
	assert.Equal(t, 1, store.modeCalls, "the mode is read once, not re-confirmed")
	assert.Equal(t, 1, store.completedCalls, "the wizard flag is read once")
}

type countingPostureStore struct {
	mode           collab.Mode
	completed      bool
	modeCalls      int
	completedCalls int
}

func (c *countingPostureStore) Mode(context.Context) (collab.Mode, error) {
	c.modeCalls++
	return c.mode, nil
}

func (c *countingPostureStore) WizardCompleted(context.Context) (bool, error) {
	c.completedCalls++
	return c.completed, nil
}

// TestServerScheme_DerivesTheSchemeFromTheTLSConfig, never from an assumption.
//
// The mutation "the scheme is assumed to be https" survived the first version
// of this file, and it survived for a reason worth recording: every other test
// here hands checkInstancePosture a scheme string as an argument, so nothing
// could see where that string came from. Testing a function at the wrong seam
// tests its inputs and calls its behaviour untested.
//
// The dangerous direction is the one that matters: a server with no TLS config
// claiming to be https would pass a public instance's gate and then serve it in
// the clear, so the nil case is asserted to be "http" explicitly rather than
// just "not https".
func TestServerScheme_DerivesTheSchemeFromTheTLSConfig(t *testing.T) {
	cases := []struct {
		name string
		tls  *tls.Config
		want string
	}{
		{"no TLS configured, so plain HTTP", nil, "http"},
		{"TLS configured, so https", &tls.Config{MinVersion: tls.VersionTLS12}, "https"},
		// A zero-valued config is still a CONFIGURED config. Startup only
		// calls ListenAndServeTLS when TLSConfig != nil, so a non-nil empty
		// config does serve TLS and must be reported as https -- a check
		// that asked "is TLS enabled" by inspecting fields would get this
		// wrong and downgrade a TLS server to http.
		{"a zero-valued TLS config is still TLS", &tls.Config{}, "https"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{}
			s.TLSConfig = tc.tls
			assert.Equal(t, tc.want, s.scheme())
		})
	}
}

// TestServerScheme_AConfiguredTLSServerAndItsPostureAgree: the derived scheme is
// the one the gate is given.
//
// This is the seam the mutation could not reach -- the derivation and the check
// are only connected through Start(), so a change to one that ignores the other
// would leave every other test green. Asserting the two agree catches exactly
// that, and it is cheap because both are already functions.
func TestServerScheme_AConfiguredTLSServerAndItsPostureAgree(t *testing.T) {
	t.Run("no TLS, a public instance is refused", func(t *testing.T) {
		s := &Server{}
		s.manager = &manager.Manager{InstanceModeStore: &sqlite.InstanceModeStore{}}
		assert.Equal(t, "http", s.scheme())

		// A public instance over the scheme the server actually serves.
		err := collab.CheckStartup(collab.ModePublic, s.scheme(), true)
		require.Error(t, err, "the gate must refuse what scheme() reports")
	})

	t.Run("TLS, the same instance is allowed", func(t *testing.T) {
		s := &Server{}
		s.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		assert.Equal(t, "https", s.scheme())

		assert.NoError(t, collab.CheckStartup(collab.ModePublic, s.scheme(), true))
	})
}
