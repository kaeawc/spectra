//! The seams every Spectra collector is built on. Collectors take these as arguments instead of
//! calling the clock, the environment, subprocesses or the filesystem directly, so tests can fake
//! them (AGENTS.md, "Time, randomness, env, subprocess, filesystem").

pub mod clock;
pub mod env;
pub mod fs;
pub mod fsutil;
pub mod gojson;
pub mod hostos;
pub mod idgen;
pub mod logger;
pub mod plist;
pub mod proc;
