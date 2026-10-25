package main

import (
	"reflect"
	"sync"
	"time"

	"github.com/pkg/errors"
)

// configuration captures the plugin's settings as exposed in the Mattermost
// server configuration, plus the clamping that keeps a mistyped value from
// taking the scheduler down.
//
// Every public field is deserialized from the server configuration in
// OnConfigurationChange. Because plugin hooks run concurrently and the
// configuration can change at any moment, the active value is guarded by a lock
// and replaced wholesale rather than mutated in place.
//
// All settings take effect immediately, with one exception: SchedulerInterval
// is read at the top of each scheduler tick, so a change to it applies from the
// following tick rather than interrupting one in progress.
type configuration struct {
	EnableScheduler              bool
	SchedulerIntervalSeconds     int
	MaxRemindersPerUser          int
	MaxRecurringRemindersPerUser int
	MinIntervalSeconds           int
	MaxHorizonDays               int
	CompletedRetentionDays       int
	EnableDebugLogging           bool
}

// Settings are clamped rather than rejected. An administrator who types 100000
// into the scheduler interval should get a slow scheduler, not a plugin that
// refuses to start.
const (
	minSchedulerIntervalSeconds = 10
	maxSchedulerIntervalSeconds = 900

	maxRemindersCeiling = 10000
	maxRecurringCeiling = 1000

	minIntervalFloor   = 30
	minIntervalCeiling = 86400

	maxHorizonCeiling = 3650

	retentionCeiling = 365
)

// SchedulerInterval is how often the scheduler wakes.
func (c *configuration) SchedulerInterval() time.Duration {
	return time.Duration(clamp(c.SchedulerIntervalSeconds, minSchedulerIntervalSeconds, maxSchedulerIntervalSeconds)) * time.Second
}

// ReminderLimit is the maximum number of scheduled reminders one user may hold.
func (c *configuration) ReminderLimit() int {
	return clamp(c.MaxRemindersPerUser, 1, maxRemindersCeiling)
}

// RecurringLimit is the maximum number of repeating reminders one user may hold.
func (c *configuration) RecurringLimit() int {
	return clamp(c.MaxRecurringRemindersPerUser, 0, maxRecurringCeiling)
}

// MinInterval is the soonest a reminder may be scheduled, and the shortest gap
// allowed between occurrences of a repeating one.
func (c *configuration) MinInterval() time.Duration {
	return time.Duration(clamp(c.MinIntervalSeconds, minIntervalFloor, minIntervalCeiling)) * time.Second
}

// MaxHorizon is the furthest ahead a reminder may be scheduled.
func (c *configuration) MaxHorizon() time.Duration {
	return time.Duration(clamp(c.MaxHorizonDays, 1, maxHorizonCeiling)) * 24 * time.Hour
}

// Retention is how long finished reminders are kept before being purged.
func (c *configuration) Retention() time.Duration {
	return time.Duration(clamp(c.CompletedRetentionDays, 0, retentionCeiling)) * 24 * time.Hour
}

func clamp(value, low, high int) int {
	return min(max(value, low), high)
}

// defaultConfiguration mirrors the defaults declared in plugin.json. It is what
// the plugin falls back to before the server has supplied a configuration.
func defaultConfiguration() *configuration {
	return &configuration{
		EnableScheduler:              true,
		SchedulerIntervalSeconds:     30,
		MaxRemindersPerUser:          100,
		MaxRecurringRemindersPerUser: 20,
		MinIntervalSeconds:           60,
		MaxHorizonDays:               730,
		CompletedRetentionDays:       30,
		EnableDebugLogging:           false,
	}
}

// Clone copies the configuration. Every field is a value type, so a shallow
// copy is a complete one; this must be revisited if a slice or map is added.
func (c *configuration) Clone() *configuration {
	clone := *c
	return &clone
}

// configurationHolder guards the active configuration.
type configurationHolder struct {
	lock  sync.RWMutex
	value *configuration
}

// get returns the active configuration. The struct it returns is treated as
// immutable: callers read from it and never write to it.
func (h *configurationHolder) get() *configuration {
	h.lock.RLock()
	defer h.lock.RUnlock()

	if h.value == nil {
		return defaultConfiguration()
	}
	return h.value
}

func (h *configurationHolder) set(c *configuration) {
	h.lock.Lock()
	defer h.lock.Unlock()

	if c != nil && h.value == c {
		// Assigning the same pointer back would mean something mutated the
		// active configuration in place, which the locking cannot protect.
		if reflect.ValueOf(*c).NumField() == 0 {
			return
		}
		panic("configuration was replaced with the value already in use")
	}
	h.value = c
}

// getConfiguration retrieves the active configuration under lock.
func (p *Plugin) getConfiguration() *configuration {
	return p.configuration.get()
}

// OnConfigurationChange is invoked when the administrator saves the plugin's
// settings, and once at activation.
func (p *Plugin) OnConfigurationChange() error {
	// Starting from the defaults means a setting the server has never been told
	// about keeps its documented default rather than becoming a zero value.
	configuration := defaultConfiguration()

	if err := p.API.LoadPluginConfiguration(configuration); err != nil {
		return errors.Wrap(err, "failed to load plugin configuration")
	}

	p.configuration.set(configuration)
	return nil
}
