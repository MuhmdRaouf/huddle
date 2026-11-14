# =============================================================================
# huddle — toolchain image for every gate and the Linux builds.
# Hand-kept. No source COPY, only the tool pins (package.json, bun.lock) — docker-compose.yml
# bind-mounts the live checkout.
# =============================================================================

# ┌ Tag-pinned only; the tag mirrors mise.toml's rust = "1.100.0"
# └ — bump both together.
FROM rust:1.100.0-bookworm

# ┌ The gates need fmt and clippy, and the Linux builds need both musl targets
# └ for cargo zigbuild to cross-compile into.
RUN rustup component add rustfmt clippy \
    && rustup target add x86_64-unknown-linux-musl aarch64-unknown-linux-musl

# ┌ zig is the C cross-compiler behind cargo zigbuild. 0.17.1 is the newest
# │ stable release ziglang.org lists. Each tarball is checked against the
# └ sha256 ziglang.org publishes for that release.
ARG ZIG_VERSION=0.17.1
RUN set -eux; \
    case "$(dpkg --print-architecture)" in \
        amd64) arch=x86_64; sha=3581863e3cbcfd0fce0efc682ce7e9e93da96668a977791d356a26d2a7e9b17a ;; \
        arm64) arch=aarch64; sha=a1546d5646eb184437918d6635ca6b762a0e889eac09cef720ee7a1ed3fe7795 ;; \
        *) echo "unsupported architecture" >&2; exit 1 ;; \
    esac; \
    curl -fsSLo zig.tar.xz "https://ziglang.org/download/${ZIG_VERSION}/zig-${arch}-linux-${ZIG_VERSION}.tar.xz"; \
    echo "${sha}  zig.tar.xz" | sha256sum --check -; \
    mkdir /usr/local/zig; \
    tar -xJf zig.tar.xz --directory /usr/local/zig --strip-components=1; \
    rm zig.tar.xz
ENV PATH="/usr/local/zig:${PATH}"

# 0.24.1 is the newest crates.io release of cargo-zigbuild.
RUN cargo install --locked cargo-zigbuild@0.24.1

# ┌ pre-commit drives the repo-lint gate. 4.7.0 is mise.toml's pin — hook
# │ behavior drifts between versions. The venv holds the install: bookworm
# │ marks the system python as externally managed, and procps gives the test
# └ guard's stale-run cleanup its ps.
RUN apt-get update -qq \
    && apt-get install -y -qq --no-install-recommends procps python3-pip python3-venv \
    && rm -rf /var/lib/apt/lists/* \
    && python3 -m venv /opt/pre-commit \
    && /opt/pre-commit/bin/pip install --no-cache-dir pre-commit==4.7.0 \
    && ln -s /opt/pre-commit/bin/pre-commit /usr/local/bin/pre-commit

# ┌ Release binaries for the pre-commit hooks. gitleaks 8.30.1 is mise.toml's
# │ pin and the rev the gitleaks hook uses; actionlint 1.7.12 is the rev its
# │ hook pins; shellcheck 0.11.0 is the release actionlint runs on workflow
# │ shell blocks when present — no other file in the repo names it. Each archive
# │ is checked against the sha256 its release publishes (gitleaks and actionlint
# │ list them in their checksums files; shellcheck publishes none, so its
# └ hashes are those of the 0.11.0 archives).
RUN set -eux; \
    case "$(dpkg --print-architecture)" in \
        amd64) sha_arch=x64; al_arch=amd64; sc_arch=x86_64; \
               gl_sha=551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb; \
               al_sha=8aca8db96f1b94770f1b0d72b6dddcb1ebb8123cb3712530b08cc387b349a3d8; \
               sc_sha=8c3be12b05d5c177a04c29e3c78ce89ac86f1595681cab149b65b97c4e227198 ;; \
        arm64) sha_arch=arm64; al_arch=arm64; sc_arch=aarch64; \
               gl_sha=e4a487ee7ccd7d3a7f7ec08657610aa3606637dab924210b3aee62570fb4b080; \
               al_sha=325e971b6ba9bfa504672e29be93c24981eeb1c07576d730e9f7c8805afff0c6; \
               sc_sha=12b331c1d2db6b9eb13cfca64306b1b157a86eb69db83023e261eaa7e7c14588 ;; \
        *) echo "unsupported architecture" >&2; exit 1 ;; \
    esac; \
    curl -fsSLo gl.tar.gz "https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/gitleaks_8.30.1_linux_${sha_arch}.tar.gz"; \
    echo "${gl_sha}  gl.tar.gz" | sha256sum --check -; \
    tar -xzf gl.tar.gz gitleaks; \
    mv gitleaks /usr/local/bin/gitleaks; \
    rm gl.tar.gz; \
    curl -fsSLo al.tar.gz "https://github.com/rhysd/actionlint/releases/download/v1.7.12/actionlint_1.7.12_linux_${al_arch}.tar.gz"; \
    echo "${al_sha}  al.tar.gz" | sha256sum --check -; \
    tar -xzf al.tar.gz actionlint; \
    mv actionlint /usr/local/bin/actionlint; \
    rm al.tar.gz; \
    curl -fsSLo sc.tar.xz "https://github.com/koalaman/shellcheck/releases/download/v0.11.0/shellcheck-v0.11.0.linux.${sc_arch}.tar.xz"; \
    echo "${sc_sha}  sc.tar.xz" | sha256sum --check -; \
    tar -xJf sc.tar.xz; \
    mv shellcheck-v0.11.0/shellcheck /usr/local/bin/shellcheck; \
    rm -r sc.tar.xz shellcheck-v0.11.0

# ┌ bun runs the dev tooling and installs the tools package.json pins. The release zip is
# │ checked against its sha256 per architecture, as zig is, and unpacked into /usr/local/bin.
# │ esbuild (the dashboard page minifier) lands in /opt/tools, outside the bind-mounted checkout,
# │ so the host's node_modules is never touched; ESBUILD points the build at it. The image is
# │ rebuilt when package.json or bun.lock changes. HUDDLE_REQUIRE_MINIFY makes a build without
# └ esbuild fail. The bun version matches mise.toml.
ARG BUN_VERSION=1.4.4
RUN set -eux; \
    case "$(dpkg --print-architecture)" in \
        amd64) arch=x64; sha=bf8ccbfb2b0b80159e0f9a6d15c55317add8077534dbc6130a3edb52fccb395c ;; \
        arm64) arch=aarch64; sha=baa6bb36f51d6e58561e02bbea825038a9df454f673d6d776e8bbf86f8c7850e ;; \
        *) echo "unsupported architecture" >&2; exit 1 ;; \
    esac; \
    apt-get update -qq; \
    apt-get install -y -qq --no-install-recommends unzip; \
    rm -rf /var/lib/apt/lists/*; \
    curl -fsSLo bun.zip "https://github.com/oven-sh/bun/releases/download/bun-v${BUN_VERSION}/bun-linux-${arch}.zip"; \
    echo "${sha}  bun.zip" | sha256sum --check -; \
    unzip -j bun.zip "bun-linux-${arch}/bun" -d /usr/local/bin; \
    rm bun.zip; \
    bun --version
COPY package.json bun.lock /opt/tools/
RUN cd /opt/tools && bun install --frozen-lockfile
ENV ESBUILD=/opt/tools/node_modules/.bin/esbuild
ENV HUDDLE_REQUIRE_MINIFY=1

WORKDIR /work
