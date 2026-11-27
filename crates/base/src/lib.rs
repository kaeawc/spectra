//! The seams every Spectra collector is built on. Collectors take these as arguments instead of
//! calling the clock, the environment, subprocesses or the filesystem directly, so tests can fake
//! them (AGENTS.md, "Time, randomness, env, subprocess, filesystem").

pub mod gojson;
pub mod hostos;
