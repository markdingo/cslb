all:
	@echo Targets: clean, fmt, test and version

.PHONY: clean
clean:
	go clean

.PHONY: fmt
fmt:
	gofmt -s -w `find . -name '*.go' -type f -print`

.PHONY: test
	fo test

.PHONY: version
version:
	sh make_version.sh ChangeLog.md >version.go
