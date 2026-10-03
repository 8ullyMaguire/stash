package api

// The three access-control directives, as runtime stubs.
//
// # WHY THESE EXIST AT ALL, GIVEN THEY DO NOTHING
//
// graphql/schema/types/access.graphql says it plainly: the directives "carry no runtime behaviour
// by themselves" and "no resolver consults them yet". They exist so the access DECISION is written
// down on the field, where a schema-walking test can require it and a reviewer can read it.
//
// That is a sound design. But it has one consequence that is easy to miss: gqlgen generates a
// `DirectiveRoot` with a field per directive, and the generated executor REFUSES a query whose
// directive has no implementation:
//
//	internal/api/generated_exec.go:26313
//	    if ec.directives.RequiresRole == nil {
//	        return zeroVal, errors.New("directive requiresRole is not implemented")
//	    }
//
// and server.go builds the schema as `NewExecutableSchema(Config{Resolvers: resolver})` -- with no
// `Directives:` field. So EVERY annotated query failed at runtime with:
//
//	{"errors":[{"message":"directive requiresRole is not implemented"}],"data":null}
//
// including `systemStatus`. The annotations were documentation; the omission made them an outage.
// A field annotated with a role was not merely unenforced, it was unreachable.
//
// # WHY PASSTHROUGH, NOT ENFORCEMENT
//
// Each stub calls `next` unconditionally. That is the honest state of the feature, and it is
// deliberately NOT quietly upgraded here: enforcement means walking the selection set of every
// query against the caller's role, which is a much larger piece of work and a security change that
// deserves its own review and its own tests. Writing a stub that LOOKS like it enforces (by
// returning an error for an unrecognised role, say) would be worse than no stub -- it would read as
// a control that is not one.
//
// The one thing these do check is that the ROLE NAME is one the ladder defines. An unknown role is a
// schema bug -- a typo like `requiresRole(role: "substriber")` would otherwise be silently
// meaningless, forever, with nothing to catch it. A typo in an access annotation should not be a
// silent no-op.

import (
	"context"
	"fmt"

	"github.com/99designs/gqlgen/graphql"
)

// accessRoleLadder is the ordered ladder from the spec's five-role model, lowest first. `public` is
// the no-login role and sits below subscriber.
var accessRoleLadder = map[string]int{
	"public":      0,
	"subscriber":  1,
	"contributor": 2,
	"steward":     3,
	"admin":       4,
}

// knownAccessRole reports whether a role name is on the ladder.
func knownAccessRole(role string) bool {
	_, ok := accessRoleLadder[role]
	return ok
}

// passthroughDirective is the shared body of all three stubs.
//
// The error names the directive and the role, because the realistic failure is a typo in a schema
// annotation and a bare "unknown role" would send someone looking through resolvers instead of at
// the field they just annotated.
func passthroughDirective(ctx context.Context, name, role string, next graphql.Resolver) (any, error) {
	if !knownAccessRole(role) {
		return nil, fmt.Errorf("@%s(role: %q) names a role that is not on the ladder %v", name, role,
			ladderNames())
	}
	return next(ctx)
}

// ladderNames lists the ladder in order, for the error message.
func ladderNames() []string {
	return []string{"public", "subscriber", "contributor", "steward", "admin"}
}

// AccessDirectives returns the DirectiveRoot for the executable schema.
//
// A function rather than a package-level var because it builds the ladderNames slice on each error
// path, and a shared mutable slice in a package var is a data race waiting for a second caller.
func AccessDirectives() DirectiveRoot {
	return DirectiveRoot{
		RequiresRole: func(ctx context.Context, obj any, next graphql.Resolver, role string) (any, error) {
			return passthroughDirective(ctx, "requiresRole", role, next)
		},
		RequiresWriteRole: func(ctx context.Context, obj any, next graphql.Resolver, role string) (any, error) {
			return passthroughDirective(ctx, "requiresWriteRole", role, next)
		},
		// @publicRead carries no argument: it is the marker meaning "deliberately readable by
		// anyone", so there is no role to validate.
		PublicRead: func(ctx context.Context, obj any, next graphql.Resolver) (any, error) {
			return next(ctx)
		},
	}
}

// NewSchemaConfig returns the gqlgen Config the server MUST build its schema from.
//
// # WHY THIS EXISTS RATHER THAN A `Directives:` FIELD AT THE CALL SITE
//
// The original bug was a missing field in a struct literal in server.go:
//
//	NewExecutableSchema(Config{Resolvers: resolver})          // no Directives -> every
//	                                                       // annotated query errors
//
// and no unit test could catch it, because the omission is not in the directive IMPLEMENTATION --
// which is fully tested -- but in the one place that assembles the schema. A test that asserted the
// source text of server.go would be the fourth such test in this package, and the first three
// (stashforge_media_gate_coverage_test.go among them) asserted buggy text and so certified the bug.
//
// So the schema is built in ONE place, here, and server.go calls it. There is no longer a literal for
// a caller to forget a field of: omitting the directives now means not calling this function, which
// is one edit and one line, rather than one word in a struct literal that reads as complete.
//
// This is the same reasoning as the #7238 `scenePaths` extraction, and it is the reason that one was
// worth doing: a branch nothing can reach is a branch nothing can test.
func NewSchemaConfig(resolvers ResolverRoot) Config {
	return Config{
		Resolvers:  resolvers,
		Directives: AccessDirectives(),
	}
}
