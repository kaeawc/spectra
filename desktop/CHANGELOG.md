# Changelog

Notable changes to Spectra Desktop. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow
[Semantic Versioning](https://semver.org/). Add entries under `## [Unreleased]`;
`scripts/release.sh` moves them under the new version.

## [Unreleased]

## [0.1.0] - 2026-11-27

### Added

First release. A diagnosis-first desktop app for every process on this Mac, reading
[Spectra](https://github.com/kaeawc/spectra) (its daemon where it has a reading, else the CLI):

- Overview: what's wrong, worst first, with "Right now" causes from `spectra whatswrong`, and
  findings for sustained CPU, memory growth, code signatures, JVM heap and GC, memory pressure,
  swap, throttling and full disks.
- Machine, Memory and alerts, Activity (process starts and exits, spawn storms), Timeline,
  Crashes (signatures, reports, memory kills, Show code), Network, Startup (with Disable and
  Enable for your own launch agents), Storage, Compare (snapshot diffs), Toolchains and Other Macs
  (read-only, through Spectra Remote).
- All processes, grouped by app or as a tree, by kind and by runtime; app and process pages with
  live charts, JVM tools, flame graphs, logs, Quit and Kill.
- Releases: macOS binaries (Apple silicon and Intel) on GitHub Releases, built when a version bump
  lands on main.
