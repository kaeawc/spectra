# fseventsd backup leak

Use this playbook when a Mac has severe memory pressure and `fseventsd`,
`backupd`, APFS snapshots, or Time Machine scheduling appear in the same
incident window.

```bash
spectra playbook fseventsd-leak
spectra playbook fseventsd-leak --json
spectra playbook --commands fseventsd-leak
```

The playbook exits early with `no memory pressure detected.` unless either
compressed memory is more than 25% of physical memory or swap use is more than
10% of physical memory.

When memory pressure is present, Spectra correlates:

- `spectra process --min-rss=1GB --sort=rss --json`
- `spectra timemachine --json`
- `spectra storage --snapshots --json`
- `spectra services --label com.apple.backupd-auto --json`
- `spectra logs --process backupd --level Error --last 24h --top 50 --json`

A match reports `backup.destinationless_scheduler_leak` when `fseventsd` is in
the top resident processes, Time Machine has no destinations, `backupd-auto` is
loaded, and an `MSUPrepareUpdate` APFS snapshot is present.

The remediation is intentionally explicit:

```bash
sudo tmutil disable
sudo killall backupd
sudo killall -HUP fseventsd
```

`--auto-fix` prints the remediation and requires confirmation. It cannot be
combined with `--non-interactive` unless `--yes` is also passed.
