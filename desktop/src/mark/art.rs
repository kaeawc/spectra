//! The mark as data. A glass prism shaped like the wordmark's A, with a frosted (sandblasted)
//! crossbar band, takes in the ASCII bits of "spectra" as white light and splits it into the
//! six hex colours of the spectrum. Every framing (app tile, Dock, maskable, favicons, menu bar,
//! wordmark) is built here from [`PRISM`] and a layout; nothing is drawn by hand elsewhere.
//!
//! Coordinates are the approved masters' "prism space": the 1200×400 wordmark the A comes from.
//! Framings place the artwork with a uniform scale and a translation.

use super::glyphs::{self, Line};
use super::scene::{
    Cmd, Item, Paint, Path, Place, Pt, Scene, circle, group, linear, place, polygon, polyline,
    rect, rgb, round_rect, solid, stop,
};

/// The ground every framing sits on.
pub const GROUND: u32 = 0x0d1117;
/// The tile's rim.
pub const RIM: u32 = 0x30363d;
/// Light text and the beam.
pub const INK: u32 = 0xe6edf3;
/// The dim end of the bits' gradient.
pub const DIM: u32 = 0x6e7681;
/// The prism's edges at small sizes.
pub const EDGE: u32 = 0xf0f6fc;
/// The spectrum out of the prism, top to bottom.
pub const SPECTRUM: [u32; 6] = [0xff453a, 0xff9f0a, 0xffd60a, 0x30d158, 0x0a84ff, 0xbf5af2];
/// The word whose ASCII bits go in.
pub const WORD: &str = "spectra";

const WHITE: u32 = 0xffffff;
const BLACK: u32 = 0x000000;

/// The bytes of [`WORD`] as eight binary digits each.
pub fn bytes() -> Vec<String> {
    WORD.bytes().map(|b| format!("{b:08b}")).collect()
}

/// `#RRGGBB` for a colour, as the hex labels print it.
pub fn label(hex: u32) -> String {
    format!("#{hex:06X}")
}

/// The extruded A: its front face is a triangle from the apex down to the baseline, pushed back
/// by `depth`, with a frosted band where the letter's crossbar would be.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Prism {
    /// The apex's y and the baseline's.
    pub top: f32,
    pub base: f32,
    /// The front face's baseline corners' x.
    pub left: f32,
    pub right: f32,
    /// The extrusion, up and to the right.
    pub depth: Pt,
    /// The crossbar band's top and bottom.
    pub crossbar: [f32; 2],
}

/// Matched to the wordmark's stroked capitals: the cap extent of the letters is 147.2–284.8.
pub const PRISM: Prism = Prism {
    top: 147.6,
    base: 284.4,
    left: 852.0,
    right: 962.0,
    depth: [17.0, -10.0],
    crossbar: [236.0, 251.0],
};

impl Prism {
    pub fn apex_x(&self) -> f32 {
        (self.left + self.right) / 2.0
    }

    pub fn a(&self) -> Pt {
        [self.apex_x(), self.top]
    }

    pub fn l(&self) -> Pt {
        [self.left, self.base]
    }

    pub fn r(&self) -> Pt {
        [self.right, self.base]
    }

    pub fn back(&self, p: Pt) -> Pt {
        [p[0] + self.depth[0], p[1] + self.depth[1]]
    }

    /// The front face's left edge's x at height `y`.
    pub fn xl(&self, y: f32) -> f32 {
        self.left + (self.base - y) / (self.base - self.top) * (self.apex_x() - self.left)
    }

    /// The front face's right edge's x at height `y`.
    pub fn xr(&self, y: f32) -> f32 {
        self.right - (self.base - y) / (self.base - self.top) * (self.right - self.apex_x())
    }

    pub fn face(&self) -> Path {
        polygon(&[self.a(), self.l(), self.r()])
    }

    /// The outline of the prism with its depth: front face and the faces up and to the right.
    pub fn silhouette(&self) -> [Pt; 5] {
        let (a, l, r) = (self.a(), self.l(), self.r());
        [a, self.back(a), self.back(r), r, l]
    }
}

fn white(a0: f32, a1: f32) -> [super::scene::Stop; 2] {
    [stop(0.0, WHITE, a0), stop(1.0, WHITE, a1)]
}

/// The glass: back face, three side faces and the front face's sheen. `edge` scales the lines.
pub fn glass(p: &Prism, edge: f32) -> Vec<Item> {
    let (a, l, r) = (p.a(), p.l(), p.r());
    let (ab, lb, rb) = (p.back(a), p.back(l), p.back(r));
    let face = |pts: &[Pt], fill: Paint, line: f32, width: f32| {
        [
            Item::fill(polygon(pts), fill),
            Item::stroke(polygon(pts), width * edge, solid(WHITE, line)).round_join(),
        ]
    };
    let mut out = Vec::new();
    out.extend(face(&[ab, lb, rb], solid(WHITE, 0.04), 0.16, 1.0));
    out.extend(face(
        &[a, l, lb, ab],
        linear(a, l, &white(0.08, 0.03)),
        0.14,
        1.2,
    ));
    out.extend(face(
        &[l, r, rb, lb],
        linear([l[0], 0.0], [rb[0], 0.0], &white(0.19, 0.06)),
        0.38,
        1.2,
    ));
    out.extend(face(
        &[r, a, ab, rb],
        linear(r, ab, &white(0.34, 0.11)),
        0.38,
        1.2,
    ));
    out.push(Item::fill(
        p.face(),
        linear([0.0, p.top], [0.0, p.base], &white(0.22, 0.05)),
    ));
    out
}

/// The sandblasted crossbar: a frosted band inside the front face, lit along its top edge and
/// shadowed along its bottom, with a glint where it meets the right edge.
pub fn crossbar(p: &Prism, edge: f32) -> Vec<Item> {
    let [y0, y1] = p.crossbar;
    let (x0, x1) = (p.left, p.right);
    let sand = linear(
        [x0, 0.0],
        [x1, 0.0],
        &[
            stop(0.0, WHITE, 0.10),
            stop(0.55, WHITE, 0.17),
            stop(0.85, WHITE, 0.26),
            stop(1.0, WHITE, 0.12),
        ],
    );
    let g = [p.xr(y0) - 7.0, y0 + 0.5];
    vec![
        Item::fill(rect(x0, y0, x1 - x0, y1 - y0), sand).clip(p.face()),
        Item::stroke(
            polyline(&[[x0, y0], [x1, y0]]),
            0.7 * edge,
            solid(WHITE, 0.30),
        )
        .clip(p.face()),
        Item::stroke(
            polyline(&[[x0, y1], [x1, y1]]),
            0.7 * edge,
            solid(BLACK, 0.35),
        )
        .clip(p.face()),
        Item::fill(
            circle(g, 9.0),
            Paint::Radial {
                centre: g,
                r: 9.0,
                stops: vec![
                    stop(0.0, WHITE, 0.85),
                    stop(0.4, WHITE, 0.25),
                    stop(1.0, WHITE, 0.0),
                ],
            },
        ),
        Item::stroke(
            vec![
                Cmd::Move([g[0] - 7.0, g[1]]),
                Cmd::Line([g[0] + 7.0, g[1]]),
                Cmd::Move([g[0], g[1] - 5.0]),
                Cmd::Line([g[0], g[1] + 5.0]),
            ],
            0.7 * edge,
            solid(WHITE, 0.7),
        )
        .round_cap(),
    ]
}

/// The front face's bright outline and the three edges running back.
pub fn front_edges(p: &Prism, edge: f32) -> Vec<Item> {
    let mut out = vec![Item::stroke(p.face(), 2.4 * edge, solid(WHITE, 0.9)).round_join()];
    for q in [p.a(), p.l(), p.r()] {
        out.push(
            Item::stroke(polyline(&[q, p.back(q)]), 1.8 * edge, solid(WHITE, 0.6)).round_cap(),
        );
    }
    out
}

/// One spectrum band: colour, top, bottom, middle.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Band {
    pub colour: u32,
    pub y0: f32,
    pub y1: f32,
    pub yc: f32,
}

/// Wedges of colour inside the glass, from the entry point to each band's exit.
pub fn fans(p: &Prism, bands: &[Band], opacity: f32, entry: Pt) -> Vec<Item> {
    bands
        .iter()
        .map(|b| {
            Item::fill(
                polygon(&[entry, [p.xr(b.y0), b.y0], [p.xr(b.y1), b.y1]]),
                solid(b.colour, opacity),
            )
        })
        .collect()
}

fn text(line: Line, s: &str, paint: Paint) -> Item {
    Item::stroke(line.path(s), line.stroke(), paint)
}

/// A colour's `#RRGGBB` label in that colour. The `#` is drawn lighter than the figures, as
/// in the masters' font, or its four strokes close up into a blot.
fn hex_label(line: Line, colour: u32) -> Vec<Item> {
    let hash = Line {
        weight: glyphs::HAIR,
        ..line
    };
    let figures = label(colour).replace('#', " ");
    vec![
        text(hash, "#", solid(colour, 1.0)),
        text(line, &figures, solid(colour, 1.0)),
    ]
}

/// Where an artwork's extent sits: its horizontal span and the y it is centred on.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Extent {
    pub x0: f32,
    pub x1: f32,
    pub ymid: f32,
}

impl Extent {
    /// The placement that makes the artwork `width` wide, centred on `c`.
    pub fn place(&self, c: Pt, width: f32) -> Place {
        let k = width / (self.x1 - self.x0);
        Place {
            k,
            at: [c[0] - k * (self.x0 + self.x1) / 2.0, c[1] - k * self.ymid],
        }
    }
}

/// The full artwork, "Compressed Ledger": a block of the bytes on the left converging into the
/// prism, and a hex column of the spectrum on the right.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Ledger {
    pub cy: f32,
    pub hex_size: f32,
    pub hex_pitch: f32,
    pub band: f32,
    pub bit_size: f32,
    pub gap: f32,
}

pub const LEDGER: Ledger = Ledger {
    cy: 210.0,
    hex_size: 11.0,
    hex_pitch: 15.0,
    band: 11.0,
    bit_size: 9.6,
    gap: 5.0,
};

impl Ledger {
    pub fn bands(&self) -> Vec<Band> {
        SPECTRUM
            .iter()
            .enumerate()
            .map(|(i, &colour)| {
                let yc = self.cy + (i as f32 - 2.5) * self.hex_pitch;
                Band {
                    colour,
                    y0: yc - self.band / 2.0,
                    y1: yc + self.band / 2.0,
                    yc,
                }
            })
            .collect()
    }

    /// Where the hex column starts: clear of the prism's side face.
    pub fn hex_x(&self) -> f32 {
        let p = PRISM;
        self.bands()
            .iter()
            .map(|b| p.xr(b.y1))
            .fold(f32::MIN, f32::max)
            + p.depth[0]
            + self.gap
    }

    pub fn hex_w(&self) -> f32 {
        7.0 * glyphs::ADVANCE * self.hex_size
    }

    /// The bit block's right edge: the hex column mirrored about the apex.
    pub fn bits_r(&self) -> f32 {
        2.0 * PRISM.apex_x() - self.hex_x()
    }

    pub fn bits_w(&self) -> f32 {
        8.0 * glyphs::ADVANCE * self.bit_size
    }

    /// Seven bytes over the height of six bands.
    pub fn bit_pitch(&self) -> f32 {
        6.0 * self.hex_pitch / 7.0
    }

    pub fn entry(&self) -> Pt {
        [PRISM.xl(self.cy - 4.0), self.cy - 4.0]
    }

    pub fn extent(&self) -> Extent {
        Extent {
            x0: self.bits_r() - self.bits_w(),
            x1: self.hex_x() + self.hex_w(),
            ymid: (PRISM.top + PRISM.depth[1] + PRISM.base) / 2.0,
        }
    }

    /// Points bounding the artwork, for fitting it inside a circle.
    pub fn hull(&self) -> Vec<Pt> {
        let e = self.extent();
        let bp = self.bit_pitch();
        let (top, bot) = (self.cy - 3.5 * bp, self.cy + 3.5 * bp);
        let b = self.bands();
        let (b0, b5) = (b[0].y0, b[5].y1);
        let mut pts = vec![
            [e.x0, top],
            [e.x0, bot],
            [self.bits_r(), top],
            [self.bits_r(), bot],
            [self.hex_x(), b0],
            [e.x1 + 3.0, b0],
            [e.x1 + 3.0, b5],
            [self.hex_x(), b5],
        ];
        pts.extend(PRISM.silhouette());
        pts
    }

    pub fn art(&self) -> Vec<Item> {
        let p = PRISM;
        let (bits_r, entry, hex_x, hex_w) =
            (self.bits_r(), self.entry(), self.hex_x(), self.hex_w());
        let bp = self.bit_pitch();
        let ys: Vec<f32> = (0..7).map(|i| self.cy + (i as f32 - 3.0) * bp).collect();
        let (top, bot) = (ys[0] - bp / 2.0, ys[6] + bp / 2.0);
        let mut out = vec![Item::fill(
            polygon(&[[bits_r + 2.0, top], entry, [bits_r + 2.0, bot]]),
            linear([bits_r, 0.0], [entry[0], 0.0], &white(0.10, 0.32)),
        )];
        for &y in &ys {
            out.push(Item::stroke(
                polyline(&[[bits_r + 2.5, y], entry]),
                0.6,
                solid(INK, 0.55),
            ));
        }
        let bits = linear(
            [bits_r - self.bits_w(), 0.0],
            [bits_r, 0.0],
            &[stop(0.0, DIM, 1.0), stop(1.0, INK, 1.0)],
        );
        let mut path = Vec::new();
        for (byte, &y) in bytes().iter().zip(&ys) {
            path.extend(bit_line(bits_r, y, self.bit_size).path(byte));
        }
        let line = bit_line(bits_r, 0.0, self.bit_size);
        out.push(Item::stroke(path, line.stroke(), bits));
        let bands = self.bands();
        out.extend(fans(&p, &bands, 0.38, entry));
        for b in &bands {
            let x = p.xr(b.y0);
            out.push(Item::fill(
                rect(x, b.y0, hex_x + hex_w + 3.0 - x, b.y1 - b.y0),
                linear(
                    [x, 0.0],
                    [hex_x + hex_w, 0.0],
                    &[
                        stop(0.0, b.colour, 0.42),
                        stop(0.6, b.colour, 0.16),
                        stop(1.0, b.colour, 0.04),
                    ],
                ),
            ));
        }
        out.extend(glass(&p, 1.0));
        out.extend(crossbar(&p, 1.0));
        out.extend(front_edges(&p, 1.0));
        for b in &bands {
            let line = Line {
                x: hex_x,
                y: b.yc,
                size: self.hex_size,
                weight: glyphs::HEAVY,
                spacing: 0.0,
                end: false,
            };
            out.extend(hex_label(line, b.colour));
        }
        out
    }
}

fn bit_line(right: f32, y: f32, size: f32) -> Line {
    Line {
        x: right,
        y,
        size,
        weight: glyphs::BOLD,
        spacing: 0.0,
        end: true,
    }
}

/// The artwork for 48–128 px, where text is mush: each byte becomes a row of dashes (its
/// 1-bits, bright, over a dim track) and each hex label a solid block of its colour.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Bars {
    /// Width of the dash rows and of the hex column.
    pub column: f32,
    pub pitch: f32,
    pub bar: f32,
    pub cy: f32,
    pub gap: f32,
    pub bit_h: f32,
    /// The share of the hex column the solid label block takes.
    pub label: f32,
    /// How much heavier the prism's lines are than in the full artwork.
    pub edge: f32,
}

pub const BARS: Bars = Bars {
    column: 42.0,
    pitch: 19.0,
    bar: 13.0,
    cy: 210.0,
    gap: 6.0,
    bit_h: 7.0,
    label: 0.62,
    edge: 2.1,
};

/// The runs of 1s in a byte, as half-open digit ranges.
fn runs(byte: &str) -> Vec<(usize, usize)> {
    let d = byte.as_bytes();
    let mut out = Vec::new();
    let mut i = 0;
    while i < d.len() {
        if d[i] == b'1' {
            let j = (i..d.len()).find(|&j| d[j] != b'1').unwrap_or(d.len());
            out.push((i, j));
            i = j;
        } else {
            i += 1;
        }
    }
    out
}

impl Bars {
    pub fn bands(&self) -> Vec<Band> {
        SPECTRUM
            .iter()
            .enumerate()
            .map(|(i, &colour)| {
                let yc = self.cy + (i as f32 - 2.5) * self.pitch;
                Band {
                    colour,
                    y0: yc - self.bar / 2.0,
                    y1: yc + self.bar / 2.0,
                    yc,
                }
            })
            .collect()
    }

    pub fn hex_x(&self) -> f32 {
        let p = PRISM;
        self.bands()
            .iter()
            .map(|b| p.xr(b.y1))
            .fold(f32::MIN, f32::max)
            + p.depth[0]
            + self.gap
    }

    pub fn bits_r(&self) -> f32 {
        2.0 * PRISM.apex_x() - self.hex_x()
    }

    pub fn entry(&self) -> Pt {
        [PRISM.xl(self.cy - 4.0), self.cy - 4.0]
    }

    pub fn extent(&self) -> Extent {
        Extent {
            x0: self.bits_r() - self.column,
            x1: self.hex_x() + self.column,
            ymid: (PRISM.top + PRISM.depth[1] + PRISM.base) / 2.0,
        }
    }

    pub fn art(&self) -> Vec<Item> {
        let p = PRISM;
        let (e, br, cw, h) = (self.entry(), self.bits_r(), self.column, self.bit_h);
        let ext = self.extent();
        let bands = self.bands();
        let ys: Vec<f32> = bands.iter().map(|b| b.yc).collect();
        let (top, bot) = (ys[0] - h, ys[5] + h);
        let mut out = vec![Item::fill(
            polygon(&[[br + 2.0, top], e, [br + 2.0, bot]]),
            linear([br, 0.0], [e[0], 0.0], &white(0.12, 0.38)),
        )];
        for &y in &ys {
            out.push(Item::stroke(
                polyline(&[[br + 2.0, y], e]),
                1.3,
                solid(INK, 0.5),
            ));
        }
        let bits = linear(
            [ext.x0, 0.0],
            [br, 0.0],
            &[stop(0.0, DIM, 1.0), stop(1.0, INK, 1.0)],
        );
        let cell = cw / 8.0;
        for (byte, &y) in bytes().iter().zip(&ys) {
            out.push(Item::fill(
                round_rect(ext.x0 + 0.6, y - h / 2.0, cw - 1.2, h, 1.2),
                solid(DIM, 0.28),
            ));
            for (a, b) in runs(byte) {
                out.push(Item::fill(
                    round_rect(
                        ext.x0 + a as f32 * cell + 0.6,
                        y - h / 2.0,
                        (b - a) as f32 * cell - 1.2,
                        h,
                        1.2,
                    ),
                    bits.clone(),
                ));
            }
        }
        out.extend(fans(&p, &bands, 0.5, e));
        let lab = ext.x1 - self.label * cw;
        for b in &bands {
            let x = p.xr(b.y0);
            out.push(Item::fill(
                rect(x, b.y0, lab - x, b.y1 - b.y0),
                solid(b.colour, 0.42),
            ));
            out.push(Item::fill(
                round_rect(lab, b.y0, ext.x1 - lab, b.y1 - b.y0, 1.5),
                solid(b.colour, 1.0),
            ));
        }
        out.extend(glass(&p, self.edge));
        let [c0, c1] = p.crossbar;
        out.push(
            Item::fill(
                rect(p.left, c0, p.right - p.left, c1 - c0),
                solid(WHITE, 0.30),
            )
            .clip(p.face()),
        );
        out.extend(front_edges(&p, self.edge));
        out
    }
}

/// A superellipse |x|ⁿ + |y|ⁿ = aⁿ (n = 5, Apple's icon body), `steps` points per quadrant.
pub fn squircle(c: Pt, a: f32) -> Path {
    let steps = 96;
    let pts: Vec<Pt> = (0..4 * steps)
        .map(|i| {
            let t = i as f32 / steps as f32 * std::f32::consts::FRAC_PI_2;
            let (s, co) = t.sin_cos();
            let e = 2.0 / 5.0;
            [
                c[0] + a * co.signum() * co.abs().powf(e),
                c[1] + a * s.signum() * s.abs().powf(e),
            ]
        })
        .collect();
    polygon(&pts)
}

/// How the artwork is framed on a 1024 square.
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Frame {
    /// A full-bleed dark tile with corner radius 224 and a rim: web and Linux app icons.
    Tile,
    /// Apple's icon grid: an 824-point superellipse body with a soft shadow.
    Dock,
    /// A square ground with the art inside the 80% safe circle: web manifest `maskable`.
    Maskable,
    /// A square ground, art at the tile's size: `apple-touch-icon`, which iOS rounds itself.
    Square,
}

/// Half the Dock body's 824 points.
const DOCK_BODY: f32 = 412.0;
/// The art's width on the tile; on the Dock body it keeps the same proportion.
const TILE_ART: f32 = 832.0;
const DOCK_ART: f32 = TILE_ART * 824.0 / 1024.0;
/// The maskable safe circle's radius on 1024 (80% of the width is 409.6), less a margin.
const SAFE: f32 = 400.0;

fn frame_items(frame: Frame) -> Vec<Item> {
    match frame {
        Frame::Tile => vec![
            Item::fill(
                round_rect(0.0, 0.0, 1024.0, 1024.0, 224.0),
                solid(GROUND, 1.0),
            ),
            Item::stroke(
                round_rect(3.0, 3.0, 1018.0, 1018.0, 221.0),
                6.0,
                solid(RIM, 1.0),
            ),
        ],
        Frame::Dock => vec![
            Item::fill(
                squircle([512.0, 524.0], DOCK_BODY - 4.0),
                solid(BLACK, 0.38),
            )
            .blur(14.0),
            Item::fill(squircle([512.0, 512.0], DOCK_BODY), solid(GROUND, 1.0)),
            Item::stroke(
                squircle([512.0, 512.0], DOCK_BODY - 2.5),
                5.0,
                solid(RIM, 1.0),
            ),
        ],
        Frame::Maskable | Frame::Square => {
            vec![Item::fill(
                rect(0.0, 0.0, 1024.0, 1024.0),
                solid(GROUND, 1.0),
            )]
        }
    }
}

fn art_width(frame: Frame) -> f32 {
    match frame {
        Frame::Dock => DOCK_ART,
        Frame::Tile | Frame::Square => TILE_ART,
        Frame::Maskable => {
            let e = LEDGER.extent();
            let c = [(e.x0 + e.x1) / 2.0, e.ymid];
            let reach = LEDGER
                .hull()
                .iter()
                .map(|p| (p[0] - c[0]).hypot(p[1] - c[1]))
                .fold(0.0, f32::max);
            SAFE / reach * (e.x1 - e.x0)
        }
    }
}

/// The full artwork in `frame`, 1024 units square.
pub fn icon(id: &'static str, frame: Frame) -> Scene {
    let mut items = frame_items(frame);
    let at = LEDGER.extent().place([512.0, 512.0], art_width(frame));
    items.extend(group(place(LEDGER.art(), at), "art"));
    Scene {
        id,
        width: 1024.0,
        height: 1024.0,
        items,
    }
}

/// The bars artwork in `frame` (the tile or the Dock), for 32–128 px.
pub fn small_icon(id: &'static str, frame: Frame) -> Scene {
    let mut items = frame_items(frame);
    let at = BARS.extent().place([512.0, 512.0], art_width(frame));
    items.extend(group(place(BARS.art(), at), "art"));
    Scene {
        id,
        width: 1024.0,
        height: 1024.0,
        items,
    }
}

/// The 32 px favicon, on a 32 grid: the prism with its edges on whole pixels where it can,
/// four 1 px dash rows of bits and four 2 px stripes (red, yellow, green, blue).
pub fn favicon32(id: &'static str) -> Scene {
    let (a, l, r, d) = ([16.0, 6.5], [9.0, 24.5], [23.0, 24.5], [2.0, -1.0]);
    let back = |p: Pt| [p[0] + d[0], p[1] + d[1]];
    let (ab, rb) = (back(a), back(r));
    let xl = |y: f32| l[0] + (l[1] - y) / (l[1] - a[1]) * (a[0] - l[0]);
    let xr = |y: f32| r[0] - (r[1] - y) / (r[1] - a[1]) * (r[0] - a[0]);
    let entry = [xl(15.0), 15.0];
    let stripes = [SPECTRUM[0], SPECTRUM[2], SPECTRUM[3], SPECTRUM[4]];
    let rows = [10.0, 13.0, 16.0, 19.0];
    let dashes: [(f32, &[(f32, f32)]); 4] = [
        (12.0, &[(3.0, 5.0), (6.0, 8.0)]),
        (14.0, &[(3.0, 8.0)]),
        (16.0, &[(4.0, 5.0), (6.0, 8.0)]),
        (18.0, &[(3.0, 6.0), (7.0, 8.0)]),
    ];
    let face = polygon(&[a, l, r]);
    let mut items = vec![
        Item::fill(round_rect(0.0, 0.0, 32.0, 32.0, 7.0), solid(GROUND, 1.0)),
        Item::stroke(round_rect(0.5, 0.5, 31.0, 31.0, 6.5), 1.0, solid(RIM, 1.0)),
    ];
    for (y, _) in dashes {
        items.push(Item::stroke(
            polyline(&[[8.3, y + 0.5], entry]),
            0.6,
            solid(INK, 0.35),
        ));
    }
    let bits = linear(
        [3.0, 0.0],
        [8.0, 0.0],
        &[stop(0.0, DIM, 1.0), stop(1.0, INK, 1.0)],
    );
    for (y, segs) in dashes {
        for &(x0, x1) in segs {
            items.push(Item::fill(rect(x0, y, x1 - x0, 1.0), bits.clone()).crisp());
        }
    }
    for (&c, &y) in stripes.iter().zip(&rows) {
        items.push(Item::fill(
            polygon(&[entry, [xr(y), y], [xr(y + 2.0), y + 2.0]]),
            solid(c, 0.35),
        ));
    }
    for (&c, &y) in stripes.iter().zip(&rows) {
        let x0 = xr(y + 1.0);
        items.push(Item::fill(rect(x0, y, 29.0 - x0, 2.0), solid(c, 1.0)).crisp());
    }
    items.push(Item::fill(polygon(&[r, a, ab, rb]), solid(WHITE, 0.26)));
    items.push(Item::fill(face.clone(), solid(WHITE, 0.10)));
    items.push(
        Item::fill(rect(9.0, 20.0, 14.0, 1.0), solid(WHITE, 0.5))
            .clip(face.clone())
            .crisp(),
    );
    items.push(Item::stroke(face, 1.3, solid(EDGE, 1.0)).round_join());
    items.push(Item::stroke(polyline(&[a, ab, rb, r]), 1.0, solid(EDGE, 0.6)).round_join());
    Scene {
        id,
        width: 32.0,
        height: 32.0,
        items,
    }
}

/// The 16 px favicon's pixels, row by row. `.` ground, `W` edge, `g` glass, `s` side face,
/// `c` crossbar, `1`–`3` bit dashes dim to bright, `b` a ray, `R Y G U` stripes, and `r y n u`
/// a stripe seen through the side face.
pub fn pixels16() -> [[u8; 16]; 16] {
    // The A's half-width on rows 2 to 13, around its apex column.
    const HALF: [usize; 12] = [0, 0, 1, 1, 1, 2, 2, 3, 3, 3, 4, 4];
    const APEX: usize = 8;
    let left = |r: usize| APEX - HALF[r - 2];
    let right = |r: usize| APEX + HALF[r - 2];
    let mut g = [[b'.'; 16]; 16];
    for r in 2..14 {
        let row = &mut g[r];
        row[left(r)..=right(r)].fill(if r == 13 { b'W' } else { b'g' });
        row[left(r)] = b'W';
        row[right(r)] = b'W';
        if r < 13 {
            row[right(r) + 1] = b's';
        }
    }
    g[11][left(11) + 1..right(11)].fill(b'c');
    for (r, ch, seen) in [
        (4, b'R', b'r'),
        (6, b'Y', b'y'),
        (8, b'G', b'n'),
        (10, b'U', b'u'),
    ] {
        g[r][right(r) + 1] = seen;
        g[r][right(r) + 2..15].fill(ch);
    }
    for r in [5, 7, 9] {
        g[r][1..4].copy_from_slice(b"123");
    }
    g[7][4] = b'b';
    g
}

fn mix(a: u32, b: u32, t: f32) -> [u8; 3] {
    let (a, b) = (rgb(a), rgb(b));
    std::array::from_fn(|i| {
        (f32::from(a[i]) + (f32::from(b[i]) - f32::from(a[i])) * t).round() as u8
    })
}

fn pixel_colour(ch: u8) -> Option<[u8; 3]> {
    let stripe = |i: usize| [SPECTRUM[0], SPECTRUM[2], SPECTRUM[3], SPECTRUM[4]][i];
    Some(match ch {
        b'W' => rgb(EDGE),
        b'g' => rgb(0x2b323c),
        b's' => rgb(DIM),
        b'c' => rgb(0x9aa4ae),
        b'1' | b'b' => rgb(0x484f58),
        b'2' => rgb(0x8b949e),
        b'3' => rgb(INK),
        b'R' => rgb(stripe(0)),
        b'Y' => rgb(stripe(1)),
        b'G' => rgb(stripe(2)),
        b'U' => rgb(stripe(3)),
        b'r' => mix(stripe(0), 0xc9d1d9, 0.35),
        b'y' => mix(stripe(1), 0xc9d1d9, 0.35),
        b'n' => mix(stripe(2), 0xc9d1d9, 0.35),
        b'u' => mix(stripe(3), 0xc9d1d9, 0.35),
        _ => return None,
    })
}

/// The 16 px favicon: [`pixels16`] on a rounded dark tile.
pub fn favicon16(id: &'static str) -> Scene {
    let mut items = vec![Item::fill(
        round_rect(0.0, 0.0, 16.0, 16.0, 3.0),
        solid(GROUND, 1.0),
    )];
    for (y, row) in pixels16().iter().enumerate() {
        let mut x = 0;
        while x < 16 {
            let ch = row[x];
            let run = (x..16).find(|&k| row[k] != ch).unwrap_or(16) - x;
            if let Some(c) = pixel_colour(ch) {
                items.push(
                    Item::fill(
                        rect(x as f32, y as f32, run as f32, 1.0),
                        Paint::Solid(c, 1.0),
                    )
                    .crisp(),
                );
            }
            x += run;
        }
    }
    Scene {
        id,
        width: 16.0,
        height: 16.0,
        items,
    }
}

/// The menu bar template on a 36 grid (18 pt at 2x): black and alpha only, so macOS tints it.
/// The prism (with the crossbar as a gap), the beam in, and three rays out whose horizontal
/// edges sit on even coordinates so 1x is crisp too. The rays are their own group, for status
/// states.
pub fn tray(id: &'static str) -> Scene {
    let ink = || solid(BLACK, 1.0);
    let mut items = vec![
        Item::fill(
            [
                polygon(&[[15.0, 3.0], [22.862, 22.0], [7.138, 22.0]]),
                polygon(&[[5.483, 26.0], [24.517, 26.0], [27.0, 32.0], [3.0, 32.0]]),
            ]
            .concat(),
            ink(),
        ),
        Item::stroke(polyline(&[[1.0, 8.0], [12.276, 12.0]]), 2.5, ink()).round_cap(),
    ];
    let rays = [(22.0, 10.0, 12.0), (24.0, 16.0, 10.0), (26.0, 22.0, 8.0)]
        .map(|(x, y, w)| Item::fill(rect(x, y, w, 4.0), ink()).crisp());
    items.extend(group(rays.to_vec(), "rays"));
    Scene {
        id,
        width: 36.0,
        height: 36.0,
        items,
    }
}

/// The wordmark's stroked capitals S P E C T R, in their own units (placed by [`LETTERS_AT`])
/// and drawn 26 wide with round caps and joins. The A is the prism.
pub fn letters() -> Vec<Path> {
    use Cmd::{Arc, Cubic as C, Line as L, Move as M};
    let arc = |r: f32, large: bool, sweep: bool, to: Pt| Arc {
        r,
        large,
        sweep,
        to,
    };
    vec![
        vec![
            M([99.0, 163.0]),
            C([89.0, 147.0], [81.2, 143.0], [57.0, 143.0]),
            C([32.7, 143.0], [13.0, 157.55], [13.0, 175.5]),
            C([13.0, 193.45], [32.7, 208.0], [57.0, 208.0]),
            C([81.3, 208.0], [101.0, 223.45], [101.0, 242.5]),
            C([101.0, 261.55], [81.3, 277.0], [57.0, 277.0]),
            C([32.8, 277.0], [23.0, 273.0], [15.0, 257.0]),
        ],
        vec![
            M([167.0, 277.0]),
            L([167.0, 143.0]),
            L([212.0, 143.0]),
            arc(38.0, false, true, [212.0, 219.0]),
            L([167.0, 219.0]),
        ],
        vec![
            M([402.0, 143.0]),
            L([323.0, 143.0]),
            L([323.0, 277.0]),
            L([402.0, 277.0]),
            M([323.0, 210.0]),
            L([390.0, 210.0]),
        ],
        vec![
            M([578.79, 165.17]),
            arc(67.0, true, false, [578.79, 254.83]),
        ],
        vec![
            M([634.0, 143.0]),
            L([732.0, 143.0]),
            M([683.0, 143.0]),
            L([683.0, 277.0]),
        ],
        vec![
            M([788.0, 277.0]),
            L([788.0, 143.0]),
            L([833.0, 143.0]),
            arc(38.0, false, true, [833.0, 219.0]),
            L([788.0, 219.0]),
            M([835.0, 219.0]),
            L([879.0, 277.0]),
        ],
    ]
}

pub const LETTERS_AT: Place = Place {
    k: 0.86,
    at: [64.0, 35.4],
};
const LETTER_STROKE: f32 = 26.0;

/// The wordmark's spectrum: wider bands than the icon's, one per cap-height step.
pub fn word_bands() -> Vec<Band> {
    SPECTRUM
        .iter()
        .enumerate()
        .map(|(i, &colour)| {
            let y0 = PRISM.top + 4.5 + i as f32 * 23.0;
            Band {
                colour,
                y0,
                y1: y0 + 17.0,
                yc: y0 + 8.5,
            }
        })
        .collect()
}

/// Where the wordmark's bands fade out.
const WORD_FADE: f32 = 1150.0;
/// The beam runs along y 139 above the capitals and bends down into the A at x 826.
const BEAM: [f32; 2] = [139.0, 826.0];

/// The wordmark's artwork in its 1200×400 space: SPECTR in stroked capitals, the bits of
/// "spectra" riding a beam over them into the A-prism, and the spectrum out with its labels.
pub fn wordmark_art() -> Vec<Item> {
    let p = PRISM;
    let entry = [p.xl(182.0), 182.0];
    let bin = |x0: f32, x1: f32| {
        linear(
            [x0, 0.0],
            [x1, 0.0],
            &[
                stop(0.0, 0x8b949e, 0.0),
                stop(0.3, 0x8b949e, 0.8),
                stop(1.0, INK, 1.0),
            ],
        )
    };
    let [beam_y, bend] = BEAM;
    let mut out = vec![
        Item::fill(rect(64.0, beam_y - 0.75, bend - 64.0, 1.5), bin(64.0, bend)),
        Item::stroke(polyline(&[[bend, beam_y], entry]), 1.6, solid(INK, 1.0)).round_cap(),
    ];
    let line = Line {
        x: bend - 6.0,
        y: 131.72 - 0.36 * 14.0,
        size: 14.0,
        weight: glyphs::SEMIBOLD,
        spacing: 1.2,
        end: true,
    };
    let bits = bytes().join(" ");
    let x0 = line.x - line.width(&bits);
    out.push(text(line, &bits, bin(x0, line.x)));
    for d in letters() {
        let mut it = Item::stroke(d, LETTER_STROKE, solid(INK, 1.0)).round();
        it.place = LETTERS_AT;
        out.push(it);
    }
    let bands = word_bands();
    out.extend(fans(&p, &bands, 0.36, entry));
    for b in &bands {
        let x = p.xr(b.y0);
        out.push(Item::fill(
            rect(x, b.y0, WORD_FADE - x, b.y1 - b.y0),
            linear(
                [p.xr(b.yc), 0.0],
                [WORD_FADE, 0.0],
                &[
                    stop(0.0, b.colour, 0.34),
                    stop(0.6, b.colour, 0.12),
                    stop(1.0, b.colour, 0.0),
                ],
            ),
        ));
    }
    out.extend(glass(&p, 1.0));
    out.extend(crossbar(&p, 1.0));
    out.extend(front_edges(&p, 1.0));
    for b in &bands {
        let line = Line {
            x: p.xr(b.yc) + p.depth[0] + 14.0,
            y: b.yc,
            size: 12.5,
            weight: glyphs::HEAVY,
            spacing: 0.0,
            end: false,
        };
        out.extend(hex_label(line, b.colour));
    }
    out
}

/// The wordmark, 1200×400: on its dark plate with a rim, or (`plate` false) on nothing, for a
/// dark page.
pub fn wordmark(id: &'static str, plate: bool) -> Scene {
    let mut items = Vec::new();
    if plate {
        items.push(Item::fill(
            round_rect(0.0, 0.0, 1200.0, 400.0, 28.0),
            solid(GROUND, 1.0),
        ));
        items.push(Item::stroke(
            round_rect(1.0, 1.0, 1198.0, 398.0, 27.0),
            2.0,
            solid(RIM, 1.0),
        ));
    }
    items.extend(wordmark_art());
    Scene {
        id,
        width: 1200.0,
        height: 400.0,
        items,
    }
}

/// A 1280×640 social preview (GitHub's size): the wordmark centred on the ground.
pub fn social(id: &'static str) -> Scene {
    let mut items = vec![Item::fill(
        rect(0.0, 0.0, 1280.0, 640.0),
        solid(GROUND, 1.0),
    )];
    // The wordmark's ink spans x 64–1150 and y 125–290.
    let ext = Extent {
        x0: 64.0,
        x1: WORD_FADE,
        ymid: 207.5,
    };
    items.extend(place(wordmark_art(), ext.place([640.0, 320.0], 1086.0)));
    Scene {
        id,
        width: 1280.0,
        height: 640.0,
        items,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_bits_are_the_ascii_of_spectra() {
        assert_eq!(
            bytes().join(" "),
            "01110011 01110000 01100101 01100011 01110100 01110010 01100001"
        );
        assert_eq!(runs("01110010"), vec![(1, 4), (6, 7)]);
    }

    #[test]
    fn the_ledger_matches_the_approved_layout() {
        // From the generator of the approved master (final-app.svg).
        let e = LEDGER.extent();
        assert!((e.x0 - 796.391).abs() < 0.01, "{e:?}");
        assert!((e.x1 - 1017.73).abs() < 0.01, "{e:?}");
        assert!((LEDGER.entry()[0] - 883.520).abs() < 0.01);
        let at = e.place([512.0, 512.0], TILE_ART);
        assert!((at.k - 3.7589).abs() < 0.001, "{at:?}");
        assert!((art_width(Frame::Maskable) - 726.367).abs() < 0.05);
        let b = BARS.extent();
        assert!(
            (b.x0 - 795.202).abs() < 0.01 && (b.x1 - 1018.798).abs() < 0.01,
            "{b:?}"
        );
    }

    #[test]
    fn the_maskable_art_stays_inside_the_safe_circle() {
        let at = LEDGER
            .extent()
            .place([512.0, 512.0], art_width(Frame::Maskable));
        for p in LEDGER.hull() {
            let q = [p[0] * at.k + at.at[0], p[1] * at.k + at.at[1]];
            assert!((q[0] - 512.0).hypot(q[1] - 512.0) <= 409.6, "{q:?}");
        }
    }

    #[test]
    fn the_16_px_pixels_match_the_hand_set_grid() {
        let rows: Vec<String> = pixels16()
            .iter()
            .map(|r| String::from_utf8_lossy(r).into_owned())
            .collect();
        let approved = [
            "................",
            "................",
            "........Ws......",
            "........Ws......",
            ".......WgWrRRRR.",
            ".123...WgWs.....",
            ".......WgWyYYYY.",
            ".123b.WgggWs....",
            "......WgggWnGGG.",
            ".123.WgggggWs...",
            ".....WgggggWuUU.",
            ".....WcccccWs...",
            "....WgggggggWs..",
            "....WWWWWWWWW...",
            "................",
            "................",
        ];
        assert_eq!(rows, approved);
    }
}
