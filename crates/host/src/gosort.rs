//! Go's `sort.Slice` (pattern-defeating quicksort, `sort/zsortfunc.go`), ported step for step.
//!
//! `sort.Slice` is not stable: with a comparator that is not a total order (two processes with
//! the same RSS), the order Go prints depends on its exact algorithm. Porting it keeps those ties
//! in Go's order. Comparators that are total orders can use Rust's own sorts instead.

/// Go's `sort.Slice(v, less)`.
pub fn slice<T>(v: &mut [T], mut less: impl FnMut(&T, &T) -> bool) {
    let n = v.len();
    let limit = bits_len(n);
    let mut data = Data { v, less: &mut less };
    pdqsort(&mut data, 0, n, limit);
}

/// `bits.Len(uint(n))`.
fn bits_len(n: usize) -> usize {
    (usize::BITS - n.leading_zeros()) as usize
}

struct Data<'a, T, F> {
    v: &'a mut [T],
    less: &'a mut F,
}

impl<T, F: FnMut(&T, &T) -> bool> Data<'_, T, F> {
    fn less(&mut self, i: usize, j: usize) -> bool {
        (self.less)(&self.v[i], &self.v[j])
    }

    fn swap(&mut self, i: usize, j: usize) {
        self.v.swap(i, j);
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Hint {
    Unknown,
    Increasing,
    Decreasing,
}

fn insertion_sort<T, F: FnMut(&T, &T) -> bool>(data: &mut Data<'_, T, F>, a: usize, b: usize) {
    for i in a + 1..b {
        let mut j = i;
        while j > a && data.less(j, j - 1) {
            data.swap(j, j - 1);
            j -= 1;
        }
    }
}

fn sift_down<T, F: FnMut(&T, &T) -> bool>(data: &mut Data<'_, T, F>, lo: usize, hi: usize, first: usize) {
    let mut root = lo;
    loop {
        let mut child = 2 * root + 1;
        if child >= hi {
            return;
        }
        if child + 1 < hi && data.less(first + child, first + child + 1) {
            child += 1;
        }
        if !data.less(first + root, first + child) {
            return;
        }
        data.swap(first + root, first + child);
        root = child;
    }
}

fn heap_sort<T, F: FnMut(&T, &T) -> bool>(data: &mut Data<'_, T, F>, a: usize, b: usize) {
    let first = a;
    let hi = b - a;
    let mut i = (hi as isize - 1) / 2;
    while i >= 0 {
        sift_down(data, i as usize, hi, first);
        i -= 1;
    }
    let mut i = hi as isize - 1;
    while i >= 0 {
        data.swap(first, first + i as usize);
        sift_down(data, 0, i as usize, first);
        i -= 1;
    }
}

fn pdqsort<T, F: FnMut(&T, &T) -> bool>(data: &mut Data<'_, T, F>, mut a: usize, mut b: usize, mut limit: usize) {
    const MAX_INSERTION: usize = 12;
    let mut was_balanced = true;
    let mut was_partitioned = true;
    loop {
        let length = b - a;
        if length <= MAX_INSERTION {
            insertion_sort(data, a, b);
            return;
        }
        if limit == 0 {
            heap_sort(data, a, b);
            return;
        }
        if !was_balanced {
            break_patterns(data, a, b);
            limit -= 1;
        }
        let (mut pivot, mut hint) = choose_pivot(data, a, b);
        if hint == Hint::Decreasing {
            reverse_range(data, a, b);
            pivot = (b - 1) - (pivot - a);
            hint = Hint::Increasing;
        }
        if was_balanced && was_partitioned && hint == Hint::Increasing && partial_insertion_sort(data, a, b) {
            return;
        }
        if a > 0 && !data.less(a - 1, pivot) {
            a = partition_equal(data, a, b, pivot);
            continue;
        }
        let (mid, already) = partition(data, a, b, pivot);
        was_partitioned = already;
        let (left, right) = (mid - a, b - mid);
        let threshold = length / 8;
        if left < right {
            was_balanced = left >= threshold;
            pdqsort(data, a, mid, limit);
            a = mid + 1;
        } else {
            was_balanced = right >= threshold;
            pdqsort(data, mid + 1, b, limit);
            b = mid;
        }
    }
}

fn partition<T, F: FnMut(&T, &T) -> bool>(data: &mut Data<'_, T, F>, a: usize, b: usize, pivot: usize) -> (usize, bool) {
    data.swap(a, pivot);
    // i and j are inclusive; j may go one below a+1, so work in isize.
    let (mut i, mut j) = (a as isize + 1, b as isize - 1);
    while i <= j && data.less(i as usize, a) {
        i += 1;
    }
    while i <= j && !data.less(j as usize, a) {
        j -= 1;
    }
    if i > j {
        data.swap(j as usize, a);
        return (j as usize, true);
    }
    data.swap(i as usize, j as usize);
    i += 1;
    j -= 1;
    loop {
        while i <= j && data.less(i as usize, a) {
            i += 1;
        }
        while i <= j && !data.less(j as usize, a) {
            j -= 1;
        }
        if i > j {
            break;
        }
        data.swap(i as usize, j as usize);
        i += 1;
        j -= 1;
    }
    data.swap(j as usize, a);
    (j as usize, false)
}

fn partition_equal<T, F: FnMut(&T, &T) -> bool>(data: &mut Data<'_, T, F>, a: usize, b: usize, pivot: usize) -> usize {
    data.swap(a, pivot);
    let (mut i, mut j) = (a as isize + 1, b as isize - 1);
    loop {
        while i <= j && !data.less(a, i as usize) {
            i += 1;
        }
        while i <= j && data.less(a, j as usize) {
            j -= 1;
        }
        if i > j {
            break;
        }
        data.swap(i as usize, j as usize);
        i += 1;
        j -= 1;
    }
    i as usize
}

fn partial_insertion_sort<T, F: FnMut(&T, &T) -> bool>(data: &mut Data<'_, T, F>, a: usize, b: usize) -> bool {
    const MAX_STEPS: usize = 5;
    const SHORTEST_SHIFTING: usize = 50;
    let mut i = a + 1;
    for _ in 0..MAX_STEPS {
        while i < b && !data.less(i, i - 1) {
            i += 1;
        }
        if i == b {
            return true;
        }
        if b - a < SHORTEST_SHIFTING {
            return false;
        }
        data.swap(i, i - 1);
        if i - a >= 2 {
            let mut j = i - 1;
            while j >= 1 {
                if !data.less(j, j - 1) {
                    break;
                }
                data.swap(j, j - 1);
                j -= 1;
            }
        }
        if b - i >= 2 {
            let mut j = i + 1;
            while j < b {
                if !data.less(j, j - 1) {
                    break;
                }
                data.swap(j, j - 1);
                j += 1;
            }
        }
    }
    false
}

fn break_patterns<T, F: FnMut(&T, &T) -> bool>(data: &mut Data<'_, T, F>, a: usize, b: usize) {
    let length = b - a;
    if length >= 8 {
        let mut random = length as u64;
        let modulus = 1usize << bits_len(length);
        let start = a + (length / 4) * 2 - 1;
        for idx in start..=a + (length / 4) * 2 + 1 {
            random ^= random << 13;
            random ^= random >> 7;
            random ^= random << 17;
            let mut other = (random as usize) & (modulus - 1);
            if other >= length {
                other -= length;
            }
            data.swap(idx, a + other);
        }
    }
}

fn choose_pivot<T, F: FnMut(&T, &T) -> bool>(data: &mut Data<'_, T, F>, a: usize, b: usize) -> (usize, Hint) {
    const SHORTEST_NINTHER: usize = 50;
    const MAX_SWAPS: usize = 4 * 3;
    let l = b - a;
    let mut swaps = 0;
    let mut i = a + l / 4;
    let mut j = a + l / 4 * 2;
    let mut k = a + l / 4 * 3;
    if l >= 8 {
        if l >= SHORTEST_NINTHER {
            i = median_adjacent(data, i, &mut swaps);
            j = median_adjacent(data, j, &mut swaps);
            k = median_adjacent(data, k, &mut swaps);
        }
        j = median(data, i, j, k, &mut swaps);
    }
    match swaps {
        0 => (j, Hint::Increasing),
        MAX_SWAPS => (j, Hint::Decreasing),
        _ => (j, Hint::Unknown),
    }
}

fn order2<T, F: FnMut(&T, &T) -> bool>(data: &mut Data<'_, T, F>, a: usize, b: usize, swaps: &mut usize) -> (usize, usize) {
    if data.less(b, a) {
        *swaps += 1;
        return (b, a);
    }
    (a, b)
}

fn median<T, F: FnMut(&T, &T) -> bool>(data: &mut Data<'_, T, F>, a: usize, b: usize, c: usize, swaps: &mut usize) -> usize {
    let (a, b) = order2(data, a, b, swaps);
    let (b, c) = order2(data, b, c, swaps);
    let _ = c;
    let (_, b) = order2(data, a, b, swaps);
    b
}

fn median_adjacent<T, F: FnMut(&T, &T) -> bool>(data: &mut Data<'_, T, F>, a: usize, swaps: &mut usize) -> usize {
    median(data, a - 1, a, a + 1, swaps)
}

fn reverse_range<T, F: FnMut(&T, &T) -> bool>(data: &mut Data<'_, T, F>, a: usize, b: usize) {
    let (mut i, mut j) = (a, b - 1);
    while i < j {
        data.swap(i, j);
        i += 1;
        j -= 1;
    }
}

#[cfg(test)]
mod tests;
