# =============================================================================
# huddle — toolchain image for every gate and the Linux builds.
# Hand-kept. No source COPY — docker-compose.yml bind-mounts the live checkout.
# =============================================================================

# ┌ Tag-pinned only; the tag mirrors mise.toml's rust = "1.99.1"
# └ — bump both together.
FROM rust:1.99.1-bookworm

# ┌ The gates need fmt and clippy, and the Linux builds need both musl targets
# └ for cargo zigbuild to cross-compile into.
RUN rustup component add rustfmt clippy \
    && rustup target add x86_64-unknown-linux-musl aarch64-unknown-linux-musl

# ┌ zig is the C cross-compiler behind cargo zigbuild. 0.16.0 is the release
# │ the pinned cargo-zigbuild documents. Each tarball is checked against the
# └ sha256 ziglang.org publishes for that release.
ARG ZIG_VERSION=0.16.0
RUN set -eux; \
    case "$(dpkg --print-architecture)" in \
        amd64) arch=x86_64; sha=70e49664a74374b48b51e6f3fdfbf437f6395d42509050588bd49abe52ba3d00 ;; \
        arm64) arch=aarch64; sha=ea4b09bfb22ec6f6c6ceac57ab63efb6b46e17ab08d21f69f3a48b38e1534f17 ;; \
        *) echo "unsupported architecture" >&2; exit 1 ;; \
    esac; \
    curl -fsSLo zig.tar.xz "https://ziglang.org/download/${ZIG_VERSION}/zig-${arch}-linux-${ZIG_VERSION}.tar.xz"; \
    echo "${sha}  zig.tar.xz" | sha256sum --check -; \
    mkdir /usr/local/zig; \
    tar -xJf zig.tar.xz --directory /usr/local/zig --strip-components=1; \
    rm zig.tar.xz
ENV PATH="/usr/local/zig:${PATH}"

# ┌ 0.24.1 is the newest crates.io release of cargo-zigbuild, and the release
# └ that pairs with zig 0.16.
RUN cargo install --locked cargo-zigbuild@0.24.1

# ┌ pre-commit drives the repo-lint gate. 4.6.2 is mise.toml's pin — hook
# │ behavior drifts between versions. The venv holds the install: bookworm
# │ marks the system python as externally managed, and procps gives the test
# └ guard's stale-run cleanup its ps.
RUN apt-get update -qq \
    && apt-get install -y -qq --no-install-recommends procps python3-pip python3-venv \
    && rm -rf /var/lib/apt/lists/* \
    && python3 -m venv /opt/pre-commit \
    && /opt/pre-commit/bin/pip install --no-cache-dir pre-commit==4.6.2 \
    && ln -s /opt/pre-commit/bin/pre-commit /usr/local/bin/pre-commit

# ┌ Release binaries for the pre-commit hooks. gitleaks 8.30.1 is mise.toml's
# │ pin and the rev the gitleaks hook uses; actionlint 1.7.12 is the rev its
# │ hook pins; shellcheck 0.11.0 is the release actionlint runs on workflow
# └ shell blocks when present — no other file in the repo names it.
RUN set -eux; \
    case "$(dpkg --print-architecture)" in \
        amd64) sha_arch=x64; al_arch=amd64; sc_arch=x86_64 ;; \
        arm64) sha_arch=arm64; al_arch=arm64; sc_arch=aarch64 ;; \
        *) echo "unsupported architecture" >&2; exit 1 ;; \
    esac; \
    curl -fsSLo gl.tar.gz "https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/gitleaks_8.30.1_linux_${sha_arch}.tar.gz"; \
    tar -xzf gl.tar.gz gitleaks; \
    mv gitleaks /usr/local/bin/gitleaks; \
    rm gl.tar.gz; \
    curl -fsSLo al.tar.gz "https://github.com/rhysd/actionlint/releases/download/v1.7.12/actionlint_1.7.12_linux_${al_arch}.tar.gz"; \
    tar -xzf al.tar.gz actionlint; \
    mv actionlint /usr/local/bin/actionlint; \
    rm al.tar.gz; \
    curl -fsSLo sc.tar.xz "https://github.com/koalaman/shellcheck/releases/download/v0.11.0/shellcheck-v0.11.0.linux.${sc_arch}.tar.xz"; \
    tar -xJf sc.tar.xz; \
    mv shellcheck-v0.11.0/shellcheck /usr/local/bin/shellcheck; \
    rm -r sc.tar.xz shellcheck-v0.11.0

WORKDIR /work
