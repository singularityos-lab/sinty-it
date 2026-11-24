PREFIX ?= /usr

.PHONY: all build release test vet install install-agent clean

all: build

build:
	go build -o sinty-it ./cmd/sinty-it

release:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o sinty-it ./cmd/sinty-it

test:
	go test ./...

vet:
	go vet ./...

install: release
	install -Dm755 sinty-it $(DESTDIR)$(PREFIX)/bin/sinty-it
	install -Dm644 packaging/sinty-it.service $(DESTDIR)$(PREFIX)/lib/systemd/system/sinty-it.service
	install -Dm644 packaging/sinty-it.conf $(DESTDIR)/etc/default/sinty-it

install-agent: release
	install -Dm755 sinty-it $(DESTDIR)$(PREFIX)/bin/sinty-it
	install -Dm644 packaging/sinty-it-agent.service $(DESTDIR)$(PREFIX)/lib/systemd/system/sinty-it-agent.service

clean:
	rm -f sinty-it
