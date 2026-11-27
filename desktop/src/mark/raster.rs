//! Draws a [`Scene`] into RGBA pixels with no dependencies: curves and arcs are flattened to
//! polylines, strokes become a union of quads, joins and caps, and each shape is filled with
//! nonzero winding at several samples per pixel, then averaged down. One shape's samples are
//! painted once however many of its pieces overlap, so a translucent stroke doesn't darken where
//! its segments meet.

use std::f32::consts::TAU;

use super::scene::{Cap, Cmd, Join, Path, Pt, Scene, Shape};

/// How far a flattened curve may stray from the true one, in pixels.
const TOLERANCE: f32 = 0.01;

/// A flattened subpath and whether it is closed.
type Poly = (Vec<Pt>, bool);

/// `path` as polylines, each point through `map`. `scale` is the factor `map` scales by, so arcs
/// and curves get enough points at the size they are drawn.
pub fn flatten(path: &Path, map: impl Fn(Pt) -> Pt, scale: f32) -> Vec<Poly> {
    let mut out: Vec<Poly> = Vec::new();
    let mut cur: Vec<Pt> = Vec::new();
    let mut at = [0.0, 0.0];
    let mut start = [0.0, 0.0];
    let flush = |cur: &mut Vec<Pt>, out: &mut Vec<Poly>, closed: bool| {
        if cur.len() > 1 {
            out.push((std::mem::take(cur), closed));
        } else {
            cur.clear();
        }
    };
    for c in path {
        match *c {
            Cmd::Move(p) => {
                flush(&mut cur, &mut out, false);
                cur.push(map(p));
                at = p;
                start = p;
            }
            Cmd::Line(p) => {
                cur.push(map(p));
                at = p;
            }
            Cmd::Cubic(a, b, p) => {
                let len = dist(at, a) + dist(a, b) + dist(b, p);
                let n = ((len * scale).sqrt() * 1.5).ceil().clamp(4.0, 200.0) as usize;
                for i in 1..=n {
                    let t = i as f32 / n as f32;
                    let u = 1.0 - t;
                    let w = [u * u * u, 3.0 * u * u * t, 3.0 * u * t * t, t * t * t];
                    cur.push(map(std::array::from_fn(|k| {
                        w[0] * at[k] + w[1] * a[k] + w[2] * b[k] + w[3] * p[k]
                    })));
                }
                at = p;
            }
            Cmd::Arc {
                r,
                large,
                sweep,
                to,
            } => {
                for p in arc(at, to, r, large, sweep, r * scale) {
                    cur.push(map(p));
                }
                at = to;
            }
            Cmd::Close => {
                flush(&mut cur, &mut out, true);
                at = start;
                cur.push(map(at));
            }
        }
    }
    flush(&mut cur, &mut out, false);
    out
}

fn dist(a: Pt, b: Pt) -> f32 {
    (b[0] - a[0]).hypot(b[1] - a[1])
}

/// Angle step that keeps a circle of radius `r_px` within [`TOLERANCE`].
fn step(r_px: f32) -> f32 {
    if r_px <= TOLERANCE {
        return TAU / 4.0;
    }
    (2.0 * (1.0 - TOLERANCE / r_px).acos()).clamp(TAU / 512.0, TAU / 8.0)
}

/// Points after `from` along SVG's circular arc to `to` (SVG 1.1 F.6.5 with rx = ry).
fn arc(from: Pt, to: Pt, r: f32, large: bool, sweep: bool, r_px: f32) -> Vec<Pt> {
    let h = [(from[0] - to[0]) / 2.0, (from[1] - to[1]) / 2.0];
    let d2 = h[0] * h[0] + h[1] * h[1];
    if d2 == 0.0 {
        return Vec::new();
    }
    let r = r.max(d2.sqrt());
    let mut k = ((r * r - d2) / d2).max(0.0).sqrt();
    if large == sweep {
        k = -k;
    }
    let mid = [(from[0] + to[0]) / 2.0, (from[1] + to[1]) / 2.0];
    let c = [mid[0] + k * h[1], mid[1] - k * h[0]];
    let a0 = (from[1] - c[1]).atan2(from[0] - c[0]);
    let a1 = (to[1] - c[1]).atan2(to[0] - c[0]);
    let mut da = a1 - a0;
    if sweep && da < 0.0 {
        da += TAU;
    } else if !sweep && da > 0.0 {
        da -= TAU;
    }
    let n = (da.abs() / step(r_px)).ceil().max(1.0) as usize;
    (1..=n)
        .map(|i| {
            if i == n {
                return to;
            }
            let a = a0 + da * i as f32 / n as f32;
            [c[0] + r * a.cos(), c[1] + r * a.sin()]
        })
        .collect()
}

/// A regular polygon around `c`, fine enough for radius `r` in pixels.
fn disc(c: Pt, r: f32) -> Vec<Pt> {
    let n = (TAU / step(r)).ceil().max(8.0) as usize;
    (0..n)
        .map(|i| {
            let a = TAU * i as f32 / n as f32;
            [c[0] + r * a.cos(), c[1] + r * a.sin()]
        })
        .collect()
}

fn area(ring: &[Pt]) -> f32 {
    let n = ring.len();
    (0..n)
        .map(|i| {
            let (a, b) = (ring[i], ring[(i + 1) % n]);
            a[0] * b[1] - b[0] * a[1]
        })
        .sum::<f32>()
        / 2.0
}

/// `ring` wound the same way as every other stroke piece, so nonzero winding unions them.
fn positive(mut ring: Vec<Pt>) -> Vec<Pt> {
    if area(&ring) < 0.0 {
        ring.reverse();
    }
    ring
}

/// SVG's default `stroke-miterlimit`.
const MITER_LIMIT: f32 = 4.0;

/// The outline of `polys` stroked `width` wide, as rings whose union is the stroke.
pub fn stroke(polys: &[Poly], width: f32, join: Join, cap: Cap) -> Vec<Vec<Pt>> {
    let h = width / 2.0;
    let mut out = Vec::new();
    for (pts, closed) in polys {
        let mut p: Vec<Pt> = Vec::with_capacity(pts.len());
        for &q in pts {
            if p.last().is_none_or(|l: &Pt| dist(*l, q) > 1e-4) {
                p.push(q);
            }
        }
        if *closed && p.len() > 2 && dist(p[0], p[p.len() - 1]) <= 1e-4 {
            p.pop();
        }
        if p.len() < 2 {
            if cap == Cap::Round && !p.is_empty() {
                out.push(disc(p[0], h));
            }
            continue;
        }
        let n = p.len();
        let segs = if *closed { n } else { n - 1 };
        let dir = |i: usize| {
            let (a, b) = (p[i % n], p[(i + 1) % n]);
            let l = dist(a, b);
            [(b[0] - a[0]) / l, (b[1] - a[1]) / l]
        };
        for i in 0..segs {
            let (a, b) = (p[i], p[(i + 1) % n]);
            let d = dir(i);
            let m = [-d[1] * h, d[0] * h];
            out.push(positive(vec![
                [a[0] + m[0], a[1] + m[1]],
                [b[0] + m[0], b[1] + m[1]],
                [b[0] - m[0], b[1] - m[1]],
                [a[0] - m[0], a[1] - m[1]],
            ]));
        }
        let joints: Vec<usize> = if *closed {
            (0..n).collect()
        } else {
            (1..n - 1).collect()
        };
        for j in joints {
            let at = p[j];
            if join == Join::Round {
                out.push(disc(at, h));
                continue;
            }
            let (d1, d2) = (dir((j + n - 1) % n), dir(j));
            let cross = d1[0] * d2[1] - d1[1] * d2[0];
            if cross.abs() < 1e-6 {
                continue;
            }
            let side = -cross.signum();
            let (n1, n2) = ([-d1[1], d1[0]], [-d2[1], d2[0]]);
            let o1 = [at[0] + side * h * n1[0], at[1] + side * h * n1[1]];
            let o2 = [at[0] + side * h * n2[0], at[1] + side * h * n2[1]];
            let dot = n1[0] * n2[0] + n1[1] * n2[1];
            if dot > -1.0 && (2.0 / (1.0 + dot)).sqrt() <= MITER_LIMIT {
                let f = side * h / (1.0 + dot);
                let tip = [at[0] + f * (n1[0] + n2[0]), at[1] + f * (n1[1] + n2[1])];
                out.push(positive(vec![at, o1, tip, o2]));
            } else {
                out.push(positive(vec![at, o1, o2]));
            }
        }
        if !*closed && cap == Cap::Round {
            out.push(disc(p[0], h));
            out.push(disc(p[n - 1], h));
        }
    }
    out
}

/// An RGBA image drawn at `samples`×`samples` points per pixel, then averaged down.
pub struct Canvas {
    w: u32,
    h: u32,
    samples: u32,
    /// Straight-alpha sRGB, one per sample.
    buf: Vec<[u8; 4]>,
}

impl Canvas {
    /// A transparent canvas; small images take more samples per pixel.
    pub fn new(w: u32, h: u32) -> Canvas {
        let samples = (4096 / w.max(h).max(1)).clamp(4, 8);
        let len = (w * samples) as usize * (h * samples) as usize;
        Canvas {
            w,
            h,
            samples,
            buf: vec![[0; 4]; len],
        }
    }

    /// Paints `colour` (chosen per sample, at its position in pixels) wherever the rings wind
    /// nonzero and, given a clip, the clip's rings do too.
    pub fn paint(
        &mut self,
        rings: &[Vec<Pt>],
        clip: Option<&[Vec<Pt>]>,
        colour: impl Fn(f32, f32) -> [u8; 4],
    ) {
        let ss = self.samples as f32;
        let (cols, rows) = (
            (self.w * self.samples) as i64,
            (self.h * self.samples) as i64,
        );
        let bounds = |rs: &[Vec<Pt>]| {
            rs.iter()
                .flatten()
                .fold((f32::MAX, f32::MIN), |(lo, hi), p| {
                    (lo.min(p[1]), hi.max(p[1]))
                })
        };
        let (mut lo, mut hi) = bounds(rings);
        if let Some(c) = clip {
            let (clo, chi) = bounds(c);
            (lo, hi) = (lo.max(clo), hi.min(chi));
        }
        let row0 = ((lo * ss - 0.5).ceil() as i64).max(0);
        let row1 = ((hi * ss - 0.5).ceil() as i64).min(rows);
        let (mut spans, mut clips, mut both) = (Vec::new(), Vec::new(), Vec::new());
        let mut xs = Vec::new();
        for row in row0..row1 {
            let y = (row as f32 + 0.5) / ss;
            nonzero_spans(rings, y, &mut xs, &mut spans);
            let spans = match clip {
                Some(c) => {
                    nonzero_spans(c, y, &mut xs, &mut clips);
                    intersect(&spans, &clips, &mut both);
                    &both
                }
                None => &spans,
            };
            for &(x0, x1) in spans {
                let c0 = ((x0 * ss - 0.5).ceil() as i64).max(0);
                let c1 = ((x1 * ss - 0.5).ceil() as i64).min(cols);
                for col in c0..c1 {
                    let src = colour((col as f32 + 0.5) / ss, y);
                    let at = (row * cols + col) as usize;
                    self.buf[at] = over(src, self.buf[at]);
                }
            }
        }
    }

    /// Lays a `w`×`h` RGBA image (straight alpha) over every sample of its pixel.
    pub fn composite(&mut self, rgba: &[u8]) {
        let s = self.samples as usize;
        let cols = self.w as usize * s;
        for (i, at) in self.buf.iter_mut().enumerate() {
            let (row, col) = (i / cols, i % cols);
            let p = ((row / s) * self.w as usize + col / s) * 4;
            *at = over([rgba[p], rgba[p + 1], rgba[p + 2], rgba[p + 3]], *at);
        }
    }

    /// The image as straight-alpha RGBA rows, each pixel the average of its samples.
    pub fn into_rgba(self) -> Vec<u8> {
        let (w, h, s) = (self.w as usize, self.h as usize, self.samples as usize);
        let cols = w * s;
        let mut out = Vec::with_capacity(w * h * 4);
        for py in 0..h {
            for px in 0..w {
                let mut acc = [0.0f32; 4];
                for sy in 0..s {
                    for sx in 0..s {
                        let p = self.buf[(py * s + sy) * cols + px * s + sx];
                        let a = f32::from(p[3]) / 255.0;
                        (0..3).for_each(|k| acc[k] += f32::from(p[k]) * a);
                        acc[3] += a;
                    }
                }
                let a = acc[3] / (s * s) as f32;
                let rgb: [u8; 3] = std::array::from_fn(|k| {
                    if acc[3] > 0.0 {
                        (acc[k] / acc[3]).round().clamp(0.0, 255.0) as u8
                    } else {
                        0
                    }
                });
                out.extend_from_slice(&[rgb[0], rgb[1], rgb[2], (a * 255.0).round() as u8]);
            }
        }
        out
    }
}

/// The x ranges on the line `y` where `rings` wind nonzero, sorted and disjoint.
fn nonzero_spans(rings: &[Vec<Pt>], y: f32, xs: &mut Vec<(f32, i32)>, out: &mut Vec<(f32, f32)>) {
    xs.clear();
    out.clear();
    for ring in rings {
        let n = ring.len();
        for k in 0..n {
            let (a, b) = (ring[k], ring[(k + 1) % n]);
            let dir = if a[1] <= y && y < b[1] {
                1
            } else if b[1] <= y && y < a[1] {
                -1
            } else {
                continue;
            };
            xs.push((a[0] + (y - a[1]) * (b[0] - a[0]) / (b[1] - a[1]), dir));
        }
    }
    xs.sort_by(|a, b| a.0.total_cmp(&b.0));
    let mut wind = 0;
    let mut from = 0.0;
    for &(x, d) in xs.iter() {
        let was = wind;
        wind += d;
        if was == 0 && wind != 0 {
            from = x;
        } else if was != 0 && wind == 0 && x > from {
            out.push((from, x));
        }
    }
}

fn intersect(a: &[(f32, f32)], b: &[(f32, f32)], out: &mut Vec<(f32, f32)>) {
    out.clear();
    let (mut i, mut j) = (0, 0);
    while i < a.len() && j < b.len() {
        let (lo, hi) = (a[i].0.max(b[j].0), a[i].1.min(b[j].1));
        if lo < hi {
            out.push((lo, hi));
        }
        if a[i].1 < b[j].1 { i += 1 } else { j += 1 }
    }
}

/// `src` over `dst`, straight alpha.
fn over(src: [u8; 4], dst: [u8; 4]) -> [u8; 4] {
    if src[3] == 255 || dst[3] == 0 {
        return src;
    }
    if src[3] == 0 {
        return dst;
    }
    let (sa, da) = (f32::from(src[3]) / 255.0, f32::from(dst[3]) / 255.0);
    let a = sa + da * (1.0 - sa);
    let c: [u8; 3] = std::array::from_fn(|k| {
        ((f32::from(src[k]) * sa + f32::from(dst[k]) * da * (1.0 - sa)) / a).round() as u8
    });
    [c[0], c[1], c[2], (a * 255.0).round() as u8]
}

/// Blurs straight-alpha RGBA in place: three box blurs approximating a Gaussian of `sigma` px.
fn blur(rgba: &mut [u8], w: usize, h: usize, sigma: f32) {
    let mut pre: Vec<[f32; 4]> = rgba
        .as_chunks::<4>()
        .0
        .iter()
        .map(|p| {
            let a = f32::from(p[3]) / 255.0;
            [
                f32::from(p[0]) * a,
                f32::from(p[1]) * a,
                f32::from(p[2]) * a,
                a,
            ]
        })
        .collect();
    let mut tmp = pre.clone();
    for r in box_radii(sigma) {
        box_pass(&pre, &mut tmp, w, h, r, true);
        box_pass(&tmp, &mut pre, w, h, r, false);
    }
    for (p, q) in rgba.as_chunks_mut::<4>().0.iter_mut().zip(&pre) {
        let a = q[3].clamp(0.0, 1.0);
        for k in 0..3 {
            p[k] = if a > 0.0 {
                (q[k] / a).round().clamp(0.0, 255.0) as u8
            } else {
                0
            };
        }
        p[3] = (a * 255.0).round() as u8;
    }
}

/// Radii of three box blurs whose sequence approximates a Gaussian of `sigma`.
fn box_radii(sigma: f32) -> [usize; 3] {
    let n = 3.0;
    let ideal = (12.0 * sigma * sigma / n + 1.0).sqrt();
    let mut wl = ideal.floor() as i32;
    if wl % 2 == 0 {
        wl -= 1;
    }
    let wl = wl.max(1);
    let wu = wl + 2;
    let (s2, wlf) = (sigma * sigma, wl as f32);
    let m = ((12.0 * s2 - n * wlf * wlf - 4.0 * n * wlf - 3.0 * n) / (-4.0 * wlf - 4.0)).round();
    std::array::from_fn(|i| (if (i as f32) < m { wl } else { wu } as usize - 1) / 2)
}

/// One box blur of radius `r` along rows (`horizontal`) or columns; outside is transparent.
fn box_pass(
    src: &[[f32; 4]],
    dst: &mut [[f32; 4]],
    w: usize,
    h: usize,
    r: usize,
    horizontal: bool,
) {
    let (lines, len) = if horizontal { (h, w) } else { (w, h) };
    let at = |line: usize, i: usize| {
        if horizontal {
            line * w + i
        } else {
            i * w + line
        }
    };
    let norm = 1.0 / (2 * r + 1) as f32;
    for line in 0..lines {
        let mut acc = [0.0f32; 4];
        for i in 0..=r.min(len - 1) {
            (0..4).for_each(|k| acc[k] += src[at(line, i)][k]);
        }
        for i in 0..len {
            dst[at(line, i)] = acc.map(|v| v * norm);
            if i + r + 1 < len {
                (0..4).for_each(|k| acc[k] += src[at(line, i + r + 1)][k]);
            }
            if i >= r {
                (0..4).for_each(|k| acc[k] -= src[at(line, i - r)][k]);
            }
        }
    }
}

/// `scene` drawn `w`×`h` pixels, as straight-alpha RGBA rows.
pub fn rasterize(scene: &Scene, w: u32, h: u32) -> Vec<u8> {
    let s = w as f32 / scene.width;
    let mut canvas = Canvas::new(w, h);
    for it in &scene.items {
        let k = it.place.k * s;
        let at = [it.place.at[0] * s, it.place.at[1] * s];
        let to_px = |p: Pt| [p[0] * k + at[0], p[1] * k + at[1]];
        let rings: Vec<Vec<Pt>> = match &it.shape {
            Shape::Fill(path) => flatten(path, to_px, k).into_iter().map(|p| p.0).collect(),
            Shape::Stroke {
                path,
                width,
                join,
                cap,
            } => stroke(&flatten(path, to_px, k), width * k, *join, *cap),
        };
        let clip: Option<Vec<Vec<Pt>>> = it
            .clip
            .as_ref()
            .map(|c| flatten(c, to_px, k).into_iter().map(|p| p.0).collect());
        let colour = |x: f32, y: f32| it.paint.at([(x - at[0]) / k, (y - at[1]) / k]);
        if it.blur > 0.0 {
            let mut layer = Canvas::new(w, h);
            layer.paint(&rings, clip.as_deref(), colour);
            let mut px = layer.into_rgba();
            blur(&mut px, w as usize, h as usize, it.blur * k);
            canvas.composite(&px);
        } else {
            canvas.paint(&rings, clip.as_deref(), colour);
        }
    }
    canvas.into_rgba()
}

#[cfg(test)]
mod tests {
    use super::super::scene::{Item, Scene, circle, polygon, polyline, rect, solid};
    use super::*;

    fn pixel(rgba: &[u8], w: u32, x: u32, y: u32) -> [u8; 4] {
        let i = ((y * w + x) * 4) as usize;
        [rgba[i], rgba[i + 1], rgba[i + 2], rgba[i + 3]]
    }

    fn scene(w: f32, items: Vec<Item>) -> Scene {
        Scene {
            id: "t",
            width: w,
            height: w,
            items,
        }
    }

    #[test]
    fn a_square_fills_its_pixels_and_half_covers_its_edge() {
        let img = rasterize(
            &scene(
                8.0,
                vec![Item::fill(rect(2.0, 2.0, 4.0, 4.0), solid(0xff0000, 1.0))],
            ),
            8,
            8,
        );
        assert_eq!(pixel(&img, 8, 3, 3), [255, 0, 0, 255]);
        assert_eq!(pixel(&img, 8, 1, 3)[3], 0);
        let img = rasterize(
            &scene(
                8.0,
                vec![Item::fill(rect(2.5, 0.0, 6.0, 8.0), solid(0x0000ff, 1.0))],
            ),
            8,
            8,
        );
        assert_eq!(pixel(&img, 8, 2, 4)[3], 128, "half a pixel covered");
    }

    /// Two triangles sharing a diagonal cover every sample once: no seam, no double cover.
    #[test]
    fn neighbours_leave_no_seam() {
        let paint = solid(0x0ac864, 1.0);
        let img = rasterize(
            &scene(
                16.0,
                vec![
                    Item::fill(
                        polygon(&[[0.0, 0.0], [16.0, 0.0], [0.0, 16.0]]),
                        paint.clone(),
                    ),
                    Item::fill(polygon(&[[16.0, 0.0], [16.0, 16.0], [0.0, 16.0]]), paint),
                ],
            ),
            16,
            16,
        );
        assert!(
            img.as_chunks::<4>()
                .0
                .iter()
                .all(|p| *p == [10, 200, 100, 255])
        );
    }

    /// A translucent polyline is one shape: where its segments overlap it is no darker.
    #[test]
    fn a_translucent_stroke_paints_its_overlaps_once() {
        let line = polyline(&[[2.0, 8.0], [14.0, 8.0], [8.0, 2.0], [8.0, 14.0]]);
        let img = rasterize(
            &scene(
                16.0,
                vec![Item::stroke(line, 3.0, solid(0xffffff, 0.5)).round()],
            ),
            16,
            16,
        );
        assert_eq!(pixel(&img, 16, 8, 8)[3], 128, "the crossing");
        assert_eq!(pixel(&img, 16, 4, 8)[3], 128, "a plain stretch");
    }

    #[test]
    fn arcs_close_into_a_circle() {
        let img = rasterize(
            &scene(
                32.0,
                vec![Item::fill(circle([16.0, 16.0], 10.0), solid(0x000000, 1.0))],
            ),
            32,
            32,
        );
        assert_eq!(pixel(&img, 32, 16, 16)[3], 255);
        assert_eq!(pixel(&img, 32, 16, 4)[3], 0);
        assert_eq!(pixel(&img, 32, 16, 7)[3], 255);
        let covered = img
            .as_chunks::<4>()
            .0
            .iter()
            .map(|p| f32::from(p[3]) / 255.0)
            .sum::<f32>();
        assert!((covered - 314.16).abs() < 0.6, "{covered}");
    }

    #[test]
    fn a_clip_keeps_only_what_is_inside_it() {
        let item = Item::fill(rect(0.0, 0.0, 8.0, 8.0), solid(0xffffff, 1.0))
            .clip(rect(0.0, 0.0, 4.0, 8.0));
        let img = rasterize(&scene(8.0, vec![item]), 8, 8);
        assert_eq!(pixel(&img, 8, 2, 2)[3], 255);
        assert_eq!(pixel(&img, 8, 6, 2)[3], 0);
    }

    #[test]
    fn a_blur_spreads_a_shape_and_keeps_its_weight() {
        let item = Item::fill(rect(12.0, 12.0, 8.0, 8.0), solid(0x000000, 1.0)).blur(2.0);
        let img = rasterize(&scene(32.0, vec![item]), 32, 32);
        assert!(pixel(&img, 32, 16, 16)[3] > 200);
        assert!(pixel(&img, 32, 10, 16)[3] > 0, "spills past the edge");
        let weight = img
            .as_chunks::<4>()
            .0
            .iter()
            .map(|p| f32::from(p[3]) / 255.0)
            .sum::<f32>();
        assert!((weight - 64.0).abs() < 2.0, "{weight}");
    }
}
