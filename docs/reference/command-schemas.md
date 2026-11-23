# Command result schemas

Each JSON command that `spectra capabilities --json` advertises carries a
`result_schema` name and version. The protocol-owned schemas
(`spectra.inspect`, `spectra.snapshot`, `spectra.capabilities`) are described
in [Result schema](result-schema.md) and [Capabilities](capabilities.md). This
page documents the schemas Spectra itself owns, one section per schema.

Each section ends with the schema's field list: one line per JSON path and its
JSON type (`string`, `number`, `bool`, `object`, `array`, `null`). `a[]` is an
element of array `a`; `m.*` is any value of object map `m`. Fields marked
`omitempty` in Go are listed although they are absent when empty or zero.
A recursive field is listed for its first level only (for example lineage `tree.children[]`); deeper levels repeat the same shape.

## Stability

The field lists are checked in and compared with the Go result types by
`TestCommandResultSchemasMatchDocs` in `cmd/spectra/result_schemas_test.go`;
any difference fails the test. Within a schema version, fields may be added:
regenerate the lists with

```bash
TMPDIR=/tmp go test ./cmd/spectra/ -run TestCommandResultSchemasMatchDocs -update-command-schemas
```

Removing or renaming a field, or changing its JSON type, is a breaking
change: bump the version in `internal/capabilities` (and the `<!-- fields -->`
marker below) in the same change. Consumers must check the advertised
`result_schema` name and version before decoding and must ignore unknown
fields.

## Unsupported on this OS

Subcommands that only exist on macOS (`spectra help` marks them
`[macOS only]`) exit 1 off macOS. With `--json` they print one object on
stdout instead of a stderr line, so a script can tell "not on this OS" from
a failure:

```json
{"command": "vmmap", "error": "unsupported_platform", "feature": "vmmap memory composition",
 "os": "linux", "message": "vmmap memory composition: unsupported on host OS (linux)",
 "hint": "on Linux, read /proc/<pid>/smaps_rollup; `spectra procdiag <pid>` covers cgroup memory limits"}
```

`hint` is present when there is a Linux alternative. Commands that work on
both systems but have nothing to report still print valid JSON:
`spectra jvm --json` prints `[]` with no JVMs, and `spectra rules --json`
prints `[]` with no findings.

## spectra.load

`spectra load --json` emits one object: whether this machine can take another
job (`verdict` is `admit`, `hold` or `backoff`, mirrored in `exit_code`: 0, 3
and 4), the 1-minute load and available memory against their limits, the
likely `cause`, and the top processes, process families, root directories and
booted simulators or emulators behind the load. `available_gb` is `null` when
memory could not be read. `cpu_basis` is `window`, `recent` or `lifetime`;
`cpu_window_ms` is positive only for `window`. `spotlight` (macOS) reports
Spotlight indexing state per `--root`.

<!-- fields: spectra.load v1 -->
```text
available_gb number
backoff_free_gb number
cause object
cause.cpu_pct number
cause.detail string
cause.kind string
cause.repo string
cause.root string
cause.runner string
cause.worktree string
cpu_basis string
cpu_window_ms number
cpus number
devices object
devices.boot_window_seconds number
devices.booted number
devices.booted_recently number
devices.by_root array
devices.by_root[] object
devices.by_root[].booted number
devices.by_root[].booted_recently number
devices.by_root[].root string
devices.emulators number
devices.list array
devices.list[] object
devices.list[].age_seconds number
devices.list[].avd string
devices.list[].booted_recently bool
devices.list[].cpu_pct number
devices.list[].id string
devices.list[].kind string
devices.list[].members number
devices.list[].pid number
devices.list[].root string
devices.list[].rss_kib number
devices.list[].started_at string
devices.list[].udid string
devices.simulators number
dirs array
dirs[] object
dirs[].cpu_pct number
dirs[].dir string
dirs[].procs number
dirs[].rss_kib number
elapsed_ms number
exit_code number
families array
families[] object
families[].cpu_pct number
families[].kind string
families[].members number
families[].name string
families[].rss_kib number
load1 number
load_limit number
min_free_gb number
notes array
notes[] string
process_count number
reason string
spotlight array
spotlight[] object
spotlight[].excluded bool
spotlight[].hint string
spotlight[].marker string
spotlight[].noindex_dir string
spotlight[].privacy_entry string
spotlight[].privacy_list string
spotlight[].root string
spotlight[].size object
spotlight[].size.bytes number
spotlight[].size.files number
spotlight[].size.partial bool
spotlight[].size_error string
spotlight[].unsupported string
spotlight[].volume object
spotlight[].volume.detail string
spotlight[].volume.mount_point string
spotlight[].volume.progress object
spotlight[].volume.progress.percent number
spotlight[].volume.progress.phase string
spotlight[].volume.status string
spotlight[].volume_error string
top_cpu array
top_cpu[] object
top_cpu[].command string
top_cpu[].cpu_pct number
top_cpu[].cwd string
top_cpu[].dir string
top_cpu[].pid number
top_cpu[].rss_kib number
top_rss array
top_rss[] object
top_rss[].command string
top_rss[].cpu_pct number
top_rss[].cwd string
top_rss[].dir string
top_rss[].pid number
top_rss[].rss_kib number
unknown object
unknown.cpu_pct number
unknown.dir string
unknown.procs number
unknown.rss_kib number
verdict string
```

## spectra.orphans

`spectra orphans --json` emits one object listing leftover processes (parent
pid 1, working directory under a root), grouped by root and worktree.
`daemon_by_design` processes are counted in `daemon_by_design_total` and left
out of `total` and the exit code (0 none, 3 non-daemon orphans found).
`limitations` states what the heuristic cannot tell apart.

<!-- fields: spectra.orphans v1 -->
```text
daemon_by_design_total number
groups array
groups[] object
groups[].processes array
groups[].processes[] object
groups[].processes[].age_seconds number
groups[].processes[].command string
groups[].processes[].command_line string
groups[].processes[].cpu_pct number
groups[].processes[].cwd string
groups[].processes[].cwd_missing bool
groups[].processes[].cwd_unreadable bool
groups[].processes[].daemon_by_design bool
groups[].processes[].descendants number
groups[].processes[].origin object
groups[].processes[].origin.agent object
groups[].processes[].origin.agent.alive bool
groups[].processes[].origin.agent.command string
groups[].processes[].origin.agent.command_line string
groups[].processes[].origin.agent.cwd string
groups[].processes[].origin.agent.depth number
groups[].processes[].origin.agent.exited_at string
groups[].processes[].origin.agent.pid number
groups[].processes[].origin.agent.role string
groups[].processes[].origin.agent.start_time string
groups[].processes[].origin.first_seen string
groups[].processes[].origin.known bool
groups[].processes[].origin.parent object
groups[].processes[].origin.parent.alive bool
groups[].processes[].origin.parent.command string
groups[].processes[].origin.parent.command_line string
groups[].processes[].origin.parent.cwd string
groups[].processes[].origin.parent.depth number
groups[].processes[].origin.parent.exited_at string
groups[].processes[].origin.parent.pid number
groups[].processes[].origin.parent.role string
groups[].processes[].origin.parent.start_time string
groups[].processes[].origin.reason string
groups[].processes[].origin.recording_since string
groups[].processes[].origin.session object
groups[].processes[].origin.session.alive bool
groups[].processes[].origin.session.command string
groups[].processes[].origin.session.command_line string
groups[].processes[].origin.session.cwd string
groups[].processes[].origin.session.depth number
groups[].processes[].origin.session.exited_at string
groups[].processes[].origin.session.pid number
groups[].processes[].origin.session.role string
groups[].processes[].origin.session.start_time string
groups[].processes[].pid number
groups[].processes[].rss_kib number
groups[].processes[].subtree_rss_kib number
groups[].processes[].user string
groups[].root string
groups[].rss_kib number
groups[].worktree string
groups[].worktree_removed bool
limitations array
limitations[] string
roots array
roots[] string
skipped_unreadable_cwd number
total number
```

## spectra.owner

`spectra owner --json <target>` emits one object for a port (`:8080`,
`tcp:8080`, `udp:53`), path or pid target: the parsed `target`, and every
visible `holder` with how it holds the target (`hold`, `mode`) and its parent
`chain`. `incomplete` and `hint` say when processes could not be read (for
example, without root). Exit 0 when a holder is found, 3 when none.

<!-- fields: spectra.owner v1 -->
```text
hint string
holders array
holders[] object
holders[].chain array
holders[].chain[] object
holders[].chain[].command string
holders[].chain[].pid number
holders[].command string
holders[].cwd string
holders[].detail string
holders[].executable string
holders[].hold string
holders[].mode string
holders[].pid number
holders[].started string
holders[].user string
incomplete bool
target object
target.kind string
target.path string
target.pid number
target.port number
target.proto string
target.raw string
```

## spectra.owner.under

`spectra owner --under --json <dir>...` emits one object listing processes
whose working directory is inside any of `dirs`. Exit 0 when one is found, 3
when none is and every process was read, 4 when none is visible but
`incomplete` is true (the tree may still be in use).

<!-- fields: spectra.owner.under v1 -->
```text
dirs array
dirs[] string
hint string
holders array
holders[] object
holders[].chain array
holders[].chain[] object
holders[].chain[].command string
holders[].chain[].pid number
holders[].command string
holders[].cwd string
holders[].detail string
holders[].executable string
holders[].hold string
holders[].mode string
holders[].pid number
holders[].started string
holders[].user string
incomplete bool
```

## spectra.lineage

`spectra lineage --json <pid>` emits one object: the process's `ancestors` (up
to pid 1) and its descendant `tree`. Each tree node has `children` of the same
node shape; `count`, `subtree_rss_kib` and `subtree_cpu_pct` cover the node
and all its descendants.

<!-- fields: spectra.lineage v1 -->
```text
ancestors array
ancestors[] object
ancestors[].command string
ancestors[].pid number
ancestors[].ppid number
ancestors[].start_time string
ancestors[].user string
flags array
flags[] string
pid number
tree object
tree.children array
tree.children[] object
tree.children[].command string
tree.children[].count number
tree.children[].cpu_pct number
tree.children[].flags array
tree.children[].flags[] string
tree.children[].pid number
tree.children[].ppid number
tree.children[].rss_kib number
tree.children[].start_time string
tree.children[].subtree_cpu_pct number
tree.children[].subtree_rss_kib number
tree.children[].user string
tree.command string
tree.count number
tree.cpu_pct number
tree.flags array
tree.flags[] string
tree.pid number
tree.ppid number
tree.rss_kib number
tree.start_time string
tree.subtree_cpu_pct number
tree.subtree_rss_kib number
tree.user string
```

## spectra.spawns

`spectra spawns --json` emits newline-delimited JSON: one `event` record per
process start or exit seen while polling (`event` is `start` or `exit`;
`lifetime_ms` and `lifetime_max_error_ms` appear on exits only), then one
`summary` record, recognised by `"type": "summary"`. Field lines below are
prefixed with their record kind.

<!-- fields: spectra.spawns v1 -->
```text
event: command string
event: event string
event: lifetime_max_error_ms number
event: lifetime_ms number
event: parent_chain array
event: parent_chain[] string
event: pid number
event: ppid number
event: time string
summary: by_command array
summary: by_command[] object
summary: by_command[].count number
summary: by_command[].name string
summary: by_command_total number
summary: by_parent array
summary: by_parent[] object
summary: by_parent[].count number
summary: by_parent[].name string
summary: by_parent_total number
summary: duration_seconds number
summary: effective_interval_seconds number
summary: exits number
summary: interval_seconds number
summary: note string
summary: polls number
summary: respawn_loops array
summary: respawn_loops[] object
summary: respawn_loops[].command string
summary: respawn_loops[].count number
summary: respawn_loops[].parent string
summary: root_exited bool
summary: shortest_lived array
summary: shortest_lived[] object
summary: shortest_lived[].command string
summary: shortest_lived[].lifetime_seconds number
summary: shortest_lived[].pid number
summary: skipped_ticks number
summary: starts number
summary: type string
```

## spectra.devices

`spectra devices --json` emits one object listing the mobile devices work on
this host may be using, from three sources, each with its own `status` (`ok`,
`not_found`, `server_not_running`, `unsupported` or `error`) and `message`:

- `android`: `adb devices -l` (USB, emulators, and devices reached through
  `adb connect`, marked `remote`). adb is asked only when an adb server is
  already running, or `ADB_SERVER_SOCKET`/`ANDROID_ADB_SERVER_ADDRESS` points
  at one, or `--start-adb` is given; otherwise the status is
  `server_not_running`, because asking would start a server that keeps
  running after Spectra exits.
- `ios_simulators`: booted simulators from `xcrun simctl` (macOS;
  `unsupported` elsewhere).
- `processes`: emulator and simulator processes on this host, the same block
  as `spectra.load`'s `devices`, attributed to each `--root`.

Lists are `[]` when empty. Exit 0.

<!-- fields: spectra.devices v1 -->
```text
android object
android.adb string
android.devices array
android.devices[] object
android.devices[].device string
android.devices[].emulator bool
android.devices[].model string
android.devices[].product string
android.devices[].remote bool
android.devices[].serial string
android.devices[].state string
android.devices[].transport_id string
android.message string
android.status string
ios_simulators object
ios_simulators.booted array
ios_simulators.booted[] object
ios_simulators.booted[].name string
ios_simulators.booted[].runtime string
ios_simulators.booted[].state string
ios_simulators.booted[].udid string
ios_simulators.message string
ios_simulators.status string
limitations array
limitations[] string
os string
processes object
processes.boot_window_seconds number
processes.booted number
processes.booted_recently number
processes.by_root array
processes.by_root[] object
processes.by_root[].booted number
processes.by_root[].booted_recently number
processes.by_root[].root string
processes.emulators number
processes.list array
processes.list[] object
processes.list[].age_seconds number
processes.list[].avd string
processes.list[].booted_recently bool
processes.list[].cpu_pct number
processes.list[].id string
processes.list[].kind string
processes.list[].members number
processes.list[].pid number
processes.list[].root string
processes.list[].rss_kib number
processes.list[].started_at string
processes.list[].udid string
processes.simulators number
```

## spectra.idle

`spectra idle --json` emits one object: the criteria used (`roots`, `pids`,
`min_age_seconds`, `max_cpu_pct`, `window_ms`) and in `result` the long-idle
holders with their parent chain, descendants and what they wait on, plus
counts of processes skipped and why. Exit 0 when no idle holder is found, 3
when one is.

<!-- fields: spectra.idle v1 -->
```text
cpu_basis string
hint string
incomplete bool
limitations array
limitations[] string
max_cpu_pct number
min_age_seconds number
pids array
pids[] number
result object
result.checked number
result.holders array
result.holders[] object
result.holders[].age_seconds number
result.holders[].chain array
result.holders[].chain[] object
result.holders[].chain[].command string
result.holders[].chain[].pid number
result.holders[].command string
result.holders[].command_line string
result.holders[].cpu_pct number
result.holders[].cwd string
result.holders[].descendants array
result.holders[].descendants[] object
result.holders[].descendants[].command string
result.holders[].descendants[].cpu_pct number
result.holders[].descendants[].pid number
result.holders[].descendants[].ppid number
result.holders[].descendants[].started string
result.holders[].descendants[].wait object
result.holders[].descendants[].wait.lock_files array
result.holders[].descendants[].wait.lock_files[] string
result.holders[].descendants[].wait.wchan string
result.holders[].descendants_total number
result.holders[].matched string
result.holders[].pid number
result.holders[].ppid number
result.holders[].started string
result.holders[].tree_cpu_pct number
result.holders[].user string
result.holders[].wait object
result.holders[].wait.lock_files array
result.holders[].wait.lock_files[] string
result.holders[].wait.wchan string
result.missing_pids array
result.missing_pids[] number
result.nested number
result.skipped_busy number
result.skipped_unmeasured number
result.skipped_young number
roots array
roots[] string
window_ms number
```

## spectra.startup

`spectra startup list --json` (macOS) emits one object listing launchd agents,
daemons, login items and background-task-management entries, with the state of
each source that was read.

<!-- fields: spectra.startup v1 -->
```text
collected_at string
errors array
errors[] object
errors[].error string
errors[].label string
errors[].path string
items array
items[] object
items[].apple bool
items[].btm object
items[].btm.assoc_bundle_ids array
items[].btm.assoc_bundle_ids[] string
items[].btm.bundle_id string
items[].btm.developer_name string
items[].btm.disposition array
items[].btm.disposition[] string
items[].btm.executable_path string
items[].btm.identifier string
items[].btm.parent_identifier string
items[].btm.team_id string
items[].btm.type string
items[].btm.uid number
items[].btm.url string
items[].disabled bool
items[].disabled_by string
items[].domain string
items[].duplicate_of array
items[].duplicate_of[] string
items[].kind string
items[].label string
items[].last_exit string
items[].loaded bool
items[].name string
items[].owner object
items[].owner.app string
items[].owner.bundle_id string
items[].owner.bundle_path string
items[].owner.developer string
items[].owner.team_id string
items[].owner.via string
items[].pid number
items[].plist_owner string
items[].plist_path string
items[].program string
items[].program_arguments array
items[].program_arguments[] string
items[].run_at_load bool
items[].running bool
items[].scope string
items[].sources array
items[].sources[] string
items[].trigger string
items[].triggers array
items[].triggers[] string
sources array
sources[] object
sources[].detail string
sources[].name string
sources[].status string
uid number
```

## spectra.startup.change

`spectra startup disable|enable <label> --json` (macOS) emits one object with
the launchctl `commands` run and the item `before` and `after` the change,
re-read from launchd. `refused` with `reason` explains a change that was not
made (with `suggested_commands` to run yourself); `mismatch` explains one that
did not take effect. Exit 0
ok, 1 error or mismatch, 3 refused.

<!-- fields: spectra.startup.change v1 -->
```text
action string
after object
after.apple bool
after.btm object
after.btm.assoc_bundle_ids array
after.btm.assoc_bundle_ids[] string
after.btm.bundle_id string
after.btm.developer_name string
after.btm.disposition array
after.btm.disposition[] string
after.btm.executable_path string
after.btm.identifier string
after.btm.parent_identifier string
after.btm.team_id string
after.btm.type string
after.btm.uid number
after.btm.url string
after.disabled bool
after.disabled_by string
after.domain string
after.duplicate_of array
after.duplicate_of[] string
after.kind string
after.label string
after.last_exit string
after.loaded bool
after.name string
after.owner object
after.owner.app string
after.owner.bundle_id string
after.owner.bundle_path string
after.owner.developer string
after.owner.team_id string
after.owner.via string
after.pid number
after.plist_owner string
after.plist_path string
after.program string
after.program_arguments array
after.program_arguments[] string
after.run_at_load bool
after.running bool
after.scope string
after.sources array
after.sources[] string
after.trigger string
after.triggers array
after.triggers[] string
before object
before.apple bool
before.btm object
before.btm.assoc_bundle_ids array
before.btm.assoc_bundle_ids[] string
before.btm.bundle_id string
before.btm.developer_name string
before.btm.disposition array
before.btm.disposition[] string
before.btm.executable_path string
before.btm.identifier string
before.btm.parent_identifier string
before.btm.team_id string
before.btm.type string
before.btm.uid number
before.btm.url string
before.disabled bool
before.disabled_by string
before.domain string
before.duplicate_of array
before.duplicate_of[] string
before.kind string
before.label string
before.last_exit string
before.loaded bool
before.name string
before.owner object
before.owner.app string
before.owner.bundle_id string
before.owner.bundle_path string
before.owner.developer string
before.owner.team_id string
before.owner.via string
before.pid number
before.plist_owner string
before.plist_path string
before.program string
before.program_arguments array
before.program_arguments[] string
before.run_at_load bool
before.running bool
before.scope string
before.sources array
before.sources[] string
before.trigger string
before.triggers array
before.triggers[] string
commands array
commands[] object
commands[].argv array
commands[].argv[] string
commands[].exit_code number
commands[].stderr string
domain string
label string
message string
mismatch string
pending bool
reason string
refused bool
suggested_commands array
suggested_commands[] string
unreadable_plists array
unreadable_plists[] object
unreadable_plists[].error string
unreadable_plists[].label string
unreadable_plists[].path string
```

## spectra.uninstall.plan

`spectra uninstall plan --json <app>` (macOS) emits the plan that
`spectra uninstall apply --plan` reads: the app, and every file, launch item
and process found for it with size, owner, confidence and whether it can be
moved. Its own `schema` field is `spectra.uninstall.plan/v1`; apply rejects any
other value.

<!-- fields: spectra.uninstall.plan v1 -->
```text
app object
app.app_groups array
app.app_groups[] string
app.apple_signed bool
app.bundle_id string
app.declared_launchd_labels array
app.declared_launchd_labels[] string
app.display_names array
app.display_names[] string
app.helper_bundle_ids array
app.helper_bundle_ids[] string
app.name string
app.path string
app.receipts array
app.receipts[] string
app.team_id string
app.version string
created_at string
home string
items array
items[] object
items[].action string
items[].command string
items[].confidence string
items[].detail string
items[].kind string
items[].note string
items[].owner_uid number
items[].path string
items[].reason string
items[].removable_by_user bool
items[].shared bool
items[].size_bytes number
items[].symlink bool
launchd_labels array
launchd_labels[] string
limitations array
limitations[] string
processes array
processes[] object
processes[].name string
processes[].pid number
processes[].start_time string
schema string
uid number
warnings array
warnings[] string
```

## spectra.uninstall.apply

`spectra uninstall apply --plan <file> --json` (macOS) emits one object with
what was moved to the Trash, skipped (and why), booted out, and what needs
root. `error` is present when apply stopped early. Exit 3 when items need root
or failed to move.

<!-- fields: spectra.uninstall.apply v1 -->
```text
app string
bootouts array
bootouts[] object
bootouts[].error string
bootouts[].failed bool
bootouts[].target string
bundle_id string
dry_run bool
error string
leftovers bool
moved array
moved[] object
moved[].kind string
moved[].path string
moved[].to string
needs_root array
needs_root[] object
needs_root[].command string
needs_root[].kind string
needs_root[].path string
relaunched array
relaunched[] object
relaunched[].name string
relaunched[].pid number
relaunched[].start_time string
running array
running[] object
running[].name string
running[].pid number
running[].start_time string
skipped array
skipped[] object
skipped[].kind string
skipped[].path string
skipped[].reason string
terminated array
terminated[] object
terminated[].name string
terminated[].pid number
terminated[].start_time string
trash_dir string
```
