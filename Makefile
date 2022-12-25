.PHONY: build
build:
	go build -ldflags "-s -w" -o outline-ss-server cmd/outline-ss-server/main.go

.PHONY: install
install:
	cp outline-ss-server /usr/local/bin/outline-ss-server
	chmod +x /usr/local/bin/outline-ss-server

.PHONY: clean
clean:
	rm -rf outline-ss-server
