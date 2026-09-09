// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

use dimidiumlabs_config::{
    NonBlankString, NonZeroDuration, U16ByteSize, U32ByteSize, UsizeByteSize,
};
use serde::Deserialize;

#[derive(Debug, Deserialize, garde::Validate)]
#[serde(default, deny_unknown_fields)]
pub struct Server {
    #[garde(skip)]
    pub addr: std::net::SocketAddr,
    #[serde(deserialize_with = "deserialize_authorities")]
    #[garde(skip)]
    pub hostnames: Vec<axum::http::uri::Authority>,
    #[garde(dive)]
    pub header_read_timeout: NonZeroDuration,
    #[garde(dive)]
    pub http1_max_buffer_bytes: UsizeByteSize<{ 8 * 1024 }>,
    #[garde(range(min = 1))]
    pub http2_max_concurrent_streams: u32,
    #[garde(dive)]
    pub http2_max_header_list_bytes: U32ByteSize<1>,
    #[garde(dive)]
    pub request_body_idle_timeout: NonZeroDuration,
    #[garde(dive)]
    pub request_body_max_bytes: UsizeByteSize<1>,
    #[garde(skip)]
    pub trusted_proxies: Vec<ipnet::IpNet>,
    #[garde(dive)]
    pub compression_min_bytes: U16ByteSize<1>,
    #[garde(range(max = 22))]
    pub compression_level: u8,
    #[garde(range(min = 1))]
    pub max_concurrent_requests: usize,
    #[garde(range(min = 1))]
    pub max_queued_requests: usize,
    #[garde(dive)]
    pub admission_wait: NonZeroDuration,
    #[garde(dive)]
    pub shutdown_timeout: NonZeroDuration,
}

impl Default for Server {
    fn default() -> Self {
        Self {
            addr: "127.0.0.1:8080"
                .parse()
                .expect("default server address is valid"),
            hostnames: Vec::new(),
            header_read_timeout: NonZeroDuration::from_secs(10),
            http1_max_buffer_bytes: UsizeByteSize::kib(32),
            http2_max_concurrent_streams: 64,
            http2_max_header_list_bytes: U32ByteSize::kib(16),
            request_body_idle_timeout: NonZeroDuration::from_secs(60),
            request_body_max_bytes: UsizeByteSize::gib(1),
            trusted_proxies: Vec::new(),
            compression_min_bytes: U16ByteSize::b(128),
            compression_level: 5,
            max_concurrent_requests: 64,
            max_queued_requests: 128,
            admission_wait: NonZeroDuration::from_secs(1),
            shutdown_timeout: NonZeroDuration::from_secs(25),
        }
    }
}

#[derive(Debug, Default, Deserialize, garde::Validate)]
#[serde(default, deny_unknown_fields)]
pub struct Webhook {
    #[garde(skip)]
    pub secret: String,
}

#[derive(Debug, Deserialize, garde::Validate)]
#[serde(deny_unknown_fields)]
pub struct Database {
    #[garde(dive)]
    pub url: NonBlankString,
    #[serde(default = "default_max_connections")]
    #[garde(range(min = 1))]
    pub max_connections: u32,
    #[serde(default = "default_connect_timeout_seconds")]
    #[garde(range(min = 1))]
    pub connect_timeout_seconds: u64,
}

const fn default_max_connections() -> u32 {
    10
}

const fn default_connect_timeout_seconds() -> u64 {
    5
}

#[derive(Debug, Deserialize, garde::Validate)]
#[serde(deny_unknown_fields)]
pub struct Config {
    #[serde(default)]
    #[garde(dive)]
    pub server: Server,
    #[serde(default)]
    #[garde(dive)]
    pub webhook: Webhook,
    #[garde(dive)]
    pub database: Database,
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

#[cfg(test)]
mod tests {
    use std::io::Write as _;

    use dimidiumlabs_config::load;

    use super::Config;

    async fn load_config(source: &str) -> Result<Config, dimidiumlabs_config::Error> {
        let mut file = tempfile::Builder::new().suffix(".toml").tempfile().unwrap();
        file.write_all(source.as_bytes()).unwrap();
        load("mirum-server", file.path())
            .await
            .map(|loaded| loaded.into_parts().0)
    }

    #[tokio::test]
    async fn defaults_server_and_pool_settings() {
        let config = load_config("[database]\nurl = 'postgres://localhost/mirum'\n")
            .await
            .unwrap();
        assert_eq!(config.server.addr.to_string(), "127.0.0.1:8080");
        assert_eq!(config.server.http1_max_buffer_bytes.as_u64(), 32 * 1024);
        assert_eq!(config.server.max_concurrent_requests, 64);
        assert!(config.webhook.secret.is_empty());
        assert_eq!(config.database.max_connections, 10);
        assert_eq!(config.database.connect_timeout_seconds, 5);
    }

    #[tokio::test]
    async fn loads_distributed_toml_example() {
        let config = load_config(include_str!("../../../deploy/mirum.toml"))
            .await
            .unwrap();
        assert_eq!(config.database.max_connections, 10);
    }

    #[cfg(target_pointer_width = "32")]
    #[tokio::test]
    async fn rejects_http1_buffer_size_that_does_not_fit_usize() {
        let error = load_config(
            "[server]\nhttp1_max_buffer_bytes = '4GiB'\n[database]\nurl = 'postgres://localhost/mirum'\n",
        )
        .await
        .unwrap_err();
        let dimidiumlabs_config::Error::Validation { source, .. } = error else {
            panic!("expected validation error");
        };
        assert!(source.to_string().contains("server.http1_max_buffer_bytes"));
    }

    #[tokio::test]
    async fn rejects_numeric_and_unitless_quantities() {
        assert!(
            load_config(
                "[server]\nheader_read_timeout = 10\n[database]\nurl = 'postgres://localhost/mirum'\n"
            )
            .await
            .is_err()
        );
        assert!(
            load_config(
                "[server]\nhttp1_max_buffer_bytes = '32768'\n[database]\nurl = 'postgres://localhost/mirum'\n"
            )
            .await
            .is_err()
        );
    }

    #[tokio::test]
    async fn rejects_blank_database_urls() {
        for url in ["''", "'   '"] {
            let error = load_config(&format!("[database]\nurl = {url}\n"))
                .await
                .unwrap_err();
            let dimidiumlabs_config::Error::Validation { source, .. } = error else {
                panic!("expected validation error");
            };
            assert!(source.to_string().contains("database.url"));
        }
    }

    #[tokio::test]
    async fn rejects_unknown_and_invalid_settings() {
        assert!(
            load_config("unknown = true\n[database]\nurl = 'postgres://localhost/mirum'\n")
                .await
                .is_err()
        );

        let error = load_config(
            "[server]\nmax_concurrent_requests = 0\n[database]\nurl = 'postgres://localhost/mirum'\n",
        )
        .await
        .unwrap_err();
        let dimidiumlabs_config::Error::Validation { source, .. } = error else {
            panic!("expected validation error");
        };
        assert!(
            source
                .to_string()
                .contains("server.max_concurrent_requests")
        );
    }
}
