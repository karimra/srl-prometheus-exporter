# Docker container for Prometheus Exporter
NAME        := srl/prometheus-exporter
LAST_COMMIT := $(shell sh -c "git log -1 --pretty=%h")
TODAY       := $(shell sh -c "date +%Y%m%d_%H%M")
TAG         := ${TODAY}.${LAST_COMMIT}
IMG         := ${NAME}:${TAG}
LATEST      := ${NAME}:latest
# HTTP_PROXY  := "http://proxy.lbs.alcatel-lucent.com:8000"
ifndef SR_LINUX_RELEASE
override SR_LINUX_RELEASE="latest"
endif

docker-build: build
	sudo docker build --build-arg SRL_PROMETHEUS_EXPORTER_RELEASE=${TAG} \
	                  --build-arg http_proxy=${HTTP_PROXY} \
										--build-arg https_proxy=${HTTP_PROXY} \
	                  --build-arg SR_LINUX_RELEASE="${SR_LINUX_RELEASE}" \
	                  -f ./Dockerfile -t ${IMG} .
	sudo docker tag ${IMG} ${LATEST}

build:
	mkdir -p builds
	go build -o builds/
	docker run --rm -v $$PWD:/tmp -w /tmp goreleaser/nfpm package     --config /tmp/nfpm.yaml     --target /tmp     --packager rpm
