.PHONY: build test run clean

build:
	go build -o reviews ./cmd/reviews

test:
	go test ./...

run: build
	./reviews

clean:
	rm -f reviews
