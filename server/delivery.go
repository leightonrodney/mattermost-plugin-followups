package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/pkg/errors"

	"github.com/leightonrodney/mattermost-plugin-followups/server/reminder"
)

// postReminder sends the reminder to its owner as a direct message from the
// plugin's bot.
//
// Delivering through an ordinary post is deliberate: it means reminders follow
// each person's existing notification settings, reach the mobile apps through
// the same push pipeline as any other direct message, and are searchable
// afterwards. The plugin does not need, and does not have, a notification
// channel of its own.
func (p *Plugin) postReminder(rec *reminder.Reminder) error {
	post := &model.Post{
		Message: p.renderReminder(rec),
	}

	post.AddProp(model.PostPropsAttachments, []*model.MessageAttachment{p.reminderActions(rec)})

	if err := p.client.Post.DM(p.botID, rec.UserID, post); err != nil {
		return errors.Wrap(err, "failed to send the reminder")
	}
	return nil
}

// renderReminder builds the message body.
//
// The user's own text is placed on its own line and never interpolated into
// surrounding markup, so that it cannot alter the structure of the message
// around it. Mattermost renders post text as sanitised Markdown on every
// client, which is what keeps the text itself safe to display.
func (p *Plugin) renderReminder(rec *reminder.Reminder) string {
	var b strings.Builder

	b.WriteString(":alarm_clock: **Reminder**\n")
	b.WriteString(rec.Message)

	if link := p.permalink(rec); link != "" {
		b.WriteString("\n\n")
		b.WriteString("In reply to: " + link)
	} else if rec.PostID != "" {
		// The reminder was created from a message the user can no longer read,
		// because they left the channel or it was deleted. Say so rather than
		// offering a link that will fail.
		b.WriteString("\n\n_The message this reminder came from is no longer available to you._")
	}

	if rec.IsRecurring() {
		b.WriteString("\n\n_Repeats " + rec.Recurrence.Describe() + "._")
	}

	return b.String()
}

// permalink builds a link back to the message a reminder was created from, but
// only when the owner may still read that channel.
//
// The check matters: channel membership can be revoked between creating a
// reminder and it firing, and a permalink the user cannot open would at best be
// confusing and at worst confirm the existence of a private channel.
func (p *Plugin) permalink(rec *reminder.Reminder) string {
	if rec.PostID == "" || rec.ChannelID == "" {
		return ""
	}

	if !p.client.User.HasPermissionToChannel(rec.UserID, rec.ChannelID, model.PermissionReadChannel) {
		return ""
	}

	teamName := p.teamNameFor(rec)
	if teamName == "" {
		return ""
	}

	siteURL := p.siteURL()
	if siteURL == "" {
		return ""
	}

	return fmt.Sprintf("%s/%s/pl/%s", strings.TrimRight(siteURL, "/"), teamName, rec.PostID)
}

// teamNameFor resolves the team whose URL the permalink should be built under.
// Direct and group messages belong to no team, so any team the user is a member
// of will serve.
func (p *Plugin) teamNameFor(rec *reminder.Reminder) string {
	if rec.TeamID != "" {
		team, err := p.client.Team.Get(rec.TeamID)
		if err == nil && team != nil {
			return team.Name
		}
	}

	teams, err := p.client.Team.List(pluginapi.FilterTeamsByUser(rec.UserID))
	if err != nil || len(teams) == 0 {
		return ""
	}
	return teams[0].Name
}

func (p *Plugin) siteURL() string {
	config := p.client.Configuration.GetConfig()
	if config == nil || config.ServiceSettings.SiteURL == nil {
		return ""
	}
	return *config.ServiceSettings.SiteURL
}

// Snooze presets offered on a delivered reminder.
var snoozePresets = []struct {
	label   string
	minutes int
}{
	{"20 minutes", 20},
	{"1 hour", 60},
	{"3 hours", 180},
	{"Tomorrow", 0}, // resolved against the user's timezone at click time
}

// reminderActions builds the buttons shown under a delivered reminder.
func (p *Plugin) reminderActions(rec *reminder.Reminder) *model.MessageAttachment {
	actions := make([]*model.PostAction, 0, len(snoozePresets)+1)

	for _, preset := range snoozePresets {
		actions = append(actions, &model.PostAction{
			Type: model.PostActionTypeButton,
			Name: "Snooze " + strings.ToLower(preset.label),
			Integration: &model.PostActionIntegration{
				URL: p.actionURL("snooze"),
				Context: map[string]any{
					contextReminderID: rec.ID,
					contextMinutes:    preset.minutes,
				},
			},
		})
	}

	// A repeating reminder is already scheduled for its next occurrence by the
	// time this is read, so the button acknowledges the occurrence rather than
	// ending the series.
	doneLabel := "Mark done"
	if rec.IsRecurring() {
		doneLabel = "Dismiss"
	}
	actions = append(actions, &model.PostAction{
		Type:  model.PostActionTypeButton,
		Name:  doneLabel,
		Style: "primary",
		Integration: &model.PostActionIntegration{
			URL:     p.actionURL("complete"),
			Context: map[string]any{contextReminderID: rec.ID},
		},
	})

	return &model.MessageAttachment{
		Footer:  "Scheduled for " + formatTime(rec.DueTime()),
		Actions: actions,
	}
}

// actionURL returns the path the server posts a button click back to. A path
// relative to the plugin is resolved by the server, which keeps the plugin from
// having to know its own site URL here.
func (p *Plugin) actionURL(name string) string {
	return "/plugins/" + manifest.Id + "/api/v1/actions/" + name
}

// Context keys on an interactive button. The user ID is deliberately absent:
// the server supplies the clicking user, and taking it from the context instead
// would let a crafted request act as somebody else.
const (
	contextReminderID = "reminder_id"
	contextMinutes    = "minutes"
)

// formatTime renders an instant the way the plugin's messages phrase it,
// including the zone so that a reminder delivered while travelling is not
// ambiguous.
func formatTime(t time.Time) string {
	return t.Format("Mon 2 Jan 2006 at 15:04 MST")
}
