// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

use std::sync::{Arc, LazyLock};

use axum::{
    Router,
    body::Bytes,
    extract::{DefaultBodyLimit, Path, State},
    http::{HeaderMap, StatusCode, header},
    response::{Html, IntoResponse, Response},
    routing::{get, post},
};
use dimidiumlabs_server::{
    HtmlCompressionPredicate, assets_router,
    service::{
        AdmissionLayer, ClientIpLayer, DrainLayer, ForwardedHeader, HostLayer, HostPattern,
        HtmlLayer, PeerAddr, TrustedProxies,
        compression::{CompressionLayer, CompressionLevel},
    },
    transport::{HttpTransport, TransportPolicyError},
};
use dimidiumlabs_ui::{AssetsCatalog, Document, FOUNDATION};
use hyper_util::{rt::TokioIo, service::TowerToHyperService};
use maud::{Render, html};
use sqlx::{PgPool, Row, postgres::PgPoolOptions};

use crate::{config::Config, styles};

mod licenses {
    pub const JSON: &str = include_str!(concat!(env!("OUT_DIR"), "/licenses.json"));
}

static ASSETS: LazyLock<Arc<AssetsCatalog>> = LazyLock::new(|| {
    Arc::new(
        AssetsCatalog::new()
            .with(FOUNDATION)
            .expect("foundation assets are valid")
            .with(styles::APPLICATION)
            .expect("Mirum assets are valid and unique"),
    )
});

#[derive(Clone)]
struct AppState {
    database: PgPool,
    webhook_secret: Arc<str>,
    worker: Arc<tokio::sync::Semaphore>,
}

pub async fn run(config: Config) -> Result<(), Box<dyn std::error::Error>> {
    let database = PgPoolOptions::new()
        .max_connections(config.database.max_connections)
        .acquire_timeout(std::time::Duration::from_secs(
            config.database.connect_timeout_seconds,
        ))
        .connect(&config.database.url)
        .await?;

    initialize_database(&database).await?;

    let app = Router::<AppState>::new()
        .merge(assets_router::<AppState>(Arc::clone(&ASSETS)))
        .route("/", get(index))
        .route("/builds/{id}", get(build))
        .route(
            "/webhook",
            post(webhook).layer(DefaultBodyLimit::max(1024 * 1024)),
        )
        .route("/-/ready", get(readiness))
        .route(
            "/-/licenses.json",
            get(async || {
                response(
                    StatusCode::OK,
                    "application/json; charset=utf-8",
                    licenses::JSON.as_bytes().to_vec(),
                )
            }),
        )
        .layer(HtmlLayer::new(&ASSETS).with_negotiated_compression())
        .layer(
            CompressionLayer::new()
                .quality(CompressionLevel::Precise(i32::from(
                    config.server.compression_level,
                )))
                .compress_when(HtmlCompressionPredicate::new(
                    u16::try_from(config.server.compression_min_bytes.as_u64())
                        .expect("compression threshold fits u16"),
                )),
        )
        .with_state(AppState {
            database,
            webhook_secret: config.webhook.secret.into(),
            worker: Arc::new(tokio::sync::Semaphore::new(1)),
        });
    let app = restrict_hosts(app, &config.server.hostnames);
    let (app, drain_handle, transport) = harden(app, &config.server)?;
    // Liveness stays outside admission and draining so overload cannot cause restart loops.
    let app = app.route("/-/health", get(health));

    let listen_addr = config.server.addr;
    let listener = tokio::net::TcpListener::bind(listen_addr).await?;
    let shutdown = tokio_util::sync::CancellationToken::new();
    eprintln!("mirum-server: listening on {listen_addr}");

    let server = serve(listener, app, transport, shutdown.clone());
    tokio::pin!(server);
    tokio::select! {
        result = &mut server => result?,
        () = shutdown_signal() => {
            let _ = drain_handle.begin();
            shutdown.cancel();
            let drained = tokio::time::timeout(config.server.shutdown_timeout, async {
                server.await?;
                drain_handle.wait().await;
                std::io::Result::Ok(())
            })
            .await;
            match drained {
                Ok(result) => result?,
                Err(_) => {
                    return Err(std::io::Error::new(
                        std::io::ErrorKind::TimedOut,
                        "HTTP shutdown exceeded its deadline",
                    ).into());
                }
            }
        }
    }
    Ok(())
}

fn restrict_hosts(app: Router, hostnames: &[axum::http::uri::Authority]) -> Router {
    let hosts = hostnames
        .iter()
        .map(|hostname| HostPattern::new(hostname.as_str()))
        .collect::<Result<Vec<_>, _>>()
        .expect("listener hostnames are validated");
    if hosts.is_empty() {
        app
    } else {
        app.layer(HostLayer::new(hosts))
    }
}

fn harden(
    app: Router,
    config: &crate::config::Server,
) -> Result<
    (
        Router,
        dimidiumlabs_server::service::DrainHandle,
        HttpTransport,
    ),
    TransportPolicyError,
> {
    let (drain_layer, drain_handle) = DrainLayer::new();
    let app = app
        .layer(
            dimidiumlabs_server::service::body::RequestBodyLimitLayer::new(
                usize::try_from(config.request_body_max_bytes.as_u64())
                    .expect("request body limit fits usize"),
            ),
        )
        .layer(
            dimidiumlabs_server::service::timeout::RequestBodyTimeoutLayer::new(
                config.request_body_idle_timeout,
            ),
        )
        .layer(
            AdmissionLayer::new(
                std::num::NonZeroUsize::new(config.max_concurrent_requests)
                    .expect("concurrency limit is non-zero"),
            )
            .with_wait(
                config.admission_wait,
                std::num::NonZeroUsize::new(config.max_queued_requests)
                    .expect("queue limit is non-zero"),
            ),
        )
        .layer(ClientIpLayer::new(TrustedProxies::new(
            config.trusted_proxies.iter().copied(),
            ForwardedHeader::XForwardedFor,
        )))
        .layer(drain_layer);
    let transport = HttpTransport::new(
        config.header_read_timeout,
        usize::try_from(config.http1_max_buffer_bytes.as_u64())
            .expect("HTTP/1 buffer size fits usize"),
        std::num::NonZeroU32::new(config.http2_max_concurrent_streams)
            .expect("HTTP/2 stream limit is non-zero"),
        std::num::NonZeroU32::new(
            u32::try_from(config.http2_max_header_list_bytes.as_u64())
                .expect("HTTP/2 header-list size fits u32"),
        )
        .expect("HTTP/2 header limit is non-zero"),
    )?;
    Ok((app, drain_handle, transport))
}

async fn serve(
    listener: tokio::net::TcpListener,
    app: Router,
    transport: HttpTransport,
    shutdown: tokio_util::sync::CancellationToken,
) -> std::io::Result<()> {
    let mut connections = tokio::task::JoinSet::new();

    loop {
        tokio::select! {
            () = shutdown.cancelled() => break,
            accepted = listener.accept() => {
                let (stream, peer) = accepted?;
                let app = app
                    .clone()
                    .layer(axum::Extension(axum::extract::ConnectInfo(peer)))
                    .layer(axum::Extension(PeerAddr(peer)));
                let transport = transport.clone();
                let shutdown = shutdown.clone();
                connections.spawn(async move {
                    let builder = transport.builder();
                    let connection = builder.serve_connection_with_upgrades(
                        TokioIo::new(stream),
                        TowerToHyperService::new(app),
                    );
                    tokio::pin!(connection);
                    let result = tokio::select! {
                        result = &mut connection => result,
                        () = shutdown.cancelled() => {
                            connection.as_mut().graceful_shutdown();
                            connection.await
                        }
                    };
                    if let Err(error) = result {
                        eprintln!("mirum-server: HTTP connection failed: {error}");
                    }
                });
            }
            Some(result) = connections.join_next(), if !connections.is_empty() => {
                if let Err(error) = result {
                    eprintln!("mirum-server: HTTP connection task failed: {error}");
                }
            }
        }
    }

    while let Some(result) = connections.join_next().await {
        if let Err(error) = result {
            eprintln!("mirum-server: HTTP connection task failed: {error}");
        }
    }
    Ok(())
}

async fn initialize_database(database: &PgPool) -> Result<(), sqlx::Error> {
    sqlx::query(
        r#"
        CREATE TABLE IF NOT EXISTS builds (
            id BIGSERIAL PRIMARY KEY,
            repository TEXT NOT NULL,
            clone_url TEXT NOT NULL,
            git_ref TEXT NOT NULL,
            commit_sha TEXT NOT NULL,
            status TEXT NOT NULL,
            exit_code INTEGER,
            log TEXT NOT NULL DEFAULT ''
        )
        "#,
    )
    .execute(database)
    .await?;
    sqlx::query("UPDATE builds SET status = 'interrupted' WHERE status IN ('queued', 'running')")
        .execute(database)
        .await?;
    Ok(())
}

#[derive(serde::Deserialize)]
struct GithubPush {
    #[serde(rename = "ref")]
    git_ref: String,
    after: String,
    repository: GithubRepository,
}

#[derive(serde::Deserialize)]
struct GithubRepository {
    full_name: String,
    clone_url: String,
}

async fn webhook(State(state): State<AppState>, headers: HeaderMap, body: Bytes) -> Response {
    let signature = headers
        .get("x-hub-signature-256")
        .and_then(|value| value.to_str().ok())
        .unwrap_or_default();
    if !verify_signature(&state.webhook_secret, signature, &body) {
        return text_response(StatusCode::UNAUTHORIZED, "invalid signature\n");
    }
    if headers
        .get("x-github-event")
        .and_then(|value| value.to_str().ok())
        != Some("push")
    {
        return response(StatusCode::NO_CONTENT, "text/plain", Vec::new());
    }
    let push: GithubPush = match serde_json::from_slice(&body) {
        Ok(push) => push,
        Err(_) => return text_response(StatusCode::BAD_REQUEST, "invalid payload\n"),
    };
    if push.after.bytes().all(|byte| byte == b'0') {
        return response(StatusCode::NO_CONTENT, "text/plain", Vec::new());
    }
    if !matches!(push.after.len(), 40 | 64)
        || !push.after.bytes().all(|byte| byte.is_ascii_hexdigit())
        || !(push.git_ref.starts_with("refs/heads/") || push.git_ref.starts_with("refs/tags/"))
        || push.repository.full_name.is_empty()
        || push.repository.clone_url.is_empty()
    {
        return text_response(StatusCode::BAD_REQUEST, "invalid push\n");
    }

    let id = match sqlx::query_scalar::<_, i64>(
        "INSERT INTO builds (repository, clone_url, git_ref, commit_sha, status) \
         VALUES ($1, $2, $3, $4, 'queued') RETURNING id",
    )
    .bind(&push.repository.full_name)
    .bind(&push.repository.clone_url)
    .bind(&push.git_ref)
    .bind(push.after.to_ascii_lowercase())
    .fetch_one(&state.database)
    .await
    {
        Ok(id) => id,
        Err(error) => return internal_error("queue build", error),
    };

    let database = state.database.clone();
    let worker = Arc::clone(&state.worker);
    tokio::spawn(async move {
        let _permit = worker.acquire_owned().await.expect("worker stays open");
        run_build(&database, id, &push).await;
    });

    axum::http::Response::builder()
        .status(StatusCode::ACCEPTED)
        .header(header::LOCATION, format!("/builds/{id}"))
        .body(axum::body::Body::empty())
        .expect("build response is valid")
}

fn verify_signature(secret: &str, signature: &str, body: &[u8]) -> bool {
    let Some(signature) = signature.strip_prefix("sha256=") else {
        return false;
    };
    let Ok(signature) = hex::decode(signature) else {
        return false;
    };
    let key = ring::hmac::Key::new(ring::hmac::HMAC_SHA256, secret.as_bytes());
    !secret.is_empty() && ring::hmac::verify(&key, body, &signature).is_ok()
}

async fn run_build(database: &PgPool, id: i64, push: &GithubPush) {
    let _ = sqlx::query("UPDATE builds SET status = 'running' WHERE id = $1")
        .bind(id)
        .execute(database)
        .await;
    let mut log = format!(
        "mirum-server: cloning {} at {}\n",
        push.repository.full_name, push.after
    );
    let result = execute_build(push, &mut log).await;
    let (status, exit_code) = match result {
        Ok(0) => ("succeeded", Some(0)),
        Ok(code) => {
            log.push_str(&format!(
                "mirum-server: Mirumfile exited with code {code}\n"
            ));
            ("failed", Some(code))
        }
        Err(error) => {
            log.push_str(&format!("mirum-server: {error}\n"));
            ("failed", None)
        }
    };
    if let Err(error) =
        sqlx::query("UPDATE builds SET status = $2, exit_code = $3, log = $4 WHERE id = $1")
            .bind(id)
            .bind(status)
            .bind(exit_code)
            .bind(log)
            .execute(database)
            .await
    {
        eprintln!("mirum-server: cannot finish build {id}: {error}");
    }
}

async fn execute_build(push: &GithubPush, log: &mut String) -> Result<i32, String> {
    let temporary = tempfile::Builder::new()
        .prefix("mirum-build-")
        .tempdir()
        .map_err(|error| format!("create workspace: {error}"))?;
    let checkout = temporary.path().join("repository");

    let mut command = tokio::process::Command::new("git");
    command
        .args(["clone", "--no-checkout", "--"])
        .arg(&push.repository.clone_url)
        .arg(&checkout);
    run_checked(&mut command, "git clone", log).await?;

    let mut command = tokio::process::Command::new("git");
    command
        .arg("-C")
        .arg(&checkout)
        .args(["checkout", "--detach"])
        .arg(&push.after);
    run_checked(&mut command, "git checkout", log).await?;

    let mirumfile = checkout.join("Mirumfile");
    let metadata =
        std::fs::metadata(&mirumfile).map_err(|error| format!("read Mirumfile: {error}"))?;
    use std::os::unix::fs::PermissionsExt;
    if !metadata.is_file() || metadata.permissions().mode() & 0o111 == 0 {
        return Err("Mirumfile is not an executable file".to_owned());
    }

    let mut command = tokio::process::Command::new(&mirumfile);
    command
        .current_dir(checkout)
        .env("GITHUB_EVENT_NAME", "push")
        .env("GITHUB_REF", &push.git_ref)
        .env("GITHUB_SHA", &push.after)
        .env("GITHUB_REPOSITORY", &push.repository.full_name);
    let output = command
        .kill_on_drop(true)
        .output()
        .await
        .map_err(|error| format!("start Mirumfile: {error}"))?;
    append_output(log, &output);
    Ok(output.status.code().unwrap_or(1))
}

async fn run_checked(
    command: &mut tokio::process::Command,
    name: &str,
    log: &mut String,
) -> Result<(), String> {
    let output = command
        .kill_on_drop(true)
        .output()
        .await
        .map_err(|error| format!("start {name}: {error}"))?;
    append_output(log, &output);
    if output.status.success() {
        Ok(())
    } else {
        Err(format!("{name} exited with {}", output.status))
    }
}

fn append_output(log: &mut String, output: &std::process::Output) {
    log.push_str(&String::from_utf8_lossy(&output.stdout));
    log.push_str(&String::from_utf8_lossy(&output.stderr));
}

async fn index(State(state): State<AppState>) -> Response {
    let rows = match sqlx::query(
        "SELECT id, repository, commit_sha, status FROM builds ORDER BY id DESC LIMIT 100",
    )
    .fetch_all(&state.database)
    .await
    {
        Ok(rows) => rows,
        Err(error) => return internal_error("load builds", error),
    };
    let body = html! {
        main class="page" {
            header {
                p class="eyebrow" { "Dimidium Labs" }
                h1 { "Mirum" }
            }
            h2 { "Builds" }
            @if rows.is_empty() {
                p { "No builds yet." }
            } @else {
                ul {
                    @for row in rows {
                        @let id: i64 = row.get("id");
                        @let repository: String = row.get("repository");
                        @let commit: String = row.get("commit_sha");
                        @let status: String = row.get("status");
                        li {
                            a href=(format!("/builds/{id}")) {
                                "#" (id) " " (repository) " " (&commit[..12])
                            }
                            " — " (status)
                        }
                    }
                }
            }
            footer { a href="https://git.dimidiumlabs.io/mirum" { "Source code" } }
        }
    };
    page("Mirum", body, false).into_response()
}

async fn build(State(state): State<AppState>, Path(id): Path<i64>) -> Response {
    let row = match sqlx::query(
        "SELECT repository, git_ref, commit_sha, status, exit_code, log \
         FROM builds WHERE id = $1",
    )
    .bind(id)
    .fetch_optional(&state.database)
    .await
    {
        Ok(Some(row)) => row,
        Ok(None) => return text_response(StatusCode::NOT_FOUND, "build not found\n"),
        Err(error) => return internal_error("load build", error),
    };
    let repository: String = row.get("repository");
    let git_ref: String = row.get("git_ref");
    let commit: String = row.get("commit_sha");
    let status: String = row.get("status");
    let exit_code: Option<i32> = row.get("exit_code");
    let log: String = row.get("log");
    let running = matches!(status.as_str(), "queued" | "running");
    let title = format!("Build #{id} - Mirum");
    let body = html! {
        main class="page" {
            header {
                p class="eyebrow" { "Dimidium Labs" }
                h1 { "Build #" (id) }
            }
            p { (repository) " at " code { (commit) } }
            p { (git_ref) " — " (status) }
            @if let Some(exit_code) = exit_code { p { "Exit code: " (exit_code) } }
            h2 { "Log" }
            pre { (log) }
            footer { a href="/" { "All builds" } }
        }
    };
    page(&title, body, running).into_response()
}

fn page(title: &str, body: maud::Markup, refresh: bool) -> Html<String> {
    Html(
        Document::new(title, body, &ASSETS)
            .with_manifest()
            .with_svg_icon()
            .with_apple_touch_icon()
            .with_head(html! {
                meta name="generator" content="Mirum";
                @if refresh { meta http-equiv="refresh" content="2"; }
            })
            .render()
            .into_string(),
    )
}

async fn health() -> Response {
    json_status(StatusCode::OK, "ok")
}

async fn readiness(State(state): State<AppState>) -> Response {
    match sqlx::query_scalar::<_, i32>("SELECT 1")
        .fetch_one(&state.database)
        .await
    {
        Ok(1) => json_status(StatusCode::OK, "ready"),
        Ok(_) | Err(_) => json_status(StatusCode::SERVICE_UNAVAILABLE, "unavailable"),
    }
}

fn json_status(status: StatusCode, value: &'static str) -> Response {
    response(
        status,
        "application/json; charset=utf-8",
        format!("{{\"status\":\"{value}\"}}\n").into_bytes(),
    )
}

fn internal_error(context: &str, error: impl std::fmt::Display) -> Response {
    eprintln!("mirum-server: {context}: {error}");
    text_response(StatusCode::INTERNAL_SERVER_ERROR, "internal server error\n")
}

fn text_response(status: StatusCode, body: &'static str) -> Response {
    response(
        status,
        "text/plain; charset=utf-8",
        body.as_bytes().to_vec(),
    )
}

fn response(status: StatusCode, content_type: &'static str, body: Vec<u8>) -> Response {
    (status, [(header::CONTENT_TYPE, content_type)], body).into_response()
}

async fn shutdown_signal() {
    let terminate = async {
        tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
            .expect("install SIGTERM handler")
            .recv()
            .await;
    };
    let interrupt = async {
        tokio::signal::unix::signal(tokio::signal::unix::SignalKind::interrupt())
            .expect("install SIGINT handler")
            .recv()
            .await;
    };
    tokio::select! {
        _ = terminate => {}
        _ = interrupt => {}
    }
}

#[cfg(test)]
mod tests {
    use super::verify_signature;

    #[test]
    fn verifies_github_signature() {
        let body = b"payload";
        let key = ring::hmac::Key::new(ring::hmac::HMAC_SHA256, b"secret");
        let signature = format!("sha256={}", hex::encode(ring::hmac::sign(&key, body)));
        assert!(verify_signature("secret", &signature, body));
        assert!(!verify_signature("wrong", &signature, body));
        assert!(!verify_signature("", &signature, body));
    }
}
