package main

import (
	// The plugin computes recurrence in each user's own timezone, so it needs a
	// zone database. Embedding Go's copy means the plugin behaves the same way
	// on a minimal container image that ships no system tzdata as it does on a
	// full distribution.
	_ "time/tzdata"

	"github.com/mattermost/mattermost/server/public/plugin"
)

func main() {
	plugin.ClientMain(&Plugin{})
}
