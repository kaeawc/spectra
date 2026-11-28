//! A PNG writer with no dependencies: per-row adaptive filters, then DEFLATE with LZ77 matching
//! and a dynamic Huffman code per block. Assets are written once and downloaded many times, so
//! it searches harder than it needs to be fast.

/// `rgba` (straight alpha, `w`×`h` rows) as an 8-bit RGBA PNG.
pub fn encode(w: u32, h: u32, rgba: &[u8]) -> Vec<u8> {
    assert_eq!(rgba.len(), (w * h * 4) as usize, "{w}x{h} RGBA");
    let mut out = b"\x89PNG\r\n\x1a\n".to_vec();
    let mut ihdr = Vec::new();
    ihdr.extend_from_slice(&w.to_be_bytes());
    ihdr.extend_from_slice(&h.to_be_bytes());
    ihdr.extend_from_slice(&[8, 6, 0, 0, 0]);
    chunk(&mut out, b"IHDR", &ihdr);
    chunk(
        &mut out,
        b"IDAT",
        &zlib(&filter(w as usize, h as usize, rgba)),
    );
    chunk(&mut out, b"IEND", &[]);
    out
}

/// The width and height in a PNG's header.
pub fn size(png: &[u8]) -> Option<(u32, u32)> {
    if png.len() < 24 || &png[..8] != b"\x89PNG\r\n\x1a\n" || &png[12..16] != b"IHDR" {
        return None;
    }
    let be = |i: usize| u32::from_be_bytes([png[i], png[i + 1], png[i + 2], png[i + 3]]);
    Some((be(16), be(20)))
}

fn chunk(out: &mut Vec<u8>, ty: &[u8; 4], data: &[u8]) {
    out.extend_from_slice(&(data.len() as u32).to_be_bytes());
    let start = out.len();
    out.extend_from_slice(ty);
    out.extend_from_slice(data);
    let crc = crc32(&out[start..]);
    out.extend_from_slice(&crc.to_be_bytes());
}

fn crc32(data: &[u8]) -> u32 {
    let mut table = [0u32; 256];
    for (n, t) in table.iter_mut().enumerate() {
        let mut c = n as u32;
        for _ in 0..8 {
            c = if c & 1 == 1 {
                0xedb8_8320 ^ (c >> 1)
            } else {
                c >> 1
            };
        }
        *t = c;
    }
    !data.iter().fold(!0u32, |c, &b| {
        table[((c ^ u32::from(b)) & 0xff) as usize] ^ (c >> 8)
    })
}

/// Each row prefixed by the filter (none, sub, up, average, Paeth) whose output is smallest.
fn filter(w: usize, h: usize, rgba: &[u8]) -> Vec<u8> {
    let stride = w * 4;
    let mut out = Vec::with_capacity(h * (stride + 1));
    let zero = vec![0u8; stride];
    let mut cand = vec![0u8; stride];
    let mut best = vec![0u8; stride];
    for y in 0..h {
        let row = &rgba[y * stride..(y + 1) * stride];
        let up = if y == 0 {
            &zero[..]
        } else {
            &rgba[(y - 1) * stride..y * stride]
        };
        let mut best_ty = 0u8;
        let mut best_cost = u64::MAX;
        for ty in 0..5u8 {
            for i in 0..stride {
                let a = if i >= 4 { row[i - 4] } else { 0 };
                let b = up[i];
                let c = if i >= 4 { up[i - 4] } else { 0 };
                let pred = match ty {
                    0 => 0,
                    1 => a,
                    2 => b,
                    3 => ((u16::from(a) + u16::from(b)) / 2) as u8,
                    _ => paeth(a, b, c),
                };
                cand[i] = row[i].wrapping_sub(pred);
            }
            let cost = cand
                .iter()
                .map(|&v| u64::from((v as i8).unsigned_abs()))
                .sum();
            if cost < best_cost {
                best_cost = cost;
                best_ty = ty;
                best.copy_from_slice(&cand);
            }
        }
        out.push(best_ty);
        out.extend_from_slice(&best);
    }
    out
}

fn paeth(a: u8, b: u8, c: u8) -> u8 {
    let p = i16::from(a) + i16::from(b) - i16::from(c);
    let (pa, pb, pc) = (
        (p - i16::from(a)).abs(),
        (p - i16::from(b)).abs(),
        (p - i16::from(c)).abs(),
    );
    if pa <= pb && pa <= pc {
        a
    } else if pb <= pc {
        b
    } else {
        c
    }
}

fn zlib(data: &[u8]) -> Vec<u8> {
    let mut out = vec![0x78, 0xda];
    out.extend_from_slice(&deflate(data));
    let (mut a, mut b) = (1u32, 0u32);
    for chunk in data.chunks(5552) {
        for &x in chunk {
            a += u32::from(x);
            b += a;
        }
        a %= 65521;
        b %= 65521;
    }
    out.extend_from_slice(&((b << 16) | a).to_be_bytes());
    out
}

const LEN_BASE: [u16; 29] = [
    3, 4, 5, 6, 7, 8, 9, 10, 11, 13, 15, 17, 19, 23, 27, 31, 35, 43, 51, 59, 67, 83, 99, 115, 131,
    163, 195, 227, 258,
];
const LEN_EXTRA: [u8; 29] = [
    0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3, 4, 4, 4, 4, 5, 5, 5, 5, 0,
];
const DIST_BASE: [u16; 30] = [
    1, 2, 3, 4, 5, 7, 9, 13, 17, 25, 33, 49, 65, 97, 129, 193, 257, 385, 513, 769, 1025, 1537,
    2049, 3073, 4097, 6145, 8193, 12289, 16385, 24577,
];
const DIST_EXTRA: [u8; 30] = [
    0, 0, 0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 6, 7, 7, 8, 8, 9, 9, 10, 10, 11, 11, 12, 12, 13,
    13,
];
/// The order code-length code lengths are sent in.
const CL_ORDER: [usize; 19] = [
    16, 17, 18, 0, 8, 7, 9, 6, 10, 5, 11, 4, 12, 3, 13, 2, 14, 1, 15,
];

#[derive(Clone, Copy)]
enum Token {
    Lit(u8),
    Match(u16, u16),
}

const WINDOW: usize = 32 * 1024;
const MAX_CHAIN: usize = 256;
const MAX_MATCH: usize = 258;

/// LZ77 with hash chains and one step of lazy matching.
fn tokens(data: &[u8]) -> Vec<Token> {
    let hash = |i: usize| {
        ((u32::from(data[i]) << 10) ^ (u32::from(data[i + 1]) << 5) ^ u32::from(data[i + 2]))
            as usize
            & 0x7fff
    };
    let mut head = vec![usize::MAX; 1 << 15];
    let mut prev = vec![usize::MAX; data.len()];
    let insert = |i: usize, head: &mut Vec<usize>, prev: &mut Vec<usize>| {
        if i + 2 < data.len() {
            let h = hash(i);
            prev[i] = head[h];
            head[h] = i;
        }
    };
    let longest = |i: usize, head: &Vec<usize>, prev: &Vec<usize>| -> (usize, usize) {
        if i + 2 >= data.len() {
            return (0, 0);
        }
        let max = MAX_MATCH.min(data.len() - i);
        let (mut best, mut dist) = (0, 0);
        let mut j = head[hash(i)];
        let mut chain = MAX_CHAIN;
        while j != usize::MAX && i - j <= WINDOW && chain > 0 {
            if data[j + best] == data[i + best] {
                let mut n = 0;
                while n < max && data[j + n] == data[i + n] {
                    n += 1;
                }
                if n > best {
                    (best, dist) = (n, i - j);
                    if n == max {
                        break;
                    }
                }
            }
            j = prev[j];
            chain -= 1;
        }
        if best >= 3 { (best, dist) } else { (0, 0) }
    };
    let mut out = Vec::with_capacity(data.len() / 4);
    let mut i = 0;
    while i < data.len() {
        let (len, dist) = longest(i, &head, &prev);
        insert(i, &mut head, &mut prev);
        if len == 0 {
            out.push(Token::Lit(data[i]));
            i += 1;
            continue;
        }
        // A longer match one byte on is worth a literal now.
        let (next, _) = if len < 32 {
            longest(i + 1, &head, &prev)
        } else {
            (0, 0)
        };
        if next > len {
            out.push(Token::Lit(data[i]));
            i += 1;
            continue;
        }
        out.push(Token::Match(len as u16, dist as u16));
        for k in 1..len {
            insert(i + k, &mut head, &mut prev);
        }
        i += len;
    }
    out
}

fn len_code(len: u16) -> usize {
    LEN_BASE.iter().rposition(|&b| b <= len).unwrap_or(0)
}

fn dist_code(dist: u16) -> usize {
    DIST_BASE.iter().rposition(|&b| b <= dist).unwrap_or(0)
}

struct Bits {
    out: Vec<u8>,
    acc: u64,
    n: u32,
}

impl Bits {
    fn put(&mut self, v: u32, n: u32) {
        self.acc |= u64::from(v) << self.n;
        self.n += n;
        while self.n >= 8 {
            self.out.push(self.acc as u8);
            self.acc >>= 8;
            self.n -= 8;
        }
    }

    /// A Huffman code, which DEFLATE packs most significant bit first.
    fn code(&mut self, code: u32, len: u8) {
        let rev = code.reverse_bits() >> (32 - u32::from(len.max(1)));
        self.put(rev, u32::from(len));
    }

    fn finish(mut self) -> Vec<u8> {
        if self.n > 0 {
            self.out.push(self.acc as u8);
        }
        self.out
    }
}

/// Code lengths (at most `limit` bits) for symbols with these frequencies: Huffman's, with
/// frequencies flattened until the deepest code fits.
fn lengths(freq: &[u32], limit: u8) -> Vec<u8> {
    let mut f: Vec<u64> = freq.iter().map(|&x| u64::from(x)).collect();
    loop {
        let used: Vec<usize> = (0..f.len()).filter(|&i| f[i] > 0).collect();
        let mut out = vec![0u8; f.len()];
        if used.len() == 1 {
            out[used[0]] = 1;
            return out;
        }
        if used.is_empty() {
            return out;
        }
        // Nodes: leaves first, then merges; parent links give depths.
        let mut parent: Vec<usize> = vec![usize::MAX; used.len() * 2];
        let mut heap: std::collections::BinaryHeap<std::cmp::Reverse<(u64, usize)>> = used
            .iter()
            .enumerate()
            .map(|(n, &i)| std::cmp::Reverse((f[i], n)))
            .collect();
        let mut next = used.len();
        while heap.len() > 1 {
            let std::cmp::Reverse((wa, a)) = heap.pop().expect("two nodes");
            let std::cmp::Reverse((wb, b)) = heap.pop().expect("two nodes");
            parent[a] = next;
            parent[b] = next;
            heap.push(std::cmp::Reverse((wa + wb, next)));
            next += 1;
        }
        let mut deepest = 0;
        for (n, &i) in used.iter().enumerate() {
            let (mut d, mut at) = (0u8, n);
            while parent[at] != usize::MAX {
                at = parent[at];
                d += 1;
            }
            out[i] = d;
            deepest = deepest.max(d);
        }
        if deepest <= limit {
            return out;
        }
        f.iter_mut()
            .filter(|x| **x > 0)
            .for_each(|x| *x = (*x).div_ceil(2));
    }
}

/// Canonical codes for these lengths.
fn codes(lens: &[u8]) -> Vec<u32> {
    let max = lens.iter().copied().max().unwrap_or(0) as usize;
    let mut count = vec![0u32; max + 2];
    lens.iter()
        .filter(|&&l| l > 0)
        .for_each(|&l| count[l as usize] += 1);
    let mut next = vec![0u32; max + 2];
    let mut code = 0;
    for bits in 1..=max {
        code = (code + count[bits - 1]) << 1;
        next[bits] = code;
    }
    lens.iter()
        .map(|&l| {
            if l == 0 {
                return 0;
            }
            let c = next[l as usize];
            next[l as usize] += 1;
            c
        })
        .collect()
}

/// Run-length coded code lengths: (symbol 0–18, extra bits value).
fn rle(lens: &[u8]) -> Vec<(u8, u8)> {
    let mut out = Vec::new();
    let mut i = 0;
    while i < lens.len() {
        let l = lens[i];
        let mut run = 1;
        while i + run < lens.len() && lens[i + run] == l {
            run += 1;
        }
        i += run;
        if l == 0 {
            while run >= 11 {
                let n = run.min(138);
                out.push((18, (n - 11) as u8));
                run -= n;
            }
            if run >= 3 {
                out.push((17, (run - 3) as u8));
                run = 0;
            }
        } else {
            out.push((l, 0));
            run -= 1;
            while run >= 3 {
                let n = run.min(6);
                out.push((16, (n - 3) as u8));
                run -= n;
            }
        }
        for _ in 0..run {
            out.push((l, 0));
        }
    }
    out
}

fn deflate(data: &[u8]) -> Vec<u8> {
    let toks = tokens(data);
    let mut bits = Bits {
        out: Vec::new(),
        acc: 0,
        n: 0,
    };
    let blocks: Vec<&[Token]> = toks.chunks(1 << 16).collect();
    if blocks.is_empty() {
        // An empty stored block.
        bits.put(1, 3);
        bits.put(0, 5);
        bits.put(0, 16);
        bits.put(0xffff, 16);
        return bits.finish();
    }
    for (b, block) in blocks.iter().enumerate() {
        let mut lit_freq = vec![0u32; 286];
        let mut dist_freq = vec![0u32; 30];
        for t in block.iter() {
            match *t {
                Token::Lit(v) => lit_freq[v as usize] += 1,
                Token::Match(l, d) => {
                    lit_freq[257 + len_code(l)] += 1;
                    dist_freq[dist_code(d)] += 1;
                }
            }
        }
        lit_freq[256] = 1;
        if dist_freq.iter().all(|&f| f == 0) {
            dist_freq[0] = 1;
        }
        let lit_len = lengths(&lit_freq, 15);
        let dist_len = lengths(&dist_freq, 15);
        let lit_code = codes(&lit_len);
        let dist_code_ = codes(&dist_len);
        let hlit = 257.max(lit_len.iter().rposition(|&l| l > 0).map_or(0, |p| p + 1));
        let hdist = 1.max(dist_len.iter().rposition(|&l| l > 0).map_or(0, |p| p + 1));
        let mut all = lit_len[..hlit].to_vec();
        all.extend_from_slice(&dist_len[..hdist]);
        let runs = rle(&all);
        let mut cl_freq = vec![0u32; 19];
        runs.iter().for_each(|&(s, _)| cl_freq[s as usize] += 1);
        let cl_len = lengths(&cl_freq, 7);
        let cl_code = codes(&cl_len);
        let hclen = 4.max(
            CL_ORDER
                .iter()
                .rposition(|&s| cl_len[s] > 0)
                .map_or(0, |p| p + 1),
        );

        bits.put(u32::from(b + 1 == blocks.len()), 1);
        bits.put(2, 2);
        bits.put((hlit - 257) as u32, 5);
        bits.put((hdist - 1) as u32, 5);
        bits.put((hclen - 4) as u32, 4);
        for &s in &CL_ORDER[..hclen] {
            bits.put(u32::from(cl_len[s]), 3);
        }
        for &(s, extra) in &runs {
            bits.code(cl_code[s as usize], cl_len[s as usize]);
            match s {
                16 => bits.put(u32::from(extra), 2),
                17 => bits.put(u32::from(extra), 3),
                18 => bits.put(u32::from(extra), 7),
                _ => {}
            }
        }
        for t in block.iter() {
            match *t {
                Token::Lit(v) => bits.code(lit_code[v as usize], lit_len[v as usize]),
                Token::Match(l, d) => {
                    let lc = len_code(l);
                    bits.code(lit_code[257 + lc], lit_len[257 + lc]);
                    bits.put(u32::from(l - LEN_BASE[lc]), u32::from(LEN_EXTRA[lc]));
                    let dc = dist_code(d);
                    bits.code(dist_code_[dc], dist_len[dc]);
                    bits.put(u32::from(d - DIST_BASE[dc]), u32::from(DIST_EXTRA[dc]));
                }
            }
        }
        bits.code(lit_code[256], lit_len[256]);
    }
    bits.finish()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_crc_matches_the_standard_check_value() {
        assert_eq!(crc32(b"123456789"), 0xcbf4_3926);
    }

    #[test]
    fn lengths_form_a_complete_prefix_code_within_the_limit() {
        let freq: Vec<u32> = (0..40).map(|i| 1 << (i % 20)).collect();
        let lens = lengths(&freq, 7);
        assert!(lens.iter().all(|&l| (1..=7).contains(&l)));
        let kraft: f64 = lens.iter().map(|&l| 0.5f64.powi(i32::from(l))).sum();
        assert!((kraft - 1.0).abs() < 1e-9, "{kraft}");
    }

    #[test]
    fn the_header_carries_the_size() {
        let png = encode(3, 2, &[7; 24]);
        assert_eq!(size(&png), Some((3, 2)));
        assert_eq!(&png[png.len() - 8..png.len() - 4], b"IEND");
    }
}
