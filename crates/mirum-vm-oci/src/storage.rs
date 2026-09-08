// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

use crate::{Error, Result};
use oci_client::manifest::OciImageIndex;
use std::io::SeekFrom;
use std::path::{Component, Path, PathBuf};
use tokio::fs::{self, File};
use tokio::io::{AsyncSeekExt, AsyncWriteExt};
use tokio::task;

/// Filesystem storage for OCI content and materialized HMI images.
///
/// The roots are independent so callers can place content-addressed OCI data
/// and large materialized disks on different filesystems.
#[derive(Clone, Debug)]
pub struct Storage {
    oci_root: PathBuf,
    hmi_root: PathBuf,
}

impl Storage {
    /// Opens the storage and creates its filesystem layout when necessary.
    pub async fn open(oci_root: impl Into<PathBuf>, hmi_root: impl Into<PathBuf>) -> Result<Self> {
        let storage = Self {
            oci_root: oci_root.into(),
            hmi_root: hmi_root.into(),
        };

        fs::create_dir_all(&storage.hmi_root).await?;
        fs::create_dir_all(storage.blobs_dir()).await?;

        let layout = storage.oci_root.join("oci-layout");
        if !fs::try_exists(&layout).await? {
            fs::write(&layout, br#"{"imageLayoutVersion":"1.0.0"}"#).await?;
        }

        Ok(storage)
    }

    pub fn oci_root(&self) -> &Path {
        &self.oci_root
    }

    pub fn hmi_root(&self) -> &Path {
        &self.hmi_root
    }

    fn blobs_dir(&self) -> PathBuf {
        self.oci_root.join("blobs").join("sha256")
    }

    fn index_path(&self) -> PathBuf {
        self.oci_root.join("index.json")
    }

    fn hmi_image_dir(&self, manifest_digest: &str) -> Result<PathBuf> {
        let digest = safe_component(strip_sha256(manifest_digest), "manifest digest")?;
        Ok(self.hmi_root.join(digest))
    }

    fn hmi_file_path(&self, manifest_digest: &str, title: &str) -> Result<PathBuf> {
        let title = safe_component(title, "image file name")?;
        Ok(self.hmi_image_dir(manifest_digest)?.join(title))
    }

    fn blob_path(&self, digest: &str) -> Result<PathBuf> {
        let digest = safe_component(strip_sha256(digest), "blob digest")?;
        Ok(self.blobs_dir().join(digest))
    }

    pub(crate) async fn read_blob(&self, digest: &str) -> Result<Vec<u8>> {
        Ok(fs::read(self.blob_path(digest)?).await?)
    }

    /// Writes raw bytes as a content-addressed blob, no-op if already present.
    pub(crate) async fn write_blob<T>(&self, data: T) -> Result<(String, T)>
    where
        T: AsRef<[u8]> + Send + Sync + 'static,
    {
        let (digest, data) =
            task::spawn_blocking(move || (sha256_hex(data.as_ref()), data)).await?;
        let path = self.blob_path(&digest)?;
        if !fs::try_exists(&path).await? {
            let tmp = path.with_extension("tmp");
            fs::write(&tmp, data.as_ref()).await?;
            fs::rename(&tmp, &path).await?;
        }
        Ok((digest, data))
    }

    pub(crate) async fn read_index(&self) -> Result<OciImageIndex> {
        let path = self.index_path();
        if !fs::try_exists(&path).await? {
            return Ok(OciImageIndex {
                schema_version: 2,
                media_type: None,
                manifests: vec![],
                artifact_type: None,
                annotations: None,
            });
        }
        let data = fs::read(&path).await?;
        Ok(serde_json::from_slice(&data)?)
    }

    pub(crate) async fn write_index(&self, index: &OciImageIndex) -> Result<()> {
        let data = serde_json::to_vec_pretty(index)?;
        fs::write(self.index_path(), data).await?;
        Ok(())
    }

    pub(crate) async fn ensure_hmi_image(&self, manifest_digest: &str) -> Result<()> {
        fs::create_dir_all(self.hmi_image_dir(manifest_digest)?).await?;
        Ok(())
    }

    pub(crate) async fn cache_hmi_file(
        &self,
        manifest_digest: &str,
        title: &str,
        source: &Path,
    ) -> Result<()> {
        let destination = self.hmi_file_path(manifest_digest, title)?;
        if !fs::try_exists(&destination).await? {
            Self::hardlink_or_copy(source, &destination).await?;
        }
        Ok(())
    }

    pub(crate) async fn write_hmi_file(
        &self,
        manifest_digest: &str,
        title: &str,
        mut chunks: Vec<(u64, Vec<u8>)>,
    ) -> Result<()> {
        chunks.sort_by_key(|(offset, _)| *offset);
        let mut file = File::create(self.hmi_file_path(manifest_digest, title)?).await?;
        for (offset, data) in chunks {
            file.seek(SeekFrom::Start(offset)).await?;
            file.write_all(&data).await?;
        }
        file.flush().await?;
        Ok(())
    }

    pub(crate) async fn export_hmi_file(
        &self,
        manifest_digest: &str,
        title: &str,
        destination: &Path,
    ) -> Result<()> {
        let source = self.hmi_file_path(manifest_digest, title)?;
        if !fs::try_exists(destination).await? {
            Self::hardlink_or_copy(&source, destination).await?;
        }
        Ok(())
    }

    async fn hardlink_or_copy(source: &Path, destination: &Path) -> Result<()> {
        if fs::hard_link(source, destination).await.is_err() {
            fs::copy(source, destination).await?;
        }
        Ok(())
    }
}

fn safe_component<'a>(value: &'a str, kind: &'static str) -> Result<&'a str> {
    let mut components = Path::new(value).components();
    match (components.next(), components.next()) {
        (Some(Component::Normal(_)), None) => Ok(value),
        _ => Err(Error::InvalidStoragePath {
            kind,
            value: value.to_string(),
        }),
    }
}

fn strip_sha256(digest: &str) -> &str {
    digest.strip_prefix("sha256:").unwrap_or(digest)
}

fn sha256_hex(data: &[u8]) -> String {
    use sha2::{Digest, Sha256};
    let mut hasher = Sha256::new();
    hasher.update(data);
    format!("sha256:{:x}", hasher.finalize())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn paths_cannot_escape_storage_roots() {
        let storage = Storage {
            oci_root: "oci".into(),
            hmi_root: "hmi".into(),
        };
        assert!(matches!(
            storage.blob_path("sha256:../../outside"),
            Err(Error::InvalidStoragePath { .. })
        ));
        assert!(matches!(
            storage.hmi_image_dir("sha256:../outside"),
            Err(Error::InvalidStoragePath { .. })
        ));
        assert!(matches!(
            storage.hmi_file_path("sha256:digest", "../outside"),
            Err(Error::InvalidStoragePath { .. })
        ));
    }
}
