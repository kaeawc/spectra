//! Every file in `assets/brand/`, drawn from [`super::art`]: SVGs, PNGs, the macOS app icon
//! (`.icns`), the menu bar template, the favicon and the Linux icon theme set.
//! `examples/brand.rs` writes them; a test checks the committed SVGs still match the code.

use super::art::{self, Frame};
use super::png;
use super::raster::rasterize;
use super::scene::Scene;

/// The app's reverse-DNS id: the Linux `.desktop` file, its icons and the macOS bundle.
pub const APP_ID: &str = "io.github.kaeawc.SpectraDesktop";

/// A file to write under `assets/brand/`.
#[derive(Clone, Debug, PartialEq)]
pub struct Asset {
    /// Relative to `assets/brand/`.
    pub path: String,
    pub bytes: Vec<u8>,
}

fn asset(path: impl Into<String>, bytes: Vec<u8>) -> Asset {
    Asset {
        path: path.into(),
        bytes,
    }
}

/// The square app icon (the tile) at `size` px: the full artwork from 180 px up, the bars from
/// 48, then the favicons' simplifications.
pub fn icon_scene(size: u32) -> Scene {
    match size {
        180.. => art::icon("spectra-icon", Frame::Tile),
        48.. => art::small_icon("spectra-icon-small", Frame::Tile),
        24.. => art::favicon32("spectra-favicon-32"),
        _ => art::favicon16("spectra-favicon-16"),
    }
}

/// The Dock icon at `size` px: the full artwork from 256 px, the bars from 32, the 16 px pixels.
pub fn dock_scene(size: u32) -> Scene {
    match size {
        256.. => art::icon("spectra-icon-dock", Frame::Dock),
        32.. => art::small_icon("spectra-icon-small-dock", Frame::Dock),
        _ => art::favicon16("spectra-favicon-16"),
    }
}

/// `scene` as a PNG `w`×`h` pixels.
pub fn png_of(scene: &Scene, w: u32, h: u32) -> Vec<u8> {
    png::encode(w, h, &rasterize(scene, w, h))
}

fn square(scene: &Scene, size: u32) -> Vec<u8> {
    png_of(scene, size, size)
}

/// Sizes of `png/spectra-icon-*.png`.
pub const ICON_SIZES: [u32; 8] = [16, 32, 48, 64, 128, 256, 512, 1024];
/// Sizes in the Linux hicolor icon theme.
pub const HICOLOR_SIZES: [u32; 8] = [16, 24, 32, 48, 64, 128, 256, 512];

/// The SVGs in `assets/brand/`.
pub fn svgs() -> Vec<Asset> {
    let svg = |s: Scene| s.svg().into_bytes();
    vec![
        asset(
            "spectra-icon.svg",
            svg(art::icon("spectra-icon", Frame::Tile)),
        ),
        asset(
            "spectra-icon-dock.svg",
            svg(art::icon("spectra-icon-dock", Frame::Dock)),
        ),
        asset(
            "spectra-icon-maskable.svg",
            svg(art::icon("spectra-icon-maskable", Frame::Maskable)),
        ),
        asset(
            "spectra-icon-small.svg",
            svg(art::small_icon("spectra-icon-small", Frame::Tile)),
        ),
        asset(
            "spectra-icon-small-dock.svg",
            svg(art::small_icon("spectra-icon-small-dock", Frame::Dock)),
        ),
        asset(
            "spectra-favicon-32.svg",
            svg(art::favicon32("spectra-favicon-32")),
        ),
        asset(
            "spectra-favicon-16.svg",
            svg(art::favicon16("spectra-favicon-16")),
        ),
        asset(
            "spectra-wordmark.svg",
            svg(art::wordmark("spectra-wordmark", true)),
        ),
        asset(
            "spectra-wordmark-bare.svg",
            svg(art::wordmark("spectra-wordmark-bare", false)),
        ),
        asset("macos/spectraTemplate.svg", svg(art::tray("spectra-tray"))),
        asset(
            format!("linux/hicolor/scalable/apps/{APP_ID}.svg"),
            svg(art::icon("spectra-icon", Frame::Tile)),
        ),
    ]
}

/// The rasters in `assets/brand/`.
pub fn rasters() -> Vec<Asset> {
    let mut out = Vec::new();
    for size in ICON_SIZES {
        out.push(asset(
            format!("png/spectra-icon-{size}.png"),
            square(&icon_scene(size), size),
        ));
    }
    for size in HICOLOR_SIZES {
        out.push(asset(
            format!("linux/hicolor/{size}x{size}/apps/{APP_ID}.png"),
            square(&icon_scene(size), size),
        ));
    }
    let dock: Vec<(u32, Vec<u8>)> = [16, 32, 64, 128, 256, 512, 1024]
        .into_iter()
        .map(|s| (s, rasterize(&dock_scene(s), s, s)))
        .collect();
    out.push(asset("macos/spectra.icns", icns(&dock)));
    let tray = art::tray("spectra-tray");
    out.push(asset("macos/spectraTemplate.png", square(&tray, 18)));
    out.push(asset("macos/spectraTemplate@2x.png", square(&tray, 36)));
    let favicons: Vec<(u32, Vec<u8>)> = [16, 32, 48]
        .into_iter()
        .map(|s| (s, square(&icon_scene(s), s)))
        .collect();
    out.push(asset("favicon.ico", ico(&favicons)));
    out.push(asset("web/favicon-16.png", square(&icon_scene(16), 16)));
    out.push(asset("web/favicon-32.png", square(&icon_scene(32), 32)));
    out.push(asset(
        "web/apple-touch-icon.png",
        square(&art::icon("spectra-touch", Frame::Square), 180),
    ));
    for size in [192, 512] {
        out.push(asset(
            format!("web/icon-{size}.png"),
            square(&icon_scene(size), size),
        ));
        out.push(asset(
            format!("web/icon-maskable-{size}.png"),
            square(&art::icon("spectra-icon-maskable", Frame::Maskable), size),
        ));
    }
    out.push(asset(
        "web/spectra-wordmark.png",
        png_of(&art::wordmark("spectra-wordmark", true), 1200, 400),
    ));
    out.push(asset(
        "web/social-preview.png",
        png_of(&art::social("spectra-social"), 1280, 640),
    ));
    out
}

/// Every file in `assets/brand/`.
pub fn all() -> Vec<Asset> {
    let mut out = svgs();
    out.extend(rasters());
    out
}

/// An Apple icon family from square RGBA images, each under the OSType that names its pixel
/// size and scale: 16 and 32 px as `ic04`/`ic05` ARGB (as `iconutil` writes them; macOS misreads
/// a PNG in the older `icp4`/`icp5`), the rest as PNG.
pub fn icns(images: &[(u32, Vec<u8>)]) -> Vec<u8> {
    // Retina entries repeat a size under a second type: ic11 is 16@2x, ic12 32@2x, ic13 128@2x,
    // ic14 256@2x and ic10 512@2x.
    let types: &[(u32, &[u8; 4])] = &[
        (16, b"ic04"),
        (32, b"ic05"),
        (32, b"ic11"),
        (64, b"ic12"),
        (128, b"ic07"),
        (256, b"ic08"),
        (256, b"ic13"),
        (512, b"ic09"),
        (512, b"ic14"),
        (1024, b"ic10"),
    ];
    let mut body = Vec::new();
    for (size, ty) in types {
        if let Some((_, rgba)) = images.iter().find(|(s, _)| s == size) {
            let data = if matches!(*ty, b"ic04" | b"ic05") {
                argb(rgba)
            } else {
                png::encode(*size, *size, rgba)
            };
            body.extend_from_slice(*ty);
            body.extend_from_slice(&(data.len() as u32 + 8).to_be_bytes());
            body.extend_from_slice(&data);
        }
    }
    let mut out = b"icns".to_vec();
    out.extend_from_slice(&(body.len() as u32 + 8).to_be_bytes());
    out.extend_from_slice(&body);
    out
}

/// An `ic04`/`ic05` payload: `ARGB`, then the alpha, red, green and blue planes, each
/// run-length coded (a byte under 0x80 is that many plus one literal bytes; 0x80 and up repeats
/// the next byte that many less 0x80 plus three times).
fn argb(rgba: &[u8]) -> Vec<u8> {
    let mut out = b"ARGB".to_vec();
    for channel in [3, 0, 1, 2] {
        let plane: Vec<u8> = rgba.as_chunks::<4>().0.iter().map(|p| p[channel]).collect();
        let mut i = 0;
        while i < plane.len() {
            let run = plane[i..]
                .iter()
                .take(130)
                .take_while(|&&b| b == plane[i])
                .count();
            if run >= 3 {
                out.extend_from_slice(&[0x80 + (run - 3) as u8, plane[i]]);
                i += run;
                continue;
            }
            let start = i;
            while i < plane.len() && i - start < 128 {
                if i + 2 < plane.len() && plane[i] == plane[i + 1] && plane[i] == plane[i + 2] {
                    break;
                }
                i += 1;
            }
            out.push((i - start - 1) as u8);
            out.extend_from_slice(&plane[start..i]);
        }
    }
    out
}

/// A Windows icon file holding PNGs (Vista and later, and every browser).
pub fn ico(pngs: &[(u32, Vec<u8>)]) -> Vec<u8> {
    let mut out = vec![0, 0, 1, 0];
    out.extend_from_slice(&(pngs.len() as u16).to_le_bytes());
    let mut offset = 6 + 16 * pngs.len() as u32;
    for (size, png) in pngs {
        let side = if *size >= 256 { 0 } else { *size as u8 };
        out.extend_from_slice(&[side, side, 0, 0]);
        out.extend_from_slice(&1u16.to_le_bytes());
        out.extend_from_slice(&32u16.to_le_bytes());
        out.extend_from_slice(&(png.len() as u32).to_le_bytes());
        out.extend_from_slice(&offset.to_le_bytes());
        offset += png.len() as u32;
    }
    for (_, png) in pngs {
        out.extend_from_slice(png);
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::{Path, PathBuf};

    fn brand_dir() -> PathBuf {
        Path::new(env!("CARGO_MANIFEST_DIR")).join("assets/brand")
    }

    /// Text with every number replaced by a placeholder, and the numbers: compares SVGs drawn by
    /// different hosts' maths libraries, whose last digits may differ.
    fn split_numbers(s: &str) -> (String, Vec<f64>) {
        let (mut text, mut nums, mut cur) = (String::new(), Vec::new(), String::new());
        for ch in s.chars().chain(std::iter::once(' ')) {
            if ch.is_ascii_digit() || ch == '.' || (ch == '-' && cur.is_empty()) {
                cur.push(ch);
                continue;
            }
            if let Ok(v) = cur.parse::<f64>() {
                nums.push(v);
                text.push('#');
            } else {
                text += &cur;
            }
            cur.clear();
            text.push(ch);
        }
        (text, nums)
    }

    /// `scripts/brand.sh` regenerates them; this fails when the code drawing the mark changed
    /// and the committed assets didn't.
    #[test]
    fn committed_svgs_match_the_code() {
        for a in svgs() {
            let path = brand_dir().join(&a.path);
            let committed = std::fs::read_to_string(&path)
                .unwrap_or_else(|e| panic!("{}: {e} (run scripts/brand.sh)", path.display()));
            let (t1, n1) = split_numbers(&committed);
            let (t2, n2) = split_numbers(std::str::from_utf8(&a.bytes).expect("utf-8"));
            assert_eq!(t1, t2, "{} is stale: run scripts/brand.sh", a.path);
            assert_eq!(n1.len(), n2.len(), "{}", a.path);
            assert!(
                n1.iter().zip(&n2).all(|(a, b)| (a - b).abs() <= 0.0015),
                "{} is stale: run scripts/brand.sh",
                a.path
            );
        }
    }

    /// Drawing every raster would take a while in a debug build, so this checks each one is
    /// committed at its size; the SVGs above pin the geometry they are drawn from.
    #[test]
    fn every_raster_is_committed_at_its_size() {
        let dir = brand_dir();
        let size_of = |p: &str| {
            png::size(
                &std::fs::read(dir.join(p))
                    .unwrap_or_else(|e| panic!("{p}: {e} (run scripts/brand.sh)")),
            )
        };
        for s in ICON_SIZES {
            assert_eq!(size_of(&format!("png/spectra-icon-{s}.png")), Some((s, s)));
        }
        for s in HICOLOR_SIZES {
            let p = format!("linux/hicolor/{s}x{s}/apps/{APP_ID}.png");
            assert_eq!(size_of(&p), Some((s, s)), "{p}");
        }
        assert_eq!(size_of("macos/spectraTemplate.png"), Some((18, 18)));
        assert_eq!(size_of("macos/spectraTemplate@2x.png"), Some((36, 36)));
        assert_eq!(size_of("web/apple-touch-icon.png"), Some((180, 180)));
        assert_eq!(size_of("web/social-preview.png"), Some((1280, 640)));
        assert_eq!(size_of("web/spectra-wordmark.png"), Some((1200, 400)));
        for s in [192, 512] {
            assert_eq!(size_of(&format!("web/icon-{s}.png")), Some((s, s)));
            assert_eq!(size_of(&format!("web/icon-maskable-{s}.png")), Some((s, s)));
        }
        let icns = std::fs::read(dir.join("macos/spectra.icns")).expect("spectra.icns");
        assert_eq!(&icns[..4], b"icns");
        let ico = std::fs::read(dir.join("favicon.ico")).expect("favicon.ico");
        assert_eq!(&ico[..6], &[0, 0, 1, 0, 3, 0]);
    }

    #[test]
    fn the_icns_holds_every_slot() {
        let images: Vec<_> = [16, 32, 64, 128, 256, 512, 1024]
            .map(|s| (s, vec![7u8; (s * s * 4) as usize]))
            .into();
        let icns = icns(&images);
        assert_eq!(&icns[..4], b"icns");
        assert_eq!(
            u32::from_be_bytes(icns[4..8].try_into().expect("length")) as usize,
            icns.len()
        );
        for ty in [
            b"ic04", b"ic05", b"ic11", b"ic12", b"ic07", b"ic08", b"ic13", b"ic09", b"ic14",
            b"ic10",
        ] {
            assert!(
                icns.windows(4).any(|w| w == ty),
                "{}",
                String::from_utf8_lossy(ty)
            );
        }
    }

    #[test]
    fn the_ico_points_at_each_png() {
        let pngs = vec![(16, vec![1u8; 5]), (32, vec![2u8; 7])];
        let ico = ico(&pngs);
        assert_eq!(&ico[..6], &[0, 0, 1, 0, 2, 0]);
        let e = &ico[22..38];
        let len = u32::from_le_bytes(e[8..12].try_into().expect("size")) as usize;
        let at = u32::from_le_bytes(e[12..16].try_into().expect("offset")) as usize;
        assert_eq!((e[0], len), (32, 7));
        assert_eq!(&ico[at..at + 7], &[2u8; 7]);
    }

    #[test]
    fn argb_planes_run_length_decode_to_the_image() {
        let rgba: Vec<u8> = (0..16 * 16)
            .flat_map(|i: u32| {
                [
                    (i % 7) as u8,
                    9,
                    (i / 40) as u8,
                    if i < 200 { 255 } else { 0 },
                ]
            })
            .collect();
        let data = argb(&rgba);
        assert_eq!(&data[..4], b"ARGB");
        let mut planes = Vec::new();
        let mut i = 4;
        while i < data.len() {
            let n = data[i] as usize;
            if n < 0x80 {
                planes.extend_from_slice(&data[i + 1..i + 2 + n]);
                i += 2 + n;
            } else {
                planes.extend(std::iter::repeat_n(data[i + 1], n - 0x80 + 3));
                i += 2;
            }
        }
        let px = 16 * 16;
        assert_eq!(planes.len(), 4 * px);
        for (k, p) in rgba.as_chunks::<4>().0.iter().enumerate() {
            assert_eq!(
                [
                    planes[px + k],
                    planes[2 * px + k],
                    planes[3 * px + k],
                    planes[k]
                ],
                *p
            );
        }
    }

    /// The template is black: macOS takes only its alpha.
    #[test]
    fn the_menu_bar_template_is_black_and_alpha() {
        let px = rasterize(&art::tray("t"), 18, 18);
        let chunks = px.as_chunks::<4>().0;
        assert!(chunks.iter().all(|p| p[3] == 0 || p[..3] == [0, 0, 0]));
        assert!(chunks.iter().any(|p| p[3] == 255));
    }

    #[test]
    fn the_tile_is_opaque_inside_and_clear_at_its_corners() {
        let px = rasterize(&icon_scene(64), 64, 64);
        let alpha = |x: usize, y: usize| px[(y * 64 + x) * 4 + 3];
        assert_eq!(alpha(0, 0), 0);
        assert_eq!(alpha(32, 32), 255);
    }
}
