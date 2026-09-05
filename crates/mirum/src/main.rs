// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

mod config;
mod daemon;
mod styles;

const USAGE: &str = "Usage: mirum --config <PATH>";

#[tokio::main]
async fn main() -> std::process::ExitCode {
    let config_path = match parse_arguments(std::env::args_os().skip(1)) {
        Ok(Some(path)) => path,
        Ok(None) => {
            println!("{USAGE}");
            return std::process::ExitCode::SUCCESS;
        }
        Err(error) => {
            eprintln!("mirum: {error}\n{USAGE}");
            return std::process::ExitCode::FAILURE;
        }
    };
    let config = match config::Config::load(&config_path) {
        Ok(config) => config,
        Err(error) => {
            eprintln!("mirum: {error}");
            return std::process::ExitCode::FAILURE;
        }
    };

    match daemon::run(config).await {
        Ok(()) => std::process::ExitCode::SUCCESS,
        Err(error) => {
            eprintln!("mirum: {error}");
            std::process::ExitCode::FAILURE
        }
    }
}

fn parse_arguments(
    arguments: impl IntoIterator<Item = std::ffi::OsString>,
) -> Result<Option<std::path::PathBuf>, String> {
    let mut arguments = arguments.into_iter();
    let mut config = None;

    while let Some(argument) = arguments.next() {
        if argument == "--config" {
            if config.is_some() {
                return Err("--config may only be specified once".to_owned());
            }
            let path = arguments
                .next()
                .ok_or_else(|| "--config requires a path".to_owned())?;
            if path.is_empty() {
                return Err("--config requires a non-empty path".to_owned());
            }
            config = Some(path.into());
        } else if argument == "--help" || argument == "-h" {
            return Ok(None);
        } else {
            return Err(format!("unknown argument {}", argument.to_string_lossy()));
        }
    }

    config
        .map(Some)
        .ok_or_else(|| "--config is required".to_owned())
}

#[cfg(test)]
mod tests {
    use super::parse_arguments;

    fn parse(arguments: &[&str]) -> Result<Option<std::path::PathBuf>, String> {
        parse_arguments(arguments.iter().map(std::ffi::OsString::from))
    }

    #[test]
    fn parses_explicit_config_path() {
        assert_eq!(
            parse(&["--config", "config/mirum.toml"]).unwrap(),
            Some("config/mirum.toml".into())
        );
        assert_eq!(parse(&["--help"]).unwrap(), None);
    }

    #[test]
    fn rejects_missing_or_duplicate_config_path() {
        assert!(parse(&[]).unwrap_err().contains("--config is required"));
        assert!(
            parse(&["--config"])
                .unwrap_err()
                .contains("requires a path")
        );
        assert!(
            parse(&["--config", "one.toml", "--config", "two.toml"])
                .unwrap_err()
                .contains("only be specified once")
        );
    }
}
