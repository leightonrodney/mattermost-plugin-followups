package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/pkg/errors"

	"github.com/leightonrodney/mattermost-plugin-followups/server/reminder"
)

const commandTrigger = "remind"

// shortIDLength is how many characters of a reminder ID the command surfaces.
// Six characters of a 26-character random ID is short enough to type and long
// enough that two of a user's own reminders colliding is vanishingly unlikely;
// a collision is handled rather than assumed away.
const shortIDLength = 6

// buildAutocomplete describes /remind to the autocomplete menu.
//
// Mattermost rejects an autocomplete node that carries both arguments and
// subcommands, so the free-form case is a subcommand of its own rather than a
// text argument hanging off the root. That is why "me" appears here as a
// subcommand: it is the documented subject of the sentence anyway, and it gives
// the grammar somewhere to live.
//
// The handler does not depend on this shape. It treats anything that is not a
// recognised subcommand as a reminder to create, so "/remind tomorrow at 9am to
// vote" still works even though autocomplete only suggests "me".
func buildAutocomplete() *model.AutocompleteData {
	root := model.NewAutocompleteData(commandTrigger, "[subcommand]", "Create and manage your reminders")

	create := model.NewAutocompleteData("me", "[when] to [what]", "Set a reminder for yourself")
	create.AddTextArgument(
		"When to remind you and what about, for example `in 30 minutes to check the build` or `every weekday at 9am to post the update`",
		"[when] to [what]", "")
	root.AddCommand(create)

	root.AddCommand(model.NewAutocompleteData("list", "", "Show your reminders"))

	snooze := model.NewAutocompleteData("snooze", "[id] [duration]", "Postpone a reminder")
	snooze.AddTextArgument("The reminder's ID, as shown by `/remind list`", "[id]", "")
	snooze.AddTextArgument("How long to postpone it, for example `20 minutes`", "[duration]", "")
	root.AddCommand(snooze)

	cancel := model.NewAutocompleteData("cancel", "[id]", "Cancel a reminder")
	cancel.AddTextArgument("The reminder's ID, as shown by `/remind list`", "[id]", "")
	root.AddCommand(cancel)

	del := model.NewAutocompleteData("delete", "[id]", "Delete a reminder and its history")
	del.AddTextArgument("The reminder's ID, as shown by `/remind list`", "[id]", "")
	root.AddCommand(del)

	root.AddCommand(model.NewAutocompleteData("help", "", "Show what /remind understands"))

	return root
}

// registerCommand declares /remind with the server.
func (p *Plugin) registerCommand() error {
	return p.client.SlashCommand.Register(&model.Command{
		Trigger:          commandTrigger,
		AutoComplete:     true,
		AutoCompleteDesc: "Set a reminder for yourself",
		AutoCompleteHint: "[when] to [what]",
		AutocompleteData: buildAutocomplete(),
		DisplayName:      "Follow-ups",
		Description:      "Create and manage reminders",
	})
}

// handleCommand routes /remind.
//
// Every response is ephemeral: a reminder is private to the person who set it,
// so nothing about it should land in a shared channel.
func (p *Plugin) handleCommand(args *model.CommandArgs) (*model.CommandResponse, error) {
	fields := strings.Fields(args.Command)
	if len(fields) == 0 {
		return ephemeral(p.helpText()), nil
	}

	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(args.Command), "/"+commandTrigger))
	subcommandFields := strings.Fields(rest)

	if len(subcommandFields) == 0 {
		return ephemeral(p.helpText()), nil
	}

	switch strings.ToLower(subcommandFields[0]) {
	case "help":
		return ephemeral(p.helpText()), nil
	case "list":
		return p.commandList(args.UserId)
	case "cancel":
		return p.commandCancel(args.UserId, subcommandFields[1:])
	case "delete", "remove":
		return p.commandDelete(args.UserId, subcommandFields[1:])
	case "snooze":
		return p.commandSnooze(args.UserId, subcommandFields[1:])
	default:
		return p.commandCreate(args, rest)
	}
}

// commandCreate parses and stores a new reminder.
func (p *Plugin) commandCreate(args *model.CommandArgs, input string) (*model.CommandResponse, error) {
	loc := p.service.Timezone(args.UserId)

	parsed, err := reminder.Parse(input, p.service.Now().In(loc), loc)
	if err != nil {
		// Parse errors are the user's to fix, so they are shown as written
		// rather than logged and replaced with something generic.
		return ephemeral(":warning: " + err.Error()), nil
	}

	rec, err := p.service.Create(args.UserId, createInput{
		Message:    parsed.Message,
		DueAt:      parsed.DueAt,
		Recurrence: parsed.Recurrence,
		Timezone:   loc.String(),
	})
	if err != nil {
		if IsUserFacing(err) {
			return ephemeral(":warning: " + err.Error()), nil
		}
		return nil, err
	}

	return ephemeral(p.confirmation(rec)), nil
}

// confirmation is what the user sees immediately after setting a reminder.
func (p *Plugin) confirmation(rec *reminder.Reminder) string {
	var b strings.Builder
	b.WriteString(":white_check_mark: I'll remind you ")
	if rec.IsRecurring() {
		b.WriteString(rec.Recurrence.Describe())
		b.WriteString(", starting ")
	}
	b.WriteString(formatTime(rec.DueTime()))
	b.WriteString(".\n> ")
	b.WriteString(rec.Message)

	// "/remind @alice to review the PR" parses cleanly and quietly becomes a
	// reminder to yourself whose text happens to start with a username. That
	// looks like it worked, so say plainly that it did not do what it looks
	// like. Reminding other people is not supported; see docs/ROADMAP.md for
	// why it is not a small change.
	if mentioned := leadingMention(rec.Message); mentioned != "" {
		fmt.Fprintf(&b, "\n\n:information_source: Reminders are only ever sent to you — **@%s will not be notified**. "+
			"If you meant to remind them, send them a message instead.", mentioned)
	}

	fmt.Fprintf(&b, "\n\nID `%s` — cancel it with `/remind cancel %s`.", shortID(rec.ID), shortID(rec.ID))
	return b.String()
}

// leadingMention returns the username when a reminder's text opens with an
// @mention, and an empty string otherwise.
func leadingMention(message string) string {
	if !strings.HasPrefix(message, "@") {
		return ""
	}
	// Scanned from immediately after the @ rather than split into fields, so
	// that "@ someone" is not read as a mention of someone.
	isUsernameRune := func(r rune) bool {
		return r == '.' || r == '-' || r == '_' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
	}

	end := 0
	for _, r := range message[1:] {
		if !isUsernameRune(r) {
			break
		}
		end++
	}
	return message[1 : 1+end]
}

// commandList shows the user's reminders.
func (p *Plugin) commandList(userID string) (*model.CommandResponse, error) {
	// Opening the list is a good moment to apply retention for someone whose
	// reminders the scheduler has not touched lately.
	if _, err := p.service.Purge(userID, p.service.Now()); err != nil {
		p.client.Log.Warn("Failed to apply retention", "user_id", userID, "error", err.Error())
	}

	reminders, err := p.service.List(userID)
	if err != nil {
		return nil, err
	}

	if len(reminders) == 0 {
		return ephemeral("You have no reminders. Set one with `/remind me in 30 minutes to take a break`."), nil
	}

	var pending, finished []*reminder.Reminder
	for _, rec := range reminders {
		if rec.Status.IsTerminal() {
			finished = append(finished, rec)
		} else {
			pending = append(pending, rec)
		}
	}

	var b strings.Builder

	if len(pending) > 0 {
		b.WriteString("#### Upcoming\n\n")
		b.WriteString("| ID | When | Repeats | Reminder |\n|:--|:--|:--|:--|\n")
		for _, rec := range pending {
			repeats := rec.Recurrence.Describe()
			if repeats == "" {
				repeats = "—"
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n",
				shortID(rec.ID), formatTime(rec.DueTime()), repeats, tableSafe(rec.Message))
		}
	}

	if len(finished) > 0 {
		b.WriteString("\n#### Finished\n\n")
		b.WriteString("| ID | Status | When | Reminder |\n|:--|:--|:--|:--|\n")
		for _, rec := range finished {
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n",
				shortID(rec.ID), rec.Status, formatTime(rec.DueTime()), tableSafe(rec.Message))
		}
	}

	b.WriteString("\nUse `/remind cancel <id>` to stop one, or `/remind snooze <id> 1 hour` to postpone it.")
	return ephemeral(b.String()), nil
}

func (p *Plugin) commandCancel(userID string, args []string) (*model.CommandResponse, error) {
	if len(args) == 0 {
		return ephemeral("Which one? Try `/remind cancel <id>`, using an ID from `/remind list`."), nil
	}

	id, err := p.resolveID(userID, args[0])
	if err != nil {
		return ephemeral(":warning: " + err.Error()), nil
	}

	rec, err := p.service.Cancel(userID, id)
	if err != nil {
		if IsUserFacing(err) {
			return ephemeral(":warning: " + err.Error()), nil
		}
		return nil, err
	}
	return ephemeral(fmt.Sprintf(":wastebasket: Cancelled.\n> %s", rec.Message)), nil
}

func (p *Plugin) commandDelete(userID string, args []string) (*model.CommandResponse, error) {
	if len(args) == 0 {
		return ephemeral("Which one? Try `/remind delete <id>`, using an ID from `/remind list`."), nil
	}

	id, err := p.resolveID(userID, args[0])
	if err != nil {
		return ephemeral(":warning: " + err.Error()), nil
	}

	if err := p.service.Delete(userID, id); err != nil {
		if IsUserFacing(err) {
			return ephemeral(":warning: " + err.Error()), nil
		}
		return nil, err
	}
	return ephemeral(":wastebasket: Deleted."), nil
}

func (p *Plugin) commandSnooze(userID string, args []string) (*model.CommandResponse, error) {
	if len(args) < 2 {
		return ephemeral("Try `/remind snooze <id> 20 minutes`, using an ID from `/remind list`."), nil
	}

	id, err := p.resolveID(userID, args[0])
	if err != nil {
		return ephemeral(":warning: " + err.Error()), nil
	}

	// The duration reuses the same grammar as "in 20 minutes", so there is one
	// set of accepted spellings rather than two.
	loc := p.service.Timezone(userID)
	now := p.service.Now().In(loc)
	parsed, err := reminder.Parse("in "+strings.Join(args[1:], " ")+" to snooze", now, loc)
	if err != nil {
		return ephemeral(":warning: I didn't understand that duration. Try `20 minutes`, `2 hours` or `1 day`."), nil
	}

	rec, err := p.service.Snooze(userID, id, parsed.DueAt.Sub(now))
	if err != nil {
		if IsUserFacing(err) {
			return ephemeral(":warning: " + err.Error()), nil
		}
		return nil, err
	}
	return ephemeral(fmt.Sprintf(":alarm_clock: Postponed to %s.\n> %s", formatTime(rec.DueTime()), rec.Message)), nil
}

// resolveID turns the short ID shown in listings back into a full reminder ID,
// scoped to the user's own reminders.
//
// Because the lookup only ever considers the caller's reminders, an ID that
// belongs to someone else behaves exactly like one that does not exist.
func (p *Plugin) resolveID(userID, given string) (string, error) {
	given = strings.Trim(given, "`")
	if given == "" {
		return "", errors.New("that is not a reminder ID")
	}

	reminders, err := p.service.List(userID)
	if err != nil {
		return "", err
	}

	var matches []string
	for _, rec := range reminders {
		if rec.ID == given || strings.HasPrefix(rec.ID, given) {
			matches = append(matches, rec.ID)
		}
	}

	switch len(matches) {
	case 0:
		return "", errors.Errorf("I couldn't find a reminder with the ID `%s`. Run `/remind list` to see yours", given)
	case 1:
		return matches[0], nil
	default:
		sort.Strings(matches)
		return "", errors.Errorf("`%s` matches more than one of your reminders; use the full ID", given)
	}
}

func shortID(id string) string {
	if len(id) <= shortIDLength {
		return id
	}
	return id[:shortIDLength]
}

// tableSafe keeps user text from breaking the Markdown table it is rendered in.
// Mattermost escapes the text itself; this is only about the pipe character,
// which would otherwise be read as a column separator.
func tableSafe(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
}

func ephemeral(text string) *model.CommandResponse {
	return &model.CommandResponse{
		ResponseType: model.CommandResponseTypeEphemeral,
		Text:         text,
	}
}

// helpText documents the grammar. It is kept in step with parse.go by the
// examples in the parser's tests, which use the same forms.
func (p *Plugin) helpText() string {
	config := p.getConfiguration()

	return strings.Join([]string{
		"#### Setting a reminder",
		"",
		"```",
		"/remind me in 30 minutes to check the build",
		"/remind me tomorrow at 9am to review the deployment",
		"/remind me every Monday at 10am to check the sprint board",
		"/remind me to call the vendor back on Friday at 2pm",
		"```",
		"",
		"**When** — `in 30 minutes`, `in 2 hours`, `in 3 days`, `in a week`, `tomorrow`, `tonight`, " +
			"`today at 6pm`, `at 9am`, `Monday`, `next Tuesday at 10:00`, `next week`, `Nov 3`, `3 November`, `2026-11-03 at 14:30`.",
		"",
		"**Repeating** — `every day at 9am`, `every weekday at 9:15`, `every weekend`, " +
			"`every Monday and Friday at 10am`, `every other Monday`, `every 3 days`, `every month on the 15th`, `every 2 hours`.",
		"",
		"A time with no date means the next time that clock time comes round. A date with no time means " +
			fmt.Sprintf("%02d:00.", reminder.DefaultHour) + " Everything is read in your Mattermost timezone.",
		"",
		"#### Managing them",
		"",
		"```",
		"/remind list",
		"/remind snooze <id> 20 minutes",
		"/remind cancel <id>",
		"/remind delete <id>",
		"```",
		"",
		"You can also set a reminder from any message: hover it, open the **Actions** menu and choose **Remind me**. " +
			"That is the menu next to **...**, which holds Mattermost's own one-off reminder instead.",
		"",
		fmt.Sprintf("_On this server you can hold up to %d reminders, %d of them repeating, scheduled at least %s out and at most %s ahead._",
			config.ReminderLimit(), config.RecurringLimit(), humanDuration(config.MinInterval()), humanDuration(config.MaxHorizon())),
	}, "\n")
}
