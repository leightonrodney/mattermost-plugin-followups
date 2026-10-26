package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The server validates a plugin's autocomplete data when the command is
// registered, and refuses to activate the plugin if it does not hold up. That
// made an invalid shape a plugin that would not start at all — which no unit
// test noticed, because none of them had ever asked the server's own validator.
//
// model.AutocompleteData.IsValid is exactly the check the server runs, so this
// asks it directly. It is what would have caught the root node carrying both a
// text argument and subcommands, which Mattermost forbids.
func TestAutocompleteIsValid(t *testing.T) {
	autocomplete := buildAutocomplete()
	require.NotNil(t, autocomplete)
	require.NoError(t, autocomplete.IsValid(), "the server refuses to activate a plugin whose autocomplete is invalid")
}

func TestAutocompleteSubcommandsAreAlsoValid(t *testing.T) {
	// IsValid recurses, but asserting per subcommand names the offender when one
	// of them is wrong rather than failing on the root.
	for _, sub := range buildAutocomplete().SubCommands {
		t.Run(sub.Trigger, func(t *testing.T) {
			assert.NoError(t, sub.IsValid())
		})
	}
}

func TestAutocompleteRootHasNoArguments(t *testing.T) {
	// The specific rule that was broken: a node may have arguments or
	// subcommands, never both.
	root := buildAutocomplete()
	assert.Empty(t, root.Arguments, "the root carries subcommands, so it must carry no arguments")
	assert.NotEmpty(t, root.SubCommands)
}

func TestAutocompleteCoversTheDocumentedSubcommands(t *testing.T) {
	triggers := map[string]bool{}
	for _, sub := range buildAutocomplete().SubCommands {
		triggers[sub.Trigger] = true
	}

	// Every subcommand the README and the help text promise.
	for _, expected := range []string{"me", "list", "snooze", "cancel", "delete", "help"} {
		assert.True(t, triggers[expected], "autocomplete is missing the documented subcommand %q", expected)
	}
}

// "/remind @alice to review the PR" parses perfectly well and becomes a
// reminder to yourself whose text starts with a username. Nothing is wrong with
// it mechanically, which is the problem: it looks like it reminded Alice.
//
// Found by watching someone try exactly that on a real server.
func TestLeadingMention(t *testing.T) {
	tests := []struct {
		message string
		want    string
	}{
		{"@tester to stand up", "tester"},
		{"@alice.smith review the PR", "alice.smith"},
		{"@bob-jones check the deploy", "bob-jones"},
		{"@carol_7 ping back", "carol_7"},
		{"@dave", "dave"},
		{"ask @erin about the migration", ""},
		{"check the build", ""},
		{"@", ""},
		{"@ someone", ""},
	}

	for _, test := range tests {
		t.Run(test.message, func(t *testing.T) {
			assert.Equal(t, test.want, leadingMention(test.message))
		})
	}
}
