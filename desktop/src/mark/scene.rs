//! A drawing as data: paths that are filled or stroked with a paint, optionally clipped, each
//! placed by a uniform scale and a translation. [`Scene::svg`] writes one as SVG and
//! [`super::raster::rasterize`] draws it into pixels, so the SVGs and PNGs come from the same
//! geometry.

/// A point in a scene's user units, y down.
pub type Pt = [f32; 2];

/// One path command, as SVG spells it. Arcs are circular (all the mark needs).
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Cmd {
    Move(Pt),
    Line(Pt),
    Cubic(Pt, Pt, Pt),
    Arc {
        r: f32,
        large: bool,
        sweep: bool,
        to: Pt,
    },
    Close,
}

pub type Path = Vec<Cmd>;

/// A closed polygon through `pts`.
pub fn polygon(pts: &[Pt]) -> Path {
    let mut p = polyline(pts);
    p.push(Cmd::Close);
    p
}

/// An open polyline through `pts`.
pub fn polyline(pts: &[Pt]) -> Path {
    pts.iter()
        .enumerate()
        .map(|(i, &p)| if i == 0 { Cmd::Move(p) } else { Cmd::Line(p) })
        .collect()
}

pub fn rect(x: f32, y: f32, w: f32, h: f32) -> Path {
    polygon(&[[x, y], [x + w, y], [x + w, y + h], [x, y + h]])
}

/// A rectangle with circular corners of radius `r`.
pub fn round_rect(x: f32, y: f32, w: f32, h: f32, r: f32) -> Path {
    let r = r.min(w / 2.0).min(h / 2.0);
    if r <= 0.0 {
        return rect(x, y, w, h);
    }
    let arc = |to: Pt| Cmd::Arc {
        r,
        large: false,
        sweep: true,
        to,
    };
    vec![
        Cmd::Move([x + r, y]),
        Cmd::Line([x + w - r, y]),
        arc([x + w, y + r]),
        Cmd::Line([x + w, y + h - r]),
        arc([x + w - r, y + h]),
        Cmd::Line([x + r, y + h]),
        arc([x, y + h - r]),
        Cmd::Line([x, y + r]),
        arc([x + r, y]),
        Cmd::Close,
    ]
}

pub fn circle(c: Pt, r: f32) -> Path {
    let half = |to: Pt| Cmd::Arc {
        r,
        large: true,
        sweep: false,
        to,
    };
    vec![
        Cmd::Move([c[0] - r, c[1]]),
        half([c[0] + r, c[1]]),
        half([c[0] - r, c[1]]),
        Cmd::Close,
    ]
}

/// A gradient stop: offset 0–1, sRGB and opacity.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Stop {
    pub at: f32,
    pub rgb: [u8; 3],
    pub alpha: f32,
}

/// What fills a shape. Gradients are in the shape's own units (SVG's `userSpaceOnUse`).
#[derive(Clone, Debug, PartialEq)]
pub enum Paint {
    Solid([u8; 3], f32),
    Linear {
        from: Pt,
        to: Pt,
        stops: Vec<Stop>,
    },
    Radial {
        centre: Pt,
        r: f32,
        stops: Vec<Stop>,
    },
}

/// `0xRRGGBB` as bytes.
pub const fn rgb(hex: u32) -> [u8; 3] {
    [(hex >> 16) as u8, (hex >> 8) as u8, hex as u8]
}

pub fn solid(hex: u32, alpha: f32) -> Paint {
    Paint::Solid(rgb(hex), alpha)
}

pub fn stop(at: f32, hex: u32, alpha: f32) -> Stop {
    Stop {
        at,
        rgb: rgb(hex),
        alpha,
    }
}

pub fn linear(from: Pt, to: Pt, stops: &[Stop]) -> Paint {
    Paint::Linear {
        from,
        to,
        stops: stops.to_vec(),
    }
}

impl Paint {
    /// The straight-alpha colour at `p`.
    pub fn at(&self, p: Pt) -> [u8; 4] {
        let (stops, t) = match self {
            Paint::Solid(c, a) => return [c[0], c[1], c[2], unit(*a)],
            Paint::Linear { from, to, stops } => {
                let d = [to[0] - from[0], to[1] - from[1]];
                let len2 = d[0] * d[0] + d[1] * d[1];
                let t = if len2 > 0.0 {
                    ((p[0] - from[0]) * d[0] + (p[1] - from[1]) * d[1]) / len2
                } else {
                    0.0
                };
                (stops, t)
            }
            Paint::Radial { centre, r, stops } => {
                (stops, (p[0] - centre[0]).hypot(p[1] - centre[1]) / r)
            }
        };
        let t = t.clamp(0.0, 1.0);
        let last = stops.len() - 1;
        let i = stops.iter().position(|s| s.at >= t).unwrap_or(last);
        if i == 0 || stops[i].at <= stops[i - 1].at {
            let s = stops[i.min(last)];
            return [s.rgb[0], s.rgb[1], s.rgb[2], unit(s.alpha)];
        }
        let (a, b) = (stops[i - 1], stops[i]);
        let k = (t - a.at) / (b.at - a.at);
        let c: [u8; 3] = std::array::from_fn(|j| {
            (f32::from(a.rgb[j]) + (f32::from(b.rgb[j]) - f32::from(a.rgb[j])) * k).round() as u8
        });
        [c[0], c[1], c[2], unit(a.alpha + (b.alpha - a.alpha) * k)]
    }
}

fn unit(a: f32) -> u8 {
    (a.clamp(0.0, 1.0) * 255.0).round() as u8
}

#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Join {
    Miter,
    Round,
}

#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Cap {
    Butt,
    Round,
}

#[derive(Clone, Debug, PartialEq)]
pub enum Shape {
    /// Nonzero winding, as SVG fills by default.
    Fill(Path),
    Stroke {
        path: Path,
        width: f32,
        join: Join,
        cap: Cap,
    },
}

/// `p` ↦ `p·k + at`: how an item's own units sit in the scene.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Place {
    pub k: f32,
    pub at: Pt,
}

impl Place {
    pub const ID: Place = Place {
        k: 1.0,
        at: [0.0, 0.0],
    };

    /// `self` applied first, then `outer`.
    pub fn then(self, outer: Place) -> Place {
        Place {
            k: self.k * outer.k,
            at: [
                self.at[0] * outer.k + outer.at[0],
                self.at[1] * outer.k + outer.at[1],
            ],
        }
    }
}

#[derive(Clone, Debug, PartialEq)]
pub struct Item {
    pub shape: Shape,
    pub paint: Paint,
    /// Drawn only inside this path, in the item's units.
    pub clip: Option<Path>,
    pub place: Place,
    /// A Gaussian blur's standard deviation, in the item's units; 0 for none.
    pub blur: f32,
    /// Items in a named group are wrapped in `<g id="{scene}-{group}">`.
    pub group: Option<&'static str>,
    /// Edges on whole pixels: SVG's `shape-rendering="crispEdges"`.
    pub crisp: bool,
}

impl Item {
    pub fn fill(path: Path, paint: Paint) -> Item {
        Item {
            shape: Shape::Fill(path),
            paint,
            clip: None,
            place: Place::ID,
            blur: 0.0,
            group: None,
            crisp: false,
        }
    }

    /// A stroke with SVG's defaults: mitred joins and butt caps.
    pub fn stroke(path: Path, width: f32, paint: Paint) -> Item {
        Item {
            shape: Shape::Stroke {
                path,
                width,
                join: Join::Miter,
                cap: Cap::Butt,
            },
            ..Item::fill(Vec::new(), paint)
        }
    }

    /// Round joins and caps.
    pub fn round(mut self) -> Item {
        if let Shape::Stroke { join, cap, .. } = &mut self.shape {
            *join = Join::Round;
            *cap = Cap::Round;
        }
        self
    }

    pub fn round_join(mut self) -> Item {
        if let Shape::Stroke { join, .. } = &mut self.shape {
            *join = Join::Round;
        }
        self
    }

    pub fn round_cap(mut self) -> Item {
        if let Shape::Stroke { cap, .. } = &mut self.shape {
            *cap = Cap::Round;
        }
        self
    }

    pub fn clip(mut self, path: Path) -> Item {
        self.clip = Some(path);
        self
    }

    pub fn blur(mut self, sigma: f32) -> Item {
        self.blur = sigma;
        self
    }

    pub fn crisp(mut self) -> Item {
        self.crisp = true;
        self
    }
}

/// Places every item, after whatever placement it already has.
pub fn place(items: Vec<Item>, at: Place) -> Vec<Item> {
    items
        .into_iter()
        .map(|mut it| {
            it.place = it.place.then(at);
            it
        })
        .collect()
}

pub fn group(items: Vec<Item>, name: &'static str) -> Vec<Item> {
    items
        .into_iter()
        .map(|mut it| {
            it.group = Some(name);
            it
        })
        .collect()
}

/// A drawing `width`×`height` user units, painted in order.
#[derive(Clone, Debug, PartialEq)]
pub struct Scene {
    /// Prefixes every id in the SVG, so several can share a page.
    pub id: &'static str,
    pub width: f32,
    pub height: f32,
    pub items: Vec<Item>,
}

/// A number with at most three decimals and no trailing zeros.
pub fn num(x: f32) -> String {
    let s = format!("{x:.3}");
    let s = s.trim_end_matches('0').trim_end_matches('.');
    if s == "-0" { "0".into() } else { s.into() }
}

fn pt(p: Pt) -> String {
    format!("{} {}", num(p[0]), num(p[1]))
}

/// SVG path data.
pub fn path_d(path: &Path) -> String {
    let mut d = String::new();
    for c in path {
        match *c {
            Cmd::Move(p) => d += &format!("M{}", pt(p)),
            Cmd::Line(p) => d += &format!("L{}", pt(p)),
            Cmd::Cubic(a, b, p) => d += &format!("C{} {} {}", pt(a), pt(b), pt(p)),
            Cmd::Arc {
                r,
                large,
                sweep,
                to,
            } => {
                d += &format!(
                    "A{} {} 0 {} {} {}",
                    num(r),
                    num(r),
                    u8::from(large),
                    u8::from(sweep),
                    pt(to)
                )
            }
            Cmd::Close => d.push('Z'),
        }
    }
    d
}

fn hex(c: [u8; 3]) -> String {
    format!("#{:02x}{:02x}{:02x}", c[0], c[1], c[2])
}

/// `<defs>` entries, each written once whatever number of items use it.
struct Defs {
    prefix: String,
    entries: Vec<String>,
}

impl Defs {
    /// The id of the entry `make(id)` writes, adding it if it is new.
    fn add(&mut self, kind: &str, make: impl Fn(&str) -> String) -> String {
        let key = make("\u{0}");
        let n = self
            .entries
            .iter()
            .position(|e| *e == key)
            .unwrap_or_else(|| {
                self.entries.push(key);
                self.entries.len() - 1
            });
        format!("{}-{kind}{}", self.prefix, n + 1)
    }

    fn write(&self) -> String {
        let mut out = String::new();
        for (n, e) in self.entries.iter().enumerate() {
            let (kind, body) = e.split_once('\u{1}').unwrap_or(("", e));
            out += &body.replace('\u{0}', &format!("{}-{kind}{}", self.prefix, n + 1));
            out.push('\n');
        }
        out
    }
}

fn stops(stops: &[Stop]) -> String {
    stops
        .iter()
        .map(|s| {
            let alpha = if s.alpha < 1.0 {
                format!(" stop-opacity=\"{}\"", num(s.alpha))
            } else {
                String::new()
            };
            format!(
                "<stop offset=\"{}\" stop-color=\"{}\"{alpha}/>",
                num(s.at),
                hex(s.rgb)
            )
        })
        .collect()
}

/// `fill="…"` or `stroke="…"` with its opacity, defining a gradient when it needs one.
fn paint_attr(defs: &mut Defs, attr: &str, paint: &Paint) -> String {
    match paint {
        Paint::Solid(c, a) if *a < 1.0 => {
            format!("{attr}=\"{}\" {attr}-opacity=\"{}\"", hex(*c), num(*a))
        }
        Paint::Solid(c, _) => format!("{attr}=\"{}\"", hex(*c)),
        Paint::Linear { from, to, stops: s } => {
            let id = defs.add("g", |id| {
                format!(
                    "g\u{1}<linearGradient id=\"{id}\" gradientUnits=\"userSpaceOnUse\" x1=\"{}\" y1=\"{}\" x2=\"{}\" y2=\"{}\">{}</linearGradient>",
                    num(from[0]),
                    num(from[1]),
                    num(to[0]),
                    num(to[1]),
                    stops(s)
                )
            });
            format!("{attr}=\"url(#{id})\"")
        }
        Paint::Radial {
            centre,
            r,
            stops: s,
        } => {
            let id = defs.add("g", |id| {
                format!(
                    "g\u{1}<radialGradient id=\"{id}\" gradientUnits=\"userSpaceOnUse\" cx=\"{}\" cy=\"{}\" r=\"{}\">{}</radialGradient>",
                    num(centre[0]),
                    num(centre[1]),
                    num(*r),
                    stops(s)
                )
            });
            format!("{attr}=\"url(#{id})\"")
        }
    }
}

impl Scene {
    pub fn svg(&self) -> String {
        let mut defs = Defs {
            prefix: self.id.to_string(),
            entries: Vec::new(),
        };
        let mut body = String::new();
        let mut open: Option<(Place, Option<&str>)> = None;
        for it in &self.items {
            let key = (it.place, it.group);
            if open != Some(key) {
                if open.is_some_and(|(p, g)| p != Place::ID || g.is_some()) {
                    body += "</g>\n";
                }
                if it.place != Place::ID || it.group.is_some() {
                    let mut g = String::from("<g");
                    if let Some(name) = it.group {
                        g += &format!(" id=\"{}-{name}\"", self.id);
                    }
                    if it.place != Place::ID {
                        g += &format!(" transform=\"translate({})", pt(it.place.at));
                        if it.place.k != 1.0 {
                            g += &format!(" scale({})", num(it.place.k));
                        }
                        g.push('"');
                    }
                    body += &g;
                    body += ">\n";
                }
                open = Some(key);
            }
            let mut el = String::from("<path d=\"");
            match &it.shape {
                Shape::Fill(path) => {
                    el += &path_d(path);
                    el += "\" ";
                    el += &paint_attr(&mut defs, "fill", &it.paint);
                }
                Shape::Stroke {
                    path,
                    width,
                    join,
                    cap,
                } => {
                    el += &path_d(path);
                    el += "\" fill=\"none\" ";
                    el += &paint_attr(&mut defs, "stroke", &it.paint);
                    el += &format!(" stroke-width=\"{}\"", num(*width));
                    if *join == Join::Round {
                        el += " stroke-linejoin=\"round\"";
                    }
                    if *cap == Cap::Round {
                        el += " stroke-linecap=\"round\"";
                    }
                }
            }
            if let Some(clip) = &it.clip {
                let id = defs.add("c", |id| {
                    format!(
                        "c\u{1}<clipPath id=\"{id}\"><path d=\"{}\"/></clipPath>",
                        path_d(clip)
                    )
                });
                el += &format!(" clip-path=\"url(#{id})\"");
            }
            if it.blur > 0.0 {
                let id = defs.add("f", |id| {
                    format!(
                        "f\u{1}<filter id=\"{id}\" x=\"-20%\" y=\"-20%\" width=\"140%\" height=\"140%\" color-interpolation-filters=\"sRGB\"><feGaussianBlur stdDeviation=\"{}\"/></filter>",
                        num(it.blur)
                    )
                });
                el += &format!(" filter=\"url(#{id})\"");
            }
            if it.crisp {
                el += " shape-rendering=\"crispEdges\"";
            }
            el += "/>\n";
            body += &el;
        }
        if open.is_some_and(|(p, g)| p != Place::ID || g.is_some()) {
            body += "</g>\n";
        }
        let defs = if defs.entries.is_empty() {
            String::new()
        } else {
            format!("<defs>\n{}</defs>\n", defs.write())
        };
        let (w, h) = (num(self.width), num(self.height));
        format!(
            "<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"{w}\" height=\"{h}\" viewBox=\"0 0 {w} {h}\">\n<title>Spectra</title>\n{defs}{body}</svg>\n"
        )
    }
}
