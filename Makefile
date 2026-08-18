BINARY=claude-remote
INSTALL_PATH=$(HOME)/bin/$(BINARY)
PLIST_NAME=com.claude-remote.plist
PLIST_SRC=launchd/$(PLIST_NAME)
PLIST_DST=$(HOME)/Library/LaunchAgents/$(PLIST_NAME)

.PHONY: build run test test-race clean install uninstall restart logs

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
	# The server serves ./static next to the binary, so skipping this step
	# means UI changes silently never reach the phone.
	mkdir -p $(HOME)/bin/static
	cp -R static/. $(HOME)/bin/static/
	mkdir -p $(HOME)/Library/LaunchAgents
	sed 's|__HOME__|$(HOME)|g' $(PLIST_SRC) > $(PLIST_DST)
	launchctl load $(PLIST_DST) 2>/dev/null || true
	# load is a no-op when the job is already loaded; kickstart -k restarts it.
	launchctl kickstart -k gui/$(shell id -u)/com.claude-remote 2>/dev/null || true
	@echo "Installed and restarted. Pair a phone with: claude-remote qr"

restart:
	launchctl kickstart -k gui/$(shell id -u)/com.claude-remote
	@echo "Restarted."

logs:
	tail -f $(HOME)/.claude-remote/server.log

uninstall:
	-launchctl unload $(PLIST_DST)
	-rm -f $(PLIST_DST)
	-rm -f $(INSTALL_PATH)
	@echo "Uninstalled."
