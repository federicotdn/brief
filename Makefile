clean:
	go clean
	rm -f ./brief

build: clean
	go build

fmt:
	gofmt -s -w -l .
