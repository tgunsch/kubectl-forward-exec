GOCMD=go
GOBUILD=$(GOCMD) build
BINARY_NAME=kubectl-forward-exec
GOTARGET=./cmd/kubectl-forward-exec.go
GOBUILD_ARGS= -mod=readonly -a -installsuffix cgo -v

GOCLEAN=$(GOCMD) clean

all: build

build:
	@$(GOBUILD) $(GOBUILD_ARGS) -o $(BINARY_NAME) $(GOTARGET)

clean:
	@$(GOCLEAN)
	@rm -f $(BINARY_NAME)

.PHONY: all build clean