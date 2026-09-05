// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

use std::sync::{Arc, LazyLock};

use axum::{
    Router,
    extract::State,
    http::{StatusCode, header},
    response::{Html, IntoResponse, Response},
    routing::get,
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
use sqlx::{PgPool, postgres::PgPoolOptions};

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
}

pub async fn run(config: Config) -> Result<(), Box<dyn std::error::Error>> {
    let database = PgPoolOptions::new()
        .max_connections(config.database.max_connections)
        .acquire_timeout(std::time::Duration::from_secs(
            config.database.connect_timeout_seconds,
        ))
        .connect(&config.database.url)
        .await?;

    let app = Router::<AppState>::new()
        .merge(assets_router::<AppState>(Arc::clone(&ASSETS)))
        .route("/", get(index))
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
        .with_state(AppState { database });
    let app = restrict_hosts(app, &config.server.hostnames);
    let (app, drain_handle, transport) = harden(app, &config.server)?;
    // Liveness stays outside admission and draining so overload cannot cause restart loops.
    let app = app.route("/-/health", get(health));

    let listen_addr = config.server.addr;
    let listener = tokio::net::TcpListener::bind(listen_addr).await?;
    let shutdown = tokio_util::sync::CancellationToken::new();
    eprintln!("mirum: listening on {listen_addr}");

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
                        eprintln!("mirum: HTTP connection failed: {error}");
                    }
                });
            }
            Some(result) = connections.join_next(), if !connections.is_empty() => {
                if let Err(error) = result {
                    eprintln!("mirum: HTTP connection task failed: {error}");
                }
            }
        }
    }

    while let Some(result) = connections.join_next().await {
        if let Err(error) = result {
            eprintln!("mirum: HTTP connection task failed: {error}");
        }
    }
    Ok(())
}

async fn index() -> Html<String> {
    let body = html! {
        main class="page" {
            header {
                p class="eyebrow" { "Dimidium Labs" }
                h1 { "Mirum" }
            }
            section aria-labelledby="service-status" {
                h2 id="service-status" { "Service is running" }
                p { "The HTTP service, PostgreSQL pool, and web UI are ready for development." }
            }
            footer {
                a href="https://git.dimidiumlabs.io/mirum" { "Source code" }
            }
        }
    };
    let index = Document::new("Mirum", body, &ASSETS)
        .with_manifest()
        .with_svg_icon()
        .with_apple_touch_icon()
        .with_head(html! { meta name="generator" content="Mirum"; })
        .render()
        .into_string();

    Html(index)
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
