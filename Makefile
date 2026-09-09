# internal/reconcile's suite now takes ~600-620s even under -short, past go
# test's default 10-minute PER-PACKAGE timeout ("panic: test timed out after
# 10m0s"). Pin an explicit timeout everywhere tests run so a slow package
# fails on its own merits, never on the default. CI and the docs point here
# rather than at a bare `go test ./...` so the timeout can't be dropped by
# either drifting out of sync.
GOTEST_TIMEOUT := 45m

.PHONY: build vet test-short test

build:
	go build ./...

vet:
	go vet ./...

test-short:
	go test -short -timeout $(GOTEST_TIMEOUT) ./...

test:
	go test -timeout $(GOTEST_TIMEOUT) ./...
