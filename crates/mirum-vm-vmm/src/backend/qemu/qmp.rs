// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

//! Minimal QMP client: line-delimited JSON commands/responses over the TCP
//! loopback channel qemu exposes via
//! `-qmp tcp:127.0.0.1:<port>,server=on,wait=off`. Events are read and
//! discarded, not surfaced.

use serde::Deserialize;
use serde_json::{Value, json};
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::net::TcpStream;
use tokio::net::tcp::{OwnedReadHalf, OwnedWriteHalf};

use crate::{Error, R};

pub struct Qmp {
    reader: BufReader<OwnedReadHalf>,
    writer: OwnedWriteHalf,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Status {
    Running,
    Paused,
    Transitional,
}

impl Status {
    fn from_wire(status: &str) -> Self {
        match status {
            "running" => Self::Running,
            "paused" => Self::Paused,
            _ => Self::Transitional,
        }
    }
}

impl Qmp {
    /// Connects and completes the capabilities handshake (greeting ->
    /// `qmp_capabilities`), after which arbitrary commands are allowed.
    pub async fn connect(port: u16) -> R<Self> {
        let (read_half, write_half) = TcpStream::connect(("127.0.0.1", port)).await?.into_split();
        let mut qmp = Self {
            reader: BufReader::new(read_half),
            writer: write_half,
        };
        qmp.read_message().await?; // greeting
        qmp.execute("qmp_capabilities", None).await?;
        Ok(qmp)
    }

    pub async fn status(&mut self) -> R<Status> {
        #[derive(Deserialize)]
        struct Response {
            status: String,
        }

        let response: Response = self.execute_typed("query-status", None).await?;
        Ok(Status::from_wire(&response.status))
    }

    pub async fn power_down(&mut self) -> R<()> {
        self.execute_empty("system_powerdown", None).await
    }

    pub async fn reset(&mut self) -> R<()> {
        self.execute_empty("system_reset", None).await
    }

    pub async fn pause(&mut self) -> R<()> {
        self.execute_empty("stop", None).await
    }

    pub async fn resume(&mut self) -> R<()> {
        self.execute_empty("cont", None).await
    }

    pub async fn set_balloon_size(&mut self, bytes: u64) -> R<()> {
        self.execute_empty("balloon", Some(json!({ "value": bytes })))
            .await
    }

    async fn read_message(&mut self) -> R<Value> {
        loop {
            let mut line = String::new();
            if self.reader.read_line(&mut line).await? == 0 {
                return Err(Error::InvalidState("qmp connection closed".into()));
            }

            let line = line.trim();
            if line.is_empty() {
                continue;
            }

            let value: Value = serde_json::from_str(line)
                .map_err(|e| Error::InvalidState(format!("invalid qmp message: {e}")))?;

            // Events arrive unprompted between a command and its response;
            // we don't surface them (yet), so skip past them here.
            if value.get("event").is_some() {
                continue;
            }

            return Ok(value);
        }
    }

    async fn execute(&mut self, command: &str, arguments: Option<Value>) -> R<Value> {
        let mut request = json!({ "execute": command });
        if let Some(arguments) = arguments {
            request["arguments"] = arguments;
        }
        let mut bytes = serde_json::to_vec(&request).expect("serializable request");
        bytes.push(b'\n');
        self.writer.write_all(&bytes).await?;
        let response = self.read_message().await?;
        if let Some(error) = response.get("error") {
            return Err(Error::InvalidState(format!(
                "qmp '{command}' failed: {error}"
            )));
        }
        response.get("return").cloned().ok_or_else(|| {
            Error::InvalidState(format!("qmp '{command}' response has no return value"))
        })
    }

    async fn execute_empty(&mut self, command: &str, arguments: Option<Value>) -> R<()> {
        self.execute(command, arguments).await.map(|_| ())
    }

    async fn execute_typed<T: for<'de> Deserialize<'de>>(
        &mut self,
        command: &str,
        arguments: Option<Value>,
    ) -> R<T> {
        let value = self.execute(command, arguments).await?;
        serde_json::from_value(value).map_err(|error| {
            Error::InvalidState(format!("invalid qmp '{command}' result: {error}"))
        })
    }
}

/// Picks a free TCP port on 127.0.0.1: binds to port 0 (OS assigns one),
/// reads it back, then drops the listener before qemu binds it for real.
/// Small TOCTOU race in principle; standard practice in the absence of a
/// way to ask qemu what port it actually bound.
pub async fn free_port() -> std::io::Result<u16> {
    Ok(tokio::net::TcpListener::bind(("127.0.0.1", 0))
        .await?
        .local_addr()?
        .port())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn wire_status_does_not_escape_the_qmp_boundary() {
        assert_eq!(Status::from_wire("running"), Status::Running);
        assert_eq!(Status::from_wire("paused"), Status::Paused);
        assert_eq!(Status::from_wire("inmigrate"), Status::Transitional);
    }
}
