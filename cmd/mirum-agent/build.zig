// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

const std = @import("std");

pub fn build(b: *std.Build) void {
    const target = b.standardTargetOptions(.{
        .default_target = .{ .abi = .musl },
    });
    const optimize = b.standardOptimizeOption(.{});

    const strip = b.option(bool, "strip", "Strip debug info from the agent binary") orelse false;

    // Strict flags for our own C. Unity is compiled separately, without
    // them — a third-party header should not have to satisfy our lint.
    const cflags: []const []const u8 = &.{
        "-std=c99",
        "-Wall",
        "-Wextra",
        "-Wconversion",
        "-Wshadow",
        "-Wstrict-prototypes",
    };

    // libmirum-agent: the agent logic, compiled once and exposed only
    // through mirum-agent.h. Both the executable and the tests link it.
    const lib = b.addLibrary(.{
        .name = "mirum-agent",
        .linkage = .static,
        .root_module = b.createModule(.{
            .target = target,
            .optimize = optimize,
            .strip = strip,
            .link_libc = true,
            .link_libcpp = false,
        }),
    });
    lib.root_module.addIncludePath(b.path(""));
    lib.root_module.addCSourceFile(.{
        .file = b.path("mirum-agent.c"),
        .flags = cflags,
    });

    // mirum-agent: the executable entry point, links the library.
    const exe = b.addExecutable(.{
        .name = "mirum-agent",
        .root_module = b.createModule(.{
            .target = target,
            .optimize = optimize,
            .strip = strip,
            .link_libc = true,
            .link_libcpp = false,
        }),
    });
    exe.root_module.addIncludePath(b.path(""));
    exe.root_module.addCSourceFile(.{
        .file = b.path("main.c"),
        .flags = cflags,
    });
    exe.root_module.linkLibrary(lib);

    b.installArtifact(exe);

    const run_cmd = b.addRunArtifact(exe);
    run_cmd.step.dependOn(b.getInstallStep());
    if (b.args) |args| run_cmd.addArgs(args);

    const run_step = b.step("run", "Run the agent");
    run_step.dependOn(&run_cmd.step);

    // Unity: built as its own library so its sources never see our flags.
    const unity_dep = b.dependency("unity", .{});
    const unity = b.addLibrary(.{
        .name = "unity",
        .linkage = .static,
        .root_module = b.createModule(.{
            .target = target,
            .optimize = optimize,
            .link_libc = true,
            .link_libcpp = false,
        }),
    });
    unity.root_module.addIncludePath(unity_dep.path("src"));
    unity.root_module.addCSourceFile(.{
        .file = unity_dep.path("src/unity.c"),
    });

    // test: links libmirum-agent through its public header only — the
    // agent sources are never recompiled into the test binary. Unity's
    // headers go on the system path so they bypass our warnings.
    const exe_tests = b.addExecutable(.{
        .name = "test",
        .root_module = b.createModule(.{
            .target = target,
            .optimize = optimize,
            .link_libc = true,
            .link_libcpp = false,
        }),
    });
    exe_tests.root_module.addIncludePath(b.path(""));
    exe_tests.root_module.addSystemIncludePath(unity_dep.path("src"));
    exe_tests.root_module.addCSourceFile(.{
        .file = b.path("test.c"),
        .flags = cflags,
    });
    exe_tests.root_module.linkLibrary(lib);
    exe_tests.root_module.linkLibrary(unity);

    const run_exe_tests = b.addRunArtifact(exe_tests);

    const test_step = b.step("test", "Run tests");
    test_step.dependOn(&run_exe_tests.step);
}
