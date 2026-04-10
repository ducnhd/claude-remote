BINARY=claude-remote
INSTALL_PATH=$(HOME)/bin/$(BINARY)
PLIST_NAME=com.claude-remote.plist
PLIST_SRC=launchd/$(PLIST_NAME)
PLIST_DST=$(HOME)/Library/LaunchAgents/$(PLIST_NAME)

.PHONY: build run test clean install uninstall

build:
	go build -o $(BINARY) .

run: build
	./$(BINARY) serve

test:
	go test ./... -v -count=1

test-race:
	go test -race ./... -v -count=1

clean:
	rm -f $(BINARY)

install: build
	mkdir -p $(HOME)/bin
	cp $(BINARY) $(INSTALL_PATH)
	mkdir -p $(HOME)/Library/LaunchAgents
	sed 's|__HOME__|$(HOME)|g' $(PLIST_SRC) > $(PLIST_DST)
	launchctl load $(PLIST_DST)
	@echo "Installed. Run 'claude-remote setup' if first time."

uninstall:
	-launchctl unload $(PLIST_DST)
	-rm -f $(PLIST_DST)
	-rm -f $(INSTALL_PATH)
	@echo "Uninstalled."
