// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

use serde::Deserialize;

#[derive(Debug, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct Server {
    pub addr: std::net::SocketAddr,
    #[serde(deserialize_with = "deserialize_authorities")]
    pub hostnames: Vec<axum::http::uri::Authority>,
    #[serde(with = "humantime_serde")]
    pub header_read_timeout: std::time::Duration,
    pub http1_max_buffer_bytes: bytesize::ByteSize,
    pub http2_max_concurrent_streams: u32,
    pub http2_max_header_list_bytes: bytesize::ByteSize,
    #[serde(with = "humantime_serde")]
    pub request_body_idle_timeout: std::time::Duration,
    pub request_body_max_bytes: bytesize::ByteSize,
    pub trusted_proxies: Vec<ipnet::IpNet>,
    pub compression_min_bytes: bytesize::ByteSize,
    pub compression_level: u8,
    pub max_concurrent_requests: usize,
    pub max_queued_requests: usize,
    #[serde(with = "humantime_serde")]
    pub admission_wait: std::time::Duration,
    #[serde(with = "humantime_serde")]
    pub shutdown_timeout: std::time::Duration,
}

impl Default for Server {
    fn default() -> Self {
        Self {
            addr: "127.0.0.1:8080"
                .parse()
                .expect("default server address is valid"),
            hostnames: Vec::new(),
            header_read_timeout: std::time::Duration::from_secs(10),
            http1_max_buffer_bytes: bytesize::ByteSize::kib(32),
            http2_max_concurrent_streams: 64,
            http2_max_header_list_bytes: bytesize::ByteSize::kib(16),
            request_body_idle_timeout: std::time::Duration::from_secs(60),
            request_body_max_bytes: bytesize::ByteSize::gib(1),
            trusted_proxies: Vec::new(),
            compression_min_bytes: bytesize::ByteSize::b(128),
            compression_level: 5,
            max_concurrent_requests: 64,
            max_queued_requests: 128,
            admission_wait: std::time::Duration::from_secs(1),
            shutdown_timeout: std::time::Duration::from_secs(25),
        }
    }
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Database {
    pub url: String,
    #[serde(default = "default_max_connections")]
    pub max_connections: u32,
    #[serde(default = "default_connect_timeout_seconds")]
    pub connect_timeout_seconds: u64,
}

const fn default_max_connections() -> u32 {
    10
}

const fn default_connect_timeout_seconds() -> u64 {
    5
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Config {
    #[serde(default)]
    pub server: Server,
    pub database: Database,
}

impl Config {
    pub fn load(path: &std::path::Path) -> Result<Self, Error> {
        let source = std::fs::read_to_string(path).map_err(|source| Error::Read {
            path: path.to_owned(),
            source,
        })?;
        let config: Self = toml::from_str(&source).map_err(|source| Error::Decode {
            path: path.to_owned(),
            source,
        })?;
        config.validate()?;
        Ok(config)
    }

    fn validate(&self) -> Result<(), Error> {
        let server = &self.server;
        if server.header_read_timeout.is_zero()
            || server.request_body_idle_timeout.is_zero()
            || server.admission_wait.is_zero()
            || server.shutdown_timeout.is_zero()
        {
            return Err(Error::Invalid("server durations must be greater than zero"));
        }
        if server.http1_max_buffer_bytes.as_u64() < 8 * 1024 {
            return Err(Error::Invalid(
                "server.http1_max_buffer_bytes must be at least 8KiB",
            ));
        }
        if server.http2_max_concurrent_streams == 0 {
            return Err(Error::Invalid(
                "server.http2_max_concurrent_streams must be greater than zero",
            ));
        }
        if server.http2_max_header_list_bytes.as_u64() == 0
            || u32::try_from(server.http2_max_header_list_bytes.as_u64()).is_err()
        {
            return Err(Error::Invalid(
                "server.http2_max_header_list_bytes must be non-zero and fit into u32",
            ));
        }
        if server.request_body_max_bytes.as_u64() == 0
            || usize::try_from(server.request_body_max_bytes.as_u64()).is_err()
        {
            return Err(Error::Invalid(
                "server.request_body_max_bytes must be non-zero and fit into usize",
            ));
        }
        if server.compression_min_bytes.as_u64() == 0
            || u16::try_from(server.compression_min_bytes.as_u64()).is_err()
        {
            return Err(Error::Invalid(
                "server.compression_min_bytes must be non-zero and fit into u16",
            ));
        }
        if server.compression_level > 22 {
            return Err(Error::Invalid(
                "server.compression_level must not exceed 22",
            ));
        }
        if server.max_concurrent_requests == 0 || server.max_queued_requests == 0 {
            return Err(Error::Invalid(
                "server request concurrency limits must be greater than zero",
            ));
        }
        if self.database.url.trim().is_empty() {
            return Err(Error::Invalid("database.url must not be empty"));
        }
        if self.database.max_connections == 0 {
            return Err(Error::Invalid(
                "database.max_connections must be greater than zero",
            ));
        }
        if self.database.connect_timeout_seconds == 0 {
            return Err(Error::Invalid(
                "database.connect_timeout_seconds must be greater than zero",
            ));
        }
        Ok(())
    }
}

fn deserialize_authorities<'de, D>(
    deserializer: D,
) -> Result<Vec<axum::http::uri::Authority>, D::Error>
where
    D: serde::Deserializer<'de>,
{
    Vec::<String>::deserialize(deserializer)?
        .into_iter()
        .map(|authority| {
            authority.parse().map_err(|_| {
                serde::de::Error::custom(format!("invalid HTTP authority '{authority}'"))
            })
        })
        .collect()
}

#[derive(Debug)]
pub enum Error {
    Read {
        path: std::path::PathBuf,
        source: std::io::Error,
    },
    Decode {
        path: std::path::PathBuf,
        source: toml::de::Error,
    },
    Invalid(&'static str),
}

impl std::fmt::Display for Error {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Read { path, source } => {
                write!(formatter, "cannot read {}: {source}", path.display())
            }
            Self::Decode { path, source } => {
                write!(formatter, "cannot decode {}: {source}", path.display())
            }
            Self::Invalid(message) => formatter.write_str(message),
        }
    }
}

impl std::error::Error for Error {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            Self::Read { source, .. } => Some(source),
            Self::Decode { source, .. } => Some(source),
            Self::Invalid(_) => None,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::Config;

    fn parse(source: &str) -> Result<Config, toml::de::Error> {
        toml::from_str(source)
    }

    #[test]
    fn defaults_server_and_pool_settings() {
        let config = parse("[database]\nurl = 'postgres://localhost/mirum'\n").unwrap();
        assert_eq!(config.server.addr.to_string(), "127.0.0.1:8080");
        assert_eq!(config.server.http1_max_buffer_bytes.as_u64(), 32 * 1024);
        assert_eq!(config.server.max_concurrent_requests, 64);
        assert_eq!(config.database.max_connections, 10);
        assert_eq!(config.database.connect_timeout_seconds, 5);
        config.validate().unwrap();
    }

    #[test]
    fn parses_example_configuration() {
        let config = parse(include_str!("../../../config/mirum.toml")).unwrap();
        config.validate().unwrap();
    }

    #[test]
    fn rejects_unknown_and_invalid_settings() {
        assert!(parse("unknown = true\n[database]\nurl = 'postgres://localhost/mirum'\n").is_err());

        let config =
            parse("[server]\nmax_concurrent_requests = 0\n[database]\nurl = ''\n").unwrap();
        assert!(config.validate().is_err());
    }
}
