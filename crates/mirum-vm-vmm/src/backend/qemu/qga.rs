// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

//! Minimal qemu-guest-agent client over qemu's host-side chardev socket.

use std::time::{Duration, Instant};

use base64::Engine;
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::net::TcpStream;

use crate::{Error, R};

async fn execute(
    port: u16,
    command: &str,
    arguments: Option<serde_json::Value>,
) -> R<serde_json::Value> {
    tokio::time::timeout(
        Duration::from_secs(5),
        execute_without_timeout(port, command, arguments),
    )
    .await
    .map_err(|_| Error::InvalidState(format!("qga {command} timed out")))?
}

async fn execute_without_timeout(
    port: u16,
    command: &str,
    arguments: Option<serde_json::Value>,
) -> R<serde_json::Value> {
    let stream = TcpStream::connect(("127.0.0.1", port)).await?;
    let mut stream = BufReader::new(stream);
    let mut request = serde_json::json!({ "execute": command });
    if let Some(arguments) = arguments {
        request["arguments"] = arguments;
    }
    stream
        .get_mut()
        .write_all(
            format!(
                "{}\n",
                serde_json::to_string(&request)
                    .map_err(|error| Error::InvalidState(error.to_string()))?
            )
            .as_bytes(),
        )
        .await?;

    let mut response = String::new();
    stream.read_line(&mut response).await?;
    if response.is_empty() {
        return Err(Error::InvalidState(
            "qemu-guest-agent closed its channel without replying".into(),
        ));
    }
    validate_response(command, &response)
}

fn validate_response(command: &str, response: &str) -> R<serde_json::Value> {
    let response: serde_json::Value = serde_json::from_str(response)
        .map_err(|error| Error::InvalidState(format!("invalid qga response: {error}")))?;
    if let Some(error) = response.get("error") {
        return Err(Error::InvalidState(format!(
            "qga {command} failed: {error}"
        )));
    }
    response
        .get("return")
        .cloned()
        .ok_or_else(|| Error::InvalidState(format!("qga {command} response has no return value")))
}

async fn ping(port: u16) -> R<()> {
    execute(port, "guest-ping", None).await.map(|_| ())
}

pub async fn wait_until_ready(port: u16, timeout: Duration) -> R<()> {
    let deadline = Instant::now() + timeout;
    loop {
        let attempt = tokio::time::timeout(Duration::from_secs(1), ping(port)).await;
        if matches!(attempt, Ok(Ok(()))) {
            return Ok(());
        }
        if Instant::now() >= deadline {
            return Err(Error::InvalidState(format!(
                "declared qga access did not become ready within {} seconds",
                timeout.as_secs()
            )));
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
}

pub async fn exec(port: u16, command: &[String]) -> R<std::process::Output> {
    let (path, args) = command
        .split_first()
        .ok_or_else(|| Error::InvalidState("exec command must not be empty".into()))?;
    let started = execute(
        port,
        "guest-exec",
        Some(serde_json::json!({
            "path": path,
            "arg": args,
            "capture-output": true
        })),
    )
    .await?;
    let pid = started
        .get("pid")
        .and_then(serde_json::Value::as_u64)
        .ok_or_else(|| Error::InvalidState("qga guest-exec returned no pid".into()))?;

    loop {
        let status = execute(
            port,
            "guest-exec-status",
            Some(serde_json::json!({ "pid": pid })),
        )
        .await?;
        if !status
            .get("exited")
            .and_then(serde_json::Value::as_bool)
            .unwrap_or(false)
        {
            tokio::time::sleep(Duration::from_millis(20)).await;
            continue;
        }

        return output(&status);
    }
}

fn output(status: &serde_json::Value) -> R<std::process::Output> {
    for field in ["out-truncated", "err-truncated"] {
        if status
            .get(field)
            .and_then(serde_json::Value::as_bool)
            .unwrap_or(false)
        {
            return Err(Error::InvalidState(format!(
                "qga exec output was truncated ({field})"
            )));
        }
    }
    let decode = |field: &str| -> R<Vec<u8>> {
        let Some(encoded) = status.get(field).and_then(serde_json::Value::as_str) else {
            return Ok(Vec::new());
        };
        base64::engine::general_purpose::STANDARD
            .decode(encoded)
            .map_err(|error| Error::InvalidState(format!("invalid qga {field}: {error}")))
    };
    Ok(std::process::Output {
        status: exit_status(status)?,
        stdout: decode("out-data")?,
        stderr: decode("err-data")?,
    })
}

fn exit_status(status: &serde_json::Value) -> R<std::process::ExitStatus> {
    #[cfg(unix)]
    {
        use std::os::unix::process::ExitStatusExt;
        if let Some(code) = status.get("exitcode").and_then(serde_json::Value::as_i64) {
            let code: i32 = code
                .try_into()
                .map_err(|_| Error::InvalidState("qga returned an invalid exit code".into()))?;
            return Ok(std::process::ExitStatus::from_raw(code << 8));
        }
        if let Some(signal) = status.get("signal").and_then(serde_json::Value::as_i64) {
            let signal: i32 = signal
                .try_into()
                .map_err(|_| Error::InvalidState("qga returned an invalid signal".into()))?;
            return Ok(std::process::ExitStatus::from_raw(signal & 0x7f));
        }
        Err(Error::InvalidState(
            "qga exec status has neither exitcode nor signal".into(),
        ))
    }
    #[cfg(windows)]
    {
        use std::os::windows::process::ExitStatusExt;
        let code = status
            .get("exitcode")
            .and_then(serde_json::Value::as_i64)
            .ok_or_else(|| Error::InvalidState("qga exec status has no exitcode".into()))?;
        let code: u32 = code
            .try_into()
            .map_err(|_| Error::InvalidState("qga returned an invalid exit code".into()))?;
        Ok(std::process::ExitStatus::from_raw(code))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn readiness_requires_a_successful_guest_ping_response() {
        validate_response("guest-ping", r#"{"return":{}}"#).unwrap();
        assert!(
            validate_response("guest-ping", r#"{"error":{"class":"CommandNotFound"}}"#).is_err()
        );
        assert!(validate_response("guest-ping", r#"{"event":"something"}"#).is_err());
    }

    #[test]
    fn exec_output_is_decoded_without_losing_exit_status() {
        let output = output(&serde_json::json!({
            "exited": true,
            "exitcode": 7,
            "out-data": "aGVsbG8K",
            "err-data": "b29wcwo="
        }))
        .unwrap();
        assert_eq!(output.status.code(), Some(7));
        assert_eq!(output.stdout, b"hello\n");
        assert_eq!(output.stderr, b"oops\n");
    }

    #[test]
    fn truncated_exec_output_is_an_error() {
        assert!(
            output(&serde_json::json!({
                "exited": true,
                "exitcode": 0,
                "out-truncated": true
            }))
            .is_err()
        );
    }
}
