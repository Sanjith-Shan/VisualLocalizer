# Build, test and run the positioning service. Run from service/.
GO      ?= go
ROOT    := $(abspath ..)
MAP     ?= $(ROOT)/results/maps/heads.vmap
ADDR    ?= 127.0.0.1:8080
export VLOC_CV_THREADS ?= 1

.PHONY: build build-core test race vet run-fake run-core load e2e sweep clean

build:            ## fake engine only, no C toolchain needed
	$(GO) build -o bin/ ./cmd/...

build-core:       ## links core/build/libvloc.dylib (build it first, see docs/CORE_READY.md)
	$(GO) build -tags vloc -o bin/vlocd-core ./cmd/vlocd
	$(GO) build -o bin/ ./cmd/vlocload

vet:
	$(GO) vet ./... && $(GO) vet -tags vloc ./...

test:
	$(GO) test ./...

race:
	$(GO) test -race -count=1 ./...

run-fake: build
	./bin/vlocd -engine fake -addr $(ADDR) -map-dir maps

run-core: build-core
	./bin/vlocd-core -engine cgo -addr $(ADDR) -map-dir maps -map heads=$(MAP)

load: build
	./bin/vlocload -url http://$(ADDR) -rates 5,10,20 -duration 10s

e2e: build-core   ## pose parity with vloc_eval on every 20th heads test frame
	scripts/e2e.sh

sweep: build-core ## capacity sweep, shedding on vs off, overhead (minutes)
	scripts/sweep.sh

clean:
	rm -rf bin maps
