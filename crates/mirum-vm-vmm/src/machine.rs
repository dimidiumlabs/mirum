// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

use std::fmt;
use std::process::{ExitStatus, Output};

use async_trait::async_trait;
use tokio::io::{AsyncRead, AsyncWrite};
use uuid::Uuid;

use crate::{HypervisorId, R};

/// Always a UUIDv7 from [`crate::Monitor::create`], so ids sort by creation
/// time. An optional, non-unique name can ride alongside -- see
/// [`Machine::name`].
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub struct MachineId(pub Uuid);

impl fmt::Display for MachineId {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.0)
    }
}

/// Coarse machine state, mirroring Docker's container states.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum State {
    Created,
    Running,
    Paused,
    Restarting,
    Exited(i32),
    Dead,
}

/// A live duplex connection to a machine's interactive console.
pub trait Console: AsyncRead + AsyncWrite + Send + Unpin {}
impl<T: AsyncRead + AsyncWrite + Send + Unpin> Console for T {}

/// Deployment-time config of one instance -- distinct from
/// [`mirum_vm_hmi::MachineImage`], which only declares a cpu/ram *range* and
/// knows nothing about e.g. host ports. Always concrete: callers resolve
/// image defaults before constructing this.
#[derive(Debug, Clone, PartialEq)]
pub struct Settings {
    pub cpu: u32,
    pub ram: u64, // megabytes

    /// (host, guest) TCP port pairs forwarded to the guest's access channel.
    pub port_forwards: Vec<(u16, u16)>,
}

/// A snapshot of resource usage, as of the call to `Machine::stats`.
#[derive(Debug, Clone, Copy)]
pub struct Stats {
    pub cpu_time_ns: u64,
    pub memory_bytes: u64,
}

/// A handle to one virtual machine. Method names follow the Docker Engine
/// API's container operations (see the crate docs for the two exclusions).
///
/// Everything that can touch disk or a socket is `async` -- this crate runs
/// on tokio throughout, no blocking calls.
#[async_trait]
pub trait Machine: Send + Sync {
    fn id(&self) -> MachineId;
    fn hid(&self) -> HypervisorId;

    /// Optional, caller-chosen, not required to be unique.
    fn name(&self) -> Option<&str>;

    /// In-memory, not the persisted record -- see `update`/`rename` for why
    /// those are `async` while this isn't.
    fn settings(&self) -> Settings;

    async fn state(&self) -> State;
    async fn stats(&self) -> R<Stats>;
    async fn logs(&self) -> R<Vec<u8>>;

    async fn start(&mut self) -> R<()>;
    async fn stop(&mut self) -> R<()>;
    async fn kill(&mut self) -> R<()>;
    async fn restart(&mut self) -> R<()>;
    async fn delete(&mut self) -> R<()>;

    async fn pause(&mut self) -> R<()>;
    async fn unpause(&mut self) -> R<()>;

    async fn update(&mut self, settings: Settings) -> R<()>;
    async fn rename(&mut self, name: Option<&str>) -> R<()>;

    async fn exec(&self, cmd: &[String]) -> R<Output>;
    async fn wait(&mut self) -> R<ExitStatus>;
    async fn attach(&self) -> R<Box<dyn Console>>;
}
