# Draft: Marketplace submission

Not submitted. This is the text for an issue on
[mattermost/mattermost-marketplace](https://github.com/mattermost/mattermost-marketplace/issues/new?template=add_plugin.md),
using its **Add plugin** template. Opening that issue is a public act and a real
review request, so it waits for you.

Title: `Add mattermost-plugin-followups to Marketplace`

Every field is filled. Nothing here has been submitted.

---

#### Summary

**Follow-ups & Reminders** creates one-time and recurring reminders from a slash
command or from any message, delivers them as a direct message from a bot, and
lets you manage them from a Mattermost-native right-hand sidebar.

- `/remind me in 30 minutes to check the deploy`
- `/remind me every Monday at 9am to post the standup`
- **Remind me** from any message's Actions menu, with the delivered reminder
  linking back to the message.
- Snooze, complete, reschedule and cancel from the sidebar or from buttons on
  the delivered reminder.

Everything is stored in the plugin key-value store. No external service, no
network egress, no paid dependency. Reminder text and message contents are never
written to the server log at any level, and every read and write is scoped to
the requesting user, so one user cannot see or alter another's reminders.

Screenshots are in the README: the Actions menu, the reminder dialog, the
sidebar in light and dark themes, and a delivered reminder.

#### Review commit

Source: https://github.com/leightonrodney/mattermost-plugin-followups
Tag for review: `v0.1.0` — https://github.com/leightonrodney/mattermost-plugin-followups/tree/v0.1.0

## Checklist

**Product requirements**

- [x] Open Source license — MIT, in `LICENSE`.
- [x] Source in a public Git repository.
- [x] Public issue tracker, linked from the README and set as `support_url`.
- [x] Changelog published and linked via `release_notes_url`; `CHANGELOG.md`
      follows Keep a Changelog.
- [ ] **Out of Beta at v1.0.0+** — not met. This is v0.1.0 and is submitted as a
      **Beta** plugin. The functionality is complete and tested; the version
      reflects that it has not yet run anywhere but a test deployment.
- [x] All configuration accessible via the Mattermost interface — eight settings
      under System Console → Plugins, all taking effect immediately, with
      out-of-range values clamped rather than rejected.
- [x] Plugin ID does not collide — `com.github.leightonrodney.followups`,
      checked against the 30 IDs currently in `plugins.json`. Nothing
      reminder-related is listed.

**Technical requirements**

- [ ] **60k concurrent connections and High Availability** — cannot be
      self-certified, and I would rather state plainly what has and has not been
      run. Multi-node coordination is tested by sharing one key-value store
      between plugin instances, covering single delivery under contention,
      cursor sharing, and a node dying mid-claim. That exercises the exact
      mechanism the plugin relies on (`cluster.Schedule` plus per-reminder
      compare-and-set), but it is **not** a licensed HA deployment with a
      cluster bus. There is no load test. Delivery is at-least-once by design;
      `docs/ARCHITECTURE.md` explains why exactly-once is not achievable over
      the plugin KV store.
- [x] Logs important events at appropriate levels — activation, scheduler
      cursor, delivery failures and quota refusals. Reminder text and message
      contents are never logged, at any level including debug.

**Security requirements**

- [ ] Security review — yours to run. `docs/ARCHITECTURE.md` documents each
      boundary and how it is enforced; `SECURITY.md` lists what is in and out of
      scope and what the plugin stores.
- [x] Security contact: **leighton.rodney@mattermost.com**, also published in
      `SECURITY.md`.

**Functional requirements**

- [x] `min_server_version` set — `9.5.0`.
- [x] Works on all versions at or above it — the integration suite runs in CI on
      every push against **9.5** and **latest**, installing the plugin into a
      real server and checking activation, command registration, a real
      scheduler tick delivering a reminder, and a disable/wait/re-enable cycle
      that passes only if the schedule survived in the database. Versions
      between those two are not individually tested.

**Documentation requirements**

- [x] Detailed usage documentation with screenshots, a feature list and a
      development guide, linked as `homepage_url`. The README covers
      requirements, installation, configuration, usage, troubleshooting,
      screenshots and where to get help. `docs/DEVELOPMENT.md` is the
      development guide; `docs/ARCHITECTURE.md` and `docs/TESTING.md` cover
      design and verification.

---

### Known gaps, stated up front

Reviewers find these anyway, and the honest version is shorter:

1. **No licensed HA cluster test.** See above.
2. **Push confirmed on iOS only**, through Mattermost's test proxy rather than a
   production one. Android untried.
3. **No localisation.** Everything is English. The i18n scaffolding is wired up
   but unused.
4. **No screen-reader testing.** Component tests assert roles and accessible
   names, which is not the same thing.

`docs/ROADMAP.md` lists what was consciously left out and why, including
reminders for other people and channels, which is absent because it changes the
security model rather than because it is hard.
