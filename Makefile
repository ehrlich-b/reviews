.PHONY: build test run dev clean

build:
	go build -o reviews ./cmd/reviews

test:
	go test ./...

run: build
	set -a && . ./.env && set +a && ./reviews

dev:
	set -a && . ./.env && set +a && air -build.cmd "go build -o ./tmp/reviews ./cmd/reviews" -build.bin "./tmp/reviews" -build.include_ext "go,html,css,js,sql"

clean:
	rm -f reviews
