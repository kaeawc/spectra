//! Sample machine-wide readings for the System page's headless scenes and tests: a healthy Mac,
//! one under critical memory pressure and throttled, one whose readings are failing, and a Linux
//! machine where Spectra reads no memory pressure and there's no battery.

use crate::app::App;
use crate::groups::Nav;
use crate::sample::sample_scene;
use crate::system::{
    Battery, Fail, Load, Memory, Network, Port, Power, Pressure, Reading, Thermal, ThermalState,
    Volume,
};

const GB: f64 = 1024.0 * 1024.0 * 1024.0;
const MB: f64 = 1024.0 * 1024.0;
/// Fast readings landed per sample: four minutes at one every 5 s.
pub const READINGS: usize = 48;

/// A wobble in 0..1 that repeats, so charts have a shape without randomness.
fn wave(i: usize) -> f64 {
    const W: [f64; 12] = [
        0.2, 0.35, 0.3, 0.55, 0.5, 0.7, 0.45, 0.6, 0.4, 0.25, 0.5, 0.65,
    ];
    W[i % W.len()]
}

fn memory(i: usize, pressed: bool) -> Memory {
    let (pressure, free, swap, compressed) = if pressed {
        let free = (18.0 - i as f64 * 0.3).max(4.0);
        (
            Pressure::Critical,
            free,
            GB + i as f64 * 24.0 * MB,
            5.2 * GB + i as f64 * 40.0 * MB,
        )
    } else {
        (
            Pressure::Normal,
            38.0 + wave(i) * 8.0,
            0.0,
            1.3 * GB + wave(i) * 0.3 * GB,
        )
    };
    let step = if pressed { 9000.0 } else { 600.0 };
    Memory {
        physical: 32.0 * GB,
        wired: 3.4 * GB,
        free: 1.1 * GB,
        file_backed: if pressed { 2.1 * GB } else { 9.8 * GB },
        app: if pressed { 21.5 * GB } else { 14.2 * GB },
        purgeable: 0.4 * GB,
        compressed,
        compressed_from: compressed * 2.4,
        ratio: 2.4,
        swap_total: if pressed { 3.0 * GB } else { 0.0 },
        swap_used: swap,
        pressure,
        free_pct: Some(free),
        compressions: 410_000.0 + (0..i).map(|j| step * (0.5 + wave(j))).sum::<f64>(),
        decompressions: 200_000.0,
        swap_ins: if pressed {
            1200.0 + i as f64 * 140.0
        } else {
            0.0
        },
        swap_outs: if pressed {
            3400.0 + i as f64 * 310.0
        } else {
            0.0
        },
    }
}

fn volume(mount: &str, fs_type: &str, used: f64, avail: f64) -> Volume {
    Volume {
        mount: mount.into(),
        device: String::new(),
        fs_type: fs_type.into(),
        read_only: false,
        total: used + avail,
        used,
        avail,
    }
}

fn mac_volumes(data_used: f64) -> Vec<Volume> {
    let tb = 1024.0 * GB;
    vec![
        Volume {
            read_only: true,
            ..volume("/", "apfs", 11.0 * GB, tb - data_used)
        },
        volume(
            "/System/Volumes/Data",
            "apfs",
            data_used,
            tb - data_used - 11.0 * GB,
        ),
        Volume {
            read_only: true,
            ..volume(
                "/Library/Developer/CoreSimulator/Volumes/iOS_23F77",
                "apfs",
                21.9 * GB,
                0.5 * GB,
            )
        },
        volume("/Volumes/Backup", "apfs", 1.1 * tb, 2.5 * tb),
    ]
}

fn port(port: u32, addrs: &[&str], pid: u32, command: &str) -> Port {
    Port {
        port,
        proto: "tcp".into(),
        addrs: addrs.iter().map(|a| (*a).to_owned()).collect(),
        pid: Some(pid),
        command: command.into(),
        user: "user".into(),
    }
}

fn network(iface: &str) -> Network {
    Network {
        iface: iface.into(),
        gateway: "192.168.1.1".into(),
        dns: vec!["192.168.1.1".into(), "1.1.1.1".into()],
        vpn: false,
        vpn_ifaces: Vec::new(),
        established: 87,
        // Pids from the sample's process list, and two of a system service the feed doesn't list.
        ports: vec![
            port(3000, &["127.0.0.1"], 1320, "node"),
            port(5000, &["*"], 683, "ControlCe"),
            port(5173, &["127.0.0.1", "[::1]"], 1321, "bun"),
            port(7000, &["*"], 683, "ControlCe"),
            port(8000, &["127.0.0.1"], 1340, "python3"),
            port(8080, &["*"], 4101, "java"),
            port(11434, &["127.0.0.1"], 1370, "ollama"),
        ],
        proxy: Vec::new(),
    }
}

/// Lands `READINGS` fast readings every 5 s ending `end` seconds from now, and the slow ones
/// along the way; `pressed` makes it a machine in trouble.
fn land_mac(s: &mut App, pressed: bool, end: f64) {
    let cpu = s.stats.cpu;
    for i in 0..READINGS {
        s.now = end - (READINGS - 1 - i) as f64 * 5.0;
        // The last reading matches the CPU the page shows now.
        s.stats.cpu = if i + 1 == READINGS {
            cpu
        } else {
            cpu * (0.7 + wave(i) * 0.6)
        };
        s.land_reading(Reading::Memory(Ok(memory(i, pressed))));
        s.land_reading(Reading::Load(Ok(Load {
            load1: if pressed { 14.0 } else { 3.2 } + wave(i) * 3.0,
            cpus: 10,
        })));
        let thermal = if pressed {
            Thermal {
                state: ThermalState::Throttled,
                evidence: "CPU speed limit is 62%".into(),
                speed_limit: Some(62.0),
            }
        } else {
            Thermal {
                state: ThermalState::Nominal,
                evidence: "no CPU speed limit or thermal warning recorded".into(),
                speed_limit: None,
            }
        };
        s.land_reading(Reading::Thermal(Ok(thermal)));
        if i % 6 == 0 {
            let battery = Battery {
                pct: Some(if pressed { 18.0 } else { 82.0 }),
                on_battery: pressed,
            };
            s.land_reading(Reading::Power(Ok(Power {
                battery: Some(battery),
                ..Power::default()
            })));
            let used = if pressed { 932.0 * GB } else { 612.0 * GB };
            s.land_reading(Reading::Storage(Ok(mac_volumes(used))));
            s.land_reading(Reading::Network(Ok(network("en0"))));
        }
    }
    s.stats.cpu = cpu;
    s.now = 0.0;
}

fn system_page(s: &mut App) {
    s.show(Nav::System);
    s.expanded = None;
}

/// A healthy Mac's System page.
pub fn system_scene(s: &mut App) {
    sample_scene(s);
    land_mac(s, false, 0.0);
    system_page(s);
}

/// Critical memory pressure with swap growing, a throttled CPU and a disk 92% full.
pub fn system_pressure_scene(s: &mut App) {
    sample_scene(s);
    land_mac(s, true, 0.0);
    system_page(s);
}

/// Memory readings failing since 40 s ago, disks never read, the network failing, and an older
/// Spectra without `thermal`.
pub fn system_failed_scene(s: &mut App) {
    sample_scene(s);
    land_mac(s, false, -40.0);
    s.system.storage = crate::system::System::new().storage;
    s.system.network = crate::system::System::new().network;
    s.system.thermal = crate::system::System::new().thermal;
    s.land_reading(Reading::Memory(Err(Fail::Failed(
        "spectra memory failed: host_statistics64 returned 5".into(),
    ))));
    s.land_reading(Reading::Storage(Err(Fail::Failed(
        "spectra took longer than 60 s and was stopped".into(),
    ))));
    s.land_reading(Reading::Network(Err(Fail::Failed(
        "spectra network failed: netstat: exit status 1".into(),
    ))));
    s.land_reading(Reading::Thermal(Err(Fail::Unsupported(
        "This Spectra has no `spectra thermal`.".into(),
    ))));
    system_page(s);
}

/// A Linux machine: no memory pressure reading, no battery, ext4 disks, a thermal verdict.
pub fn system_linux_scene(s: &mut App) {
    sample_scene(s);
    let cpu = s.stats.cpu;
    for i in 0..READINGS {
        s.now = -((READINGS - 1 - i) as f64) * 5.0;
        // The last reading matches the CPU the page shows now.
        s.stats.cpu = if i + 1 == READINGS {
            cpu
        } else {
            cpu * (0.7 + wave(i) * 0.6)
        };
        s.land_reading(Reading::Memory(Err(Fail::Unsupported(
            "Spectra can't read memory on this system.".into(),
        ))));
        s.land_reading(Reading::Load(Ok(Load {
            load1: 2.1 + wave(i) * 2.0,
            cpus: 10,
        })));
        s.land_reading(Reading::Thermal(Ok(Thermal {
            state: ThermalState::Nominal,
            evidence: "no thermal zone at a trip point".into(),
            speed_limit: None,
        })));
        if i % 6 == 0 {
            s.land_reading(Reading::Power(Ok(Power::default())));
            s.land_reading(Reading::Storage(Ok(vec![
                volume("/", "ext4", 88.0 * GB, 140.0 * GB),
                volume("/home", "ext4", 410.0 * GB, 520.0 * GB),
                volume("/dev/shm", "tmpfs", 0.1 * GB, 15.0 * GB),
                volume("/run/user/1000", "tmpfs", 0.0, 3.1 * GB),
            ])));
            s.land_reading(Reading::Network(Ok(network("eth0"))));
        }
    }
    s.stats.cpu = cpu;
    s.now = 0.0;
    system_page(s);
}
