# Each subdirectory containing a go.mod is an independently buildable
# wormhole. Adding a new wormhole is just dropping in a directory with its own
# module — `make <name>` and `make all` pick it up automatically.
WORMHOLES := $(notdir $(patsubst %/go.mod,%,$(wildcard */go.mod)))
BIN ?= bin

.PHONY: all clean tidy list $(WORMHOLES)

all: $(WORMHOLES)

# Build one wormhole to $(BIN)/<name>.
$(WORMHOLES):
	@echo "building $@"
	@mkdir -p $(BIN)
	cd $@ && go build -o ../$(BIN)/$@ .

# go mod tidy across every module.
tidy:
	@for w in $(WORMHOLES); do echo "tidy $$w"; (cd $$w && go mod tidy) || exit 1; done

# Print the discovered wormholes.
list:
	@echo $(WORMHOLES)

clean:
	rm -rf $(BIN)
