# Makefile for building and running with CGO

# Go build parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GORUN=$(GOCMD) run
GOCLEAN=$(GOCMD) clean

# Build flags
CGO_ENABLED=1
BINARY_NAME=cache_server
BUILD_FOLDER=bin

.PHONY: all build clean run

all: build

build:
	CGO_ENABLED=$(CGO_ENABLED) $(GOBUILD) -o $(BUILD_FOLDER)/$(BINARY_NAME) -v ./...

clean:
	$(GOCLEAN)
	rm -f $(BINARY_NAME)

run: build
	./$(BUILD_FOLDER)/$(BINARY_NAME)

test:
	$(GOCMD) test -v ./...