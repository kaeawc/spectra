//! Spectra's mark: a glass prism shaped like the wordmark's A takes in the ASCII bits of
//! "spectra" and splits them into a spectrum of hex colours.
//!
//! One description of the mark ([`art`]: the prism, its crossbar, the beam, the byte column, the
//! bands and labels, colours and framings) is drawn by one renderer: [`scene::Scene::svg`]
//! writes SVG and [`raster::rasterize`] draws the same geometry into pixels, with labels set in
//! a built-in glyph set ([`glyphs`]) rather than a font. `examples/brand.rs` writes every file in
//! `assets/brand/` ([`assets::all`]). To change the mark, edit [`art`] and run
//! `scripts/brand.sh`; a test fails while the committed SVGs are stale. `docs/brand.md` lists the
//! files and the sizes each drawing is for.

pub mod art;
pub mod assets;
pub mod glyphs;
pub mod png;
pub mod raster;
pub mod scene;
