// SPDX-FileCopyrightText: 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

fn main() {
    dimidiumlabs_ui_build::build("APPLICATION", &["src/styles"], &["src/assets"])
        .expect("failed to compile UI assets");

    println!("cargo::rerun-if-changed=../../Cargo.lock");
    println!("cargo::rerun-if-changed=../../mise.toml");
    println!("cargo::rerun-if-changed=../../mise.lock");

    let out_dir = std::env::var("OUT_DIR").expect("OUT_DIR is set by Cargo");
    let target = std::env::var("TARGET").expect("TARGET is set by Cargo");
    let output = format!("{out_dir}/licenses.json");

    let status = std::process::Command::new("mise")
        .current_dir(env!("CARGO_MANIFEST_DIR"))
        .env("MISE_DISABLE_TOOLS", "helm,shellcheck,watchexec,python")
        .env("MISE_NO_UPDATE_NOTIFIER", "1")
        .args([
            "run",
            "licenses-json",
            "--",
            "--manifest-path",
            "Cargo.toml",
            "--output",
            &output,
            "--target",
            &target,
        ])
        .status()
        .expect("failed to run licenses-json task");
    assert!(status.success(), "failed to generate licenses.json");
}
