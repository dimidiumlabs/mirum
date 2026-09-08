// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

//! Typed wrapper over the `qemu-system-*` command line. One qemu concept
//! per type/field; knows nothing about Mirum VM's image/machine model.

use std::path::PathBuf;

/// One of the architectures qemu ships a `qemu-system-*` binary for.
/// Distinct from `mirum_vm_hmi::Architecture`: this is qemu's own set of
/// per-arch conventions (binary name, machine type, TCG cpu model), not
/// Mirum VM's.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Arch {
    Amd64,
    Arm64,
    Loong64,
    Ppc64le,
    Riscv64,
    S390x,
}

impl Arch {
    /// The `qemu-system-*` binary for this target.
    pub fn binary(&self) -> String {
        let suffix = match self {
            Self::Amd64 => "x86_64",
            Self::Arm64 => "aarch64",
            Self::Loong64 => "loongarch64",
            Self::Ppc64le => "ppc64le",
            Self::Riscv64 => "riscv64",
            Self::S390x => "s390x",
        };
        format!("qemu-system-{suffix}")
    }

    /// `-machine`/`-M` value this arch needs, if any (the default machine
    /// type is fine for the others).
    pub fn machine_type(&self) -> Option<&'static str> {
        match self {
            Self::Arm64 | Self::Riscv64 => Some("virt"),
            Self::Ppc64le => Some("pseries"),
            _ => None,
        }
    }

    /// `-cpu` model to emulate when KVM isn't usable.
    pub fn tcg_cpu_model(&self) -> &'static str {
        match self {
            Self::Amd64 => "qemu64",
            Self::Arm64 => "cortex-a53",
            Self::Loong64 => "la464",
            Self::Ppc64le => "power9",
            Self::Riscv64 => "rv64",
            Self::S390x => "max",
        }
    }
}

pub enum Accel {
    Tcg,
    Kvm,
}

pub enum NetBackend {
    Nic { model: String },
    User { hostfwd: Vec<(u16, u16)> },
}

impl NetBackend {
    fn to_arg(&self) -> String {
        match self {
            Self::Nic { model } => format!("nic,model={model}"),
            Self::User { hostfwd } => {
                let mut s = String::from("user");
                for (host, guest) in hostfwd {
                    s.push_str(&format!(",hostfwd=tcp:127.0.0.1:{host}-:{guest}"));
                }
                s
            }
        }
    }
}

pub enum DriveInterface {
    /// `if=virtio`: the disk is a bootable device in its own right.
    Virtio,
    /// `if=none,id=<id>`: unattached, paired with a `Device::VirtioBlkPci`.
    None { id: String },
}

pub struct Drive {
    pub file: PathBuf,
    /// `snapshot=on`: writes go to a throwaway overlay, never to `file`.
    pub snapshot: bool,
    pub interface: DriveInterface,
}

impl Drive {
    fn to_arg(&self) -> String {
        let mut s = format!("file={},media=disk", self.file.display());
        if self.snapshot {
            s.push_str(",snapshot=on");
        }
        match &self.interface {
            DriveInterface::Virtio => s.push_str(",if=virtio"),
            DriveInterface::None { id } => s.push_str(&format!(",id={id},if=none")),
        }
        s
    }
}

pub enum Device {
    VirtioBlkPci { drive: String },
    VirtioRngPci,
    VirtioBalloon,
    VirtioSerialPci,
    VirtSerialPort { chardev: String, name: String },
}

impl Device {
    fn to_arg(&self) -> String {
        match self {
            Self::VirtioBlkPci { drive } => format!("virtio-blk-pci,drive={drive}"),
            Self::VirtioRngPci => "virtio-rng-pci".into(),
            Self::VirtioBalloon => "virtio-balloon".into(),
            Self::VirtioSerialPci => "virtio-serial-pci".into(),
            Self::VirtSerialPort { chardev, name } => {
                format!("virtserialport,chardev={chardev},name={name}")
            }
        }
    }
}

/// One `qemu-system-*` invocation. `to_argv()` is pure -- no process/IO --
/// so it's testable without spawning anything.
#[derive(Default)]
pub struct Command {
    pub binary: String,
    /// `-machine`/`-M`; qemu treats them as synonyms, so only one field.
    pub machine: Option<String>,
    pub cpu: Option<String>,
    pub accel: Option<Accel>,
    pub memory_mib: u64,
    pub smp: u32,
    pub display_none: bool,
    pub nets: Vec<NetBackend>,
    pub drives: Vec<Drive>,
    pub devices: Vec<Device>,
    pub kernel: Option<PathBuf>,
    pub initrd: Option<PathBuf>,
    pub append: Option<String>,
    /// `-pidfile`: qemu writes its own pid here. The only reliable way to
    /// signal/check it later from a process that isn't its parent.
    pub pidfile: Option<PathBuf>,
    /// `-qmp tcp:127.0.0.1:<port>,server=on,wait=off`: the control channel.
    /// TCP loopback, not a unix socket -- the latter doesn't exist on
    /// Windows at all (`tokio::net::UnixStream` is unix-only), and this way
    /// there's nothing to `#[cfg]` on the transport.
    pub qmp_port: Option<u16>,
    /// Host side of the qemu-guest-agent virtio-serial channel.
    pub qga_port: Option<u16>,
    /// Interactive guest serial console and its persistent output log.
    pub console: Option<(u16, PathBuf)>,
}

impl Command {
    pub fn to_argv(&self) -> Vec<String> {
        let mut argv = vec![self.binary.clone()];
        if let Some(machine) = &self.machine {
            argv.extend(["-machine".into(), machine.clone()]);
        }
        if let Some(cpu) = &self.cpu {
            argv.extend(["-cpu".into(), cpu.clone()]);
        }
        if matches!(self.accel, Some(Accel::Kvm)) {
            argv.push("-enable-kvm".into());
        }
        argv.extend(["-m".into(), self.memory_mib.to_string()]);
        argv.extend(["-smp".into(), format!("cpus={}", self.smp)]);
        for net in &self.nets {
            argv.extend(["-net".into(), net.to_arg()]);
        }
        for drive in &self.drives {
            argv.extend(["-drive".into(), drive.to_arg()]);
        }
        for device in &self.devices {
            argv.extend(["-device".into(), device.to_arg()]);
        }
        if self.display_none {
            argv.extend(["-display".into(), "none".into()]);
        }
        if let Some(kernel) = &self.kernel {
            argv.extend(["-kernel".into(), kernel.display().to_string()]);
        }
        if let Some(initrd) = &self.initrd {
            argv.extend(["-initrd".into(), initrd.display().to_string()]);
        }
        if let Some(append) = &self.append {
            argv.extend(["-append".into(), append.clone()]);
        }
        if let Some(pidfile) = &self.pidfile {
            argv.extend(["-pidfile".into(), pidfile.display().to_string()]);
        }
        if let Some(port) = self.qmp_port {
            argv.extend([
                "-qmp".into(),
                format!("tcp:127.0.0.1:{port},server=on,wait=off"),
            ]);
        }
        if let Some(port) = self.qga_port {
            argv.extend([
                "-chardev".into(),
                format!("socket,id=qga0,host=127.0.0.1,port={port},server=on,wait=off"),
                "-device".into(),
                Device::VirtioSerialPci.to_arg(),
                "-device".into(),
                Device::VirtSerialPort {
                    chardev: "qga0".into(),
                    name: "org.qemu.guest_agent.0".into(),
                }
                .to_arg(),
            ]);
        }
        if let Some((port, logfile)) = &self.console {
            argv.extend([
                "-chardev".into(),
                format!(
                    "socket,id=console0,host=127.0.0.1,port={port},server=on,wait=off,logfile={},logappend=off",
                    logfile.display()
                ),
                "-serial".into(),
                "chardev:console0".into(),
            ]);
        }
        argv
    }
}

/// Whether `/dev/kvm` is actually usable, not just present -- the node can
/// exist but be unopenable (e.g. group `kvm` without membership), which
/// qemu only reports once it's already mid-boot.
pub async fn kvm_available() -> bool {
    tokio::fs::OpenOptions::new()
        .read(true)
        .write(true)
        .open("/dev/kvm")
        .await
        .is_ok()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn renders_flags_in_a_stable_order() {
        let cmd = Command {
            binary: "qemu-system-x86_64".into(),
            cpu: Some("qemu64".into()),
            memory_mib: 1024,
            smp: 2,
            display_none: true,
            nets: vec![
                NetBackend::Nic {
                    model: "virtio".into(),
                },
                NetBackend::User {
                    hostfwd: vec![(8022, 22)],
                },
            ],
            drives: vec![Drive {
                file: "root.hmi".into(),
                snapshot: true,
                interface: DriveInterface::Virtio,
            }],
            devices: vec![Device::VirtioRngPci],
            ..Default::default()
        };
        assert_eq!(
            cmd.to_argv(),
            vec![
                "qemu-system-x86_64",
                "-cpu",
                "qemu64",
                "-m",
                "1024",
                "-smp",
                "cpus=2",
                "-net",
                "nic,model=virtio",
                "-net",
                "user,hostfwd=tcp:127.0.0.1:8022-:22",
                "-drive",
                "file=root.hmi,media=disk,snapshot=on,if=virtio",
                "-device",
                "virtio-rng-pci",
                "-display",
                "none",
            ]
        );
    }

    #[test]
    fn kvm_accel_adds_enable_kvm_flag() {
        let cmd = Command {
            binary: "qemu-system-x86_64".into(),
            cpu: Some("host".into()),
            accel: Some(Accel::Kvm),
            ..Default::default()
        };
        assert!(cmd.to_argv().contains(&"-enable-kvm".to_string()));
    }

    #[test]
    fn detached_drive_pairs_with_a_virtio_blk_device() {
        let cmd = Command {
            binary: "qemu-system-x86_64".into(),
            drives: vec![Drive {
                file: "root.hmi".into(),
                snapshot: true,
                interface: DriveInterface::None { id: "root".into() },
            }],
            devices: vec![Device::VirtioBlkPci {
                drive: "root".into(),
            }],
            kernel: Some("vmlinuz".into()),
            initrd: Some("initrd.img".into()),
            append: Some("console=ttyS0".into()),
            ..Default::default()
        };
        let argv = cmd.to_argv();
        assert!(argv.windows(2).any(|w| w == ["-kernel", "vmlinuz"]));
        assert!(argv.windows(2).any(|w| w == ["-initrd", "initrd.img"]));
        assert!(argv.windows(2).any(|w| w == ["-append", "console=ttyS0"]));
        assert!(argv.windows(2).any(|w| w
            == [
                "-drive",
                "file=root.hmi,media=disk,snapshot=on,id=root,if=none"
            ]));
        assert!(
            argv.windows(2)
                .any(|w| w == ["-device", "virtio-blk-pci,drive=root"])
        );
    }

    #[test]
    fn qga_uses_a_dedicated_virtio_serial_channel() {
        let argv = Command {
            binary: "qemu-system-x86_64".into(),
            qga_port: Some(1234),
            ..Default::default()
        }
        .to_argv();
        assert!(argv.windows(2).any(|w| {
            w == [
                "-chardev",
                "socket,id=qga0,host=127.0.0.1,port=1234,server=on,wait=off",
            ]
        }));
        assert!(argv.windows(2).any(|w| {
            w == [
                "-device",
                "virtserialport,chardev=qga0,name=org.qemu.guest_agent.0",
            ]
        }));
    }

    #[test]
    fn serial_console_has_an_attach_socket_and_log() {
        let argv = Command {
            binary: "qemu-system-x86_64".into(),
            console: Some((4321, "console.log".into())),
            ..Default::default()
        }
        .to_argv();
        assert!(argv.windows(2).any(|w| {
            w == [
                "-chardev",
                "socket,id=console0,host=127.0.0.1,port=4321,server=on,wait=off,logfile=console.log,logappend=off",
            ]
        }));
        assert!(
            argv.windows(2)
                .any(|w| w == ["-serial", "chardev:console0"])
        );
    }
}
