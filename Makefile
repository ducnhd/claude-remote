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
	# bootout+bootstrap, not load/kickstart: `load` is a no-op on an already
	# loaded job and keeps serving the OLD plist, and `kickstart` then fails
	# with EX_CONFIG(78) leaving the service dead.
	@# bootout is asynchronous: bootstrapping too soon fails with
	@# "Input/output error", so wait for the label to disappear first.
	@launchctl bootout gui/$(shell id -u)/com.claude-remote 2>/dev/null || true
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
		launchctl list | grep -q com.claude-remote || break; \
		sleep 1; \
	done
	@launchctl bootstrap gui/$(shell id -u) $(PLIST_DST) || \
		(sleep 2; launchctl bootstrap gui/$(shell id -u) $(PLIST_DST))
	@sleep 3
	@launchctl list | grep -q com.claude-remote && echo "Service running." || (echo "SERVICE FAILED TO START — check: make logs"; exit 1)
	@echo "Installed. Pair a phone with: claude-remote qr"

restart:
	@# bootout is asynchronous: bootstrapping too soon fails with
	@# "Input/output error", so wait for the label to disappear first.
	@launchctl bootout gui/$(shell id -u)/com.claude-remote 2>/dev/null || true
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
		launchctl list | grep -q com.claude-remote || break; \
		sleep 1; \
	done
	@launchctl bootstrap gui/$(shell id -u) $(PLIST_DST) || \
		(sleep 2; launchctl bootstrap gui/$(shell id -u) $(PLIST_DST))
	@sleep 3
	@launchctl list | grep -q com.claude-remote && echo "Restarted." || (echo "FAILED TO START — check: make logs"; exit 1)

logs:
	tail -f $(HOME)/.claude-remote/server.log

uninstall:
	-launchctl unload $(PLIST_DST)
	-rm -f $(PLIST_DST)
	-rm -f $(INSTALL_PATH)
	@echo "Uninstalled."
