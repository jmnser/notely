.PHONY: build run lint

build:
	GOOS=js GOARCH=wasm go build -o web/app.wasm
	go build -o gnotes

run: build
	./gnotes

lint:
	golangci-lint run
	GOOS=js GOARCH=wasm golangci-lint run
