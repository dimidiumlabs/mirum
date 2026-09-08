// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

//! OCI image storage and registry operations for Mirum VM images.

mod error;
mod storage;

use mirum_vm_hmi::MachineImage;
use oci_client::annotations::{ORG_OPENCONTAINERS_IMAGE_REF_NAME, ORG_OPENCONTAINERS_IMAGE_TITLE};
use oci_client::client::{ClientConfig, ClientProtocol, Config, ImageLayer};
use oci_client::manifest::{
    ImageIndexEntry, OCI_IMAGE_MEDIA_TYPE, OciDescriptor, OciImageIndex, OciImageManifest,
};
use oci_client::secrets::RegistryAuth;
use oci_client::{Client, Reference};
use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};
use tokio::fs::{self, File};
use tokio::io::AsyncReadExt;
use tokio::task;

pub use error::{Error, Result};
pub use storage::Storage;

type R<T> = Result<T>;

// ---- local OCI-layout store -------------------------------------------

const CHUNK_SIZE: usize = 256 * 1024 * 1024;
const MIRUM_VM_CHUNK_MEDIA_TYPE: &str = "application/vnd.mirum.vm.disk.chunk.v1";
const MIRUM_VM_CONFIG_MEDIA_TYPE: &str = "application/vnd.mirum.vm.machine.config.v1+json";
const ANNOTATION_CHUNK_OFFSET: &str = "io.dimidiumlabs.mirum.vm.chunk.offset";
const ANNOTATION_CHUNK_LENGTH: &str = "io.dimidiumlabs.mirum.vm.chunk.length";

fn index_lookup(index: &OciImageIndex, reference: &str) -> Option<String> {
    index
        .manifests
        .iter()
        .find(|m| {
            m.annotations
                .as_ref()
                .and_then(|a| a.get(ORG_OPENCONTAINERS_IMAGE_REF_NAME))
                .map(String::as_str)
                == Some(reference)
        })
        .map(|m| m.digest.clone())
}

fn index_set(index: &mut OciImageIndex, reference: &str, digest: &str, size: u64) {
    index.manifests.retain(|m| {
        m.annotations
            .as_ref()
            .and_then(|a| a.get(ORG_OPENCONTAINERS_IMAGE_REF_NAME))
            .map(String::as_str)
            != Some(reference)
    });
    let mut annotations = BTreeMap::new();
    annotations.insert(
        ORG_OPENCONTAINERS_IMAGE_REF_NAME.to_string(),
        reference.to_string(),
    );
    index.manifests.push(ImageIndexEntry {
        media_type: OCI_IMAGE_MEDIA_TYPE.to_string(),
        digest: digest.to_string(),
        size: size as i64,
        platform: None,
        annotations: Some(annotations),
        artifact_type: None,
    });
}

/// Manages Mirum VM images backed by a [`Storage`].
#[derive(Clone, Debug)]
pub struct ImageManager {
    storage: Storage,
}

impl ImageManager {
    pub fn new(storage: Storage) -> Self {
        Self { storage }
    }

    pub fn storage(&self) -> &Storage {
        &self.storage
    }

    /// Splits `path` into fixed-size chunks, compresses each independently
    /// and writes each as its own blob.
    async fn write_file_chunks(&self, path: &Path) -> R<Vec<OciDescriptor>> {
        let title = path
            .file_name()
            .ok_or_else(|| Error::InvalidImagePath(path.to_path_buf()))?
            .to_string_lossy()
            .to_string();
        let mut file = File::open(path).await?;
        let mut descriptors = Vec::new();
        let mut offset: u64 = 0;
        loop {
            let mut buf = Vec::with_capacity(CHUNK_SIZE);
            (&mut file)
                .take(CHUNK_SIZE as u64)
                .read_to_end(&mut buf)
                .await?;
            if buf.is_empty() {
                break;
            }
            let length = buf.len() as u64;
            let compressed =
                task::spawn_blocking(move || zstd::stream::encode_all(&buf[..], 0)).await??;
            let compressed_len = compressed.len();
            let (digest, _) = self.storage.write_blob(compressed).await?;

            let mut annotations = BTreeMap::new();
            annotations.insert(ORG_OPENCONTAINERS_IMAGE_TITLE.to_string(), title.clone());
            annotations.insert(ANNOTATION_CHUNK_OFFSET.to_string(), offset.to_string());
            annotations.insert(ANNOTATION_CHUNK_LENGTH.to_string(), length.to_string());

            descriptors.push(OciDescriptor {
                media_type: MIRUM_VM_CHUNK_MEDIA_TYPE.to_string(),
                digest,
                size: compressed_len as i64,
                urls: None,
                annotations: Some(annotations),
                artifact_type: None,
            });

            offset += length;
            if length < CHUNK_SIZE as u64 {
                break;
            }
        }
        if descriptors.is_empty() {
            return Err(Error::EmptyImageFile(path.to_path_buf()));
        }
        Ok(descriptors)
    }
}

fn make_client(reference: &Reference) -> Client {
    let registry = reference.resolve_registry();
    let host = registry.split(':').next().unwrap_or(registry);
    let protocol = if host == "localhost" || host == "127.0.0.1" {
        ClientProtocol::Http
    } else {
        ClientProtocol::Https
    };
    Client::new(ClientConfig {
        protocol,
        ..Default::default()
    })
}

fn parse_reference(s: &str) -> R<Reference> {
    s.parse().map_err(|source| Error::InvalidReference {
        reference: s.to_string(),
        source,
    })
}

// ---- image operations -------------------------------------------------

impl ImageManager {
    /// Loads a prepared HMI image directory and optionally tags it.
    pub async fn load(&self, path: &Path, reference: Option<&str>) -> R<String> {
        let config_path = path.join("config.json");
        match fs::metadata(&config_path).await {
            Ok(metadata) if metadata.is_file() => {}
            Ok(_) => return Err(Error::MissingConfig(path.to_path_buf())),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
                return Err(Error::MissingConfig(path.to_path_buf()));
            }
            Err(error) => return Err(error.into()),
        }
        let config_bytes = fs::read(&config_path).await?;
        MachineImage::from_json(&config_bytes)?;

        let mut dir = fs::read_dir(path).await?;
        let mut entries = Vec::new();
        while let Some(entry) = dir.next_entry().await? {
            entries.push(entry);
        }
        entries.sort_by_key(|entry| entry.file_name());

        let mut layers = Vec::new();
        let mut sources: Vec<(String, PathBuf)> = Vec::new();
        for entry in entries {
            if !entry.file_type().await?.is_file() {
                continue;
            }
            let name = entry.file_name().to_string_lossy().to_string();
            if name == "config.json" {
                continue;
            }
            let file_path = entry.path();
            layers.extend(self.write_file_chunks(&file_path).await?);
            sources.push((name, file_path));
        }

        let config_size = config_bytes.len();
        let (config_digest, _) = self.storage.write_blob(config_bytes).await?;
        let config = OciDescriptor {
            media_type: MIRUM_VM_CONFIG_MEDIA_TYPE.to_string(),
            digest: config_digest,
            size: config_size as i64,
            urls: None,
            annotations: None,
            artifact_type: None,
        };

        let manifest = OciImageManifest {
            schema_version: 2,
            media_type: Some(OCI_IMAGE_MEDIA_TYPE.to_string()),
            config,
            layers,
            subject: None,
            artifact_type: None,
            annotations: None,
        };
        let manifest_bytes = serde_json::to_vec(&manifest)?;
        let manifest_size = manifest_bytes.len();
        let (manifest_digest, _) = self.storage.write_blob(manifest_bytes).await?;

        self.storage.ensure_hmi_image(&manifest_digest).await?;
        for (title, source) in &sources {
            self.storage
                .cache_hmi_file(&manifest_digest, title, source)
                .await?;
        }

        if let Some(reference) = reference {
            let mut index = self.storage.read_index().await?;
            index_set(
                &mut index,
                reference,
                &manifest_digest,
                manifest_size as u64,
            );
            self.storage.write_index(&index).await?;
        }
        Ok(manifest_digest)
    }

    /// Pushes a locally tagged image to its registry reference.
    pub async fn push(&self, reference_str: &str) -> R<()> {
        let index = self.storage.read_index().await?;
        let manifest_digest = index_lookup(&index, reference_str)
            .ok_or_else(|| Error::ImageNotFound(reference_str.to_string()))?;
        let manifest_bytes = self.storage.read_blob(&manifest_digest).await?;
        let manifest: OciImageManifest = serde_json::from_slice(&manifest_bytes)?;

        let reference = parse_reference(reference_str)?;
        let client = make_client(&reference);
        let auth = RegistryAuth::Anonymous;

        let mut layers = Vec::new();
        for descriptor in &manifest.layers {
            let data = self.storage.read_blob(&descriptor.digest).await?;
            layers.push(ImageLayer::new(
                data,
                descriptor.media_type.clone(),
                descriptor.annotations.clone(),
            ));
        }
        let config_bytes = self.storage.read_blob(&manifest.config.digest).await?;
        let config = Config::new(
            config_bytes,
            manifest.config.media_type.clone(),
            manifest.config.annotations.clone(),
        );

        client
            .push(&reference, &layers, config, &auth, Some(manifest))
            .await?;

        Ok(())
    }

    /// Pulls an image into the OCI layout and materializes it in the HMI store.
    pub async fn pull(&self, reference_str: &str) -> R<String> {
        let reference = parse_reference(reference_str)?;
        let client = make_client(&reference);
        let auth = RegistryAuth::Anonymous;

        let mut image_data = client
            .pull(&reference, &auth, vec![MIRUM_VM_CHUNK_MEDIA_TYPE])
            .await?;
        let manifest = image_data.manifest.ok_or(Error::MissingManifest)?;

        // `client.pull()` fetches layers via `buffer_unordered`, so `image_data.layers` is
        // in completion order, not manifest order. Each layer carries its own annotations.
        for layer in &mut image_data.layers {
            let data = std::mem::take(&mut layer.data);
            let (_, data) = self.storage.write_blob(data).await?;
            layer.data = data;
        }
        let config_data = std::mem::take(&mut image_data.config.data);
        let (_, config_data) = self.storage.write_blob(config_data).await?;
        image_data.config.data = config_data;

        let manifest_bytes = serde_json::to_vec(&manifest)?;
        let manifest_size = manifest_bytes.len();
        let (manifest_digest, _) = self.storage.write_blob(manifest_bytes).await?;
        self.storage.ensure_hmi_image(&manifest_digest).await?;

        let mut by_title: BTreeMap<String, Vec<(u64, ImageLayer)>> = BTreeMap::new();
        for layer in image_data.layers {
            let annotations = layer
                .annotations
                .as_ref()
                .ok_or(Error::InvalidLayer("missing annotations"))?;
            let title = annotations
                .get(ORG_OPENCONTAINERS_IMAGE_TITLE)
                .ok_or(Error::InvalidLayer("missing title annotation"))?;
            let offset_value = annotations
                .get(ANNOTATION_CHUNK_OFFSET)
                .ok_or(Error::InvalidLayer("missing offset annotation"))?;
            let offset: u64 = offset_value.parse().map_err(|_| Error::InvalidAnnotation {
                name: "offset",
                value: offset_value.clone(),
            })?;
            by_title
                .entry(title.clone())
                .or_default()
                .push((offset, layer));
        }

        for (title, chunks) in by_title {
            let mut decompressed_chunks = Vec::with_capacity(chunks.len());
            for (offset, layer) in chunks {
                let data = task::spawn_blocking(move || zstd::stream::decode_all(&layer.data[..]))
                    .await??;
                decompressed_chunks.push((offset, data));
            }
            self.storage
                .write_hmi_file(&manifest_digest, &title, decompressed_chunks)
                .await?;
        }

        let mut index = self.storage.read_index().await?;
        index_set(
            &mut index,
            reference_str,
            &manifest_digest,
            manifest_size as u64,
        );
        self.storage.write_index(&index).await?;

        Ok(manifest_digest)
    }

    /// Returns whether a reference is present in the local OCI layout.
    pub async fn contains(&self, reference_str: &str) -> R<bool> {
        Ok(index_lookup(&self.storage.read_index().await?, reference_str).is_some())
    }

    /// Materializes a locally tagged image into `destination` and returns config.json.
    pub async fn materialize(&self, reference_str: &str, destination: &Path) -> R<Vec<u8>> {
        let manifest_digest = index_lookup(&self.storage.read_index().await?, reference_str)
            .ok_or_else(|| Error::ImageNotFound(reference_str.to_string()))?;

        let manifest_bytes = self.storage.read_blob(&manifest_digest).await?;
        let manifest: OciImageManifest = serde_json::from_slice(&manifest_bytes)?;
        let config_bytes = self.storage.read_blob(&manifest.config.digest).await?;

        fs::create_dir_all(destination).await?;
        fs::write(destination.join("config.json"), &config_bytes).await?;

        let mut titles: BTreeSet<String> = BTreeSet::new();
        for descriptor in &manifest.layers {
            if let Some(title) = descriptor
                .annotations
                .as_ref()
                .and_then(|annotations| annotations.get(ORG_OPENCONTAINERS_IMAGE_TITLE))
            {
                titles.insert(title.clone());
            }
        }
        for title in titles {
            self.storage
                .export_hmi_file(&manifest_digest, &title, &destination.join(&title))
                .await?;
        }

        Ok(config_bytes)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicU64, Ordering};

    const TEST_CONFIG: &[u8] = br#"{
        "schemaVersion": 1,
        "kind": "MachineImage",
        "system": {
            "os": "linux",
            "name": "test",
            "version": "1",
            "architecture": "amd64"
        },
        "machine": {
            "cpu": { "minimum": 1, "default": 1 },
            "ram": { "minimum": 268435456, "default": 268435456 },
            "boot": [{ "protocol": "firmware-disk/bios", "disk": "root" }],
            "access": [{
                "type": "ssh",
                "port": 22,
                "user": "root",
                "auth": "empty-password"
            }],
            "network": { "mode": "dhcp" }
        },
        "disks": [{
            "id": "root",
            "format": "qcow2",
            "path": "root.hmi",
            "digest": "sha256:372409142c91c51316a7ab2b055596e145de56d99aa0f5eb110068876cef0be4",
            "virtSize": 14,
            "diskSize": 14
        }]
    }"#;

    fn temporary_directory() -> PathBuf {
        static NEXT_ID: AtomicU64 = AtomicU64::new(0);
        std::env::temp_dir().join(format!(
            "mirum-vm-oci-test-{}-{}",
            std::process::id(),
            NEXT_ID.fetch_add(1, Ordering::Relaxed)
        ))
    }

    #[test]
    fn manager_uses_separate_oci_and_hmi_roots() {
        tokio::runtime::Builder::new_current_thread()
            .build()
            .unwrap()
            .block_on(async {
                let root = temporary_directory();
                let source = root.join("source");
                let oci_root = root.join("oci");
                let hmi_root = root.join("hmi");
                let destination = root.join("destination");
                fs::create_dir_all(&source).await.unwrap();
                fs::write(source.join("config.json"), TEST_CONFIG)
                    .await
                    .unwrap();
                fs::write(source.join("root.hmi"), b"test hmi image")
                    .await
                    .unwrap();

                let storage = Storage::open(&oci_root, &hmi_root).await.unwrap();
                let images = ImageManager::new(storage);
                let reference = "example.test/mirum-vm:tokio";
                let digest = images.load(&source, Some(reference)).await.unwrap();

                assert!(images.contains(reference).await.unwrap());
                assert!(fs::try_exists(oci_root.join("index.json")).await.unwrap());
                assert!(
                    fs::try_exists(
                        hmi_root
                            .join(digest.strip_prefix("sha256:").unwrap_or(&digest))
                            .join("root.hmi")
                    )
                    .await
                    .unwrap()
                );

                let config = images.materialize(reference, &destination).await.unwrap();
                assert!(!config.is_empty());
                assert_eq!(
                    fs::read(destination.join("root.hmi")).await.unwrap(),
                    b"test hmi image"
                );

                assert!(matches!(images.contains("missing").await, Ok(false)));
                assert!(matches!(
                    images.materialize("missing", &destination).await,
                    Err(Error::ImageNotFound(reference)) if reference == "missing"
                ));

                fs::remove_dir_all(root).await.unwrap();
            });
    }
}
