//! Wall-clock time and sleeping, behind traits so tests never read the real clock or wait.

use std::sync::Mutex;
use std::time::Duration;

use jiff::Timestamp;

/// The current time. Collectors take `&dyn Clock` instead of calling `Timestamp::now()`.
pub trait Clock: Send + Sync {
    fn now(&self) -> Timestamp;
}

/// The real clock.
#[derive(Clone, Copy, Debug, Default)]
pub struct SystemClock;

impl Clock for SystemClock {
    #[allow(clippy::disallowed_methods)] // the one place the real clock is read
    fn now(&self) -> Timestamp {
        Timestamp::now()
    }
}

/// A clock that only moves when a test moves it.
#[derive(Debug)]
pub struct FakeClock {
    now: Mutex<Timestamp>,
}

impl FakeClock {
    pub fn new(start: Timestamp) -> Self {
        FakeClock { now: Mutex::new(start) }
    }

    pub fn set(&self, t: Timestamp) {
        *lock(&self.now) = t;
    }

    /// Moves the clock forward by `d` (saturating at jiff's maximum timestamp).
    pub fn advance(&self, d: Duration) {
        let mut now = lock(&self.now);
        let span = jiff::SignedDuration::try_from(d).unwrap_or(jiff::SignedDuration::MAX);
        *now = now.checked_add(span).unwrap_or(Timestamp::MAX);
    }
}

impl Clock for FakeClock {
    fn now(&self) -> Timestamp {
        *lock(&self.now)
    }
}

/// Waiting between retries or polls. Tests use [`FakeSleeper`], which returns at once.
pub trait Sleeper: Send + Sync {
    fn sleep(&self, d: Duration);
}

/// Really sleeps.
#[derive(Clone, Copy, Debug, Default)]
pub struct SystemSleeper;

impl Sleeper for SystemSleeper {
    #[allow(clippy::disallowed_methods)] // the one place a real sleep happens
    fn sleep(&self, d: Duration) {
        std::thread::sleep(d);
    }
}

/// Records each requested sleep and returns immediately.
#[derive(Debug, Default)]
pub struct FakeSleeper {
    slept: Mutex<Vec<Duration>>,
}

impl FakeSleeper {
    pub fn new() -> Self {
        FakeSleeper::default()
    }

    /// Every duration passed to [`Sleeper::sleep`], in order.
    pub fn slept(&self) -> Vec<Duration> {
        lock(&self.slept).clone()
    }
}

impl Sleeper for FakeSleeper {
    fn sleep(&self, d: Duration) {
        lock(&self.slept).push(d);
    }
}

/// Locks, recovering the data if another thread panicked while holding the lock: these mutexes
/// guard plain values that are never left half-written.
pub(crate) fn lock<T>(m: &Mutex<T>) -> std::sync::MutexGuard<'_, T> {
    m.lock().unwrap_or_else(std::sync::PoisonError::into_inner)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn fake_clock_moves_only_when_told() {
        let start = Timestamp::new(1_795_785_300, 0).expect("ts");
        let c = FakeClock::new(start);
        assert_eq!(c.now(), start);
        c.advance(Duration::from_millis(1500));
        assert_eq!(c.now(), Timestamp::new(1_795_785_301, 500_000_000).expect("ts"));
        c.set(start);
        assert_eq!(c.now(), start);
    }

    #[test]
    fn fake_sleeper_records() {
        let s = FakeSleeper::new();
        s.sleep(Duration::from_secs(1));
        s.sleep(Duration::from_millis(5));
        assert_eq!(s.slept(), vec![Duration::from_secs(1), Duration::from_millis(5)]);
    }
}
