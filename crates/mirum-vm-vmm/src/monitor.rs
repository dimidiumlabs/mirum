// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

use std::path::{Path, PathBuf};

use mirum_vm_hmi::MachineImage;
use tokio::sync::mpsc::Receiver;
use uuid::Uuid;

use crate::{Error, Hypervisor, Machine, MachineId, R, Settings, State};

/// A fleet-wide state change, as delivered by [`Monitor::events`].
#[derive(Debug, Clone)]
pub struct Event {
    pub machine: MachineId,
    pub kind: EventKind,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum EventKind {
    Created,
    Started,
    Stopped,
    Killed,
    Restarted,
    Paused,
    Unpaused,
    Removed,
}

/// Tracks a fleet of machines, possibly across different hypervisors.
pub struct Monitor {
    #[allow(dead_code)] // read by list()/get() once persisted state lands
    run_dir: PathBuf,

    hypervisors: Vec<Box<dyn Hypervisor>>,
}

impl Monitor {
    pub fn new(run_dir: PathBuf, hypervisors: Vec<Box<dyn Hypervisor>>) -> R<Self> {
        let mut seen = std::collections::HashSet::new();
        for hv in &hypervisors {
            if !seen.insert(hv.id()) {
                return Err(Error::DuplicateHypervisor(hv.id()));
            }
        }
        Ok(Self {
            run_dir,
            hypervisors,
        })
    }

    /// Picks the first (hypervisor, boot protocol) pair the hypervisor
    /// supports and creates through it. Manifest boot order isn't significant.
    pub async fn create(
        &self,
        image: &MachineImage,
        dir: &Path,
        name: Option<&str>,
        settings: &Settings,
    ) -> R<Box<dyn Machine>> {
        let id = MachineId(Uuid::now_v7());
        let (hv, boot) = image
            .machine
            .boot
            .iter()
            .filter(|b| b.is_recognized())
            .find_map(|b| {
                self.hypervisors
                    .iter()
                    .find(|hv| hv.supports(b))
                    .map(|hv| (hv, b))
            })
            .ok_or_else(|| {
                Error::Unsupported(
                    "no registered hypervisor supports any declared boot protocol".into(),
                )
            })?;
        hv.create(id, name, image, dir, boot, settings).await
    }

    /// All machines known to this monitor, running or not.
    pub async fn list(&self) -> R<Vec<Box<dyn Machine>>> {
        todo!("needs a persisted run-state format -- see open design question")
    }

    pub async fn get(&self, _id: MachineId) -> R<Box<dyn Machine>> {
        todo!("needs a persisted run-state format -- see open design question")
    }

    /// Live stream of fleet-wide state changes. Needs a publish path from
    /// `Machine` impls -- same open question as `list`/`get`.
    pub async fn events(&self) -> R<Receiver<Event>> {
        todo!("needs a publish path from Machine implementations")
    }

    /// Deletes every non-running machine (`list` + `delete`; no hypervisor
    /// has a native bulk op). Returns the count deleted.
    pub async fn prune(&self) -> R<usize> {
        let mut deleted = 0;
        for mut machine in self.list().await? {
            if machine.state().await != State::Running {
                machine.delete().await?;
                deleted += 1;
            }
        }
        Ok(deleted)
    }
}
