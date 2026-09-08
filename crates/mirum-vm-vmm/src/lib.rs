// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

//! Machine lifecycle: hypervisor plugins, machine handles, and the monitor
//! that tracks a fleet of them.
//!
//! No OCI, registry, or storage concerns -- callers hand in an
//! already-resolved [`mirum_vm_hmi::MachineImage`] plus a directory of its
//! files.
//!
//! `Machine`/`Monitor` mirror Docker's container API term-for-term.
//! Excluded: `changes`/`export`/`archive` (no host-visible VM filesystem
//! without guest cooperation), `top` (guest internals are opaque to the
//! host by construction -- use `exec`).

pub mod backend;
mod hypervisor;
mod machine;
mod monitor;

use std::fmt;

pub use hypervisor::{Hypervisor, HypervisorId};
pub use machine::{Console, Machine, MachineId, Settings, State, Stats};
pub use monitor::Monitor;

#[derive(Debug)]
pub enum Error {
    /// No registered hypervisor supports any boot protocol the image declares.
    Unsupported(String),

    /// The image is not resolvable in this context (e.g. `boot` references an unknown disk id).
    InvalidImage(String),

    /// The machine isn't in the state the operation requires.
    InvalidState(String),

    /// No machine matches the given id.
    NotFound(MachineId),

    /// Two registered hypervisors share a `HypervisorId`.
    DuplicateHypervisor(HypervisorId),

    /// Underlying OS/process failure.
    Io(std::io::Error),
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Unsupported(msg) => write!(f, "unsupported: {msg}"),
            Self::InvalidImage(msg) => write!(f, "invalid image: {msg}"),
            Self::InvalidState(msg) => write!(f, "invalid state: {msg}"),
            Self::NotFound(id) => write!(f, "no machine '{id}'"),
            Self::DuplicateHypervisor(id) => {
                write!(
                    f,
                    "duplicate hypervisor id {id} registered with this monitor"
                )
            }
            Self::Io(err) => write!(f, "{err}"),
        }
    }
}

impl std::error::Error for Error {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            Self::Io(err) => Some(err),
            _ => None,
        }
    }
}

impl From<std::io::Error> for Error {
    fn from(err: std::io::Error) -> Self {
        Self::Io(err)
    }
}

pub type R<T> = std::result::Result<T, Error>;
