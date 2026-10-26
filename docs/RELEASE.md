# Releasing

## Versioning

Semantic versioning. The version lives in **`plugin.json`** and nowhere else;
everything derives from it. The release workflow refuses to publish if the git
tag and the manifest version disagree.

- **Patch** — fixes that change no behaviour anyone depends on.
- **Minor** — new features, new settings, new API endpoints.
- **Major** — anything that breaks an existing reminder, a stored record, or a
  documented command form.

While the version is **0.x**, every release is published as a GitHub
**pre-release**, and so is any tag carrying a semver suffix such as
`v1.0.0-rc.1`. The workflow decides this from the tag itself, so there is
nothing to remember and nothing to set by hand. The practical effect is that
`/releases/latest` keeps pointing at the newest *stable* release, which is what
install instructions and scripts follow; v0.1.0 had to be corrected after
publication, which is why the rule exists.

## Before a release

```bash
make check-style       # gofmt, go vet, golangci-lint, eslint, tsc, manifest
make test              # server and webapp unit tests
make test-integration  # needs Docker; see docs/TESTING.md
make dist
./build/check-bundle.sh
```

CI already runs the integration suite against 9.5 and latest on every push to
`main`, so a tagged commit has been through it. Running it locally is still
worth doing before a release, because it is cheap and because a release is a
bad moment to discover that the green tick was from three commits ago.

Most of what used to be a manual checklist here is now in the integration
suite, because a list of things to remember on a Friday afternoon is a list of
things that get skipped. `make test-integration` covers:

| Was a manual step | Now |
|:--|:--|
| Upload the bundle and enable it | `TestPluginIsInstalledAndRespondsToItsAPI` |
| `/remind` creates a reminder and it arrives | `TestADueReminderIsDelivered` |
| `list`, `snooze`, `cancel`, `help` all respond | `TestTheCommandSurfaceWorks` |
| The snooze buttons on a delivered reminder work | `TestSnoozeButtonReschedulesOnARealServer` |
| Changing a setting takes effect without a restart | `TestAdministratorLimitsTakeEffectImmediately` |
| Disable, re-enable, a pending reminder still fires | `TestAPendingReminderSurvivesAPluginRestart` |
| One user cannot see another's reminders | `TestRemindersAreNotVisibleToOtherUsers` |

That last restart check is the one worth knowing about. It switches the plugin
off before a reminder is due, leaves it off until well past the due time,
switches it back on, and waits for delivery. Nothing is held in memory, so it
passes only if the reminder, the hour index and the scheduler cursor all came
back from the database — which is the claim the whole scheduling design rests
on, and the only test anywhere that proves it against a real server.

**What has to be done by hand**, because it is visual or needs a device. All
three were done for 0.1.0, on the dates noted; repeat them for any release that
touches the webapp or the delivery path:

- [x] **The webapp.** Open the sidebar, move through all three tabs, add a
      reminder from the box, edit one, snooze one, cancel one. Then **Remind me**
      from a message's **Actions** menu, and check the delivered reminder links
      back to it. Done on 26 October 2026 against Team Edition in a desktop
      browser.
- [x] **A dark theme.** Switch to Onyx and look at the dialog and the sidebar.
      A test asserts no colour is hard-coded, which is not the same as the
      result being readable. Contrast was measured on 26 October 2026 and every
      element clears WCAG AA except the primary button, which matches
      Mattermost's own; see docs/TESTING.md.
- [x] **A mobile client.** Confirm a reminder arrives as a push notification.
      The suite checks the post is the kind the server will notify about; it
      cannot check that a phone buzzes. Done once on iOS, 26 October 2026 —
      see docs/TESTING.md, including why an empty server log proves nothing
      here.

Three items rather than ten, and none of them is something an automated test
could have covered. Every bug found in this plugin so far came from exactly this
kind of use, so it is worth the twenty minutes.

`make dev-server` brings up a server to do it on.

## Cutting the release

1. Bump the version in `plugin.json`.
2. Update `CHANGELOG.md`, moving **Unreleased** into the new version with
   today's date.
3. Commit, and open a pull request. Let CI go green.
4. Merge.
5. Tag and push:
   ```bash
   git tag -s -a v1.0.0 -m "v1.0.0"
   git push origin v1.0.0
   ```
   The Makefile's `make patch` / `make minor` / `make major` targets do the tag
   and push for you, with a confirmation prompt.

The `release` workflow then runs the tests, builds the bundle, checks it,
generates `checksums.txt` and creates the GitHub release with both attached.
Nothing publishes on a merge to `main`: a release only happens from a tag, or
from a manual `workflow_dispatch`.

The whole workflow, publish included, ran for the first time on 26 October 2026
for v0.1.0 and completed green: both gates, the tests, the five-platform build,
the bundle check, the checksums and the publish. The published artifact was then
downloaded, checked against `checksums.txt`, and installed on a clean server to
confirm it activates. So this is no longer untested machinery — but a release is
still worth watching rather than firing and forgetting.

To repeat that dry run yourself:

```bash
git clone https://github.com/leightonrodney/mattermost-plugin-followups.git /tmp/release-check
cd /tmp/release-check
make apply && ./build/bin/manifest version   # must match the tag you intend
go test ./server/... && make dist && ./build/check-bundle.sh
```

## Signing

The bundle is not signed, and for the public Marketplace it does not need to be.
Mattermost builds and signs every listed plugin itself, with a key its servers
already trust; the submission template is explicit that the link given for review
"cannot point at an already-built plugin". Signing your own bundle only matters
for private distribution, where an administrator has added your public key to the
server. For a plain GitHub download, `checksums.txt` is what verifies it.

## Submitting to the Marketplace

Mattermost's Marketplace is populated from
[`mattermost/mattermost-marketplace`](https://github.com/mattermost/mattermost-marketplace).
Submission is an **issue** on that repository, using its "Add plugin" template —
not a pull request and not an upload. Mattermost then reviews the source, forks
it privately, and cuts and signs the release itself.

Before submitting, the plugin needs:

- [x] A public GitHub repository with an OSI-approved licence — **done**, MIT.
- [x] A tagged GitHub release with the bundle attached — **done**, v1.0.0,
      published by the release workflow on 26 October 2026. An earlier v0.1.0
      was published and withdrawn the same day.
- [x] A complete `plugin.json`: `id`, `name`, `description`, `version`,
      `min_server_version`, `homepage_url`, `support_url`, `release_notes_url`,
      `icon_path` — **done**, all present.
- [x] An SVG icon — **done**, `assets/icon.svg`.
- [x] A README with installation and usage instructions — **done**.
- [x] **Screenshots.** Done — five are in `docs/images/` and embedded in the
      README: the Actions menu, the reminder dialog, the sidebar in the light
      and dark themes, and a delivered reminder. All were taken from a real
      installation carrying sample reminders.
- [x] **Verification against the supported server versions.** The declared
      `min_server_version` of 9.5.0 began as a reading of API availability and
      is now tested: the integration suite runs in CI against both 9.5 and
      latest on every push, and passed against both on 26 October 2026.
- [x] ~~A signed bundle~~ — **not a submitter deliverable.** Mattermost builds and
      signs the plugin as part of listing it. Nothing to do here.
- [ ] **Version 1.0.0 or later.** The template requires a plugin to be out of
      Beta at v1.0.0+ for a full listing. That is met as of 1.0.0.
- [ ] **A security contact** — an email address or Community Server username,
      posted in the submission issue. SECURITY.md routes reports through GitHub
      private advisories, which is fine for the repository but is not what the
      template asks for.

Two further requirements are judged by a Mattermost developer during review
rather than by anything in this repository: that the plugin behaves at 60k
concurrent connections and in a High Availability deployment, and that it logs
important events at levels an administrator can act on. There is no public tool
to demonstrate the first; [docs/TESTING.md](TESTING.md) records what the
multi-node tests do and do not prove.

The text for that issue is already written, in
[MARKETPLACE-SUBMISSION.md](MARKETPLACE-SUBMISSION.md): every checklist item
answered, the gaps stated up front rather than left for a reviewer to find, and
nothing submitted. Review it, then open the issue against
`mattermost-marketplace` from its "Add plugin" template. Expect to answer questions about security and about what data the
plugin stores; [ARCHITECTURE.md](ARCHITECTURE.md) has both.

**Do not submit without the owner's authorisation.** Publishing a release and
submitting to the Marketplace are both outward-facing acts, and neither is part
of a routine build.

## Release notes

Write them for someone deciding whether to upgrade: what changed, what broke,
what to do about it. The workflow passes `--generate-notes`, which lists merged
pull requests; replace that with a written summary for anything beyond a patch.

## After a release

- [x] Download the published bundle and check it against `checksums.txt`.
      Done for 0.1.0: the SHA-256 of the downloaded archive matched the
      published `checksums.txt` exactly.
- [x] Install that exact artifact on a clean server and confirm it activates.
      Done for 0.1.0, on a throwaway Team Edition server that had never seen
      this plugin. The log records `Follow-ups activated` with a bot user ID,
      `Plugin activated version=0.1.0`, and the scheduler establishing its
      cursor; the bot account exists, and the plugin's API answers an
      unauthenticated request with 401 rather than 404, which is what shows the
      handler is actually serving. Activation succeeding at all is also the
      check that catches an invalid autocomplete definition, which once stopped
      the plugin starting on every server while CI stayed green.
- [x] Open the next `## Unreleased` section in `CHANGELOG.md` — already present
      above the released heading, so there was nothing to add.

One thing the install surfaces, worth not misreading: the server logs
`No signature when persisting plugin to filestore` at warn level. That is
expected for a bundle installed by hand and is not an error. Signature checks
apply to Marketplace and pre-packaged installs, not to an administrator
uploading a file.
