# spectra-desktop

A diagnosis-first desktop app for the JVMs on this machine, built with
[Rune](https://github.com/anomalyco/rune). It opens on what's wrong, worst first; each finding
expands into its evidence and a fix.

It reads `spectra jvm -json` from the [Spectra](https://github.com/kaeawc/spectra) CLI, so every
running JVM shows up with nothing to set up. Set `SPECTRA_BIN` if `spectra` isn't on `PATH`.

```sh
cargo run                                              # the window
cargo run -- --snap out.png --scene sample --theme dark  # headless render with sample data
cargo test                                             # unit tests
cargo test -- --ignored --nocapture                    # reads the live feed
```

- `src/data.rs`: the feed and its parsing.
- `src/findings.rs`: the rules that turn a JVM's readings into findings.
- `src/main.rs`: state, polling and the view.

Rune is pre-release and pinned to a `dev` rev in `Cargo.toml`; the `[patch.crates-io]` revs must
match it.
