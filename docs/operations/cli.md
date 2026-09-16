# CLI

`spectra` performs diagnostics on the local Mac.

```bash
spectra /Applications/Slack.app
spectra --json /Applications/Slack.app
spectra snapshot --baseline pre-incident
spectra diff baseline pre-incident live
spectra jvm
spectra network
spectra toolchain
```

The principal commands are `inspect`, `list`, `snapshot`, `diff`, `jvm`,
`network`, `power`, `storage`, `process`, `toolchain`, `rules`, `issues`, and
`playbook`. Run `spectra help` for the installed command list.

Spectra has no `serve`, `connect`, `fan`, `hosts`, or `install-daemon`
commands. Cross-machine operations use the separately distributed Spectra
Remote project.
