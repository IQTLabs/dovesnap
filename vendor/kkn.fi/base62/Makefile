
IMPORT_PATH := kkn.fi/base62
NAME := base62

GOMETALINTER := $(GOPATH)/bin/gometalinter

.PHONY: test
test:
	go test -v $(IMPORT_PATH)

.PHONY: clean
clean:
	@rm -f cpu.out mem.out bench.txt $(NAME).test

$(NAME).test:
	go test -c

benchmark: $(NAME).test
	./$(NAME).test -test.bench=. -test.count=5 | tee bench.txt

mem.out:
	go test -v -benchmem -memprofile=mem.out -run=^$$ -bench=.

mem-profile: mem.out
	go tool pprof -alloc_space $(NAME).test mem.out

cpu.out:
	go test -v -cpuprofile=cpu.out -run=^$$ -bench=.

cpu-profile: cpu.out
	go tool pprof $(NAME).test cpu.out

.PHONY: lint
lint: $(GOMETALINTER)
	gometalinter ./...

$(GOMETALINTER):
	go get -u github.com/alecthomas/gometalinter
	gometalinter --install
