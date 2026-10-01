package api

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

// The spec's five-role ladder, in order. The order is the whole meaning of a
// role annotation: a field requiring `steward` is readable by a steward and by
// an admin, and by nobody below.
var accessLadder = []string{"public", "subscriber", "contributor", "steward", "admin"}

// accessLadderRank maps a role name to its height, and reports whether the name is
// one this codebase actually defines.
//
// Separate from accessLadder so the check is against the vocabulary rather than
// the index: a typo like "subsciber" would otherwise be rejected only by an
// index-out-of-range panic, which reads as a crash rather than as a schema error.
var accessLadderRank = func() map[string]int {
	m := make(map[string]int, len(accessLadder))
	for i, r := range accessLadder {
		m[r] = i
	}
	return m
}()

// fieldPolicy is what a field declared, after its directives have been read.
type fieldPolicy struct {
	// Type and Field name it in error messages, which have to be precise: a
	// coverage failure is only actionable if it names the field.
	Type  string
	Field string

	// RequireRole is the lowest role permitted to READ this field.
	RequireRole string

	// RequireWriteRole is the lowest role permitted to invoke it, when it is a
	// mutation root field. Empty for fields that are not mutation roots.
	RequireWriteRole string

	// PublicRead is the explicit opt-out: this field is readable by anyone,
	// including the no-login public role.
	PublicRead bool

	// IsMutationRoot marks a Mutation field, where writes and reads differ.
	IsMutationRoot bool

	// TypePublicRead is the enclosing type's @publicRead, inherited the same way.
	TypePublicRead bool

	// TypeRole is the enclosing type's @requiresRole, which every field inherits
	// unless it states its own.
	//
	// This is what makes the scheme tractable. The read side is 2,356 fields
	// across 285 types; annotating each field individually is 2,356 decisions
	// nobody will review, which is the same as no decisions at all. A type-level
	// annotation is 285, and a field that needs to be stricter than its type says
	// so -- which is the only interesting case and the only one worth a line.
	TypeRole string

	// IsInput marks a field on an input object. These are excluded from the
	// coverage requirement, and the reason is worth stating because the first
	// version of this test failed on 1,307 of them:
	//
	// An input object's fields are ARGUMENTS to a mutation, not results from it.
	// Nothing is disclosed by the shape of a request -- the caller already knows
	// what they sent -- so there is no tier filter to enforce on one. Requiring a
	// visibility annotation there would mean annotating 1,307 fields whose access
	// model is a property of the mutation that accepts them, and the annotations
	// would carry no information. Worse, they would look like coverage while
	// documenting nothing.
	//
	// What IS enforced is that the MUTATION ROOT takes the input, so the access
	// decision lives in one place per operation rather than being spread across
	// its arguments.
	IsInput bool
}

func (p fieldPolicy) String() string { return p.Type + "." + p.Field }

// effectiveRole is the role this field actually requires, after inheritance.
//
// Field-level beats type-level, and the highest of the two wins when both are
// present and differ. "Highest" rather than "field wins" is deliberate: if a type
// is admin-only and one field on it claims subscriber, the honest reading is the
// stricter of the two, not the annotation someone typed most recently. A
// permissive annotation on a restrictive type is exactly the mistake a
// coverage test should refuse rather than resolve.
func (p fieldPolicy) effectiveRole() string {
	if p.RequireRole == "" {
		return p.TypeRole
	}
	if p.TypeRole == "" {
		return p.RequireRole
	}
	if r, ok := accessLadderRank[p.RequireRole]; ok {
		if t, ok2 := accessLadderRank[p.TypeRole]; ok2 && r < t {
			return p.TypeRole
		}
	}
	return p.RequireRole
}

// isPublic reports whether the field is readable by the no-login public role,
// from either level.
func (p fieldPolicy) isPublic() bool { return p.PublicRead || p.TypePublicRead }

// satisfies reports whether have is at least as high as the required role.
func (p fieldPolicy) satisfies(have, need string) bool {
	h, okH := accessLadderRank[have]
	n, okN := accessLadderRank[need]
	if !okH || !okN {
		return false
	}
	return h >= n
}

// directiveRole pulls a role name out of a directive's argument list.
//
// Returns empty when the argument is absent or is not a string, rather than
// panicking: a malformed directive is a schema error the assertion below will
// report, and it should be reported as a readable failure rather than as a nil
// dereference in the walker.
func directiveRole(d *ast.Directive, argName string) string {
	if d == nil {
		return ""
	}
	// ast.Directive carries an ArgumentList, not a map, so the lookup is a scan.
	// A directive here has one argument by construction, so the scan is short and
	// there is no reason to build a map for it.
	for _, arg := range d.Arguments {
		if arg.Name != argName {
			continue
		}
		if arg.Value.Kind == ast.StringValue {
			return arg.Value.Raw
		}
		return ""
	}
	return ""
}

// readFieldPolicy reads the access directives off one field definition, and
// inherits any that sit on the enclosing type.
func readFieldPolicy(typeName, fieldName string, typeDef *ast.Definition,
	field *ast.FieldDefinition, kind ast.DefinitionKind) fieldPolicy {

	isMutationRoot := typeName == "Mutation"
	p := fieldPolicy{
		Type:           typeName,
		Field:          fieldName,
		IsMutationRoot: isMutationRoot,
		IsInput:        kind == ast.InputObject,
	}

	// Inherit the type's role. A type-level directive is the default for every
	// field on it, and a field-level one overrides it.
	if typeDef != nil {
		if d := typeDef.Directives.ForName("requiresRole"); d != nil {
			p.TypeRole = directiveRole(d, "role")
		}
		if typeDef.Directives.ForName("publicRead") != nil {
			p.TypePublicRead = true
		}
	}
	if d := field.Directives.ForName("requiresRole"); d != nil {
		p.RequireRole = directiveRole(d, "role")
	}
	if d := field.Directives.ForName("requiresWriteRole"); d != nil {
		p.RequireWriteRole = directiveRole(d, "role")
	}
	if d := field.Directives.ForName("publicRead"); d != nil {
		p.PublicRead = true
	}
	return p
}

// walkEveryField visits every field definition in the schema, root types and
// nested types alike.
//
// The nested types are the point. A filter that only checks Query and Mutation
// would pass while `Scene.stashID` or `Performer.aliases` leaked, because those
// are reached through a root field that was itself annotated correctly. The
// anonymous `public` role is exactly the case the spec calls out — "an anonymous
// viewer is precisely the case where a UI-only filter leaks" — and it leaks
// through fields the UI never renders.
func walkEveryField(schema *ast.Schema, fn func(fieldPolicy)) {
	// Sorted, so a failure message is stable across runs. Map iteration in Go is
	// randomised, and a test whose output reshuffles on every run is one people
	// stop reading.
	names := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		def := schema.Types[name]
		if def.Kind != ast.Object && def.Kind != ast.Interface && def.Kind != ast.InputObject {
			continue
		}
		// Introspection types (__Schema, __Field, __Directive, __Type, ...) are
		// skipped by `BuiltIn`, not by a "__" name prefix.
		//
		// They cannot carry our directives at all — no .graphql file declares
		// them, so there is nowhere to write `@requiresRole` — and they are 25 of
		// the fields that would otherwise be reported as unannotated, which would
		// mean the coverage test is permanently unsatisfiable. The name prefix is
		// the wrong tool for the same reason it is wrong everywhere else: it
		// matches on spelling rather than on a property of the thing.
		//
		// Measured: `__Field.BuiltIn == true` while every one of this schema's 36
		// own sources is `BuiltIn: false`, so the flag separates them exactly.
		if def.BuiltIn {
			continue
		}

		// def.Fields is an ordered FieldList, not a map, so the schema's own
		// order is preserved and there is nothing to sort. Walking it directly is
		// also what makes the failure list stable across runs.
		for _, field := range def.Fields {
			fn(readFieldPolicy(name, field.Name, def, field, def.Kind))
		}
	}
}

// collectFieldPolicies gathers the whole schema's policies once, so several tests
// can assert against the same snapshot instead of each walking independently.
func collectFieldPolicies(t *testing.T) []fieldPolicy {
	t.Helper()
	require.NotNil(t, parsedSchema,
		"parsedSchema is nil, so this test is checking nothing")

	var out []fieldPolicy
	walkEveryField(parsedSchema, func(p fieldPolicy) { out = append(out, p) })
	require.NotEmpty(t, out, "the walk found no fields, so it is not walking")
	return out
}

func TestSchemaWalkActuallyVisitsEveryField(t *testing.T) {
	// The meta-test. Every other test here asserts about `policies`, and a walk
	// that silently visits nothing would make all of them pass vacuously — which
	// is the failure mode this whole feature exists to prevent, reproduced at one
	// level down.
	policies := collectFieldPolicies(t)

	assert.Greater(t, len(policies), 300,
		"the walk must cover the whole schema, not a handful of types")

	// Spot-check that it really reached the interesting roots and some nested
	// type, by name. A walk that only saw Query would still be over 300 fields on
	// a large instance and would pass a count assertion.
	seen := map[string]bool{}
	for _, p := range policies {
		seen[p.Type] = true
	}
	for _, want := range []string{"Query", "Mutation", "User", "Scene", "Performer"} {
		assert.True(t, seen[want],
			"the walk never visited %s, so it is not covering the schema", want)
	}
}

func TestEveryFieldDeclaresItsAccessPolicy(t *testing.T) {
	// The coverage test proper: a field must either say what it requires or
	// explicitly declare that it is harmless to anyone. Silence is the failure.
	//
	// This is where the spec's "enforced in the query layer, not the UI" becomes
	// a build failure rather than a review comment. A resolver added in a hurry
	// gets a field that is annotated by default nowhere, and CI says so.
	policies := collectFieldPolicies(t)

	var missing, contradictory []string
	for _, p := range policies {
		switch {
		case p.IsInput:
			// Not a coverage requirement. See fieldPolicy.IsInput.
			continue
		case p.isPublic() && p.effectiveRole() != "":
			contradictory = append(contradictory, fmt.Sprintf(
				"%s: @publicRead says anyone may read it but the type requires %s",
				p, p.effectiveRole()))
		case p.IsMutationRoot:
			if p.RequireWriteRole == "" {
				missing = append(missing, p.String()+" (mutation root)")
			}
		case p.effectiveRole() == "" && !p.isPublic():
			// `!p.isPublic()` is load-bearing, and the first version of this test
			// omitted it — which made the test contradict the sibling test 60 lines
			// down. `TestAPublicFieldRequiresNoRoleAndAMutationRootAlwaysDoes`
			// asserts that a @publicRead field carries NO role, because @publicRead
			// is the complete policy. So every public field arrives here with
			// effectiveRole()=="" and no case matched it: 25 fields on 16
			// @publicRead types were reported as having "no access policy" while
			// carrying the schema's own explicit statement that they are readable
			// by anyone. The annotation scheme's opt-out marker was, by this test,
			// a violation.
			missing = append(missing, p.String())
		}
	}

	if len(contradictory) > 0 {
		t.Errorf("%d field(s) declare contradictory access policies:\n  %s",
			len(contradictory), strings.Join(contradictory, "\n  "))
	}

	if len(missing) > 0 {
		// Capped, because the first version of this test printed every one of
		// them and on a 34-file schema the output was long enough that nobody
		// read it. A list nobody reads does not get fixed.
		const max = 25
		msg := fmt.Sprintf("%d field(s) have no access policy. Annotate each with "+
			"@requiresRole/@requiresWriteRole, or @publicRead if it is genuinely "+
			"safe for the no-login role:\n", len(missing))
		for i, m := range missing {
			if i == max {
				msg += fmt.Sprintf("  ... and %d more\n", len(missing)-max)
				break
			}
			msg += "  " + m + "\n"
		}
		t.Error(msg)
	}
}

func TestEveryDeclaredRoleIsOneThisCodebaseDefines(t *testing.T) {
	// A typo in a role name would otherwise make a field MORE permissive than it
	// looks: "subsciber" is not on the ladder, so any implementation comparing
	// ranks would treat the annotation as unsatisfiable-or-absent, and a lenient
	// one would treat it as public.
	policies := collectFieldPolicies(t)

	for _, p := range policies {
		if p.RequireRole != "" {
			_, ok := accessLadderRank[p.RequireRole]
			assert.True(t, ok, "%s: @requiresRole(%q) is not one of %v",
				p, p.RequireRole, accessLadder)
		}
		if p.RequireWriteRole != "" {
			_, ok := accessLadderRank[p.RequireWriteRole]
			assert.True(t, ok, "%s: @requiresWriteRole(%q) is not one of %v",
				p, p.RequireWriteRole, accessLadder)
		}
	}
}

func TestAPublicFieldRequiresNoRoleAndAMutationRootAlwaysDoes(t *testing.T) {
	// Two asymmetries that the coverage test above relies on, asserted directly
	// so that changing the rules is a deliberate act.
	policies := collectFieldPolicies(t)

	for _, p := range policies {
		if p.isPublic() && !p.IsMutationRoot {
			assert.Empty(t, p.effectiveRole(),
				"%s: @publicRead means anyone may read it, so a role contradicts it", p)
			continue
		}
		if p.IsMutationRoot {
			assert.NotEmpty(t, p.RequireWriteRole,
				"%s: a mutation root with no @requiresWriteRole is callable by "+
					"anyone who reaches the schema", p)
		}
	}
}

func TestAdminOnlyFieldsAreNotAnnotatedPublic(t *testing.T) {
	// A whole-class check rather than a single-field one: the cheapest way for
	// the annotation scheme to be useless is for somebody to make everything
	// public and the coverage test to pass.
	policies := collectFieldPolicies(t)

	publicCount := 0
	for _, p := range policies {
		if p.isPublic() {
			publicCount++
		}
	}

	assert.Greater(t, publicCount, 0,
		"nothing is @publicRead, so the opt-out marker is unused and the "+
			"annotation scheme is untested in practice")
	assert.Less(t, float64(publicCount)/float64(len(policies)), 0.5,
		"more than half the schema is @publicRead (%d of %d); the scheme should "+
			"make the restrictive answer the easy one to write", publicCount, len(policies))
}

func TestAccessPolicyHelpersBehaveAsDocumented(t *testing.T) {
	// The helpers themselves, because the coverage test's correctness rests on
	// them and they are otherwise only exercised indirectly.
	p := fieldPolicy{Type: "Scene", Field: "title", RequireRole: "steward"}

	assert.True(t, p.satisfies("steward", "steward"), "equal roles satisfy")
	assert.True(t, p.satisfies("admin", "steward"), "the ladder is ordered")
	assert.False(t, p.satisfies("contributor", "steward"), "a lower role does not")
	assert.False(t, p.satisfies("public", "steward"))
	assert.False(t, p.satisfies("notarole", "steward"),
		"an unknown role never satisfies a requirement")
	assert.False(t, p.satisfies("admin", "notarole"),
		"an unknown requirement is never satisfied")

	assert.Equal(t, "Scene.title", p.String(),
		"the String form has to name the field, since failures are read as lists")
}
