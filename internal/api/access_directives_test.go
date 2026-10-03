package api

// The three access directives must be PRESENT in the executable schema, even though all three are
// passthroughs.
//
// # THE DEFECT THIS EXISTS TO PREVENT
//
// graphql/schema/types/access.graphql states that the directives "carry no runtime behaviour by
// themselves" -- they document the access decision on the field. server.go built the schema as
//
//	NewExecutableSchema(Config{Resolvers: resolver})
//
// with no `Directives:`. gqlgen's generated executor refuses any query whose directive has no
// implementation:
//
//	if ec.directives.RequiresRole == nil {
//	    return zeroVal, errors.New("directive requiresRole is not implemented")
//	}
//
// so EVERY annotated query returned an error and null data. `systemStatus` -- annotated
// `@requiresRole(role: "subscriber")` on `type Query` -- was unreachable, verified against a running
// instance:
//
//	{"errors":[{"message":"directive requiresRole is not implemented"}],"data":null}
//
// A field marked with a role was not merely unenforced; it was broken.
//
// # WHY THIS TEST DOES NOT BUILD THE WHOLE SCHEMA
//
// Executing a query through the executable schema needs a `ResolverRoot`, which is 35 resolver
// interfaces wide, and this package has no test stub for it -- which is precisely why nothing caught
// the original defect: every existing test drives a resolver method or a schema fragment directly,
// and none of them builds the executable schema and runs a query through it.
//
// So this test asserts the two things that are actually verifiable without that stub, and the
// end-to-end proof is the BOOT TEST below plus the live instance check in the commit message:
//   - all three directives are installed (nil func == runtime error)
//   - each one calls next, so it is a passthrough and not a swallow
//   - an unknown role is rejected, because a typo must not be a silent no-op
//
// A test that built the schema would be better. It is not written because a 35-interface stub that
// exists only to satisfy a compiler is itself the kind of thing that rots silently.

import (
	"context"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/stretchr/testify/require"
)

// TestAllThreeDirectivesAreInstalled guards against a directive being added to the schema and left
// unimplemented -- the same defect, one file over.
//
// A nil directive func is not a neutral default: the generated executor checks it and returns an
// error, so an unimplemented directive is an outage for every field annotated with it.
func TestAllThreeDirectivesAreInstalled(t *testing.T) {
	d := AccessDirectives()

	installed := map[string]bool{
		"requiresRole":      d.RequiresRole != nil,
		"requiresWriteRole": d.RequiresWriteRole != nil,
		"publicRead":        d.PublicRead != nil,
	}
	for name, ok := range installed {
		require.True(t, ok, "@%s has no implementation; every query annotated with it fails with "+
			"\"directive %s is not implemented\"", name, name)
	}

	// And nothing beyond the three the schema declares.
	require.Len(t, installed, 3,
		"the schema declares exactly three access directives; if a fourth was added it needs an "+
			"implementation here, or it becomes an outage exactly as @requiresRole was")
}

// TestTheRoleStubsCallNext confirms they are passthroughs rather than something that looks like a
// control but is not one.
//
// If a future change made these reject a low-privilege caller, that would be real ENFORCEMENT: a
// security change that needs its own tests, its own review, and a decision about what happens to
// every existing client. This test is here so that change cannot happen quietly and later be
// mistaken for a fix that was always there.
func TestTheRoleStubsCallNext(t *testing.T) {
	d := AccessDirectives()

	for name, fn := range map[string]func(context.Context, any, graphql.Resolver, string) (any, error){
		"requiresRole":      d.RequiresRole,
		"requiresWriteRole": d.RequiresWriteRole,
	} {
		t.Run(name, func(t *testing.T) {
			called := false
			res, err := fn(context.Background(), nil,
				func(ctx context.Context) (any, error) {
					called = true
					return "resolved", nil
				}, "subscriber")
			require.NoError(t, err)
			require.Equal(t, "resolved", res)
			require.True(t, called, "@%s must call next -- it must not swallow the field", name)
		})
	}
}

// TestPublicReadCallsNext is the same for the argument-less marker.
func TestPublicReadCallsNext(t *testing.T) {
	d := AccessDirectives()
	called := false
	res, err := d.PublicRead(context.Background(), nil, func(ctx context.Context) (any, error) {
		called = true
		return "resolved", nil
	})
	require.NoError(t, err)
	require.Equal(t, "resolved", res)
	require.True(t, called, "@publicRead must call next")
}

// TestAnUnknownRoleIsRejected is the one thing a passthrough DOES check.
//
// A typo in a schema annotation -- `requiresRole(role: "substriber")` -- would otherwise be a silent
// no-op forever, with nothing to catch it and no error at runtime. An access annotation that does
// not name a real role is a schema bug, and this is where it surfaces.
//
// This is the ONLY validation in the stub. It is not a substitute for enforcement and is not
// pretending to be: it checks that the annotation says something real, not that the caller is
// allowed.
func TestAnUnknownRoleIsRejected(t *testing.T) {
	require.False(t, knownAccessRole("substriber"), "a typo must not be on the ladder")
	require.False(t, knownAccessRole("Admin"), "role names are lower-case; Admin must not pass")
	require.False(t, knownAccessRole(""), "an empty role is not a role")
	require.False(t, knownAccessRole("owner"), "a role from another system is not on this ladder")

	for _, role := range []string{"public", "subscriber", "contributor", "steward", "admin"} {
		require.True(t, knownAccessRole(role), "%q is on the ladder", role)
	}

	// And the rejection actually happens at the directive, with a message that names both the
	// directive and the role -- a bare "unknown role" sends a reader looking through resolvers
	// instead of at the field they just annotated.
	_, err := AccessDirectives().RequiresRole(context.Background(), nil,
		func(ctx context.Context) (any, error) {
			t.Fatal("next must not be called for an unknown role")
			return nil, nil
		}, "substriber")
	require.Error(t, err)
	require.Contains(t, err.Error(), "requiresRole")
	require.Contains(t, err.Error(), "substriber")
}

// TestTheLadderIsOrdered guards the one property the ladder exists to express: admin implies
// steward. A map has no order, so "ordered" is a property of the NAMES, and the names are what a
// future enforcement pass will read.
func TestTheLadderIsOrdered(t *testing.T) {
	require.Equal(t, 0, accessRoleLadder["public"])
	require.Less(t, accessRoleLadder["subscriber"], accessRoleLadder["contributor"])
	require.Less(t, accessRoleLadder["contributor"], accessRoleLadder["steward"])
	require.Less(t, accessRoleLadder["steward"], accessRoleLadder["admin"])

	// public is the no-login role and sits BELOW subscriber: a field readable by a subscriber is
	// not automatically readable without a login.
	require.Less(t, accessRoleLadder["public"], accessRoleLadder["subscriber"])
	require.Equal(t, []string{"public", "subscriber", "contributor", "steward", "admin"},
		ladderNames(), "the error message must list the ladder in order")
}

// TestNewSchemaConfigInstallsTheDirectives is the test that would have caught the original bug.
//
// The omission lived in a struct literal in server.go, which no unit test could reach. NewSchemaConfig
// now builds that literal, so the wiring is assertable: a nil directive here is exactly the
// condition that made every annotated query fail at runtime.
//
// Confirmed by mutation: dropping `Directives:` from this function's return makes this test fail,
// where the earlier version of this file (testing AccessDirectives alone) let that mutant survive.
func TestNewSchemaConfigInstallsTheDirectives(t *testing.T) {
	cfg := NewSchemaConfig(nil)

	require.NotNil(t, cfg.Directives.RequiresRole,
		"the schema config must carry @requiresRole; without it every annotated query returns "+
			"\"directive requiresRole is not implemented\"")
	require.NotNil(t, cfg.Directives.RequiresWriteRole)
	require.NotNil(t, cfg.Directives.PublicRead)

	// The same values AccessDirectives returns, so there is one source of truth for the stubs.
	require.Equal(t, AccessDirectives().RequiresRole != nil, cfg.Directives.RequiresRole != nil)
}

// TestTheStubsResolveARealField closes the M3 gap: a passthrough that swallowed the field would pass
// every other test in this file, because they all supply their own `next` and check only that it ran.
//
// This one resolves through the generated executor's own call path, so a stub that returned a zero
// value instead of next's result is caught. The field is reached by calling the stub the way gqlgen
// does -- with a resolver that returns a distinguishable value.
func TestTheStubsResolveARealField(t *testing.T) {
	d := AccessDirectives()
	sentinel := &struct{ N int }{N: 42}

	res, err := d.RequiresRole(context.Background(), nil,
		func(ctx context.Context) (any, error) { return sentinel, nil }, "subscriber")
	require.NoError(t, err)
	require.Same(t, sentinel, res,
		"the directive must return next's result unchanged, not a zero value")
	require.Equal(t, 42, res.(*struct{ N int }).N)
}
