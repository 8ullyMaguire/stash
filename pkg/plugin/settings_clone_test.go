package plugin

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stashapp/stash/pkg/javascript"
	"github.com/stashapp/stash/pkg/plugin/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Upstream's TestJSPluginCannotMutateStoredPluginSettings covers a SCALAR:
// `input.Settings.enabled = false`. That is the one value a top-level clone
// detaches, so the test passes while the guarantee it is named for does not
// hold.
//
// Plugin settings are Viper's Raw() tree, so a structured setting arrives as
// map[string]interface{} or []interface{}. maps.Clone copies the top level only
// and shares those, and goja's JS value for a Go map is a REFERENCE -- so a
// plugin doing `input.Settings.tags.push("x")` writes into the live
// configuration, which the next SetPluginConfiguration persists.
//
// The first version of this test drove it through buildPluginInput alone, which
// proves the map is shared but not that a plugin can reach it. This goes through
// the VM, which is the only place the claim matters.

// A plugin mutating a nested OBJECT it was handed.
func TestJSPluginCannotMutateNestedObjectInStoredSettings(t *testing.T) {
	config := &pluginInputTestConfig{settings: map[string]map[string]interface{}{
		"test-plugin": {"nested": map[string]interface{}{"k": "original"}},
	}}
	cache := NewCache(config)

	task := &jsPluginTask{
		pluginTask: pluginTask{
			plugin: &Config{id: "test-plugin", Name: "Test plugin"},
			input: cache.buildPluginInput(
				&Config{id: "test-plugin"}, nil, common.StashServerConnection{}, nil),
		},
		vm: javascript.NewVM(),
	}
	require.NoError(t, task.initVM())

	_, err := task.vm.RunString(`input.Settings.nested.k = "MUTATED"`)
	require.NoError(t, err, "a plugin mutating its own settings is not a JS error")

	stored := config.GetPluginConfiguration("test-plugin")
	got := stored["nested"].(map[string]interface{})["k"]
	assert.Equal(t, "original", got,
		"the plugin reached the stored configuration through its input; the next "+
			"SetPluginConfiguration will persist this")
}

// A plugin mutating a nested LIST it was handed.
//
// MEASURED, and the reason this test asserts what it asserts: goja WRAPS a Go
// slice into a JS array that is not a live view of the backing store. So
// `push`, `splice` and `pop` do NOT reach the stored slice -- the VM grows or
// shrinks its own array and the Go slice is untouched. An earlier version of
// this test asserted that push was contained, which is TRUE, and it passed
// against the shallow clone as well, which is the problem: a containment test
// that cannot fail is decoration.
//
// Element assignment is the operation that does write through (see the next
// test), so that is what the list case asserts here, and the push result is
// recorded as the reason it is not asserted.
func TestJSPluginCannotMutateNestedListInStoredSettings(t *testing.T) {
	config := &pluginInputTestConfig{settings: map[string]map[string]interface{}{
		"test-plugin": {"tags": []interface{}{"a", "b"}},
	}}
	cache := NewCache(config)

	task := &jsPluginTask{
		pluginTask: pluginTask{
			plugin: &Config{id: "test-plugin", Name: "Test plugin"},
			input: cache.buildPluginInput(
				&Config{id: "test-plugin"}, nil, common.StashServerConnection{}, nil),
		},
		vm: javascript.NewVM(),
	}
	require.NoError(t, task.initVM())

	_, err := task.vm.RunString(`input.Settings.tags.push("injected")`)
	require.NoError(t, err)

	tags, ok := config.GetPluginConfiguration("test-plugin")["tags"].([]interface{})
	require.True(t, ok, "the stored value is no longer a []interface{}")
	assert.Len(t, tags, 2, "the stored list grew")
	assert.NotContains(t, tags, "injected")
}

// ...and the reason a list still has to be cloned rather than shared: the VM's
// array wrapper is only a view for IN-PLACE element writes. Confirmed against
// goja directly -- push/splice/pop leave the Go slice unchanged, while
// `tags[0] = ...` mutates it. So "the slice is copied" and "the elements are
// copied" are different guarantees, and only the second is what a plugin can
// actually violate.
//
// The slice here is handed over DELIBERATELY UNCLONED: this test is about goja's
// semantics, so it must not go through buildPluginInput, or the clone would hide
// the very effect it is measuring. The first version did go through the clone and
// then asserted the element write had landed on the caller's slice -- which
// failed, correctly, because the clone is the fix and the test was measuring the
// wrong thing.
func TestJSPluginListWrapperIsNotALiveView(t *testing.T) {
	stored := []interface{}{"a", "b"}

	vm := javascript.NewVM()
	task := &jsPluginTask{
		pluginTask: pluginTask{
			// `plugin` is dereferenced by initVM, so it is not optional here --
			// the earlier version left it nil and panicked at js.go:87.
			plugin: &Config{Name: "Test plugin"},
			input:  common.PluginInput{Settings: map[string]interface{}{"tags": stored}},
		},
		vm: vm,
	}
	require.NoError(t, task.initVM())

	_, err := task.vm.RunString(`input.Settings.tags.push("injected")`)
	require.NoError(t, err)
	assert.Len(t, stored, 2, "goja grows its own array; the Go slice is unchanged")

	_, err = task.vm.RunString(`input.Settings.tags[0] = "MUTATED"`)
	require.NoError(t, err)
	assert.Equal(t, "MUTATED", stored[0],
		"element assignment DOES write through the wrapper -- measured here on "+
			"purpose, with no clone in the way")
}

// And the same two behaviours, now through the real path, where the clone is in
// place: neither a push nor an element assignment may reach the stored settings.
// This is the pair that fails against maps.Clone.
func TestBuildPluginInputProtectsStoredListUnderBothListOperations(t *testing.T) {
	for _, js := range []string{
		`input.Settings.tags.push("injected")`,
		`input.Settings.tags[0] = "MUTATED"`,
		`input.Settings.tags.splice(0, 1, "MUTATED")`,
	} {
		t.Run(js, func(t *testing.T) {
			config := &pluginInputTestConfig{settings: map[string]map[string]interface{}{
				"test-plugin": {"tags": []interface{}{"a", "b"}},
			}}
			cache := NewCache(config)

			task := &jsPluginTask{
				pluginTask: pluginTask{
					plugin: &Config{id: "test-plugin", Name: "Test plugin"},
					input: cache.buildPluginInput(
						&Config{id: "test-plugin"}, nil, common.StashServerConnection{}, nil),
				},
				vm: javascript.NewVM(),
			}
			require.NoError(t, task.initVM())

			_, err := task.vm.RunString(js)
			require.NoError(t, err)

			tags := config.GetPluginConfiguration("test-plugin")["tags"].([]interface{})
			assert.Equal(t, []interface{}{"a", "b"}, tags,
				"the plugin changed the stored settings through its input")
		})
	}
}

// Two invocations must not see each other's nested values. With a shared nested
// map this fails under -race, and without -race the assertion below catches the
// same thing as a plain wrong value -- so the test is meaningful either way.
func TestBuildPluginInputNestedSettingsAreIndependentPerInvocation(t *testing.T) {
	config := &pluginInputTestConfig{settings: map[string]map[string]interface{}{
		"test-plugin": {"nested": map[string]interface{}{"k": "v"}},
	}}
	cache := NewCache(config)
	plugin := &Config{id: "test-plugin"}

	first := cache.buildPluginInput(plugin, nil, common.StashServerConnection{}, nil)
	second := cache.buildPluginInput(plugin, nil, common.StashServerConnection{}, nil)

	first.Settings["nested"].(map[string]interface{})["k"] = "MUTATED"

	assert.Equal(t, "v", second.Settings["nested"].(map[string]interface{})["k"],
		"one invocation's plugin changed another's settings")
}

// A SETTING whose value is a list of objects -- what a repeat-object plugin
// setting deserialises to. Cloning has to recurse through both, and a clone that
// handles maps but not lists leaves the elements shared.
func TestCloneSettingsRecursesThroughListsOfObjects(t *testing.T) {
	stored := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{"when": "a", "then": "x"},
			map[string]interface{}{"when": "b", "then": "y"},
		},
	}

	snapshot := cloneSettings(stored)
	rules := snapshot["rules"].([]interface{})
	rules[0].(map[string]interface{})["then"] = "MUTATED"
	rules = append(rules, "extra")
	snapshot["rules"] = rules

	origRules := stored["rules"].([]interface{})
	assert.Equal(t, "x", origRules[0].(map[string]interface{})["then"],
		"an object inside a list is shared with the stored configuration")
	assert.Len(t, origRules, 2, "the stored list grew")
}

// The deep clone must not disturb the shapes the rest of the code depends on:
// scalars by value, and the exact set of keys preserved.
func TestCloneSettingsPreservesValuesAndKeys(t *testing.T) {
	stored := map[string]interface{}{
		"enabled": true,
		"count":   3.0,
		"name":    "example",
		"empty":   map[string]interface{}{},
		"blank":   []interface{}{},
	}

	snapshot := cloneSettings(stored)

	assert.Len(t, snapshot, len(stored))
	for k, v := range stored {
		assert.Equal(t, v, snapshot[k], "key %q", k)
	}
	// An empty map and an empty slice must stay usable, not become nil -- a
	// plugin doing settings.empty.x = 1 should not panic on nil.
	assert.NotNil(t, snapshot["empty"], "an empty object became nil")
	assert.NotNil(t, snapshot["blank"], "an empty list became nil")
	assert.Len(t, snapshot["empty"], 0)
	assert.Len(t, snapshot["blank"], 0)
}

// nil in, nil out -- so buildPluginInput can substitute its empty object.
func TestCloneSettingsPreservesNil(t *testing.T) {
	assert.Nil(t, cloneSettings(nil))
}

// A JSON round trip is what the settings actually came from, so build the
// fixture that way rather than hand-writing types json.Unmarshal would never
// produce. A hand-written map[string]any{...} with a map[string]string inside is
// a type cloneSettings does not handle, and the test would be asserting about a
// shape that cannot occur.
func TestCloneSettingsHandlesJSONRoundTrippedValues(t *testing.T) {
	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(
		[]byte(`{"a":{"b":["c",{"d":1}]},"e":[["f"]]}`), &decoded))

	snapshot := cloneSettings(decoded)
	a := snapshot["a"].(map[string]interface{})
	b := a["b"].([]interface{})
	b[1].(map[string]interface{})["d"] = 2.0
	b = append(b, "extra")
	a["b"] = b
	snapshot["e"].([]interface{})[0].([]interface{})[0] = "MUTATED"

	assert.Equal(t, 1.0, decoded["a"].(map[string]interface{})["b"].([]interface{})[1].(map[string]interface{})["d"])
	assert.Len(t, decoded["a"].(map[string]interface{})["b"].([]interface{}), 2)
	assert.Equal(t, "f", decoded["e"].([]interface{})[0].([]interface{})[0])
}

// The shape a JS plugin actually sees must survive the clone, since the VM
// stringifies it: every value must still be something JSON can render.
func TestJSPluginSeesCompleteNestedSettings(t *testing.T) {
	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(
		[]byte(`{"nested":{"k":"v"},"tags":["a","b"],"n":2}`), &decoded))

	task := &jsPluginTask{
		pluginTask: pluginTask{
			plugin: &Config{Name: "Test plugin"},
			input:  common.PluginInput{Settings: cloneSettings(decoded)},
		},
		vm: javascript.NewVM(),
	}
	require.NoError(t, task.initVM())

	value, err := task.vm.RunString(`JSON.stringify(input.Settings)`)
	require.NoError(t, err)
	assert.JSONEq(t, fmt.Sprintf(`{"nested":{"k":"v"},"tags":["a","b"],"n":2}`), value.String())
}
