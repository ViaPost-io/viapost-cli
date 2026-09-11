.PHONY: test verify build

test:
	go test ./... -race -count=1

build:
	go build -trimpath -o bin/viapost ./cmd/viapost

verify:
	go mod verify
	test -z "$$(gofmt -l .)"
	go vet ./...
	go tool govulncheck ./...
	go test ./... -race -count=1
	go build -trimpath -o bin/viapost ./cmd/viapost
	./scripts/check-contract.sh openapi/public.yaml
	git diff --check
