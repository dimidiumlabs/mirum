// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

use oci_client::errors::OciDistributionError;
use std::fmt;
use std::path::PathBuf;

#[derive(Debug)]
pub enum Error {
    Io(std::io::Error),
    Json(serde_json::Error),
    InvalidImage(mirum_vm_hmi::ParseError),
    InvalidReference {
        reference: String,
        source: oci_client::ParseError,
    },
    Registry(OciDistributionError),
    Task(tokio::task::JoinError),
    InvalidImagePath(PathBuf),
    MissingConfig(PathBuf),
    ImageNotFound(String),
    MissingManifest,
    InvalidLayer(&'static str),
    InvalidAnnotation {
        name: &'static str,
        value: String,
    },
    EmptyImageFile(PathBuf),
    InvalidStoragePath {
        kind: &'static str,
        value: String,
    },
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Io(error) => error.fmt(f),
            Self::Json(error) => error.fmt(f),
            Self::InvalidImage(error) => error.fmt(f),
            Self::InvalidReference { reference, source } => {
                write!(f, "invalid reference '{reference}': {source}")
            }
            Self::Registry(error) => error.fmt(f),
            Self::Task(error) => write!(f, "background task failed: {error}"),
            Self::InvalidImagePath(path) => {
                write!(f, "{} has no file name", path.display())
            }
            Self::MissingConfig(path) => {
                write!(f, "{} does not contain config.json", path.display())
            }
            Self::ImageNotFound(reference) => {
                write!(f, "no local image tagged '{reference}'")
            }
            Self::MissingManifest => f.write_str("registry returned no image manifest"),
            Self::InvalidLayer(reason) => write!(f, "invalid chunk layer: {reason}"),
            Self::InvalidAnnotation { name, value } => {
                write!(f, "invalid chunk {name} annotation '{value}'")
            }
            Self::EmptyImageFile(path) => write!(f, "{} is empty", path.display()),
            Self::InvalidStoragePath { kind, value } => {
                write!(f, "invalid {kind} '{value}'")
            }
        }
    }
}

impl std::error::Error for Error {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            Self::Io(error) => Some(error),
            Self::Json(error) => Some(error),
            Self::InvalidImage(error) => Some(error),
            Self::InvalidReference { source, .. } => Some(source),
            Self::Registry(error) => Some(error),
            Self::Task(error) => Some(error),
            Self::InvalidImagePath(_)
            | Self::MissingConfig(_)
            | Self::ImageNotFound(_)
            | Self::MissingManifest
            | Self::InvalidLayer(_)
            | Self::InvalidAnnotation { .. }
            | Self::EmptyImageFile(_)
            | Self::InvalidStoragePath { .. } => None,
        }
    }
}

impl From<std::io::Error> for Error {
    fn from(error: std::io::Error) -> Self {
        Self::Io(error)
    }
}

impl From<serde_json::Error> for Error {
    fn from(error: serde_json::Error) -> Self {
        Self::Json(error)
    }
}

impl From<mirum_vm_hmi::ParseError> for Error {
    fn from(error: mirum_vm_hmi::ParseError) -> Self {
        Self::InvalidImage(error)
    }
}

impl From<OciDistributionError> for Error {
    fn from(error: OciDistributionError) -> Self {
        Self::Registry(error)
    }
}

impl From<tokio::task::JoinError> for Error {
    fn from(error: tokio::task::JoinError) -> Self {
        Self::Task(error)
    }
}

pub type Result<T> = std::result::Result<T, Error>;
