.PHONY: build test app
build:
	mkdir -p dist
	go build -trimpath -o dist/burn ./cmd/burn
test:
	go test -race ./...
	go vet ./...
app:
	./scripts/build-app.sh
