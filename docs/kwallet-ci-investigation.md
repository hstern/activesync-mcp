# Driving KDE's wallet wizard from CI: a postmortem

We test our keyring code against multiple Secret Service providers
because they don't all behave the same. On Linux that's gnome-keyring
and KWallet. The gnome-keyring leg has worked since the start. This
is the story of why the KWallet leg doesn't, after eight CI iterations
and a few hours of local Docker spelunking.

It's parked on the `kwallet-leg` branch with full commit history. If
someone with more KDE Frameworks 6 context picks this up later, this
doc is the briefing.

## What we wanted

`zalando/go-keyring` talks D-Bus Secret Service. Both gnome-keyring
and KWallet implement that spec, but they differ in subtle ways
(collection/attribute handling, prompt-on-locked behavior) that bite
real KDE users. Running the integration suite against both providers
catches regressions specific to either path. The original CI matrix
had both; we dropped the kwallet leg when the runner image stopped
shipping the package.

## The architecture surprise

KF5 was simple: `kwalletd5` had a `--secret-service` flag. Run
the daemon with that flag and it registered `org.freedesktop.secrets`
on the bus. Done.

KF6 split this in two:

- **ksecretd** — the actual Secret Service implementation. Owns the
  storage, registers `org.freedesktop.secrets`. New binary in KF6.
- **kwalletd6** — a "KWallet compatibility service, wrapping upon
  Secret Service." The legacy KDE-API shim that talks to ksecretd
  underneath. Its `--help` literally says this; it took us several
  iterations to read that line carefully.

For Secret Service consumers (us), the right binary is **ksecretd
alone**. kwalletd6 is irrelevant. Most public examples of "running
kwallet headlessly" predate this split and are misleading.

## Distro support

GitHub Actions' `ubuntu-latest` (Noble) ships neither `kwalletd5`
nor `ksecretd` in its repos after the KF6 reorg. We worked around
that by switching the leg to `runs-on: ubuntu-latest` plus
`container: archlinux:latest`, which has `ksecretd` in the `kwallet`
package. Fedora would also work; openSUSE Tumbleweed too.

Cost of the container approach: ~30s extra to pacman-install Go and
deps in each run, plus a few container-specific footguns
(`/etc/machine-id` doesn't exist by default, the workspace ownership
mismatch trips Go's `git`-based VCS-stamp lookup with "exit status
128"). All solvable with one-line fixes.

## The actual blocker

ksecretd has no headless mode. The first call into
`Service.CreateCollection()` always pops a Qt wallet-creation
wizard. There is no `--no-prompt`, no `--allow-empty-password`, no
config knob that bypasses it. We checked the binary's strings and
its CLI surface; the wizard is the only path.

The wizard has three pages:

1. **Encryption-type chooser.** Defaults to GPG. There are no GPG
   keys in CI, so going with the default raises an error dialog
   ("Seems that your system has no keys suitable for encryption").
   The other option is "Classic, blowfish encrypted file" — which
   does allow an empty password.
2. **Password entry.** Two text fields plus a strength meter. With
   Blowfish, leaving them empty is allowed.
3. **Low-strength confirmation.** A modal asking "Use this password
   anyway?" with Yes/No.

Pre-creating a wallet file on disk doesn't help: ksecretd doesn't
discover stray `.kwl` files; the wizard always runs.

## What worked locally

Spinning up the same container on a workstation, we got the wizard
through reliably:

```bash
# fonts so the dialog text actually renders
pacman -S kwallet dbus libsecret xorg-server-xvfb openbox xdotool nettle ttf-dejavu

# Xvfb so Qt has a display; openbox so xdotool windowactivate works
# (twm doesn't support _NET_ACTIVE_WINDOW)
Xvfb :99 -screen 0 1024x768x24 &
export DISPLAY=:99
openbox &
eval "$(dbus-launch --sh-syntax)"
ksecretd &

# trigger wallet creation in the background
secret-tool store --label=bootstrap svc bootstrap <<<probe &

# wait for the wizard, then click through it
xdotool search --classname ksecretd | head -1   # find the window
xdotool windowactivate "$WIN"
xdotool mousemove 380 306 click 1  # Classic Blowfish radio
xdotool mousemove 614 501 click 1  # Finish (single-page after radio)
xdotool mousemove 511 440 click 1  # OK on empty password
xdotool mousemove 614 378 click 1  # Yes on low-strength warning
```

After this, secret-tool roundtrips work without any UI. ksecretd
auto-aliases the first collection to `default`, so SetAlias isn't
needed.

## What didn't work in CI

Same script, same container image, same packages, on
`ubuntu-latest`'s runner — and the wizard appeared but the clicks
never advanced past page 1. Six screenshots taken after each step
all came back byte-identical (same MD5).

We didn't fully diagnose this before parking the work, but the
plausible culprits are:

- **xdotool timing.** Local runs had several seconds of warmup
  between starting Xvfb/openbox and the first click; the CI runner
  is faster and may race the WM's initialization. `--sync` flags on
  xdotool calls might help.
- **Mouse-event delivery in the runner's container.** Container
  isolation occasionally interferes with how xdotool injects events
  through the X server's input extension. Possible fix: switch to
  `ydotool`, which uses `/dev/uinput` and bypasses X entirely. Needs
  uinput access in the container, which GitHub-hosted runners
  *might* permit (the container runs privileged-ish under the
  default action setup).
- **Window manager state.** openbox starts but might not have
  fully claimed the X server before we ask it to focus a window.
  A handshake (`wmctrl -l` until openbox is listed, or polling
  `_NET_SUPPORTING_WM_CHECK`) would harden this.

Each of these is a single CI iteration to test, and each iteration
costs ~5 minutes. We ran out of budget before the experiment.

## Plausible next directions

Pick one when revisiting:

- **Harden the existing approach.** Add openbox-readiness probes,
  `xdotool --sync` flags, longer settle times. Cheapest if the
  diagnosis is "just timing." Use the screenshot diagnostic the
  branch already has.
- **Switch to ydotool.** Skips X event injection entirely. Needs
  /dev/uinput; check if GitHub Actions containers expose it.
- **Pre-bake a container with the wallet already created.** Build
  a custom image (push to ghcr) where the wallet file is committed.
  ksecretd uses the existing wallet on startup, no wizard. This is
  the most reliable but adds a separate Dockerfile + image-publish
  workflow to maintain.
- **Patch ksecretd.** Add a config option like
  `KSecretD/AllowEmptyPasswordWallet=true` that bypasses the wizard
  entirely. Submit upstream. Most general fix; longest timeline.
- **Drop the leg.** gnome-keyring covers the same SS API surface;
  the kwallet-specific behaviors we'd catch are rare. Saves
  ongoing maintenance cost.

## Footguns we hit (and you'll hit too)

A grab-bag of things that ate iterations:

- **kwalletd5's `--secret-service` flag is gone in KF6.** The error
  is `Unknown option 'secret-service'` and most stale docs/SO
  answers will mislead you here.
- **kwalletd6 segfaults under `QT_QPA_PLATFORM=offscreen`.** It's a
  Qt app and crashes at QApplication construction with no display.
  Set the platform plugin or run a real X server.
- **`pkill -f Xvfb` from inside `docker exec` can kill the parent
  bash that's executing the script.** The exec process matches
  `Xvfb` in its environment. Use `pkill Xvfb` (no `-f`) or filter
  more carefully.
- **`/etc/machine-id` is missing in fresh Arch containers.** Fix:
  `dbus-uuidgen --ensure && ln -sf /var/lib/dbus/machine-id /etc/machine-id`.
- **Go's VCS stamping fails in containers because `git` refuses to
  read repos owned by a different uid than the running user.** Fix:
  `git config --global --add safe.directory "$GITHUB_WORKSPACE"`.
- **ksecretd emits `Prompt.Completed` with variant type `ao`
  (array of object paths) where the SS spec says `o` (single object
  path).** libsecret warns and discards the result. zalando/go-keyring
  doesn't actually use the variant for `CreateItem`, but
  `CreateCollection` does. The collection-creation code might
  silently misinterpret the result.
- **`secret-tool store` blocks indefinitely on the wizard.** Always
  wrap it in `timeout(1)` so a stuck dialog doesn't eat your
  step's budget.
- **dbus-send's dict-of-variant syntax for raw `CreateCollection`
  calls is fiddly.** It's easier to let `secret-tool` build the
  Properties dict (with the required `Label` property) and accept
  the libsecret detour.

## What's preserved on this branch

- Eight CI iterations as separate commits — each one has a one-line
  commit subject explaining what the iteration tried.
- The final `.github/workflows/ci.yml` change with the screenshot
  diagnostic step that proved the clicks weren't reaching the
  dialog in CI even though they did locally.
- `docs/kwallet-ci-investigation.md` — this file.

## Time spent vs. value

Total: roughly six hours across one session. The current state is:
gnome-keyring exercises the same SS API surface and catches the
overwhelming majority of regressions. The kwallet-specific
behaviors we'd catch are real (collection naming differences,
prompt-on-locked, attribute case-sensitivity) but rarely-hit.

For a Phase-1 ship, dropping the leg is the right call. Re-attempt
when one of the next-direction options above looks cheap, or when
a real user reports a kwallet-only bug we can't reproduce against
gnome-keyring.
