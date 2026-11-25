# Helper VM integration test

!!! note "Status: scripted, awaiting its first run"
    Decided by the owner (2026-11-21, issue #650): the VM-only integration
    test for the [helper launchd mutation](helper-launchd-mutation.md)
    methods runs in a disposable **Tart** VM on the owner's Mac. The owner
    provides the Developer ID test certificate at run time. Run it before
    each release that changes the helper (`cmd/spectra-helper`,
    `internal/helper`, `internal/helperclient`), and post the summary on the
    release's tracking issue.

The helper's launchd `disable`/`enable` methods change system launch daemons
as root. Their unit tests use fakes. This suite runs the real helper code
against the real `launchctl`, `codesign`, `sfltool` and filesystem. It can
only run in a throwaway VM, for two reasons:

- it installs a fixture app in `/Applications` and plists in
  `/Library/LaunchDaemons`, and disables and re-enables a daemon;
- check 7 of the design requires a verified, non-Apple team id. An ad-hoc
  signature has none, so the fixture must be signed with a real
  Apple-issued **Developer ID Application** certificate. That certificate
  should exist only inside the VM.

## The one command

On an Apple silicon Mac with [Tart](https://tart.run/) and Go installed, from
a checkout of the commit under test:

```bash
scripts/helper-vm-test.sh --p12 /path/to/spectra-vm-test.p12 \
    --p12-password-item spectra-vm-test-p12
```

`--p12-password-item` names a generic-password item in the host login
keychain that holds the `.p12` password. Create it once with
`security add-generic-password -s spectra-vm-test-p12 -a "$USER" -w`; with
`-w` last, `security` prompts for the password, so it never lands in shell
history. Without the option the script asks for the password on the
terminal, with echo off.

`scripts/helper-vm-test.sh --dry-run` prints every step and command and
changes nothing. It needs no Mac, no Tart and no certificate.

The first run pulls the base image (tens of GB). Later runs clone the
cached image in seconds.

## What the script does

1. Checks the host: macOS on arm64, `tart` with `exec`, `go`, a readable
   `.p12`, and either the keychain item or a terminal for the password.
   It checks the item exists without reading the password.
2. Cross-compiles the suite (`go test -c -tags helper_vm`) and the fixture
   daemon (`internal/helper/testdata/vmfixture`) for `darwin/arm64` on the
   host. The VM needs no Go toolchain.
3. Runs `tart clone <image> spectra-helper-vm-<UTC time>-<pid>`. The default
   image is `ghcr.io/cirruslabs/macos-tahoe-base:latest`; `--image` picks
   another.
4. Boots the clone headless (`tart run --no-graphics`).
5. Waits (up to 10 minutes) until `tart exec` answers.
6. Copies the suite, the fixture binary and `scripts/helper-vm/guest.sh` into
   `/private/var/tmp/spectra-vm` (mode `0700`) in the guest.
7. Streams the `.p12` into the guest on stdin.
8. Runs `guest.sh import-cert`, which reads the password on stdin. It
   creates a keychain that exists only in the VM, with a random password,
   and imports Apple's public Developer ID and WWDR intermediates and the
   `.p12`. It allows `codesign` to use the key, then deletes the `.p12`
   copy. It requires exactly one valid `Developer ID Application` identity
   and records only its team id.
9. Runs `guest.sh build-fixture`. This builds `SpectraVMFixture.app`
   (bundle id `dev.spectra.vmfixture`), with the daemon at
   `Contents/Library/LaunchServices/dev.spectra.vmfixture.agentd`. It signs
   the nested daemon and then the bundle, without a timestamp, and runs
   `codesign --verify --strict` on the result.
10. Runs `guest.sh run-tests`, which runs the suite as root:
    `sudo env SPECTRA_VM_TEST=1 … helper_vm.test -test.v -test.run '^TestVM'`.
11. Copies the guest's `results/` out and converts the log with
    `go tool test2json`.
12. Writes `summary.txt` and exits 0 only if every `TestVM*` test passed and
    none was skipped.
13. Stops and deletes the VM. An `EXIT` trap does this on success, on
    failure and on Ctrl-C. `--keep-vm` keeps the VM for debugging, and the
    script warns that the VM still holds the certificate.

Guest commands run through `tart exec` and the Tart guest agent, which runs
them as the image's `admin` user. The Cirrus Labs `-base` images ship the
agent and passwordless `sudo` for `admin`; `-vanilla` images have neither
the agent nor Homebrew.

## What the suite checks

`internal/helper/launchd_vm_test.go` (build tags `helper_vm && darwin`)
serves a fresh helper dispatcher, with `helper.RegisterAll` and the real
commands, on a private socket under `/tmp`. It calls the helper through
`helperclient`, so the framing and peer-uid path run too. It never touches
an installed helper or `/var/run/spectra-helper.sock`. Each test installs
its own fixture and removes it afterwards: bootout, `enable` to clear the
override, then delete the plist and the app.

| Test | Proves |
|---|---|
| `TestVMDisableThenEnable` | A loaded fixture is prepared, disabled (`disable` + `bootout`), then enabled and bootstrapped again. `launchctl print` and `print-disabled` agree with each result. The audit log pairs prepare and execute by `token_hash` and never contains the token. |
| `TestVMReplayedTokenActsOnce` | A used token is refused `token_invalid` with no commands run, and the daemon is left as it was. |
| `TestVMEnableBootstrapsUnloadedDaemon` | `enable` on a disabled, never-loaded daemon plans and runs `bootstrap`, and the job loads. |
| `TestVMTargetChangedAfterPrepare` | Rewriting the plist between prepare and execute refuses `target_changed`, and the daemon is untouched. |
| `TestVMRefusesAppleLabel` | `com.apple.*` refuses `apple_label`. |
| `TestVMRefusesSymlinkedPlist` | A symlinked plist in `/Library/LaunchDaemons` refuses `symlink`. |
| `TestVMRefusesNonRootPlist` | A plist owned by the `admin` user refuses `plist_permissions`. |
| `TestVMRefusesServiceManagementRule` | A `com.apple.servicemanagement` `Label` rule in `/Library/Managed Preferences` refuses `managed`. |

The real run also checks things the fakes cannot: what `sfltool dumpbtm`
reports for a legacy plist daemon on the image's macOS version (the pinned
flag set `legacy`, `curated`, `sandboxed`), real `codesign` verdicts for a
Developer ID bundle, and launchd's real state after each change.

Guards: without `SPECTRA_VM_TEST=1` every test skips. With it set, a test
fails unless it runs as root and `sysctl kern.hv_vmm_present` is 1, so a
stray opt-in on a real Mac changes nothing. `guest.sh` refuses to run
outside a VM for the same reason.

Not covered here: the admin-group gate for non-root callers (the suite
calls as root), the installed helper's socket permissions, and the CLI's
confirmation prompt. Unit tests cover these. An end-to-end run through
`spectra install-helper` and `spectra startup disable --scope system` could
be added later.

## Results to post

The results go to `helper-vm-results/<UTC time>/` (git-ignored), or to the
directory given with `--results`:

| File | Content |
|---|---|
| `summary.txt` | Verdict, commit, image, Tart version, guest macOS version and build, signing team id, test counts, date |
| `test.log`, `test.json` | Verbose test output, and the same as `test2json` events |
| `audit.jsonl` | The helper's audit lines from the run (token hashes only, never tokens) |
| `sw_vers.txt`, `uname.txt`, `tart-run.log` | Guest and VM details |

Post `summary.txt` on the release's tracking issue, or on #650 for the
first run. On a failure, attach `test.log` and `audit.jsonl` as well. None
of these files holds the certificate, its password or a token. The team id
is public: it is in every signature the certificate makes.

## Certificate handling

- **Who provides it:** the owner, at run time. No agent, CI job or
  repository file holds it. Use a dedicated Developer ID Application
  certificate for this test and never use it to sign anything shipped.
  Revoke it in the Apple Developer portal if the `.p12` leaks.
- **On the host,** the script reads the `.p12` path from `--p12` and streams
  the file into the VM. The password comes from the named keychain item
  (`security find-generic-password -w`) or a hidden prompt, and is piped to
  the guest. It is never an argument of a host process, never echoed, and
  never written to the results. Both scripts run with `set +x`.
- **In the guest,** the password exists only while `security import` runs,
  as its `-P` argument. That is visible only inside the disposable VM. The
  key lives in a keychain created for this run, and the `.p12` copy is
  deleted after import. Deleting the VM at the end removes both.
- `scripts/test-helper-vm-test.sh` checks these rules on any OS. It
  replaces `tart`, `go`, `security` and `uname` with shims, runs the full
  flow, and asserts that a fake password and a fake `.p12` reach the guest
  only on stdin, never on a command line or in the output. It also asserts
  that the VM is deleted after success and after failure.

## Choosing Tart and `tart exec`

The options considered for running commands in the guest:

| Option | Why not, or why |
|---|---|
| SSH with `sshpass` and the image's `admin`/`admin` login | `sshpass` is not in Homebrew core. It also needs the VM's IP, networking, and a host-key policy. |
| SSH with an injected key | Needs a password login first to install the key, so it has the same problem. |
| **`tart exec` through the Tart guest agent** (chosen) | Built into Tart and into the Cirrus Labs `-base` images. Needs no network or credentials, passes the guest's exit code through, and streams stdin (`-i`), which is how the certificate and password travel. |

Sources: [Tart quick start](https://tart.run/quick-start/) (images,
`admin`/`admin`, `--dir`), [Tart guest agent
announcement](https://tart.run/blog/2025/06/01/bridging-the-gaps-with-the-tart-guest-agent/)
(`tart exec`, exit codes, agent in non-vanilla images),
[macos-image-templates](https://github.com/cirruslabs/macos-image-templates)
(`base.pkr.hcl` installs the agent; the vanilla templates configure
passwordless `sudo`), and the Tart source for `exec`, `run --no-graphics`,
`clone`, `stop` and `delete` flags.

## Validating the script without a Mac

```bash
bash scripts/test-helper-vm-test.sh                              # shimmed full flow
BASH_UNDER_TEST=/path/to/bash-3.2 bash scripts/test-helper-vm-test.sh
shellcheck scripts/helper-vm-test.sh scripts/helper-vm/guest.sh
GOOS=darwin GOARCH=arm64 go vet -tags helper_vm ./internal/helper/
```
