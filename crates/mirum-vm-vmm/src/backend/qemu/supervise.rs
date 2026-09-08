// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

//! Runs a command so its exit code survives even once nobody is left to
//! `wait()` on it as its real OS parent -- podman does this with `conmon`;
//! here the host's own shell does the same job. Host-specific by nature
//! (`sh` on POSIX, PowerShell on Windows), which is fine: unlike guests,
//! the set of hosts we run on is small and known, so a wrapper per host is
//! not a burden. Each function has one signature; the OS split lives inside
//! as `#[cfg]`-gated blocks.

use std::fmt;
use std::io;
use std::path::Path;

use tokio::process::{Child, Command};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Pid(pub u32);

impl fmt::Display for Pid {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.0)
    }
}

/// Fixed PowerShell wrapper: `$Bin`/`$ExitFile`/`$Rest` all arrive as real
/// process arguments (via `-File`, which binds them like any `param()`
/// block), never spliced into script text.
#[cfg(windows)]
const SUPERVISE_PS1: &str = r#"
param(
    [Parameter(Mandatory=$true, Position=0)] [string]$Bin,
    [Parameter(Mandatory=$true, Position=1)] [string]$ExitFile,
    [Parameter(ValueFromRemainingArguments=$true)] [string[]]$Rest
)
& $Bin @Rest
Set-Content -Path $ExitFile -Value $LASTEXITCODE -NoNewline
"#;

/// Spawns `argv` (`argv[0]` is the binary) wrapped so that once it exits,
/// its exit code is written to `exit_code_file` -- readable later even by a
/// process that isn't its parent and so can't `wait()` on it directly.
///
/// All dynamic values travel as separate argv entries to the wrapper, never
/// interpolated into script text, so nothing needs escaping.
pub fn spawn(argv: &[String], exit_code_file: &Path) -> io::Result<Child> {
    #[cfg(unix)]
    {
        Command::new("sh")
            .arg("-c")
            .arg(r#"bin="$1"; shift; exitfile="$1"; shift; "$bin" "$@"; echo $? > "$exitfile""#)
            .arg("sh") // conventional $0 filler, unused
            .arg(&argv[0])
            .arg(exit_code_file)
            .args(&argv[1..])
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null())
            .spawn()
    }
    #[cfg(windows)]
    {
        // Content is constant, only the path is dynamic -- written next to
        // the machine's own files, same lifetime as the pidfile/exit-code
        // file.
        let script = exit_code_file
            .parent()
            .unwrap_or_else(|| Path::new("."))
            .join("supervise.ps1");
        std::fs::write(&script, SUPERVISE_PS1)?;

        Command::new("powershell")
            .args(["-NoProfile", "-ExecutionPolicy", "Bypass", "-File"])
            .arg(&script)
            .arg(&argv[0])
            .arg(exit_code_file)
            .args(&argv[1..])
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null())
            .spawn()
    }
}

/// Whether `pid` is still alive. Works for any pid, not just our own
/// children -- `Child::try_wait` only works on the latter.
pub async fn pid_alive(pid: Pid) -> bool {
    #[cfg(target_os = "linux")]
    {
        tokio::fs::try_exists(format!("/proc/{pid}"))
            .await
            .unwrap_or(false)
    }
    #[cfg(all(unix, not(target_os = "linux")))]
    {
        Command::new("kill")
            .arg("-0")
            .arg(pid.to_string())
            .status()
            .await
            .map(|s| s.success())
            .unwrap_or(false)
    }
    #[cfg(windows)]
    {
        let Ok(out) = Command::new("tasklist")
            .args(["/FI", &format!("PID eq {pid}"), "/NH"])
            .output()
            .await
        else {
            return false;
        };
        String::from_utf8_lossy(&out.stdout).contains(&pid.to_string())
    }
}

/// Force-terminates `pid`. No parent relationship required, unlike
/// `Child::kill()`, which is exactly the point.
pub async fn kill(pid: Pid) -> io::Result<()> {
    #[cfg(unix)]
    {
        let status = Command::new("kill")
            .arg("-KILL")
            .arg(pid.to_string())
            .status()
            .await?;
        if status.success() {
            Ok(())
        } else {
            Err(io::Error::other(format!("kill -KILL {pid} failed")))
        }
    }
    #[cfg(windows)]
    {
        let status = Command::new("taskkill")
            .args(["/PID", &pid.to_string(), "/F"])
            .status()
            .await?;
        if status.success() {
            Ok(())
        } else {
            Err(io::Error::other(format!("taskkill /PID {pid} /F failed")))
        }
    }
}

/// (cpu_time_ns, memory_bytes) for `pid`, read straight from the host
/// process -- no QMP/guest cooperation needed.
pub async fn process_stats(pid: Pid) -> io::Result<(u64, u64)> {
    #[cfg(target_os = "linux")]
    {
        let stat = tokio::fs::read_to_string(format!("/proc/{pid}/stat")).await?;
        // `comm` (field 2) can itself contain spaces/parens; splitting after
        // the last ')' reliably skips past it regardless.
        let after_comm = stat
            .rsplit_once(')')
            .map(|(_, rest)| rest)
            .ok_or_else(|| io::Error::other("unexpected /proc/pid/stat format"))?;
        let fields: Vec<&str> = after_comm.split_whitespace().collect();
        let field = |i: usize| -> io::Result<u64> {
            fields
                .get(i)
                .and_then(|s| s.parse().ok())
                .ok_or_else(|| io::Error::other("unexpected /proc/pid/stat format"))
        };
        // Fields are 0-indexed from `state` (original field 3); utime/stime
        // are original fields 14/15. 100 (USER_HZ) is the practically
        // universal Linux clock tick rate.
        let cpu_time_ns = (field(11)? + field(12)?) * 10_000_000;

        let status = tokio::fs::read_to_string(format!("/proc/{pid}/status")).await?;
        let memory_bytes = status
            .lines()
            .find_map(|line| line.strip_prefix("VmRSS:"))
            .and_then(|rest| rest.split_whitespace().next())
            .and_then(|kb| kb.parse::<u64>().ok())
            .map(|kb| kb * 1024)
            .ok_or_else(|| io::Error::other("VmRSS not found in /proc/pid/status"))?;

        Ok((cpu_time_ns, memory_bytes))
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = pid;
        Err(io::Error::other(
            "process stats aren't implemented for this host yet",
        ))
    }
}
