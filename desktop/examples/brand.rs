//! Writes Spectra's brand assets (`spectra_desktop::mark::assets::all`) under a directory, by
//! default the repository's `assets/brand/`: `scripts/brand.sh`, or
//! `cargo run --release --example brand [-- <dir>]`.

use std::path::PathBuf;

fn main() -> Result<(), String> {
    let dir = std::env::args_os().nth(1).map_or_else(
        || PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("assets/brand"),
        PathBuf::from,
    );
    for asset in spectra_desktop::mark::assets::all() {
        let path = dir.join(&asset.path);
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent).map_err(|e| format!("{}: {e}", parent.display()))?;
        }
        std::fs::write(&path, &asset.bytes).map_err(|e| format!("{}: {e}", path.display()))?;
        println!("{:>9} {}", asset.bytes.len(), path.display());
    }
    Ok(())
}
