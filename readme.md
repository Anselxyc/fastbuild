## moby-18.09.6

数据流容器快速构建系统基于 docker 的代码

## http.go

缓存实现代码

## 使用方式

### 编译 docker 并安装

### without cached 使用

docker build -q -t test -f Dockerfile .

### with cached (need warmup to cache files locally)

- go run http.go (runnning in the background; use crtl+c twice to exit after test)
- docker build -q --build-arg http_proxy=http://${host}:8080 --build-arg HTTP_PROXY=http://${host}:8080 -t test -f Dockerfile .
