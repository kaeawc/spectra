//! macOS calls that the Go build made through cgo (`internal/memstate`, `internal/sysinfo`,
//! `internal/process`).

use std::ffi::{CStr, CString, c_int, c_void};
use std::io;
use std::mem::{MaybeUninit, size_of};

/// Reads a sysctl by name into a byte buffer.
pub fn sysctl_raw(name: &str) -> io::Result<Vec<u8>> {
    let cname = CString::new(name).map_err(|e| io::Error::new(io::ErrorKind::InvalidInput, e))?;
    let mut len: libc::size_t = 0;
    // SAFETY: a null old-value pointer asks only for the size, written to `len`, which is a valid
    // `size_t`; `cname` is a valid NUL-terminated string that outlives the call.
    let rc = unsafe { libc::sysctlbyname(cname.as_ptr(), std::ptr::null_mut(), &mut len, std::ptr::null_mut(), 0) };
    if rc != 0 {
        return Err(io::Error::last_os_error());
    }
    let mut buf = vec![0u8; len];
    // SAFETY: `buf` has `len` writable bytes and `len` tells the kernel that size; the kernel
    // writes at most that many and stores the written length back in `len`.
    let rc = unsafe { libc::sysctlbyname(cname.as_ptr(), buf.as_mut_ptr().cast::<c_void>(), &mut len, std::ptr::null_mut(), 0) };
    if rc != 0 {
        return Err(io::Error::last_os_error());
    }
    buf.truncate(len);
    Ok(buf)
}

/// Reads a fixed-size plain-data sysctl value. `T` must be valid for any bit pattern.
fn sysctl_pod<T: Copy>(name: &str) -> io::Result<T> {
    let cname = CString::new(name).map_err(|e| io::Error::new(io::ErrorKind::InvalidInput, e))?;
    let mut value = MaybeUninit::<T>::zeroed();
    let mut len: libc::size_t = size_of::<T>();
    // SAFETY: `value` has room for `size_of::<T>()` bytes and `len` says so; `cname` is a valid
    // C string.
    let rc = unsafe { libc::sysctlbyname(cname.as_ptr(), value.as_mut_ptr().cast::<c_void>(), &mut len, std::ptr::null_mut(), 0) };
    if rc != 0 {
        return Err(io::Error::last_os_error());
    }
    if len != size_of::<T>() {
        return Err(io::Error::new(io::ErrorKind::InvalidData, format!("sysctl {name}: {len} bytes, want {}", size_of::<T>())));
    }
    // SAFETY: the kernel filled all `size_of::<T>()` bytes (checked above), and callers only use
    // integer or `#[repr(C)]` integer-only types, which are valid for any bit pattern.
    Ok(unsafe { value.assume_init() })
}

/// A 32-bit integer sysctl, such as `kern.memorystatus_level`.
pub fn sysctl_u32(name: &str) -> io::Result<u32> {
    sysctl_pod(name)
}

/// A 64-bit integer sysctl, such as `hw.memsize`.
pub fn sysctl_u64(name: &str) -> io::Result<u64> {
    sysctl_pod(name)
}

/// A string sysctl, such as `kern.osproductversion`, without its trailing NUL.
pub fn sysctl_string(name: &str) -> io::Result<String> {
    let raw = sysctl_raw(name)?;
    let end = raw.iter().position(|&b| b == 0).unwrap_or(raw.len());
    String::from_utf8(raw[..end].to_vec()).map_err(|e| io::Error::new(io::ErrorKind::InvalidData, e))
}

/// `vm.swapusage`.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct SwapUsage {
    pub total: u64,
    pub avail: u64,
    pub used: u64,
    pub page_size: u32,
    pub encrypted: bool,
}

pub fn swap_usage() -> io::Result<SwapUsage> {
    let raw: libc::xsw_usage = sysctl_pod("vm.swapusage")?;
    Ok(SwapUsage {
        total: raw.xsu_total,
        avail: raw.xsu_avail,
        used: raw.xsu_used,
        page_size: raw.xsu_pagesize,
        encrypted: raw.xsu_encrypted != 0,
    })
}

/// The kernel's page size in bytes.
pub use crate::page_size;

/// `host_statistics64(HOST_VM_INFO64)` page counts.
pub type VmStatistics64 = libc::vm_statistics64;

/// Reads the host's VM statistics. The error carries the Mach `kern_return_t`.
// libc marks `mach_host_self` deprecated in favour of the mach2 crate; it is still the same
// symbol, and one call doesn't justify another dependency.
#[allow(deprecated)]
pub fn vm_statistics64() -> Result<VmStatistics64, i32> {
    let mut stats = MaybeUninit::<libc::vm_statistics64>::zeroed();
    let mut count = libc::HOST_VM_INFO64_COUNT;
    // SAFETY: `mach_host_self` has no preconditions and returns a send right for this host;
    // `stats` has room for HOST_VM_INFO64_COUNT integers and `count` says so.
    let kr = unsafe { libc::host_statistics64(libc::mach_host_self(), libc::HOST_VM_INFO64, stats.as_mut_ptr().cast(), &mut count) };
    if kr != libc::KERN_SUCCESS {
        return Err(kr);
    }
    // SAFETY: the call succeeded and filled the struct, which holds only integers.
    Ok(unsafe { stats.assume_init() })
}

/// Energy, wakeups, CPU time and disk I/O for one process (`proc_pid_rusage`).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Rusage {
    pub user_ns: u64,
    pub system_ns: u64,
    pub pkg_idle_wakeups: u64,
    pub interrupt_wakeups: u64,
    pub disk_bytes_read: u64,
    pub disk_bytes_written: u64,
    pub billed_energy_nj: u64,
    pub serviced_energy_nj: u64,
    /// Only from the V6 flavor (macOS 14+); 0 from V4.
    pub energy_nj: u64,
    /// Only from the V6 flavor (macOS 14+); 0 from V4.
    pub p_energy_nj: u64,
    /// Which flavor answered: 6 or 4.
    pub flavor: i32,
}

/// Why `proc_pid_rusage` failed.
#[derive(Debug)]
pub enum RusageError {
    /// EPERM or EACCES: the caller doesn't own the process and lacks the entitlement.
    Permission,
    Other(io::Error),
}

const RUSAGE_INFO_V4: c_int = 4;
const RUSAGE_INFO_V6: c_int = 6;

/// `struct rusage_info_v6` from `<sys/resource.h>` (macOS 14+ SDK); libc has no binding.
#[repr(C)]
#[derive(Clone, Copy)]
struct RusageInfoV6 {
    ri_uuid: [u8; 16],
    ri_user_time: u64,
    ri_system_time: u64,
    ri_pkg_idle_wkups: u64,
    ri_interrupt_wkups: u64,
    ri_pageins: u64,
    ri_wired_size: u64,
    ri_resident_size: u64,
    ri_phys_footprint: u64,
    ri_proc_start_abstime: u64,
    ri_proc_exit_abstime: u64,
    ri_child_user_time: u64,
    ri_child_system_time: u64,
    ri_child_pkg_idle_wkups: u64,
    ri_child_interrupt_wkups: u64,
    ri_child_pageins: u64,
    ri_child_elapsed_abstime: u64,
    ri_diskio_bytesread: u64,
    ri_diskio_byteswritten: u64,
    ri_cpu_time_qos: [u64; 7],
    ri_billed_system_time: u64,
    ri_serviced_system_time: u64,
    ri_logical_writes: u64,
    ri_lifetime_max_phys_footprint: u64,
    ri_instructions: u64,
    ri_cycles: u64,
    ri_billed_energy: u64,
    ri_serviced_energy: u64,
    ri_interval_max_phys_footprint: u64,
    ri_runnable_time: u64,
    ri_flags: u64,
    ri_user_ptime: u64,
    ri_system_ptime: u64,
    ri_pinstructions: u64,
    ri_pcycles: u64,
    ri_energy_nj: u64,
    ri_penergy_nj: u64,
    ri_secure_time_in_system: u64,
    ri_secure_ptime_in_system: u64,
    ri_neural_footprint: u64,
    ri_lifetime_max_neural_footprint: u64,
    ri_interval_max_neural_footprint: u64,
    ri_reserved: [u64; 9],
}

/// Calls `proc_pid_rusage` with `flavor` into a zeroed `T`.
fn pid_rusage<T: Copy>(pid: i32, flavor: c_int) -> Result<T, io::Error> {
    let mut info = MaybeUninit::<T>::zeroed();
    // SAFETY: `info` is a zeroed buffer of the struct the flavor fills (callers pair V6 with
    // `RusageInfoV6` and V4 with `libc::rusage_info_v4`), so the kernel writes within it.
    let rc = unsafe { libc::proc_pid_rusage(pid, flavor, info.as_mut_ptr().cast()) };
    if rc != 0 {
        return Err(io::Error::last_os_error());
    }
    // SAFETY: success means the kernel filled the struct, which holds only integers and bytes.
    Ok(unsafe { info.assume_init() })
}

fn is_permission(e: &io::Error) -> bool {
    matches!(e.raw_os_error(), Some(libc::EPERM) | Some(libc::EACCES))
}

/// Reads `pid`'s rusage, trying the V6 flavor first and falling back to V4 on kernels that
/// reject it (Go's `sysinfo.ReadRusage`).
pub fn proc_pid_rusage(pid: i32) -> Result<Rusage, RusageError> {
    match pid_rusage::<RusageInfoV6>(pid, RUSAGE_INFO_V6) {
        Ok(v6) => {
            return Ok(Rusage {
                user_ns: v6.ri_user_time,
                system_ns: v6.ri_system_time,
                pkg_idle_wakeups: v6.ri_pkg_idle_wkups,
                interrupt_wakeups: v6.ri_interrupt_wkups,
                disk_bytes_read: v6.ri_diskio_bytesread,
                disk_bytes_written: v6.ri_diskio_byteswritten,
                billed_energy_nj: v6.ri_billed_energy,
                serviced_energy_nj: v6.ri_serviced_energy,
                energy_nj: v6.ri_energy_nj,
                p_energy_nj: v6.ri_penergy_nj,
                flavor: 6,
            });
        }
        Err(e) if is_permission(&e) => return Err(RusageError::Permission),
        Err(_) => {}
    }
    match pid_rusage::<libc::rusage_info_v4>(pid, RUSAGE_INFO_V4) {
        Ok(v4) => Ok(Rusage {
            user_ns: v4.ri_user_time,
            system_ns: v4.ri_system_time,
            pkg_idle_wakeups: v4.ri_pkg_idle_wkups,
            interrupt_wakeups: v4.ri_interrupt_wkups,
            disk_bytes_read: v4.ri_diskio_bytesread,
            disk_bytes_written: v4.ri_diskio_byteswritten,
            billed_energy_nj: v4.ri_billed_energy,
            serviced_energy_nj: v4.ri_serviced_energy,
            energy_nj: 0,
            p_energy_nj: 0,
            flavor: 4,
        }),
        Err(e) if is_permission(&e) => Err(RusageError::Permission),
        Err(e) => Err(RusageError::Other(e)),
    }
}

/// The executable path of `pid` (`proc_pidpath`).
pub fn proc_pidpath(pid: i32) -> io::Result<String> {
    let mut buf = vec![0u8; libc::PROC_PIDPATHINFO_MAXSIZE as usize];
    // SAFETY: `buf` has PROC_PIDPATHINFO_MAXSIZE writable bytes and that size is passed.
    let n = unsafe { libc::proc_pidpath(pid, buf.as_mut_ptr().cast::<c_void>(), buf.len() as u32) };
    if n <= 0 {
        return Err(io::Error::last_os_error());
    }
    buf.truncate(n as usize);
    String::from_utf8(buf).map_err(|e| io::Error::new(io::ErrorKind::InvalidData, e))
}

/// What `PROC_PIDTASKALLINFO` says about a process that `spectra process` reports.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct TaskAllInfo {
    /// `ptinfo.pti_threadnum`.
    pub thread_count: i32,
    /// `pbsd.pbi_name` up to its NUL.
    pub bsd_name: String,
}

/// The thread count and BSD short name of `pid`, from one `PROC_PIDTASKALLINFO` call (Go's
/// `collectProcessDetails`).
pub fn proc_task_all_info(pid: i32) -> io::Result<TaskAllInfo> {
    let mut info = MaybeUninit::<libc::proc_taskallinfo>::zeroed();
    let size = size_of::<libc::proc_taskallinfo>() as c_int;
    // SAFETY: `info` is a zeroed `proc_taskallinfo` of `size` bytes, the struct this flavor fills.
    let n = unsafe { libc::proc_pidinfo(pid, libc::PROC_PIDTASKALLINFO, 0, info.as_mut_ptr().cast::<c_void>(), size) };
    if n != size {
        return Err(io::Error::last_os_error());
    }
    // SAFETY: the call filled the whole struct (n == size), which holds only integers and arrays.
    let info = unsafe { info.assume_init() };
    let name: Vec<u8> = info.pbsd.pbi_name.iter().map(|&c| c as u8).collect();
    let end = name.iter().position(|&b| b == 0).unwrap_or(name.len());
    Ok(TaskAllInfo { thread_count: info.ptinfo.pti_threadnum, bsd_name: String::from_utf8_lossy(&name[..end]).into_owned() })
}

/// The raw `kern.procargs2` buffer of `pid` (`sysctl({CTL_KERN, KERN_PROCARGS2, pid})`): argc,
/// the exec path and the argument and environment strings, as Go's
/// `unix.SysctlRaw("kern.procargs2", pid)` returns it.
pub fn procargs2(pid: i32) -> io::Result<Vec<u8>> {
    let mut mib = [libc::CTL_KERN, libc::KERN_PROCARGS2, pid];
    let mut len: libc::size_t = 0;
    // SAFETY: `mib` holds 3 valid ints and its length is passed; a null old-value pointer asks
    // only for the size, written to `len`.
    let rc = unsafe { libc::sysctl(mib.as_mut_ptr(), 3, std::ptr::null_mut(), &mut len, std::ptr::null_mut(), 0) };
    if rc != 0 {
        return Err(io::Error::last_os_error());
    }
    let mut buf = vec![0u8; len];
    // SAFETY: `buf` has `len` writable bytes and `len` says so; the kernel writes at most that
    // many and stores the written length back in `len`.
    let rc = unsafe { libc::sysctl(mib.as_mut_ptr(), 3, buf.as_mut_ptr().cast::<c_void>(), &mut len, std::ptr::null_mut(), 0) };
    if rc != 0 {
        return Err(io::Error::last_os_error());
    }
    buf.truncate(len);
    Ok(buf)
}

/// The BSD short name (`pbi_name`, up to 32 bytes) of `pid` from `PROC_PIDTASKALLINFO`.
pub fn proc_bsd_name(pid: i32) -> io::Result<String> {
    let mut info = MaybeUninit::<libc::proc_taskallinfo>::zeroed();
    let size = size_of::<libc::proc_taskallinfo>() as c_int;
    // SAFETY: `info` is a zeroed `proc_taskallinfo` of `size` bytes, the struct this flavor fills.
    let n = unsafe { libc::proc_pidinfo(pid, libc::PROC_PIDTASKALLINFO, 0, info.as_mut_ptr().cast::<c_void>(), size) };
    if n != size {
        return Err(io::Error::last_os_error());
    }
    // SAFETY: the call filled the whole struct (n == size), which holds only integers and arrays.
    let info = unsafe { info.assume_init() };
    let name: Vec<u8> = info.pbsd.pbi_name.iter().map(|&c| c as u8).collect();
    let cstr = CStr::from_bytes_until_nul(&name).map_err(|e| io::Error::new(io::ErrorKind::InvalidData, e))?;
    Ok(cstr.to_string_lossy().into_owned())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn own_pid() -> i32 {
        std::process::id() as i32
    }

    #[test]
    fn rusage_v6_layout_matches_the_sdk() {
        // 16-byte UUID, 47 u64 fields, 9 reserved u64s.
        assert_eq!(size_of::<RusageInfoV6>(), 16 + 47 * 8 + 9 * 8);
        // The fields both flavors share must agree when read back to back, which catches an
        // offset error in the hand-written V6 struct.
        let v4 = pid_rusage::<libc::rusage_info_v4>(own_pid(), RUSAGE_INFO_V4).expect("v4");
        let v6 = pid_rusage::<RusageInfoV6>(own_pid(), RUSAGE_INFO_V6).expect("v6");
        assert!(v6.ri_user_time >= v4.ri_user_time && v6.ri_user_time - v4.ri_user_time < 1_000_000_000);
        assert!(v6.ri_diskio_bytesread >= v4.ri_diskio_bytesread);
        assert!(v6.ri_billed_energy >= v4.ri_billed_energy);
        assert!(v6.ri_logical_writes >= v4.ri_logical_writes);
        assert_eq!(v6.ri_proc_start_abstime, v4.ri_proc_start_abstime);
    }

    #[test]
    fn integer_sysctls() {
        assert!(sysctl_u64("hw.memsize").expect("hw.memsize") > 0);
        assert!(sysctl_u32("hw.ncpu").expect("hw.ncpu") > 0);
        assert!(!sysctl_string("kern.ostype").expect("kern.ostype").is_empty());
        assert!(sysctl_u64("hw.ncpu").is_err(), "a 4-byte sysctl read as 8 bytes must fail");
        assert!(sysctl_u32("spectra.no.such.oid").is_err());
    }

    #[test]
    fn swap_and_vm_statistics() {
        let swap = swap_usage().expect("vm.swapusage");
        assert!(swap.used <= swap.total);
        let vm = vm_statistics64().expect("host_statistics64");
        assert!(vm.free_count > 0 || vm.active_count > 0);
        assert!(page_size() >= 4096);
    }

    #[test]
    fn own_process_rusage_path_and_name() {
        let r = proc_pid_rusage(own_pid()).expect("own rusage");
        assert!(r.flavor == 6 || r.flavor == 4);
        assert!(r.user_ns + r.system_ns > 0);
        let path = proc_pidpath(own_pid()).expect("own path");
        assert!(path.starts_with('/'), "{path}");
        let name = proc_bsd_name(own_pid()).expect("own name");
        assert!(!name.is_empty());
        assert!(path.contains(&name), "{path} vs {name}");
    }

    #[test]
    fn own_task_info_and_procargs2() {
        let info = proc_task_all_info(own_pid()).expect("own task info");
        assert!(info.thread_count >= 1, "{info:?}");
        assert_eq!(info.bsd_name, proc_bsd_name(own_pid()).expect("name"));
        let raw = procargs2(own_pid()).expect("own procargs2");
        let argc = u32::from_ne_bytes([raw[0], raw[1], raw[2], raw[3]]) as usize;
        assert_eq!(argc, std::env::args().count());
        assert!(proc_task_all_info(i32::MAX).is_err());
        assert!(procargs2(i32::MAX).is_err());
    }

    #[test]
    fn missing_process_is_an_error() {
        assert!(proc_pidpath(i32::MAX).is_err());
        assert!(matches!(proc_pid_rusage(i32::MAX), Err(RusageError::Other(_))));
    }
}
