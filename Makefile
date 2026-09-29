.PHONY: build test install run release-check

build:
	go build -o bin/hey-agent ./cmd/hey-agent
	@# Local self-signed cert keeps Accessibility/Input-Monitoring grants
	@# across rebuilds — TCC anchors to the cert leaf, not the binary hash.
	@if [ "$$(uname)" = "Darwin" ] && security find-identity -v -p codesigning | grep -q hey-agent-local; then \
		codesign -s hey-agent-local --force bin/hey-agent; \
	fi

# LaunchAgents must exec the app bundle, not a bare binary: TCC attributes
# event taps to the responsible bundle, so launchd-spawned taps only work
# when the daemon lives inside HeyAgent.app.
bundle: build
	@if [ "$$(uname)" = "Darwin" ]; then \
		mkdir -p ~/Applications/HeyAgent.app/Contents/MacOS; \
		cp bin/hey-agent ~/Applications/HeyAgent.app/Contents/MacOS/hey-agent; \
		codesign --deep -s hey-agent-local --force ~/Applications/HeyAgent.app; \
	fi

test:
	go test ./...

install: build
	./bin/hey-agent install

run:
	hey-agent listen

release-check: test build
	git diff --check
