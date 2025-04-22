# ====================== 基础阶段 ======================
FROM golang:1.10.8 AS base
ENV GO_VERSION 1.10.8
# 使用国内镜像加速（阿里云）
ARG APT_MIRROR=archive.debian.org/debian
ARG SECURITY_MIRROR=archive.debian.org/debian-security


# 关键修改点1：更新密钥包版本
RUN echo "deb [check-valid-until=no allow-insecure=yes] http://${APT_MIRROR} stretch main contrib non-free" > /etc/apt/sources.list \
	&& echo "deb [check-valid-until=no allow-insecure=yes] http://${SECURITY_MIRROR} stretch/updates main contrib non-free" >> /etc/apt/sources.list \
	&& echo 'Acquire::Check-Valid-Until "false";' > /etc/apt/apt.conf.d/10-no-check-valid-until \
	&& echo 'APT::Get::AllowUnauthenticated "true";' > /etc/apt/apt.conf.d/10-no-auth

# 关键修改点2：使用新版密钥包（2023.3版本）
RUN wget -O /tmp/keyring.deb http://archive.debian.org/debian/pool/main/d/debian-archive-keyring/debian-archive-keyring_2019.1+deb10u1_all.deb \
	&& dpkg -i /tmp/keyring.deb \
	&& rm /tmp/keyring.deb

# ====================== CRIU阶段 ====================== 
FROM base AS criu
ENV CRIU_VERSION 3.6
RUN rm -rf /var/lib/apt/lists/*
RUN apt-get update -o Acquire::AllowInsecureRepositories=true \
	&& apt-get install -y --allow-unauthenticated \
	libnet-dev \
	libprotobuf-c0-dev \
	libprotobuf-dev \
	libnl-3-dev \
	libcap-dev \
	protobuf-compiler \
	protobuf-c-compiler \
	python-protobuf \
	&& mkdir -p /usr/src/criu \
	&& curl -sSL https://github.com/checkpoint-restore/criu/archive/v${CRIU_VERSION}.tar.gz | tar -C /usr/src/criu/ -xz --strip-components=1 \
	&& cd /usr/src/criu \
	&& make \
	&& make PREFIX=/build/ install-criu

# ====================== Registry阶段 ======================
FROM base AS registry
ENV REGISTRY_COMMIT_SCHEMA1 ec87e9b6971d831f0eff752ddb54fb64693e51cd
ENV REGISTRY_COMMIT 47a064d4195a9b56133891bbb13620c3ac83a827
RUN rm -rf /var/lib/apt/lists/*
RUN apt-get update -o Acquire::AllowInsecureRepositories=true \
	&& apt-get install -y --allow-unauthenticated git ca-certificates
RUN set -x \
	&& export GOPATH="$(mktemp -d)" \
	&& git clone https://github.com/docker/distribution.git "$GOPATH/src/github.com/docker/distribution" \
	&& (cd "$GOPATH/src/github.com/docker/distribution" && git checkout -q "$REGISTRY_COMMIT") \
	&& GOPATH="$GOPATH/src/github.com/docker/distribution/Godeps/_workspace:$GOPATH" \
	go build -buildmode=pie -o /build/registry-v2 github.com/docker/distribution/cmd/registry \
	&& case $(dpkg --print-architecture) in \
	amd64|ppc64*|s390x) \
	(cd "$GOPATH/src/github.com/docker/distribution" && git checkout -q "$REGISTRY_COMMIT_SCHEMA1"); \
	GOPATH="$GOPATH/src/github.com/docker/distribution/Godeps/_workspace:$GOPATH"; \
	go build -buildmode=pie -o /build/registry-v2-schema1 github.com/docker/distribution/cmd/registry; \
	;; \
	esac \
	&& rm -rf "$GOPATH"

# ====================== Docker-Py阶段 ======================
FROM base AS docker-py
ENV DOCKER_PY_COMMIT 8b246db271a85d6541dc458838627e89c683e42f
RUN rm -rf /var/lib/apt/lists/*
RUN apt-get update -o Acquire::AllowInsecureRepositories=true \
	&& apt-get install -y --allow-unauthenticated git
RUN git clone https://github.com/docker/docker-py.git /build \
	&& cd /build \
	&& git checkout -q $DOCKER_PY_COMMIT

# ====================== Swagger阶段 ======================
FROM base AS swagger
ENV GO_SWAGGER_COMMIT c28258affb0b6251755d92489ef685af8d4ff3eb
RUN rm -rf /var/lib/apt/lists/*
RUN apt-get update -o Acquire::AllowInsecureRepositories=true \
	&& apt-get install -y --allow-unauthenticated git ca-certificates
RUN set -x \
	&& export GOPATH="$(mktemp -d)" \
	&& git clone https://github.com/go-swagger/go-swagger.git "$GOPATH/src/github.com/go-swagger/go-swagger" \
	&& (cd "$GOPATH/src/github.com/go-swagger/go-swagger" && git checkout -q "$GO_SWAGGER_COMMIT") \
	&& go build -o /build/swagger github.com/go-swagger/go-swagger/cmd/swagger \
	&& rm -rf "$GOPATH"

# ====================== Frozen-Images阶段 ======================
FROM base AS frozen-images
RUN rm -rf /var/lib/apt/lists/*
RUN apt-get update -o Acquire::AllowInsecureRepositories=true \
	&& apt-get install -y --allow-unauthenticated jq ca-certificates
COPY contrib/download-frozen-image-v2.sh /
# 添加这行
RUN chmod +x /download-frozen-image-v2.sh  
RUN /download-frozen-image-v2.sh /build \
	buildpack-deps:jessie@sha256:dd86dced7c9cd2a724e779730f0a53f93b7ef42228d4344b25ce9a42a1486251 \
	busybox:latest@sha256:bbc3a03235220b170ba48a157dd097dd1379299370e1ed99ce976df0355d24f0 \
	busybox:glibc@sha256:0b55a30394294ab23b9afd58fab94e61a923f5834fba7ddbae7f8e0c11ba85e6 \
	debian:jessie@sha256:287a20c5f73087ab406e6b364833e3fb7b3ae63ca0eb3486555dc27ed32c6e60 \
	hello-world:latest@sha256:be0cd392e45be79ffeffa6b05338b98ebb16c87b255f48e297ec7f98e123905c

# ====================== Runtime-Dev阶段 ======================
FROM base AS runtime-dev
RUN rm -rf /var/lib/apt/lists/*
RUN apt-get update -o Acquire::AllowInsecureRepositories=true \
	&& apt-get install -y --allow-unauthenticated \
	libapparmor-dev \
	libseccomp-dev

# ====================== 工具安装阶段 ======================
FROM base AS tomlv
ENV INSTALL_BINARY_NAME=tomlv
RUN rm -rf /var/lib/apt/lists/*
COPY hack/dockerfile/install/install.sh ./install.sh
COPY hack/dockerfile/install/$INSTALL_BINARY_NAME.installer ./
# 添加权限设置
RUN chmod +x install.sh && \
	chmod +x $INSTALL_BINARY_NAME.installer
RUN PREFIX=/build/ ./install.sh $INSTALL_BINARY_NAME

FROM base AS vndr
ENV INSTALL_BINARY_NAME=vndr
RUN rm -rf /var/lib/apt/lists/*
COPY hack/dockerfile/install/install.sh ./install.sh
COPY hack/dockerfile/install/$INSTALL_BINARY_NAME.installer ./
# 添加权限设置
RUN chmod +x install.sh && \
	chmod +x $INSTALL_BINARY_NAME.installer
RUN PREFIX=/build/ ./install.sh $INSTALL_BINARY_NAME

FROM base AS containerd
RUN rm -rf /var/lib/apt/lists/*
RUN apt-get update -o Acquire::AllowInsecureRepositories=true \
	&& apt-get install -y --allow-unauthenticated btrfs-tools
ENV INSTALL_BINARY_NAME=containerd
COPY hack/dockerfile/install/install.sh ./install.sh
COPY hack/dockerfile/install/$INSTALL_BINARY_NAME.installer ./
# 添加权限设置
RUN chmod +x install.sh && \
	chmod +x $INSTALL_BINARY_NAME.installer
RUN PREFIX=/build/ ./install.sh $INSTALL_BINARY_NAME

# ====================== gometalinter 阶段 ======================
FROM base AS gometalinter
ENV INSTALL_BINARY_NAME=gometalinter
RUN rm -rf /var/lib/apt/lists/*
COPY hack/dockerfile/install/install.sh ./install.sh
COPY hack/dockerfile/install/$INSTALL_BINARY_NAME.installer ./
RUN chmod +x install.sh && chmod +x $INSTALL_BINARY_NAME.installer
RUN PREFIX=/build/ ./install.sh $INSTALL_BINARY_NAME
# ====================== tini 阶段 ======================
FROM base AS tini
RUN apt-get update -o Acquire::AllowInsecureRepositories=true \
	&& apt-get install -y --allow-unauthenticated cmake vim-common
COPY hack/dockerfile/install/install.sh ./install.sh
ENV INSTALL_BINARY_NAME=tini
COPY hack/dockerfile/install/$INSTALL_BINARY_NAME.installer ./
RUN chmod +x install.sh && chmod +x $INSTALL_BINARY_NAME.installer
RUN PREFIX=/build/ ./install.sh $INSTALL_BINARY_NAME

# ====================== proxy 阶段 ======================
FROM base AS proxy
ENV INSTALL_BINARY_NAME=proxy
RUN rm -rf /var/lib/apt/lists/*
COPY hack/dockerfile/install/install.sh ./install.sh
COPY hack/dockerfile/install/$INSTALL_BINARY_NAME.installer ./
RUN chmod +x install.sh && chmod +x $INSTALL_BINARY_NAME.installer
RUN PREFIX=/build/ ./install.sh $INSTALL_BINARY_NAME

# ====================== dockercli 阶段 ======================
FROM base AS dockercli
ENV INSTALL_BINARY_NAME=dockercli
RUN rm -rf /var/lib/apt/lists/*
COPY hack/dockerfile/install/install.sh ./install.sh
COPY hack/dockerfile/install/$INSTALL_BINARY_NAME.installer ./
RUN chmod +x install.sh && chmod +x $INSTALL_BINARY_NAME.installer
RUN PREFIX=/build/ ./install.sh $INSTALL_BINARY_NAME
# ====================== runc 阶段 ======================
FROM base AS runc  
ENV INSTALL_BINARY_NAME=runc
RUN rm -rf /var/lib/apt/lists/*
COPY hack/dockerfile/install/install.sh ./install.sh
COPY hack/dockerfile/install/$INSTALL_BINARY_NAME.installer ./
RUN chmod +x install.sh && chmod +x $INSTALL_BINARY_NAME.installer
RUN PREFIX=/build/ ./install.sh $INSTALL_BINARY_NAME
# ====================== 后续其他阶段 ======================
# 所有基于base的阶段都需要添加以下三行：
# 1. RUN rm -rf /var/lib/apt/lists/*
# 2. apt-get update -o Acquire::AllowInsecureRepositories=true
# 3. apt-get install --allow-unauthenticated

# ====================== 最终Dev环境 ======================
FROM runtime-dev AS dev
RUN rm -rf /var/lib/apt/lists/*
RUN apt-get update -o Acquire::AllowInsecureRepositories=true \
	&& apt-get install -y --allow-unauthenticated \
	apparmor \
	aufs-tools \
	bash-completion \
	btrfs-tools \
	iptables \
	jq \
	libcap2-bin \
	libdevmapper-dev \
	libudev-dev \
	libsystemd-dev \
	binutils-mingw-w64 \
	g++-mingw-w64-x86-64 \
	net-tools \
	pigz \
	python-backports.ssl-match-hostname \
	python-dev \
	python-mock \
	python-pip \
	python-requests \
	python-setuptools \
	python-websocket \
	python-wheel \
	thin-provisioning-tools \
	vim \
	vim-common \
	xfsprogs \
	zip \
	bzip2 \
	xz-utils \
	--no-install-recommends

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
# TODO: This is for the docker-py tests, which shouldn't really be needed for
# this image, but currently CI is expecting to run this image. This should be
# split out into a separate image, including all the `python-*` deps installed
# above.
RUN cd /docker-py \
	&& pip install docker-pycreds==0.2.1 \
	&& pip install yamllint==1.5.0 \
	&& pip install -r test-requirements.txt

ENV PATH=/usr/local/cli:$PATH
ENV DOCKER_BUILDTAGS apparmor seccomp selinux
# Options for hack/validate/gometalinter
ENV GOMETALINTER_OPTS="--deadline=2m"
WORKDIR /go/src/github.com/docker/docker
VOLUME /var/lib/docker
# Wrap all commands in the "docker-in-docker" script to allow nested containers
ENTRYPOINT ["hack/dind"]
# Upload docker source
COPY . /go/src/github.com/docker/docker
