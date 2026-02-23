.PHONY: build install clean test

BINARY_NAME=librarian

build:
	go build -o $(BINARY_NAME) .

install:
	go install .

clean:
	rm -f $(BINARY_NAME)

test:
	go test ./...
