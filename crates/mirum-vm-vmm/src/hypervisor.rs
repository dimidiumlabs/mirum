// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

use std::fmt;
use std::path::Path;

use async_trait::async_trait;
use mirum_vm_hmi::{Boot, MachineImage};

use crate::{Machine, MachineId, R, Settings};

/// Self-assigned backend identity, unique only within one `Monitor`
/// (checked in [`crate::Monitor::new`]) -- plugins can't coordinate globally.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub struct HypervisorId(pub u128);

impl fmt::Display for HypervisorId {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.0)
    }
}

/// A single hypervisor backend (qemu, firecracker, ...).
#[async_trait]
pub trait Hypervisor: Send + Sync {
    fn id(&self) -> HypervisorId;
    fn name(&self) -> &'static str;

    /// Whether this backend can boot the given protocol.
    fn supports(&self, boot: &Boot) -> bool;

    /// Create a machine bound to `dir`. Does not start it.
    async fn create(
        &self,
        id: MachineId,
        name: Option<&str>,
        image: &MachineImage,
        dir: &Path,
        boot: &Boot,
        settings: &Settings,
    ) -> R<Box<dyn Machine>>;

    /// Reconnect to a machine this backend previously created, e.g. after a
    /// restart. `id`/`name`/`settings` come from the monitor's own record.
    async fn reattach(
        &self,
        id: MachineId,
        name: Option<&str>,
        dir: &Path,
        settings: &Settings,
    ) -> R<Box<dyn Machine>>;
}
