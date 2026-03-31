# Mirum

An experimental CI built around virtual machines and Starlark

## Installation

Please note that the project is in its infancy and is **not** intended for production use.

**Debian/Ubuntu:**

```bash
sudo apt install curl gnupg

curl -fsSL https://dl.mirum.dev/public.gpg | sudo gpg --dearmor -o /usr/share/keyrings/mirum.gpg
echo "deb [signed-by=/usr/share/keyrings/mirum.gpg] https://dl.mirum.dev/apt/ nightly main" | sudo tee /etc/apt/sources.list.d/mirum.list
sudo apt update && sudo apt install mirum

# Start the server
sudo systemctl enable --now mirumd

# Start a worker (optional, can run on a different host)
sudo systemctl enable --now mirumw@default
```

**Fedora/RHEL:**

```bash
# DNF5 (Fedora 41+, RHEL 10+)
sudo dnf config-manager addrepo --from-repofile=https://dl.mirum.dev/rpm/nightly/mirum-nightly.repo

# DNF4 (Fedora 40 and older, RHEL 8/9)
sudo curl -o /etc/yum.repos.d/mirum-nightly.repo https://dl.mirum.dev/rpm/nightly/mirum-nightly.repo

sudo dnf install mirum

# Start the server
sudo systemctl enable --now mirumd

# Start a worker (optional, can run on a different host)
sudo systemctl enable --now mirumw@default
```

**openSUSE:**

```bash
sudo rpm --import https://dl.mirum.dev/public.gpg
sudo zypper addrepo https://dl.mirum.dev/rpm/nightly/ mirum-nightly
sudo zypper refresh
sudo zypper install mirum

# Start the server
sudo systemctl enable --now mirumd

# Start a worker (optional, can run on a different host)
sudo systemctl enable --now mirumw@default
```

## License

Copyright (C) 2026 Nikolay Govorov

This program is free software: you can redistribute it and/or modify it under
the terms of the GNU Affero General Public License as published by the Free
Software Foundation, either version 3 of the License, or (at your option) any
later version.

This program is distributed in the hope that it will be useful, but WITHOUT ANY
WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS FOR A
PARTICULAR PURPOSE. See the GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License along
with this program. If not, see <https://www.gnu.org/licenses/>.
