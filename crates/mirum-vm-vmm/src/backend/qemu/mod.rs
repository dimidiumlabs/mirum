// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

//! QMP is an unconditional implementation detail of this backend. Guest
//! access is independent: an image may be a black box, or may promise QGA;
//! a promised QGA channel must become ready for `start()` to succeed.
//!
//! `logs`/`attach` use a dedicated serial console; `exec` uses QGA.
//! Console resizing and `reattach` are not implemented yet.
//!
//! `cli` is the typed qemu CLI wrapper; `supervise` gets an exit code out of
//! a process even if we stop being its parent; this module only translates
//! Mirum VM's image/machine types into both.

mod cli;
mod qga;
mod qmp;
mod supervise;

use std::path::{Path, PathBuf};
use std::process::{ExitStatus, Output};
use std::time::Duration;

use async_trait::async_trait;
use mirum_vm_hmi::{Access, Architecture, Boot, BootLinux, Disk, MachineImage};
use tokio::process::Child;
use tokio::sync::Mutex;

use crate::{
    Console, Error, Hypervisor, HypervisorId, Machine, MachineId, R, Settings, State, Stats,
};

const QEMU_HYPERVISOR_ID: HypervisorId = HypervisorId(1);

enum QemuBoot {
    // No BootBios fields are needed here: `disk` is already resolved into
    // `QemuMachine::disk`, and `protocol` isn't used past `supports()`.
    Bios,
    Linux(BootLinux),
}

/// The wrapper (`sh`) process, not qemu itself -- once qemu has a pid on
/// disk (see `QemuMachine::pid_path`), that's the source of truth for
/// liveness/signaling, not this.
enum ChildState {
    NotStarted,
    Running(Child),
    Exited(ExitStatus),
}

pub struct QemuMachine {
    id: MachineId,
    name: Option<String>,
    dir: PathBuf,
    arch: Architecture,
    settings: Settings,
    disk: Disk,
    boot: QemuBoot,
    qga: bool,
    child: Mutex<ChildState>,
    qga_lock: Mutex<()>,
    qmp_client: Mutex<Option<qmp::Qmp>>,
}

impl QemuMachine {
    /// Where qemu's own `-pidfile` lands. Provisional: once a persisted
    /// `run/<id>/` directory exists (separate from `dir`, the materialized
    /// image files), this moves there instead of living inside `dir`.
    fn pid_path(&self) -> PathBuf {
        self.dir.join("qemu.pid")
    }

    fn exit_code_path(&self) -> PathBuf {
        self.dir.join("qemu.exit-code")
    }

    /// Where the port `start()` picked for `-qmp tcp:127.0.0.1:<port>` is
    /// recorded -- chosen by us, not qemu, so (unlike `pid_path`) nothing
    /// needs to wait for this file; it exists before qemu is spawned.
    fn qmp_port_path(&self) -> PathBuf {
        self.dir.join("qemu.qmp-port")
    }

    fn qga_port_path(&self) -> PathBuf {
        self.dir.join("qemu.qga-port")
    }

    fn console_port_path(&self) -> PathBuf {
        self.dir.join("qemu.console-port")
    }

    fn console_log_path(&self) -> PathBuf {
        self.dir.join("console.log")
    }

    async fn read_port(&self, path: &Path, name: &str) -> R<u16> {
        let data = tokio::fs::read_to_string(path).await?;
        data.trim()
            .parse()
            .map_err(|_| Error::InvalidState(format!("invalid {name} port contents")))
    }

    async fn qmp_port(&self) -> R<u16> {
        self.read_port(&self.qmp_port_path(), "qmp").await
    }

    async fn qga_port(&self) -> R<u16> {
        self.read_port(&self.qga_port_path(), "qga").await
    }

    async fn console_port(&self) -> R<u16> {
        self.read_port(&self.console_port_path(), "console").await
    }

    /// Connects on first use (there's a short window after spawn before
    /// qemu's TCP listener is actually up, so a couple of retries) and
    /// reuses the connection after that.
    async fn qmp_client(&self) -> R<tokio::sync::MutexGuard<'_, Option<qmp::Qmp>>> {
        let mut client = self.qmp_client.lock().await;
        if client.is_none() {
            let port = self.qmp_port().await?;
            let mut connected = None;
            for _ in 0..50 {
                match qmp::Qmp::connect(port).await {
                    Ok(qmp) => {
                        connected = Some(qmp);
                        break;
                    }
                    Err(_) => tokio::time::sleep(Duration::from_millis(20)).await,
                }
            }
            *client = Some(
                connected
                    .ok_or_else(|| Error::InvalidState("qemu's qmp port never came up".into()))?,
            );
        }
        Ok(client)
    }

    /// Reads qemu's pid, waiting briefly for `-pidfile` to actually appear
    /// (there's a short window between spawn and qemu writing it).
    async fn qemu_pid(&self) -> R<supervise::Pid> {
        for _ in 0..50 {
            if let Ok(data) = tokio::fs::read_to_string(self.pid_path()).await
                && let Ok(pid) = data.trim().parse()
            {
                return Ok(supervise::Pid(pid));
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        Err(Error::InvalidState(
            "qemu did not write its pidfile in time".into(),
        ))
    }

    async fn read_exit_code(&self) -> Option<ExitStatus> {
        let data = tokio::fs::read_to_string(self.exit_code_path())
            .await
            .ok()?;
        let code: i32 = data.trim().parse().ok()?;
        #[cfg(unix)]
        {
            use std::os::unix::process::ExitStatusExt;
            // `$?` is already qemu's plain exit code; shift it into the
            // wait()-status shape ExitStatus expects (bits 8-15 == exit
            // code, low byte 0 == not signaled). Loses signal detail, but
            // `.code()` -- all we ever read -- comes back right.
            Some(ExitStatus::from_raw(code << 8))
        }
        #[cfg(not(unix))]
        {
            None
        }
    }
}

#[async_trait]
impl Machine for QemuMachine {
    fn id(&self) -> MachineId {
        self.id
    }

    fn hid(&self) -> HypervisorId {
        QEMU_HYPERVISOR_ID
    }

    fn name(&self) -> Option<&str> {
        self.name.as_deref()
    }

    fn settings(&self) -> Settings {
        self.settings.clone()
    }

    async fn state(&self) -> State {
        if let Some(status) = self.read_exit_code().await {
            return State::Exited(status.code().unwrap_or(-1));
        }
        let pid = match self.qemu_pid().await {
            Ok(pid) => pid,
            Err(_) => {
                return match &*self.child.lock().await {
                    ChildState::NotStarted => State::Created,
                    ChildState::Exited(status) => State::Exited(status.code().unwrap_or(-1)),
                    ChildState::Running(_) => State::Running,
                };
            }
        };
        if !supervise::pid_alive(pid).await {
            return State::Dead;
        }
        // A live pid alone can't distinguish running from paused; ask QMP.
        if let Ok(mut client) = self.qmp_client().await
            && let Ok(qmp::Status::Paused) = client
                .as_mut()
                .expect("initialized by qmp_client")
                .status()
                .await
        {
            return State::Paused;
        }
        State::Running
    }

    async fn stats(&self) -> R<Stats> {
        let pid = self.qemu_pid().await?;
        let (cpu_time_ns, memory_bytes) = supervise::process_stats(pid).await?;
        Ok(Stats {
            cpu_time_ns,
            memory_bytes,
        })
    }

    async fn logs(&self) -> R<Vec<u8>> {
        match tokio::fs::read(self.console_log_path()).await {
            Ok(logs) => Ok(logs),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(Vec::new()),
            Err(error) => Err(error.into()),
        }
    }

    async fn start(&mut self) -> R<()> {
        let mut child = self.child.lock().await;
        if matches!(*child, ChildState::Running(_)) {
            return Err(Error::InvalidState("already started".into()));
        }
        let _ = tokio::fs::remove_file(self.pid_path()).await;
        let _ = tokio::fs::remove_file(self.exit_code_path()).await;
        let _ = tokio::fs::remove_file(self.qmp_port_path()).await;
        let _ = tokio::fs::remove_file(self.qga_port_path()).await;
        let _ = tokio::fs::remove_file(self.console_port_path()).await;
        let _ = tokio::fs::remove_file(self.console_log_path()).await;
        *self.qmp_client.lock().await = None;

        let qmp_port = qmp::free_port().await?;
        tokio::fs::write(self.qmp_port_path(), qmp_port.to_string()).await?;
        let qga_port = if self.qga {
            let mut port = qmp::free_port().await?;
            while port == qmp_port {
                port = qmp::free_port().await?;
            }
            tokio::fs::write(self.qga_port_path(), port.to_string()).await?;
            Some(port)
        } else {
            None
        };
        let mut console_port = qmp::free_port().await?;
        while console_port == qmp_port || Some(console_port) == qga_port {
            console_port = qmp::free_port().await?;
        }
        tokio::fs::write(self.console_port_path(), console_port.to_string()).await?;

        let kvm = Architecture::host() == Some(self.arch) && cli::kvm_available().await;
        let sys_arch = match self.arch {
            Architecture::Amd64 => cli::Arch::Amd64,
            Architecture::Arm64 => cli::Arch::Arm64,
            Architecture::Loong64 => cli::Arch::Loong64,
            Architecture::Ppc64le => cli::Arch::Ppc64le,
            Architecture::Riscv64 => cli::Arch::Riscv64,
            Architecture::S390x => cli::Arch::S390x,
        };
        let mut cmd = cli::Command {
            binary: sys_arch.binary(),
            machine: sys_arch.machine_type().map(String::from),
            memory_mib: self.settings.ram,
            smp: self.settings.cpu,
            display_none: true,
            nets: vec![
                cli::NetBackend::Nic {
                    model: "virtio".into(),
                },
                cli::NetBackend::User {
                    hostfwd: self.settings.port_forwards.clone(),
                },
            ],
            devices: vec![cli::Device::VirtioRngPci, cli::Device::VirtioBalloon],
            ..Default::default()
        };

        if kvm {
            cmd.cpu = Some("host".into());
            cmd.accel = Some(cli::Accel::Kvm);
        } else {
            cmd.cpu = Some(sys_arch.tcg_cpu_model().into());
            cmd.accel = Some(cli::Accel::Tcg);
        }

        let disk_path = self.dir.join(&self.disk.path);
        match &self.boot {
            QemuBoot::Bios => {
                cmd.drives.push(cli::Drive {
                    file: disk_path,
                    snapshot: true,
                    interface: cli::DriveInterface::Virtio,
                });
            }
            QemuBoot::Linux(linux) => {
                cmd.drives.push(cli::Drive {
                    file: disk_path,
                    snapshot: true,
                    interface: cli::DriveInterface::None { id: "root".into() },
                });
                cmd.devices.push(cli::Device::VirtioBlkPci {
                    drive: "root".into(),
                });
                cmd.kernel = Some(self.dir.join(&linux.kernel.path));
                cmd.initrd = Some(self.dir.join(&linux.initrd.path));
                cmd.append = Some(linux.cmdline.clone());
            }
        }
        cmd.pidfile = Some(self.pid_path());
        cmd.qmp_port = Some(qmp_port);
        cmd.qga_port = qga_port;
        cmd.console = Some((console_port, self.console_log_path()));
        let argv = cmd.to_argv();

        let wrapper = supervise::spawn(&argv, &self.exit_code_path())?;
        *child = ChildState::Running(wrapper);
        drop(child);

        if let Some(port) = qga_port
            && let Err(error) = qga::wait_until_ready(port, Duration::from_secs(60)).await
        {
            let _ = self.kill().await;
            return Err(error);
        }
        Ok(())
    }

    async fn stop(&mut self) -> R<()> {
        self.qmp_client()
            .await?
            .as_mut()
            .expect("initialized by qmp_client")
            .power_down()
            .await
    }

    async fn kill(&mut self) -> R<()> {
        let pid = self.qemu_pid().await?;
        supervise::kill(pid).await?;
        // Reap the wrapper so its resources are released; its own exit
        // status is uninteresting, qemu's (now on disk) is what matters.
        let mut child = self.child.lock().await;
        if let ChildState::Running(c) = &mut *child {
            let _ = c.wait().await;
        }
        if let Some(status) = self.read_exit_code().await {
            *child = ChildState::Exited(status);
        }
        Ok(())
    }

    async fn restart(&mut self) -> R<()> {
        self.qmp_client()
            .await?
            .as_mut()
            .expect("initialized by qmp_client")
            .reset()
            .await
    }

    async fn delete(&mut self) -> R<()> {
        if self.state().await == State::Running {
            self.kill().await?;
        }
        let _ = tokio::fs::remove_file(self.pid_path()).await;
        let _ = tokio::fs::remove_file(self.exit_code_path()).await;
        let _ = tokio::fs::remove_file(self.qmp_port_path()).await;
        let _ = tokio::fs::remove_file(self.qga_port_path()).await;
        let _ = tokio::fs::remove_file(self.console_port_path()).await;
        let _ = tokio::fs::remove_file(self.console_log_path()).await;
        Ok(())
    }

    async fn pause(&mut self) -> R<()> {
        self.qmp_client()
            .await?
            .as_mut()
            .expect("initialized by qmp_client")
            .pause()
            .await
    }

    async fn unpause(&mut self) -> R<()> {
        self.qmp_client()
            .await?
            .as_mut()
            .expect("initialized by qmp_client")
            .resume()
            .await
    }

    async fn update(&mut self, settings: Settings) -> R<()> {
        if settings.cpu != self.settings.cpu {
            return Err(Error::Unsupported(
                "qemu backend can't hot-change cpu count (needs maxcpus reserved at boot)".into(),
            ));
        }
        if settings.port_forwards != self.settings.port_forwards {
            return Err(Error::Unsupported(
                "qemu backend can't hot-change port forwards (fixed at boot via -net user)".into(),
            ));
        }
        if settings.ram != self.settings.ram {
            self.qmp_client()
                .await?
                .as_mut()
                .expect("initialized by qmp_client")
                .set_balloon_size(settings.ram * 1024 * 1024)
                .await?;
        }
        self.settings = settings;
        Ok(())
    }

    async fn rename(&mut self, name: Option<&str>) -> R<()> {
        self.name = name.map(String::from);
        Ok(())
    }

    async fn wait(&mut self) -> R<ExitStatus> {
        loop {
            if let Some(status) = self.read_exit_code().await {
                *self.child.lock().await = ChildState::Exited(status);
                return Ok(status);
            }
            let mut child = self.child.lock().await;
            match &mut *child {
                ChildState::Running(c) => {
                    // The wrapper only exits after qemu does and the exit
                    // code is flushed to disk, so looping back re-reads it.
                    c.wait().await?;
                }
                ChildState::Exited(status) => return Ok(*status),
                ChildState::NotStarted => {
                    return Err(Error::InvalidState("not started".into()));
                }
            }
        }
    }

    async fn exec(&self, cmd: &[String]) -> R<Output> {
        if !self.qga {
            return Err(Error::Unsupported(
                "this image does not declare qga access".into(),
            ));
        }
        let _guard = self.qga_lock.lock().await;
        qga::exec(self.qga_port().await?, cmd).await
    }

    async fn attach(&self) -> R<Box<dyn Console>> {
        let port = self.console_port().await?;
        for _ in 0..50 {
            match tokio::net::TcpStream::connect(("127.0.0.1", port)).await {
                Ok(stream) => return Ok(Box::new(stream)),
                Err(_) => tokio::time::sleep(Duration::from_millis(20)).await,
            }
        }
        Err(Error::InvalidState(
            "qemu serial console did not become available".into(),
        ))
    }
}

pub struct QemuHypervisor;

#[async_trait]
impl Hypervisor for QemuHypervisor {
    fn id(&self) -> HypervisorId {
        QEMU_HYPERVISOR_ID
    }

    fn name(&self) -> &'static str {
        "qemu"
    }

    fn supports(&self, boot: &Boot) -> bool {
        matches!(boot, Boot::Bios(_) | Boot::Linux(_))
    }

    async fn create(
        &self,
        id: MachineId,
        name: Option<&str>,
        image: &MachineImage,
        dir: &Path,
        boot: &Boot,
        settings: &Settings,
    ) -> R<Box<dyn Machine>> {
        let disk = image
            .disks
            .iter()
            .find(|d| d.id == boot.disk())
            .cloned()
            .ok_or_else(|| {
                Error::InvalidImage(format!(
                    "boot entry references unknown disk '{}'",
                    boot.disk()
                ))
            })?;

        let qemu_boot = match boot {
            Boot::Bios(_) => QemuBoot::Bios,
            Boot::Linux(b) => QemuBoot::Linux(b.clone()),
            Boot::Uefi(_) | Boot::Unknown(_) => {
                return Err(Error::Unsupported(format!(
                    "qemu backend cannot boot protocol '{}'",
                    boot.protocol()
                )));
            }
        };

        Ok(Box::new(QemuMachine {
            id,
            name: name.map(String::from),
            dir: dir.to_path_buf(),
            arch: image.system.architecture,
            settings: settings.clone(),
            disk,
            boot: qemu_boot,
            qga: image
                .machine
                .access
                .iter()
                .any(|access| matches!(access, Access::Qga { .. })),
            child: Mutex::new(ChildState::NotStarted),
            qga_lock: Mutex::new(()),
            qmp_client: Mutex::new(None),
        }))
    }

    async fn reattach(
        &self,
        _id: MachineId,
        _name: Option<&str>,
        _dir: &Path,
        _settings: &Settings,
    ) -> R<Box<dyn Machine>> {
        todo!("no persisted qemu run-state to reattach to yet")
    }
}
