# Mirum VM Whitepaper

> [!CAUTION]
> This document describes the intended architecture and boundaries of Mirum VM.
> The implementation is still in progress and does not yet provide every
> capability described here.

Mirum VM is a portable machine-image format and a set of tools for distributing,
running, and deriving virtual machines.

Containers made Linux user spaces easy to package and distribute, but they share
the host kernel. They cannot represent a different kernel, an arbitrary
operating system, or a machine built for another CPU architecture. Virtual
machines can, but their images and configuration are commonly tied to one
hypervisor or one cloud.

Mirum VM provides a common boundary between a prepared virtual machine, an OCI
registry, and the hypervisor that runs it.

## Design

Mirum VM is built on two existing technologies:

- OCI solves distribution: content-addressed storage, large blobs, caching,
  deduplication, authentication, and multi-platform indexes.
- Qcow2 backing files represent derived disks efficiently: a child image stores
  only the blocks that differ from an immutable parent.

Mirum VM connects them through a filesystem format rather than through a service
API:

```text
                         OCI registry
                              ↕
                          mirum-vm-oci
                              ↕
external builders  →  machine directory  →  mirum-vm-vmm
  Packer, genimg,          config.json          ↓
  manual install,          *.hmi files     QEMU / native VMM
  existing VM
                              ↓
                       Imagefile runner
                     (commands via exec)
                              ↓
                    derived machine directory
```

`mirum-vm-oci` and `mirum-vm-vmm` do not call each other. The materialized
machine directory is their only contract. A directory may be created without OCI, and a
pulled image may be inspected or copied without starting a hypervisor.

## Machine Format

A materialized machine is a directory containing a `config.json`, one or more
HMI (`.hmi`) disks, and optional boot resources such as a kernel or initrd.

```text
machine/
├── config.json
├── root.hmi
├── kernel
└── initrd
```

`config.json` describes the machine contract:

- the guest operating system and CPU architecture;
- minimum and default resources;
- available boot protocols;
- disks and other required files;
- networking expectations;
- ways to access the guest, such as SSH or a guest agent.

It describes what the image requires and supports, not a hypervisor command
line. Deployment-specific choices such as host port mappings, bridged networks,
and instance names are runtime settings and are not part of the immutable image.

### HMI

HMI is a constrained form of qcow2 intended for portable disk chains. Its
required invariant is semantic rather than byte-for-byte equivalence: given the
complete backing chain, an HMI disk and its flattened raw representation have
the same virtual size and expose the same bytes to the guest.

This invariant enables two operations:

1. Flatten an HMI chain into raw, then convert it if necessary for a hypervisor
   that cannot consume qcow2 natively.
2. Convert a modified disk back to qcow2 and safely rebase it onto its original
   base, producing a small HMI that contains only the guest-visible difference.

Qcow2 metadata such as physical cluster placement, compression, or internal
allocation does not need to survive this round trip. The virtual disk contents
do.

Backing images are immutable. A running instance writes to a separate overlay;
published images and shared cache entries are never used as writable instance
state.

## Distribution

`mirum-vm-oci` maps a machine directory and all HMI files needed by its backing
chains to OCI artifacts. Large files may be split into independently compressed
chunks so they can be transferred and verified in parallel.

It is responsible for:

- loading and saving already prepared machines;
- pushing and pulling OCI artifacts;
- maintaining a content-addressed local cache;
- resolving complete backing chains;
- materializing a machine directory on disk.

OCI indexes can contain variants for different guest operating systems and CPU
architectures. Native execution is an optimization, not a requirement: a user
may intentionally select a foreign architecture and run it through emulation.

`mirum-vm-oci` does not provision guests and does not know how a machine will be
executed.

## Runtime

`mirum-vm-vmm` starts a materialized machine directory. Hypervisor backends
translate the machine contract into their native configuration and expose a
common machine lifecycle.

QEMU is the tier-0 backend. It provides a widely available implementation of the
machine model and, importantly, full-system CPU emulation. This makes scenarios
such as a RISC-V NetBSD guest on an Arm macOS host valid even when they are not
fast.

Native backends may provide better integration and performance when the host and
guest are compatible. They may flatten or convert HMI disks into their native
storage format before starting the VM. This conversion is a runtime detail and
does not change the distributed Mirum VM image.

`mirum-vm-vmm` does not know whether its input came from an OCI registry, a local
builder, an exported VM, or a directory copied by the user.

## Derivation with Imagefile

A Imagefile is a recipe for deriving one Mirum VM image from another. Unlike an
external image builder, it does not install an operating system from scratch.
Its input is an already loaded machine that can be started and controlled
through an `exec`-capable access method.

Every Imagefile has exactly one parent image. There is no empty base and no
equivalent of `FROM scratch`: creating the first bootable machine always happens
outside the Imagefile workflow and enters Mirum VM through load or import.

The intended execution model is deliberately simple:

1. Resolve and pin the immutable parent image.
2. Start it with a persistent writable instance overlay.
3. Execute the Imagefile commands sequentially inside the guest through `exec`.
4. If every command succeeds, shut the guest down and flush its disk state.
5. Rebase the resulting disk onto the parent and produce a new HMI and
   `config.json`.
6. If any command fails, discard the temporary instance and produce no image.

One Imagefile produces one image boundary. Individual commands do not create
published layers or snapshots, and intermediate states are not part of the
format. This keeps the backing chain tied to meaningful image derivations rather
than to the number of provisioning commands.

The exact Imagefile syntax is not defined yet. It is expected to be a small shell
dialect for running arbitrary commands, with only the additional structure
needed to identify the parent and describe changes to the resulting machine
configuration. A Imagefile runs in the context of its parent guest and is not
implicitly portable across operating systems.

## Image Lifecycle

### Prepare, Import, and Load

Preparing a base guest operating system is outside Mirum VM. Users may use
Packer, a `genimg` script, an unattended installer, or a manually configured VM.
A finished machine with HMI disks and a `config.json` enters the local store
through load and can be exported again through save. A qcow2 or raw disk image
instead enters through import, which converts it to HMI and constructs the
machine configuration where possible. The resulting Mirum VM image can then be
distributed or used as the parent of a Imagefile.

This boundary is important for systems whose prebuilt images cannot be freely
redistributed. A project can publish a recipe that downloads official
installation media, builds an image on the user's machine, and pushes the result
to the user's own OCI registry.

### Publish

The prepared machine is loaded, validated, split into OCI blobs, and pushed to
a registry. Tags provide convenient names; digests identify immutable
versions.

### Run

The OCI artifact is pulled and materialized. `mirum-vm-vmm` selects a compatible
backend and boot protocol, creates writable instance state, and starts the VM.

### Derive

A Imagefile is the standard path for reproducibly deriving an image. A modified
instance disk may also be converted back to HMI and rebased onto the immutable
image from which it originated. Only the resulting difference needs to be
published; unchanged parent data is reused through the backing chain and OCI
content store.

## Scope

Mirum VM defines how a prepared machine is represented, distributed,
materialized, run, and derived. It does not:

- install a base guest operating system from installation media;
- replace Packer, unattended installers, or other from-scratch image builders;
- create a machine from an empty Imagefile parent;
- serve as a general-purpose workload orchestrator inside the guest;
- require a particular OCI registry;
- promise native acceleration for every host and guest combination;
- grant redistribution rights for operating systems or software contained in an
  image.

The format is the product boundary. Builders produce it, registries transport
it, and runtimes consume it independently.
