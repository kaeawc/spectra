//! A tiny bold monospace drawn as stroke skeletons, for the only characters the mark sets:
//! `0-9 A-F # x` and the space. SVG `<text>` would render in whatever monospace the viewer has
//! and a polygon rasterizer can't draw it, so labels are paths here and look the same in every
//! SVG viewer and PNG.
//!
//! Metrics follow Menlo, which the approved masters were drawn in: an advance of 0.602 em and a
//! cap height of 0.729 em, so `dy="0.36em"` centres a line of capitals on its `y`.

use std::f32::consts::PI;

use super::scene::{Cmd, Path, Pt};

/// Horizontal advance per character, in em.
pub const ADVANCE: f32 = 0.602;
/// Height of capitals and figures, in em.
pub const CAP: f32 = 0.729;

/// Stroke weights, in em, for the weights the masters use (600, 700 and 800), and a light one
/// for the `#` of a label.
pub const HAIR: f32 = 0.09;
pub const SEMIBOLD: f32 = 0.11;
pub const BOLD: f32 = 0.13;
pub const HEAVY: f32 = 0.142;

/// Where a skeleton's unit box sits in the glyph cell, in em: skeletons span `u` 0–1 left to
/// right and `v` 0–1 from the cap line to the baseline, and the stroke is centred on them.
fn cell(weight: f32) -> ([f32; 2], [f32; 2]) {
    let left = 0.048 + weight / 2.0;
    let right = ADVANCE - 0.048 - weight / 2.0;
    ([left, right - left], [-CAP + weight / 2.0, CAP - weight])
}

/// Points on the ellipse centred `(cu, cv)` with radii `(ru, rv)`, from angle `a0` to `a1`
/// degrees (0 is right, 90 is down, as on screen).
fn arc(c: [f32; 2], r: [f32; 2], a0: f32, a1: f32) -> Vec<[f32; 2]> {
    let n = (((a1 - a0).abs() / 15.0).ceil() as usize).max(2);
    (0..=n)
        .map(|i| {
            let a = (a0 + (a1 - a0) * i as f32 / n as f32) * PI / 180.0;
            [c[0] + r[0] * a.cos(), c[1] + r[1] * a.sin()]
        })
        .collect()
}

fn join(parts: &[&[[f32; 2]]]) -> Vec<[f32; 2]> {
    parts.iter().flat_map(|p| p.iter().copied()).collect()
}

/// A skeleton: open polylines, and closed ones marked `true`.
type Skeleton = Vec<(Vec<[f32; 2]>, bool)>;

/// The skeleton of `ch`, or `None` for a character the set doesn't have (and the space).
fn skeleton(ch: char) -> Option<Skeleton> {
    let open = |p: Vec<[f32; 2]>| (p, false);
    let closed = |p: Vec<[f32; 2]>| (p, true);
    let line = |a: [f32; 2], b: [f32; 2]| (vec![a, b], false);
    Some(match ch {
        '0' => vec![
            closed(arc([0.5, 0.5], [0.5, 0.5], 0.0, 345.0)),
            line([0.74, 0.2], [0.26, 0.8]),
        ],
        '1' => vec![
            open(vec![[0.12, 0.2], [0.55, 0.0], [0.55, 1.0]]),
            line([0.05, 1.0], [1.0, 1.0]),
        ],
        '2' => vec![open(join(&[
            &arc([0.5, 0.27], [0.48, 0.27], 165.0, 375.0),
            &[[0.0, 1.0], [1.03, 1.0]],
        ]))],
        '3' => vec![
            open(join(&[
                &arc([0.48, 0.25], [0.46, 0.25], 200.0, 450.0),
                &[[0.3, 0.5]],
            ])),
            open(join(&[
                &[[0.3, 0.5]],
                &arc([0.48, 0.75], [0.5, 0.25], 270.0, 520.0),
            ])),
        ],
        '4' => vec![
            line([0.74, 0.0], [0.74, 1.0]),
            open(vec![[0.64, 0.0], [0.0, 0.72], [1.03, 0.72]]),
        ],
        '5' => vec![open(join(&[
            &[[0.92, 0.0], [0.08, 0.0], [0.04, 0.47]],
            &arc([0.48, 0.69], [0.5, 0.31], 225.0, 515.0),
        ]))],
        '6' => vec![
            open(arc([0.5, 0.68], [0.5, 0.68], 290.0, 180.0)),
            closed(arc([0.5, 0.68], [0.5, 0.32], 0.0, 345.0)),
        ],
        '7' => vec![open(vec![[0.0, 0.0], [1.0, 0.0], [0.38, 1.0]])],
        '8' => vec![
            closed(arc([0.5, 0.24], [0.43, 0.24], 0.0, 345.0)),
            closed(arc([0.5, 0.735], [0.5, 0.265], 0.0, 345.0)),
        ],
        '9' => vec![
            closed(arc([0.5, 0.32], [0.5, 0.32], 0.0, 345.0)),
            open(arc([0.5, 0.32], [0.5, 0.68], 0.0, 110.0)),
        ],
        'A' => vec![
            open(vec![[-0.02, 1.0], [0.4, 0.0], [0.6, 0.0], [1.02, 1.0]]),
            line([0.16, 0.69], [0.84, 0.69]),
        ],
        'B' => vec![
            closed(join(&[
                &[[0.0, 1.0], [0.0, 0.0], [0.58, 0.0]],
                &arc([0.58, 0.245], [0.37, 0.245], 270.0, 450.0),
                &arc([0.6, 0.745], [0.4, 0.255], 270.0, 450.0),
            ])),
            line([0.0, 0.49], [0.6, 0.49]),
        ],
        'C' => vec![open(arc([0.55, 0.5], [0.55, 0.5], 318.0, 42.0))],
        'D' => vec![closed(join(&[
            &[[0.0, 1.0], [0.0, 0.0], [0.42, 0.0]],
            &arc([0.42, 0.5], [0.58, 0.5], 270.0, 450.0),
        ]))],
        'E' => vec![
            open(vec![[1.0, 0.0], [0.0, 0.0], [0.0, 1.0], [1.0, 1.0]]),
            line([0.0, 0.49], [0.88, 0.49]),
        ],
        'F' => vec![
            open(vec![[1.0, 0.0], [0.0, 0.0], [0.0, 1.0]]),
            line([0.0, 0.49], [0.86, 0.49]),
        ],
        '#' => vec![
            line([0.26, 0.04], [0.14, 0.96]),
            line([0.86, 0.04], [0.74, 0.96]),
            line([-0.04, 0.29], [1.06, 0.29]),
            line([-0.06, 0.71], [1.04, 0.71]),
        ],
        'x' => vec![
            line([0.02, 0.3], [0.98, 1.0]),
            line([0.98, 0.3], [0.02, 1.0]),
        ],
        _ => return None,
    })
}

/// How a line of text sits: `x` is its start, or its end when `end` is set; `y` is the middle
/// of its capitals.
#[derive(Clone, Copy, Debug)]
pub struct Line {
    pub x: f32,
    pub y: f32,
    pub size: f32,
    /// Stroke weight in em: [`SEMIBOLD`], [`BOLD`] or [`HEAVY`].
    pub weight: f32,
    /// Extra space after each character, in user units (CSS `letter-spacing`).
    pub spacing: f32,
    pub end: bool,
}

impl Line {
    /// The line's width in user units, letter spacing included.
    pub fn width(&self, text: &str) -> f32 {
        text.chars().count() as f32 * (ADVANCE * self.size + self.spacing)
    }

    /// The stroke width to draw it with.
    pub fn stroke(&self) -> f32 {
        self.weight * self.size
    }

    /// `text` as one path of stroke skeletons, in user units.
    pub fn path(&self, text: &str) -> Path {
        let ([u0, uw], [v0, vh]) = cell(self.weight);
        let left = if self.end {
            self.x - self.width(text)
        } else {
            self.x
        };
        let baseline = self.y + 0.36 * self.size;
        let mut out = Vec::new();
        for (i, ch) in text.chars().enumerate() {
            let pen = left + i as f32 * (ADVANCE * self.size + self.spacing);
            let to = |p: [f32; 2]| -> Pt {
                [
                    pen + (u0 + p[0] * uw) * self.size,
                    baseline + (v0 + p[1] * vh) * self.size,
                ]
            };
            for (pts, closed) in skeleton(ch).unwrap_or_default() {
                for (k, &p) in pts.iter().enumerate() {
                    out.push(if k == 0 {
                        Cmd::Move(to(p))
                    } else {
                        Cmd::Line(to(p))
                    });
                }
                if closed {
                    out.push(Cmd::Close);
                }
            }
        }
        out
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn every_character_the_mark_sets_has_a_skeleton() {
        for ch in "0123456789ABCDEF#x".chars() {
            assert!(skeleton(ch).is_some_and(|s| !s.is_empty()), "{ch}");
        }
        assert!(skeleton(' ').is_none());
    }

    #[test]
    fn an_end_anchored_line_ends_at_x() {
        let line = Line {
            x: 100.0,
            y: 50.0,
            size: 10.0,
            weight: BOLD,
            spacing: 0.0,
            end: true,
        };
        let xs: Vec<f32> = line
            .path("01")
            .iter()
            .filter_map(|c| match c {
                Cmd::Move(p) | Cmd::Line(p) => Some(p[0]),
                _ => None,
            })
            .collect();
        let max = xs.iter().copied().fold(f32::MIN, f32::max);
        assert!(max < 100.0 && max > 100.0 - ADVANCE * 10.0 / 2.0, "{max}");
    }
}
