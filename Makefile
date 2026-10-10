.PHONY: build run test lint

build:
	GOOS=js GOARCH=wasm go build -ldflags "-s -w" -o cmd/gnotes-web/web/app.wasm ./cmd/gnotes-web
	go build -o bin/ ./cmd/...

run: build
	./bin/gnotes-api & ./bin/gnotes-web; kill $$!

test:
	go test -race ./...

lint:
	golangci-lint run
	GOOS=js GOARCH=wasm golangci-lint run
