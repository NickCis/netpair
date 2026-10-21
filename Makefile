.PHONY: build install test clean check-versions

build:
	go build -o netpair ./cmd/netpair
	go build -o netpair-manager ./cmd/netpair-manager

install: build
	install -m 755 netpair netpair-manager $(DESTDIR)$(HOME)/.local/bin/

clean:
	rm -f netpair netpair-manager

test:
	go test ./...

check-versions:
	./scripts/check-package-versions.sh
