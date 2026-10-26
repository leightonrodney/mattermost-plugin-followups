# Releasing

## Versioning

Semantic versioning. The version lives in **`plugin.json`** and nowhere else;
everything derives from it. The release workflow refuses to publish if the git
tag and the manifest version disagree.

- **Patch** — fixes that change no behaviour anyone depends on.
- **Minor** — new features, new settings, new API endpoints.
- **Major** — anything that breaks an existing reminder, a stored record, or a
  documented command form.

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

**What still has to be done by hand**, because it is visual or needs a device:

- [ ] **The webapp.** Open the sidebar, move through all three tabs, add a
      reminder from the box, edit one, snooze one, cancel one. Then **Remind me**
      from a message's **Actions** menu, and check the delivered reminder links
      back to it.
- [ ] **A dark theme.** Switch to Onyx and look at the dialog and the sidebar.
      A test asserts no colour is hard-coded, which is not the same as the
      result being readable.
- [ ] **A mobile client.** Confirm a reminder arrives as a push notification.
      The suite checks the post is the kind the server will notify about; it
      cannot check that a phone buzzes.

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
   git tag -s -a v0.1.0 -m "v0.1.0"
   git push origin v0.1.0
   ```
   The Makefile's `make patch` / `make minor` / `make major` targets do the tag
   and push for you, with a confirmation prompt.

The `release` workflow then runs the tests, builds the bundle, checks it,
generates `checksums.txt` and creates the GitHub release with both attached.
Nothing publishes on a merge to `main`: a release only happens from a tag, or
from a manual `workflow_dispatch`.

Every step of that except the publish has been dry-run against a fresh clone —
version check, tests, five-platform build, bundle validation, checksums. The
publish itself has still never executed, so treat the first real release as
something to watch rather than fire and forget.

To repeat that dry run yourself:

```bash
git clone https://github.com/leightonrodney/mattermost-plugin-followups.git /tmp/release-check
cd /tmp/release-check
make apply && ./build/bin/manifest version   # must match the tag you intend
go test ./server/... && make dist && ./build/check-bundle.sh
```

## Signing

The bundle is not signed. Mattermost supports plugin signatures for Marketplace
distribution; if that is wanted, generate a key pair, add the private key as a
repository secret, and extend the release workflow to sign the bundle and attach
the `.sig`. Until then, `checksums.txt` is what verifies a download.

## Submitting to the Marketplace

Mattermost's Marketplace is populated from
[`mattermost/mattermost-marketplace`](https://github.com/mattermost/mattermost-marketplace).
Submission is a pull request to that repository, not an upload.

Before submitting, the plugin needs:

- [ ] A public GitHub repository with an OSI-approved licence — **done**, MIT.
- [ ] A tagged GitHub release with the bundle attached — produced by the release
      workflow.
- [ ] A complete `plugin.json`: `id`, `name`, `description`, `version`,
      `min_server_version`, `homepage_url`, `support_url`, `release_notes_url`,
      `icon_path` — **done**, all present.
- [ ] An SVG icon — **done**, `assets/icon.svg`.
- [ ] A README with installation and usage instructions — **done**.
- [ ] **Screenshots.** Not yet captured. The Marketplace listing wants images of
      the plugin in use; take them from a real installation.
- [ ] **Verification against the supported server versions.** The declared
      `min_server_version` of 9.5.0 is derived from API availability, not from
      testing against each release. Run the integration suite against the
      oldest supported server before submitting.
- [ ] A signed bundle, if Mattermost requires one at the time of submission.

Then open the pull request against `mattermost-marketplace` adding the plugin to
the manifest, and follow whatever review that repository's contributing guide
sets out. Expect to answer questions about security and about what data the
plugin stores; [ARCHITECTURE.md](ARCHITECTURE.md) has both.

**Do not submit without the owner's authorisation.** Publishing a release and
submitting to the Marketplace are both outward-facing acts, and neither is part
of a routine build.

## Release notes

Write them for someone deciding whether to upgrade: what changed, what broke,
what to do about it. The workflow passes `--generate-notes`, which lists merged
pull requests; replace that with a written summary for anything beyond a patch.

## After a release

- [ ] Download the published bundle and check it against `checksums.txt`.
- [ ] Install that exact artifact on a clean server and confirm it activates.
- [ ] Open the next `## Unreleased` section in `CHANGELOG.md`.
