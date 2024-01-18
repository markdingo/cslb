all:
	@echo Targets: clean, fmt, test and version

.PHONY: clean
clean:
	go clean

.PHONY: fmt
fmt:
	gofmt -s -w .

.PHONY: test tests
test tests:
	go vet ./...
	go test ./...

.PHONY: testrace
testrace:
	go test -race ./...

.PHONY: version
version:
	sh make_version.sh ChangeLog.md >version.go
