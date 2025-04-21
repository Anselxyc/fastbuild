# This Dockerfile builds fastbuild (moby-18.09.6) with CRIU support on Debian Buster

#################################################################
# Base stage: Go + Debian Buster
#################################################################
FROM golang:1.17-buster AS base

# Keep version info for legacy scripts
ENV GO_VERSION=1.17

# Install common build tools
RUN apt-get update && apt-get install -y --no-install-recommends \
	build-essential \
	curl \
	git \
	python3-protobuf \
	protobuf-compiler \
	libprotobuf-dev \
	libprotobuf-c-dev \
	libnl-3-dev \
	libcap-dev \
	&& rm -rf /var/lib/apt/lists/*

#################################################################
# CRIU stage: build and install CRIU 3.6 with warnings disabled
#################################################################
FROM base AS criu

ARG CRIU_VERSION=3.6
# Disable -Werror to avoid build failures with newer GCC/glibc
ENV CFLAGS="-Wno-error" CXXFLAGS="-Wno-error"

RUN apt-get update && apt-get install -y --no-install-recommends \
	libnet-dev \
	libprotobuf-dev \
	libprotobuf-c-dev \
	libnl-3-dev \
	libcap-dev \
	protobuf-compiler \
	protobuf-c-compiler \
	python3-protobuf \
	&& rm -rf /var/lib/apt/lists/* \
	&& mkdir -p /usr/src/criu /build \
	&& curl -fsSL https://github.com/checkpoint-restore/criu/archive/v${CRIU_VERSION}.tar.gz \
	| tar -xzC /usr/src/criu --strip-components=1 \
	&& cd /usr/src/criu \
	&& make CFLAGS="${CFLAGS}" CXXFLAGS="${CXXFLAGS}" \
	&& make PREFIX=/build install-criu

#################################################################
# Registry stage: unchanged from original
#################################################################
FROM base AS registry
ENV REGISTRY_COMMIT_SCHEMA1 ec87e9b6971d831f0eff752ddb54fb64693e51cd
ENV REGISTRY_COMMIT 47a064d4195a9b56133891bbb13620c3ac83a827
RUN set -x \
	&& export GOPATH="$(mktemp -d)" \
	&& git clone https://github.com/docker/distribution.git "$GOPATH/src/github.com/docker/distribution" \
	&& cd "$GOPATH/src/github.com/docker/distribution" && git checkout -q "$REGISTRY_COMMIT" \
	&& GOPATH="$GOPATH/src/github.com/docker/distribution/Godeps/_workspace:$GOPATH" \
	go build -buildmode=pie -o /build/registry-v2 github.com/docker/distribution/cmd/registry \
	&& case $(dpkg --print-architecture) in \
	amd64|ppc64*|s390x) \
	cd "$GOPATH/src/github.com/docker/distribution" && git checkout -q "$REGISTRY_COMMIT_SCHEMA1"; \
	GOPATH="$GOPATH/src/github.com/docker/distribution/Godeps/_workspace:$GOPATH"; \
	go build -buildmode=pie -o /build/registry-v2-schema1 github.com/docker/distribution/cmd/registry; \
	;; \
	esac \
	&& rm -rf "$GOPATH"

#################################################################
# docker-py stage: unchanged
#################################################################
FROM base AS docker-py
ENV DOCKER_PY_COMMIT 8b246db271a85d6541dc458838627e89c683e42f
RUN git clone https://github.com/docker/docker-py.git /build \
	&& cd /build && git checkout -q $DOCKER_PY_COMMIT

#################################################################
# swagger stage: unchanged
#################################################################
FROM base AS swagger
ENV GO_SWAGGER_COMMIT c28258affb0b6251755d92489ef685af8d4ff3eb
RUN export GOPATH="$(mktemp -d)" \
	&& git clone https://github.com/go-swagger/go-swagger.git "$GOPATH/src/github.com/go-swagger/go-swagger" \
	&& cd "$GOPATH/src/github.com/go-swagger/go-swagger" && git checkout -q "$GO_SWAGGER_COMMIT" \
	&& go build -o /build/swagger github.com/go-swagger/go-swagger/cmd/swagger \
	&& rm -rf "$GOPATH"

#################################################################
# frozen-images stage: unchanged
#################################################################
FROM base AS frozen-images
RUN apt-get update && apt-get install -y jq ca-certificates --no-install-recommends \
	&& rm -rf /var/lib/apt/lists/*
COPY contrib/download-frozen-image-v2.sh /
RUN /download-frozen-image-v2.sh /build \
	buildpack-deps:jessie@sha256:dd86dced7c9cd2a724e779730f0a53f93b7ef42228d4344b25ce9a42a1486251 \
	busybox:latest@sha256:bbc3a03235220b170ba48a157dd097dd1379299370e1ed99ce976df0355d24f0 \
	busybox:glibc@sha256:0b55a30394294ab23b9afd58fab94e61a923f5834fba7ddbae7f8e0c11ba85e6 \
	debian:jessie@sha256:287a20c5f73087ab406e6b364833e3fb7b3ae63ca0eb3486555dc27ed32c6e60 \
	hello-world:latest@sha256:be0cd392e45be79ffeffa6b05338b98ebb16c87b255f48e297ec7f98e123905c

#################################################################
# runtime-dev stage: unchanged
#################################################################
FROM base AS runtime-dev
RUN apt-get update && apt-get install -y \
	libapparmor-dev libseccomp-dev \
	&& rm -rf /var/lib/apt/lists/*

#################################################################
# Install other components: tomlv, vndr, containerd, proxy, gometalinter, dockercli, runc, tini
#################################################################
# (unchanged sections omitted for brevity)

#################################################################
# Final dev stage: unchanged
#################################################################
FROM runtime-dev AS dev
RUN groupadd -r docker && useradd --create-home --gid docker unprivilegeduser \
	&& ln -sfv /go/src/github.com/docker/docker/.bashrc ~/.bashrc \
	&& echo "source /usr/share/bash-completion/bash_completion" >> /etc/bash.bashrc \
	&& ln -s /usr/local/completion/bash/docker /etc/bash_completion.d/docker \
	&& ldconfig \
	&& apt-get update && apt-get install -y --no-install-recommends \
	apparmor aufs-tools bash-completion btrfs-tools iptables jq libcap2-bin \
	libdevmapper-dev libudev-dev libsystemd-dev binutils-mingw-w64 \
	g++-mingw-w64-x86-64 net-tools pigz python3 python3-pip thin-provisioning-tools \
	vim-common xfsprogs zip bzip2 xz-utils \
	&& rm -rf /var/lib/apt/lists/*
COPY --from=swagger /build/swagger* /usr/local/bin/
COPY --from=frozen-images /build/ /docker-frozen-images
COPY --from=gometalinter /build/ /usr/local/bin/
COPY --from=tomlv /build/ /usr/local/bin/
COPY --from=vndr /build/ /usr/local/bin/
COPY --from=tini /build/ /usr/local/bin/
COPY --from=runc /build/ /usr/local/bin/
COPY --from=containerd /build/ /usr/local/bin/
COPY --from=proxy /build/ /usr/local/bin/
COPY --from=dockercli /build/ /usr/local/cli
COPY --from=registry /build/registry* /usr/local/bin/
COPY --from=criu /build/ /usr/local/
COPY --from=docker-py /build/ /docker-py
RUN cd /docker-py && pip3 install docker-pycreds==0.2.1 yamllint==1.5.0 -r test-requirements.txt

ENV PATH=/usr/local/cli:$PATH
ENV DOCKER_BUILDTAGS="apparmor seccomp selinux"
ENV GOMETALINTER_OPTS="--deadline=2m"
WORKDIR /go/src/github.com/docker/docker
VOLUME /var/lib/docker
ENTRYPOINT ["hack/dind"]
COPY . /go/src/github.com/docker/docker
