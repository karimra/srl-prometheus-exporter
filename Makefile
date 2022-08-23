build:
	mkdir -p builds
	go build -o builds/
	docker run --rm -v $$PWD:/tmp -w /tmp goreleaser/nfpm package     --config /tmp/nfpm.yaml     --target /tmp     --packager rpm
