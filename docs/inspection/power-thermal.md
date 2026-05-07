# Power and thermal inspection

Spectra treats power and thermal state as host inspection data. It is separate
from app bundle detection and separate from remote fan-out.

The local command is:

```bash
spectra power
spectra power --json
```

Remote callers use the same collector through the daemon RPC method:

```bash
spectra connect work-mac power
spectra fan --hosts work-mac,alice-laptop power
```

`spectra fan` means fan-out across remote Spectra daemons. It does not control,
query, or tune hardware fans.

## Data model

`power.state` returns `sysinfo.PowerState`:

```go
type PowerState struct {
    OnBattery       bool             `json:"on_battery"`
    BatteryPct      int              `json:"battery_pct"`
    ThermalPressure string           `json:"thermal_pressure,omitempty"`
    Assertions      []PowerAssertion `json:"assertions,omitempty"`
    EnergyTopUsers  []EnergyUser     `json:"energy_top_users,omitempty"`
}
```

The JSON fields are:

| Field | Meaning |
|---|---|
| `on_battery` | Whether macOS reports the current power source as battery |
| `battery_pct` | Current battery percentage when reported by `pmset` |
| `thermal_pressure` | macOS thermal state such as `nominal`, `fair`, `serious`, or `critical` |
| `assertions` | Active sleep/display assertions attributed to owning PIDs |
| `energy_top_users` | A short `top` sample sorted by the macOS power column |

## Collectors

The unprivileged collector uses only built-in macOS tools:

| Source | Output | Privilege | Used for |
|---|---|---|---|
| `pmset -g batt` | Current AC/battery source and battery percentage | user | `on_battery`, `battery_pct` |
| `pmset -g therm` | Current thermal pressure state | user | `thermal_pressure` |
| `pmset -g assertions` | Wake, sleep, and display assertions by process | user | `assertions` |
| `top -l 1 -n 10 -o power -stats pid,power,command` | One-shot process energy-impact list | user | `energy_top_users` |

Failures are intentionally partial. If one command is unavailable or returns
an unexpected format, the collector still returns whatever the other sources
produced.

The privileged helper can expose deeper `powermetrics` samples through
`helper.powermetrics.sample`, but that is a separate helper RPC because it
requires elevated privileges and costs more than the default `power.state`
snapshot.

## Output

Human output is optimized for a quick terminal read:

```text
=== Power state ===
source:    battery (85%)
thermal:   nominal

assertions (1):
  pid 412      PreventUserIdleSleep          "playing audio"

energy top users:
  PID      IMPACT    COMMAND
  ----------------------------------------
  99647    12.5      Slack
```

JSON output preserves the same fields for daemon, snapshot, and remote
fan-out consumers.

## Related docs

- [live-data-sources.md](live-data-sources.md) lists the cost and privilege
  profile of the underlying commands.
- [../design/system-inventory.md](../design/system-inventory.md) shows where
  power state fits in a full host snapshot.
- [../operations/remote.md](../operations/remote.md) covers remote fan-out
  naming and examples.
