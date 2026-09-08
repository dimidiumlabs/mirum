// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

use std::fmt::Display;
use std::path::{Path, PathBuf};
use std::process::exit;

use clap::{Parser, Subcommand};
use mirum_vm_hmi::{Access, MachineImage};
use mirum_vm_vmm::backend::qemu::QemuHypervisor;
use mirum_vm_vmm::{Hypervisor, MachineId, Settings};
use uuid::Uuid;

type R<T> = std::result::Result<T, String>;

#[derive(Parser)]
#[command(version, about)]
#[command(propagate_version = true)]
struct Cli {
    #[command(subcommand)]
    command: Commands,
}

#[derive(Subcommand)]
enum Commands {
    Image {
        #[command(subcommand)]
        command: ImageCommands,
    },

    Machine {
        #[command(subcommand)]
        command: MachineCommands,
    },
}

#[derive(Subcommand)]
enum ImageCommands {
    Pull {
        name: String,
    },

    Push {
        name: String,
    },

    Load {
        image: String,

        reference: Option<String>,
    },
}

#[derive(Subcommand)]
enum MachineCommands {
    Run {
        image: String,

        #[arg(default_value_t = 8022)]
        port: u16,
    },
}

fn die(msg: impl Display) -> ! {
    eprintln!("mirum-vm: {msg}");
    exit(1);
}

async fn cmd_run(images: &mirum_vm_oci::ImageManager, reference: &str, port: u16) -> R<()> {
    if !images
        .contains(reference)
        .await
        .map_err(|error| error.to_string())?
    {
        eprintln!("mirum-vm: {reference} not found locally, pulling...");
        images
            .pull(reference)
            .await
            .map_err(|error| error.to_string())?;
    }

    let scratch = std::env::temp_dir().join(format!("mirum-vm-run-{}", std::process::id()));
    let config = images
        .materialize(reference, &scratch)
        .await
        .map_err(|error| error.to_string())?;
    let image = MachineImage::from_json(&config).map_err(|e| e.to_string())?;
    let hv = QemuHypervisor;

    let boot = image
        .machine
        .boot
        .iter()
        .find(|b| matches!(b, mirum_vm_hmi::Boot::Linux(_)) && hv.supports(b))
        .or_else(|| image.machine.boot.iter().find(|b| hv.supports(b)))
        .ok_or("no boot protocol in manifest is supported by the qemu backend")?;

    let guest_port = image
        .machine
        .access
        .iter()
        .find_map(|a| match a {
            Access::Ssh { port, .. } => Some(*port),
            Access::Qga { .. } | Access::Unknown(_) => None,
        })
        .unwrap_or(22);
    let settings = Settings {
        cpu: image.machine.cpu.default as u32,
        ram: image.machine.ram.default / (1024 * 1024),
        port_forwards: vec![(port, guest_port)],
    };

    let mut machine = hv
        .create(
            MachineId(Uuid::now_v7()),
            None,
            &image,
            &scratch,
            boot,
            &settings,
        )
        .await
        .map_err(|e| e.to_string())?;

    eprintln!(
        "mirum-vm: booting {} via {} (ssh: localhost:{port})",
        scratch.display(),
        boot.protocol()
    );
    machine.start().await.map_err(|e| e.to_string())?;
    let status = machine.wait().await.map_err(|e| e.to_string())?;
    exit(status.code().unwrap_or(1));
}

#[tokio::main]
async fn main() {
    let cli = Cli::parse();

    let home = std::env::var("HOME").unwrap_or_else(|_| die("HOME is not set"));
    let root = PathBuf::from(home).join(".mirum-vm");

    let storage = mirum_vm_oci::Storage::open(root.join("oci"), root.join("hmi"))
        .await
        .unwrap_or_else(|e| die(e));

    let images = mirum_vm_oci::ImageManager::new(storage);

    match &cli.command {
        Commands::Image { command } => match &command {
            ImageCommands::Pull { name } => {
                let digest = images.pull(name).await.unwrap_or_else(|e| die(e));
                eprintln!("mirum-vm: pulled {} ({digest})", name);
            }

            ImageCommands::Push { name } => {
                images.push(name).await.unwrap_or_else(|e| die(e));
                eprintln!("mirum-vm: pushed {}", name);
            }

            ImageCommands::Load { reference, image } => {
                let digest = images
                    .load(Path::new(image), reference.as_deref())
                    .await
                    .unwrap_or_else(|e| die(e));

                match reference {
                    Some(reference) => {
                        eprintln!("mirum-vm: loaded {} as {reference} ({digest})", image)
                    }
                    None => eprintln!("mirum-vm: loaded {} ({digest}, untagged)", image),
                }
            }
        },
        Commands::Machine { command } => match &command {
            MachineCommands::Run { image, port } => {
                cmd_run(&images, image, *port)
                    .await
                    .unwrap_or_else(|e| die(e));
            }
        },
    }
}
