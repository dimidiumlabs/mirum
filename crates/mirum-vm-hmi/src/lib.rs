// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

//! Types and validation for a Mirum VM image `config.json`.
//!
//! This crate deliberately has no filesystem, registry, or VMM concerns.  It
//! validates the declared image contract; callers decide how referenced files
//! are stored and executed.
//!
//! # Schema evolution
//!
//! Bump [`SCHEMA_VERSION`] only for breaking changes. Within a version:
//! [`Boot`]/[`Access`] are menus, so they tolerate unrecognized entries via
//! `Unknown`; [`Architecture`]/[`DiskFormat`] are single mandatory values
//! with no fallback, so they stay closed enums; `system.os` and digest
//! algorithms stay plain strings. No `deny_unknown_fields`; `rename_all =
//! "camelCase"` on every struct.

use std::collections::HashSet;
use std::fmt;
use std::path::{Component, Path};

use serde::{Deserialize, Serialize};

pub const SCHEMA_VERSION: u8 = 1;
pub const KIND: &str = "MachineImage";

pub const BOOT_LINUX_DIRECT: &str = "linux/direct";
pub const BOOT_FIRMWARE_DISK_BIOS: &str = "firmware-disk/bios";
pub const BOOT_FIRMWARE_DISK_UEFI: &str = "firmware-disk/uefi";

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum DiskFormat {
    Qcow2,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(transparent)]
pub struct Digest(pub String);

impl fmt::Display for Digest {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl Digest {
    fn validate_into(&self, field: &str, validation: &mut Validation) {
        let Some(hex) = self.0.strip_prefix("sha256:") else {
            validation.error(format!("{field} must use sha256"));
            return;
        };
        if hex.len() != 64 || !hex.bytes().all(|b| b.is_ascii_hexdigit()) {
            validation.error(format!("{field} must contain 64 hexadecimal digits"));
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ResourceRange {
    pub minimum: u64,
    pub default: u64,
}

impl ResourceRange {
    fn validate_into(self, field: &str, validation: &mut Validation) {
        if self.minimum == 0 {
            validation.error(format!("{field}.minimum must be greater than zero"));
        }
        if self.default < self.minimum {
            validation.error(format!("{field}.default must be at least minimum"));
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct FileRef {
    pub path: String,
    pub digest: Digest,
}

impl FileRef {
    fn validate_into(&self, field: &str, validation: &mut Validation) {
        validation.path(&self.path, &format!("{field}.path"));
        self.digest
            .validate_into(&format!("{field}.digest"), validation);
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct BootBios {
    pub protocol: String,
    pub disk: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct BootUefi {
    pub protocol: String,
    pub disk: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct BootLinux {
    pub protocol: String,
    pub disk: String,
    pub kernel: FileRef,
    pub initrd: FileRef,
    pub cmdline: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(untagged)]
pub enum Boot {
    Bios(BootBios),
    Uefi(BootUefi),
    Linux(BootLinux),
    Unknown(serde_json::Value),
}

impl<'de> Deserialize<'de> for Boot {
    fn deserialize<D: serde::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        use serde::de::Error;

        let value = serde_json::Value::deserialize(deserializer)?;
        let protocol = value.get("protocol").and_then(|p| p.as_str()).unwrap_or("");

        #[derive(Deserialize)]
        struct Raw {
            disk: String,
            #[serde(default)]
            kernel: Option<FileRef>,
            #[serde(default)]
            initrd: Option<FileRef>,
            #[serde(default)]
            cmdline: Option<String>,
        }

        match protocol {
            BOOT_FIRMWARE_DISK_BIOS => {
                let raw: Raw = serde_json::from_value(value).map_err(Error::custom)?;
                if raw.kernel.is_some() || raw.initrd.is_some() || raw.cmdline.is_some() {
                    return Err(Error::custom(
                        "firmware-disk boot must not contain kernel, initrd, or cmdline",
                    ));
                }
                Ok(Self::Bios(BootBios {
                    protocol: BOOT_FIRMWARE_DISK_BIOS.into(),
                    disk: raw.disk,
                }))
            }
            BOOT_FIRMWARE_DISK_UEFI => {
                let raw: Raw = serde_json::from_value(value).map_err(Error::custom)?;
                if raw.kernel.is_some() || raw.initrd.is_some() || raw.cmdline.is_some() {
                    return Err(Error::custom(
                        "firmware-disk boot must not contain kernel, initrd, or cmdline",
                    ));
                }
                Ok(Self::Uefi(BootUefi {
                    protocol: BOOT_FIRMWARE_DISK_UEFI.into(),
                    disk: raw.disk,
                }))
            }
            BOOT_LINUX_DIRECT => {
                let raw: Raw = serde_json::from_value(value).map_err(Error::custom)?;
                Ok(Self::Linux(BootLinux {
                    protocol: BOOT_LINUX_DIRECT.into(),
                    disk: raw.disk,
                    kernel: raw.kernel.ok_or_else(|| Error::missing_field("kernel"))?,
                    initrd: raw.initrd.ok_or_else(|| Error::missing_field("initrd"))?,
                    cmdline: raw.cmdline.ok_or_else(|| Error::missing_field("cmdline"))?,
                }))
            }
            _ => Ok(Self::Unknown(value)),
        }
    }
}

impl Boot {
    pub fn protocol(&self) -> &str {
        match self {
            Self::Bios(v) => &v.protocol,
            Self::Uefi(v) => &v.protocol,
            Self::Linux(v) => &v.protocol,
            Self::Unknown(v) => v.get("protocol").and_then(|p| p.as_str()).unwrap_or(""),
        }
    }

    pub fn disk(&self) -> &str {
        match self {
            Self::Bios(v) => &v.disk,
            Self::Uefi(v) => &v.disk,
            Self::Linux(v) => &v.disk,
            Self::Unknown(_) => "",
        }
    }

    /// False only for `Unknown`.
    pub fn is_recognized(&self) -> bool {
        !matches!(self, Self::Unknown(_))
    }

    fn validate_into(&self, field: &str, validation: &mut Validation) {
        match self {
            Self::Bios(value) => {
                if value.protocol != BOOT_FIRMWARE_DISK_BIOS {
                    validation.error(format!(
                        "{field} BIOS protocol must be '{BOOT_FIRMWARE_DISK_BIOS}'"
                    ));
                }
                validation.required(&value.disk, &format!("{field}.disk"));
            }
            Self::Uefi(value) => {
                if value.protocol != BOOT_FIRMWARE_DISK_UEFI {
                    validation.error(format!(
                        "{field} UEFI protocol must be '{BOOT_FIRMWARE_DISK_UEFI}'"
                    ));
                }
                validation.required(&value.disk, &format!("{field}.disk"));
            }
            Self::Linux(value) => {
                if value.protocol != BOOT_LINUX_DIRECT {
                    validation.error(format!(
                        "{field} direct Linux protocol must be '{BOOT_LINUX_DIRECT}'"
                    ));
                }
                validation.required(&value.disk, &format!("{field}.disk"));
                value
                    .kernel
                    .validate_into(&format!("{field}.kernel"), validation);
                value
                    .initrd
                    .validate_into(&format!("{field}.initrd"), validation);
                validation.required(&value.cmdline, &format!("{field}.cmdline"));
            }
            // Nothing to check: unrecognized entries are tolerated, not inspected.
            Self::Unknown(_) => {}
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Architecture {
    Amd64,
    Arm64,
    Loong64,
    Ppc64le,
    Riscv64,
    S390x,
}

impl Architecture {
    /// The architecture this process is running on, if it's one we know.
    pub fn host() -> Option<Self> {
        match std::env::consts::ARCH {
            "x86_64" => Some(Self::Amd64),
            "aarch64" => Some(Self::Arm64),
            "loongarch64" => Some(Self::Loong64),
            "riscv64" => Some(Self::Riscv64),
            "s390x" => Some(Self::S390x),
            "powerpc64" if cfg!(target_endian = "little") => Some(Self::Ppc64le),
            _ => None,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct System {
    pub os: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
    pub version: String,
    pub architecture: Architecture,
}

impl System {
    fn validate_into(&self, field: &str, validation: &mut Validation) {
        validation.required(&self.os, &format!("{field}.os"));
        if let Some(name) = &self.name {
            validation.required(name, &format!("{field}.name"));
        }
        validation.required(&self.version, &format!("{field}.version"));
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Disk {
    pub id: String,
    pub format: DiskFormat,
    pub path: String,
    pub digest: Digest,
    pub virt_size: u64,
    pub disk_size: u64,
}

impl Disk {
    fn validate_into(&self, field: &str, validation: &mut Validation) {
        validation.required(&self.id, &format!("{field}.id"));
        validation.path(&self.path, &format!("{field}.path"));
        self.digest
            .validate_into(&format!("{field}.digest"), validation);
        if self.virt_size == 0 {
            validation.error(format!("{field}.virtSize must be greater than zero"));
        }
        if self.disk_size == 0 {
            validation.error(format!("{field}.diskSize must be greater than zero"));
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(untagged, rename_all = "camelCase")]
pub enum Access {
    Ssh {
        #[serde(rename = "type")]
        kind: String,
        port: u16,
        user: String,
        auth: String,
    },
    /// qemu-guest-agent. The hypervisor backend chooses and configures a
    /// compatible transport; the image only promises that the agent runs.
    Qga {
        #[serde(rename = "type")]
        kind: String,
    },
    /// Unrecognized access surface, preserved verbatim.
    Unknown(serde_json::Value),
}

impl<'de> Deserialize<'de> for Access {
    fn deserialize<D: serde::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        use serde::de::Error;

        let value = serde_json::Value::deserialize(deserializer)?;
        let kind = value.get("type").and_then(|t| t.as_str()).unwrap_or("");

        match kind {
            "ssh" => {
                #[derive(Deserialize)]
                struct Raw {
                    port: u16,
                    user: String,
                    auth: String,
                }
                let raw: Raw = serde_json::from_value(value).map_err(Error::custom)?;
                Ok(Self::Ssh {
                    kind: "ssh".into(),
                    port: raw.port,
                    user: raw.user,
                    auth: raw.auth,
                })
            }
            "qga" => Ok(Self::Qga { kind: "qga".into() }),
            _ => Ok(Self::Unknown(value)),
        }
    }
}

impl Access {
    /// False only for `Unknown`.
    pub fn is_recognized(&self) -> bool {
        !matches!(self, Self::Unknown(_))
    }

    fn validate_into(&self, field: &str, validation: &mut Validation) {
        match self {
            Self::Ssh {
                port, user, auth, ..
            } => {
                if *port == 0 {
                    validation.error(format!("{field}.port must be greater than zero"));
                }
                validation.required(user, &format!("{field}.user"));
                validation.required(auth, &format!("{field}.auth"));
            }
            Self::Qga { .. } | Self::Unknown(_) => {}
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct NetworkDhcp {}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct NetworkStatic {
    pub address: String,
    pub gateway: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(tag = "mode", rename_all = "lowercase")]
pub enum Network {
    Dhcp(NetworkDhcp),
    Static(NetworkStatic),
}

impl Network {
    fn validate_into(&self, field: &str, validation: &mut Validation) {
        if let Self::Static(network) = self {
            validation.required(&network.address, &format!("{field}.address"));
            validation.required(&network.gateway, &format!("{field}.gateway"));
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Machine {
    pub cpu: ResourceRange,
    pub ram: ResourceRange,
    pub boot: Vec<Boot>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub access: Vec<Access>,
    pub network: Network,
}

impl Machine {
    fn validate_into(&self, field: &str, validation: &mut Validation) {
        self.cpu.validate_into(&format!("{field}.cpu"), validation);
        self.ram.validate_into(&format!("{field}.ram"), validation);
        if !self.boot.iter().any(Boot::is_recognized) {
            validation.error(format!(
                "{field}.boot must contain at least one supported boot protocol"
            ));
        }
        for (index, boot) in self.boot.iter().enumerate() {
            boot.validate_into(&format!("{field}.boot[{index}]"), validation);
        }
        if !self.access.is_empty() && !self.access.iter().any(Access::is_recognized) {
            validation.error(format!(
                "{field}.access must contain at least one supported access method"
            ));
        }
        for (index, access) in self.access.iter().enumerate() {
            access.validate_into(&format!("{field}.access[{index}]"), validation);
        }
        self.network
            .validate_into(&format!("{field}.network"), validation);
    }
}

#[derive(Default)]
struct Validation {
    issues: Vec<String>,
}

impl Validation {
    fn error(&mut self, message: impl Into<String>) {
        self.issues.push(message.into());
    }

    fn required(&mut self, value: &str, field: &str) {
        if value.trim().is_empty() {
            self.error(format!("{field} must not be empty"));
        }
    }

    fn path(&mut self, value: &str, field: &str) {
        let path = Path::new(value);
        if value.is_empty()
            || path.is_absolute()
            || path
                .components()
                .any(|component| !matches!(component, Component::Normal(_)))
        {
            self.error(format!("{field} must be a relative normalized path"));
        }
    }

    fn finish(self) -> Result<(), ValidationError> {
        if self.issues.is_empty() {
            Ok(())
        } else {
            Err(ValidationError {
                issues: self.issues,
            })
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ValidationError {
    issues: Vec<String>,
}

impl ValidationError {
    fn single(message: impl Into<String>) -> Self {
        Self {
            issues: vec![message.into()],
        }
    }

    pub fn issues(&self) -> &[String] {
        &self.issues
    }
}

impl fmt::Display for ValidationError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            f,
            "invalid Mirum VM configuration: {}",
            self.issues.join("; ")
        )
    }
}

impl std::error::Error for ValidationError {}

#[derive(Debug)]
pub enum ParseError {
    Json(serde_json::Error),
    Validation(ValidationError),
}

impl fmt::Display for ParseError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Json(error) => write!(f, "invalid JSON: {error}"),
            Self::Validation(error) => error.fmt(f),
        }
    }
}

impl std::error::Error for ParseError {}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct MachineImage {
    pub schema_version: u8,
    pub kind: String,
    pub system: System,
    pub machine: Machine,
    pub disks: Vec<Disk>,
}

impl MachineImage {
    pub fn from_json(data: &[u8]) -> Result<Self, ParseError> {
        #[derive(Deserialize)]
        #[serde(rename_all = "camelCase")]
        struct Header {
            schema_version: u8,
            #[serde(default)]
            kind: Option<String>,
        }

        let header: Header = serde_json::from_slice(data).map_err(ParseError::Json)?;
        if header.schema_version != SCHEMA_VERSION {
            return Err(ParseError::Validation(ValidationError::single(format!(
                "unsupported schemaVersion {}",
                header.schema_version
            ))));
        }
        if header.kind.as_deref() != Some(KIND) {
            return Err(ParseError::Validation(ValidationError::single(format!(
                "kind must be '{KIND}'"
            ))));
        }

        let image: Self = serde_json::from_slice(data).map_err(ParseError::Json)?;
        image.validate().map_err(ParseError::Validation)?;
        Ok(image)
    }

    pub fn validate(&self) -> Result<(), ValidationError> {
        if self.schema_version != SCHEMA_VERSION {
            return Err(ValidationError::single(format!(
                "unsupported schemaVersion {}",
                self.schema_version
            )));
        }
        if self.kind != KIND {
            return Err(ValidationError::single(format!("kind must be '{KIND}'")));
        }

        let mut validation = Validation::default();
        self.system.validate_into("system", &mut validation);
        self.machine.validate_into("machine", &mut validation);

        if self.disks.is_empty() {
            validation.error("disks must not be empty");
        }
        let mut disk_ids = HashSet::new();
        let mut paths = HashSet::new();
        for (index, disk) in self.disks.iter().enumerate() {
            let at = format!("disks[{index}]");
            disk.validate_into(&at, &mut validation);
            if !disk_ids.insert(disk.id.as_str()) {
                validation.error(format!("duplicate disk id '{}'", disk.id));
            }
            if !paths.insert(disk.path.as_str()) {
                validation.error(format!("duplicate image path '{}'", disk.path));
            }
        }

        let mut boot_protocols = HashSet::new();
        for (index, boot) in self.machine.boot.iter().enumerate() {
            if !boot.is_recognized() {
                // Tolerated, not cross-referenced: we don't know what its
                // fields (e.g. "disk") even mean.
                continue;
            }
            if !boot_protocols.insert(boot.protocol()) {
                validation.error(format!("duplicate boot protocol '{}'", boot.protocol()));
            }
            if !disk_ids.contains(boot.disk()) {
                validation.error(format!(
                    "machine.boot[{index}] references unknown disk '{}'",
                    boot.disk()
                ));
            }
            if let Boot::Linux(linux) = boot {
                if self.system.os != "linux" {
                    validation.error(format!(
                        "machine.boot[{index}] protocol '{BOOT_LINUX_DIRECT}' requires system.os 'linux'"
                    ));
                }
                for path in [&linux.kernel.path, &linux.initrd.path] {
                    if !paths.insert(path) {
                        validation.error(format!("duplicate image path '{path}'"));
                    }
                }
            }
        }

        validation.finish()
    }

    pub fn referenced_paths(&self) -> impl Iterator<Item = &str> {
        self.disks.iter().map(|d| d.path.as_str()).chain(
            self.machine
                .boot
                .iter()
                .flat_map(|boot| match boot {
                    Boot::Bios(_) | Boot::Uefi(_) | Boot::Unknown(_) => [None, None],
                    Boot::Linux(v) => [Some(v.kernel.path.as_str()), Some(v.initrd.path.as_str())],
                })
                .flatten(),
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn valid_json() -> Vec<u8> {
        br#"{"schemaVersion":1,"kind":"MachineImage","system":{"os":"linux","name":"alpine","version":"3.24","architecture":"amd64"},"machine":{"cpu":{"minimum":1,"default":2},"ram":{"minimum":268435456,"default":1073741824},"boot":[{"protocol":"firmware-disk/bios","disk":"root"}],"access":[{"type":"ssh","port":22,"user":"build","auth":"empty-password"}],"network":{"mode":"static","address":"10.0.2.15/24","gateway":"10.0.2.2"}},"disks":[{"id":"root","format":"qcow2","path":"root.hmi","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","virtSize":1024,"diskSize":512}]}"#.to_vec()
    }

    #[test]
    fn parses_and_round_trips_valid_config() {
        let image = MachineImage::from_json(&valid_json()).unwrap();
        let encoded = serde_json::to_vec(&image).unwrap();
        assert_eq!(MachineImage::from_json(&encoded).unwrap(), image);
    }

    #[test]
    fn rejects_unknown_disk_and_unsafe_path() {
        let mut image = MachineImage::from_json(&valid_json()).unwrap();
        image.disks[0].path = "../root.hmi".into();
        match &mut image.machine.boot[0] {
            Boot::Bios(v) => v.disk = "missing".into(),
            Boot::Uefi(_) | Boot::Linux(_) | Boot::Unknown(_) => unreachable!(),
        }
        let error = image.validate().unwrap_err();
        assert!(
            error
                .issues()
                .iter()
                .any(|v| v.contains("relative normalized path"))
        );
        assert!(error.issues().iter().any(|v| v.contains("unknown disk")));
    }

    #[test]
    fn rejects_invalid_digest_and_resource_range() {
        let mut image = MachineImage::from_json(&valid_json()).unwrap();
        image.disks[0].digest = Digest("md5:no".into());
        image.machine.cpu.default = 0;
        let error = image.validate().unwrap_err();
        assert_eq!(error.issues().len(), 2);
    }

    #[test]
    fn system_name_is_optional_but_not_empty() {
        let mut image = MachineImage::from_json(&valid_json()).unwrap();
        image.system.name = None;
        let encoded = serde_json::to_value(&image).unwrap();
        assert!(encoded["system"].get("name").is_none());
        image.validate().unwrap();

        image.system.name = Some(String::new());
        assert!(image.validate().is_err());
    }

    #[test]
    fn parses_uefi_firmware_disk_boot() {
        let mut image = MachineImage::from_json(&valid_json()).unwrap();
        image.machine.boot[0] = Boot::Uefi(BootUefi {
            protocol: BOOT_FIRMWARE_DISK_UEFI.into(),
            disk: "root".into(),
        });

        let encoded = serde_json::to_vec(&image).unwrap();
        let decoded = MachineImage::from_json(&encoded).unwrap();
        assert_eq!(decoded.machine.boot[0].protocol(), BOOT_FIRMWARE_DISK_UEFI);
    }

    #[test]
    fn rejects_config_whose_only_boot_entry_is_unrecognized() {
        // An unrecognized boot protocol must not fail *parsing* -- see
        // `mixed_boot_list_tolerates_unrecognized_entries` below for why --
        // but a config with *no* recognized entry at all is unusable and
        // must fail *validation*.
        let json = String::from_utf8(valid_json())
            .unwrap()
            .replace(BOOT_FIRMWARE_DISK_BIOS, "firmware-disk/unknown");
        let error = MachineImage::from_json(json.as_bytes()).unwrap_err();
        assert!(
            error
                .to_string()
                .contains("must contain at least one supported boot protocol")
        );
    }

    #[test]
    fn mixed_boot_list_tolerates_unrecognized_entries() {
        // A newer image may list a protocol this version doesn't know about
        // alongside one it does; parsing and validation must both succeed,
        // and the unrecognized entry must round-trip losslessly.
        let json = String::from_utf8(valid_json()).unwrap().replace(
            r#"[{"protocol":"firmware-disk/bios","disk":"root"}]"#,
            r#"[{"protocol":"firmware-disk/bios","disk":"root"},{"protocol":"vsock/direct","disk":"root","futureField":42}]"#,
        );
        let image = MachineImage::from_json(json.as_bytes()).unwrap();
        assert_eq!(image.machine.boot.len(), 2);
        assert!(image.machine.boot[0].is_recognized());
        assert!(!image.machine.boot[1].is_recognized());

        let encoded = serde_json::to_vec(&image).unwrap();
        assert_eq!(MachineImage::from_json(&encoded).unwrap(), image);
    }

    #[test]
    fn rejects_config_whose_only_access_entry_is_unrecognized() {
        let json = String::from_utf8(valid_json()).unwrap().replace(
            r#"{"type":"ssh","port":22,"user":"build","auth":"empty-password"}"#,
            r#"{"type":"vsock-agent","channel":9000}"#,
        );
        let error = MachineImage::from_json(json.as_bytes()).unwrap_err();
        assert!(
            error
                .to_string()
                .contains("must contain at least one supported access method")
        );
    }

    #[test]
    fn parses_qga_access() {
        let json = String::from_utf8(valid_json()).unwrap().replace(
            r#"[{"type":"ssh","port":22,"user":"build","auth":"empty-password"}]"#,
            r#"[{"type":"ssh","port":22,"user":"build","auth":"empty-password"},{"type":"qga"}]"#,
        );
        let image = MachineImage::from_json(json.as_bytes()).unwrap();
        assert_eq!(image.machine.access.len(), 2);
        assert!(matches!(image.machine.access[1], Access::Qga { .. }));
    }

    #[test]
    fn access_may_be_omitted_for_a_black_box_image() {
        let json = String::from_utf8(valid_json()).unwrap().replace(
            r#","access":[{"type":"ssh","port":22,"user":"build","auth":"empty-password"}]"#,
            "",
        );
        let image = MachineImage::from_json(json.as_bytes()).unwrap();
        assert!(image.machine.access.is_empty());
        assert!(
            serde_json::to_value(image).unwrap()["machine"]
                .get("access")
                .is_none()
        );
    }

    #[test]
    fn parses_dhcp_network_without_static_parameters() {
        let json = String::from_utf8(valid_json()).unwrap().replace(
            r#"{"mode":"static","address":"10.0.2.15/24","gateway":"10.0.2.2"}"#,
            r#"{"mode":"dhcp"}"#,
        );
        let image = MachineImage::from_json(json.as_bytes()).unwrap();
        assert!(matches!(image.machine.network, Network::Dhcp(_)));
    }

    #[test]
    fn rejects_unknown_schema_before_parsing_schema_fields() {
        let error = MachineImage::from_json(br#"{"schemaVersion":2}"#).unwrap_err();
        let ParseError::Validation(error) = error else {
            panic!("expected validation error")
        };
        assert_eq!(error.issues(), ["unsupported schemaVersion 2"]);
    }

    #[test]
    fn rejects_unknown_kind_before_parsing_kind_fields() {
        let error = MachineImage::from_json(br#"{"schemaVersion":1,"kind":"Other"}"#).unwrap_err();
        let ParseError::Validation(error) = error else {
            panic!("expected validation error")
        };
        assert_eq!(error.issues(), ["kind must be 'MachineImage'"]);
    }

    #[test]
    fn rejects_unknown_architecture() {
        let json = String::from_utf8(valid_json())
            .unwrap()
            .replace("\"architecture\":\"amd64\"", "\"architecture\":\"mips\"");
        let error = MachineImage::from_json(json.as_bytes()).unwrap_err();
        assert!(error.to_string().contains("unknown variant `mips`"));
    }

    #[test]
    fn parses_additional_oci_architectures() {
        for architecture in ["loong64", "riscv64", "s390x"] {
            let json = String::from_utf8(valid_json()).unwrap().replace(
                "\"architecture\":\"amd64\"",
                &format!("\"architecture\":\"{architecture}\""),
            );
            MachineImage::from_json(json.as_bytes()).unwrap();
        }
    }
}
