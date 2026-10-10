# muxalot build and release. `make help` lists targets.
# The version comes from git tags only: the newest vX.Y.Z tag, patch bumped by `make release`
# (BUMP=minor|major to override). DRY=1 prints release commands instead of running them.

BUMP ?= patch
DIST := dist
# Gradle 8.10+ (the system gradle is too old and there is no wrapper); override with GRADLE=/path/to/gradle
GRADLE ?= $(firstword $(wildcard $(HOME)/.gradle/wrapper/dists/gradle-8.10.2-bin/*/gradle-8.10.2/bin/gradle))
GRADLE_FLAGS ?= # e.g. --offline
export ANDROID_HOME ?= $(HOME)/android_sdk
RUN := $(if $(DRY),echo +,)

LAST   := $(shell git describe --tags --abbrev=0 --match 'v[0-9]*' 2>/dev/null || echo v0.0.0)
TAGGED := $(shell git describe --tags --exact-match --match 'v[0-9]*' HEAD 2>/dev/null)
BASE   := $(or $(TAGGED),$(LAST))
VERSION := $(patsubst v%,%,$(BASE))$(if $(TAGGED),,-dev)
# v1.20.3 -> 1020003: always increases, well under Play's 2.1e9 limit
CODE := $(shell echo $(BASE) | awk -F. '{sub(/^v/,"",$$1); print $$1*1000000+$$2*1000+$$3}')
NEXT := $(shell echo $(LAST) | awk -F. -v b=$(BUMP) '{sub(/^v/,"",$$1); if (b=="major") {$$1++;$$2=0;$$3=0} else if (b=="minor") {$$2++;$$3=0} else {$$3++}; print "v"$$1"."$$2"."$$3}')

GR = $(GRADLE) -q $(GRADLE_FLAGS) -p app -PversionName=$(VERSION) -PversionCode=$(CODE)

.PHONY: help version test agent xterm-check xterm-update desktop desktop-assets desktop-test windows deb deb-smoke appimage appimage-smoke apk aab release release-agent release-apk release-desktop release-windows check-gradle check-clean check-tag
.DEFAULT_GOAL := help

help: ## this list
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | sed -E 's/:.*## /\t/' | column -t -s "$$(printf '\t')"

version: ## print current and next version
	@echo "version $(VERSION) (code $(CODE)); latest tag $(LAST); next $(NEXT)"

test: ## go vet + go test
	cd agent && go vet ./... && go test ./...

agent: ## cross-compile the agent (linux amd64+arm64) into dist/
	mkdir -p $(DIST)
	cd agent && for a in amd64 arm64; do \
	  CGO_ENABLED=0 GOOS=linux GOARCH=$$a go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" \
	    -o ../$(DIST)/muxalot-agent-linux-$$a . || exit 1; done
	cd $(DIST) && sha256sum muxalot-agent-linux-* > SHA256SUMS

DESKTOP_VENDOR := xterm.js xterm.css addon-fit.js JetBrainsMonoNerdFontMono-Regular.woff2 LICENSE-xterm.txt LICENSE-nerdfonts.txt
# gtk3 = WebKitGTK 4.1 (libgtk-3-dev libwebkit2gtk-4.1-dev); production turns DevTools off
DESKTOP_TAGS ?= gtk3

XTERM_VERSION := 6.0.0
FIT_VERSION := 0.11.0
ASSETS := app/app/src/main/assets

xterm-check: ## verify the vendored xterm.js/css/addon-fit against xterm.sha256
	cd $(ASSETS) && sha256sum -c xterm.sha256

xterm-update: ## fetch XTERM_VERSION/FIT_VERSION from npm into the assets and rewrite xterm.sha256 (also edit VENDOR.md)
	d=$$(mktemp -d) && cd $$d && npm pack --silent @xterm/xterm@$(XTERM_VERSION) @xterm/addon-fit@$(FIT_VERSION) >/dev/null && \
	  for t in *.tgz; do mkdir $${t%.tgz} && tar xzf $$t -C $${t%.tgz}; done && \
	  cp xterm-xterm-*/package/lib/xterm.js xterm-xterm-*/package/css/xterm.css xterm-addon-fit-*/package/lib/addon-fit.js $(CURDIR)/$(ASSETS)/ && \
	  cd $(CURDIR)/$(ASSETS) && sha256sum xterm.js xterm.css addon-fit.js > xterm.sha256; rm -rf $$d

desktop-assets: xterm-check ## copy xterm.js and the font from the Android assets into desktop/frontend/vendor/
	mkdir -p desktop/frontend/vendor
	cd app/app/src/main/assets && cp $(DESKTOP_VENDOR) ../../../../../desktop/frontend/vendor/

desktop-test: desktop-assets ## desktop go vet + go test (needs GTK3/WebKitGTK dev packages; e2e needs tmux)
	cd desktop && go vet -tags "$(DESKTOP_TAGS)" ./... && go test -tags "$(DESKTOP_TAGS)" ./...

desktop: desktop-assets ## build the desktop client for this OS into dist/
	mkdir -p $(DIST)
	cd desktop && CGO_ENABLED=1 go build -tags "production $(DESKTOP_TAGS)" -trimpath -ldflags "-s -w -X main.version=$(VERSION)" \
	  -o ../$(DIST)/muxalot-desktop-$$(go env GOOS)-$$(go env GOARCH) .

DEB = $(lastword $(sort $(wildcard $(DIST)/muxalot-desktop_*.deb)))
APPIMAGE = $(lastword $(sort $(wildcard $(DIST)/muxalot-desktop_*.AppImage)))
IMAGE ?= ubuntu:24.04

windows: desktop-assets ## build dist/muxalot-desktop_<version>_windows-amd64.zip (cross-builds from Linux, CGO_ENABLED=0; needs zip)
	mkdir -p $(DIST)
	cd desktop && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -tags production -trimpath -ldflags "-s -w -X main.version=$(VERSION) -H windowsgui" \
	  -o ../$(DIST)/muxalot-desktop-windows-amd64.exe .
	ln -f $(DIST)/muxalot-desktop-windows-amd64.exe $(DIST)/muxalot-desktop.exe
	zip -j $(DIST)/muxalot-desktop_$(VERSION)_windows-amd64.zip $(DIST)/muxalot-desktop.exe desktop/packaging/windows-readme.txt
	rm -f $(DIST)/muxalot-desktop.exe

deb: desktop ## build dist/muxalot-desktop_<version>_amd64.deb on this machine (needs dpkg-dev fakeroot; release builds use Ubuntu 22.04, see release-desktop)
	rm -f $(DIST)/muxalot-desktop_*.deb
	desktop/packaging/build-deb.sh $(VERSION) $(DIST)/muxalot-desktop-linux-amd64 $(DIST)

appimage: desktop ## build dist/muxalot-desktop_<version>_amd64.AppImage on this machine (needs patchelf and curl for linuxdeploy; release builds use Ubuntu 22.04, see release-desktop)
	rm -f $(DIST)/muxalot-desktop_*.AppImage
	desktop/packaging/build-appimage.sh $(VERSION) $(DIST)/muxalot-desktop-linux-amd64 $(DIST)

deb-smoke: ## install the .deb in a container and launch it (IMAGE=debian:12; default ubuntu:24.04; needs docker)
	@test -n "$(DEB)" || { echo "no .deb in $(DIST): run make deb first"; exit 1; }
	docker run --rm --network=host -v "$(CURDIR)/desktop/packaging/smoke.sh:/smoke.sh:ro" -v "$(CURDIR)/$(DEB):/pkg/pkg.deb:ro" $(IMAGE) bash /smoke.sh

appimage-smoke: ## launch the AppImage in a container with no GTK/WebKit runtime deps at all (IMAGE=debian:12; default ubuntu:24.04; needs docker)
	@test -n "$(APPIMAGE)" || { echo "no .AppImage in $(DIST): run make appimage first"; exit 1; }
	docker run --rm -v "$(CURDIR)/desktop/packaging/smoke-appimage.sh:/smoke.sh:ro" -v "$(CURDIR)/$(APPIMAGE):/pkg/muxalot-desktop_$(VERSION)_amd64.AppImage:ro" $(IMAGE) bash /smoke.sh

check-gradle:
	@test -n "$(GRADLE)" || { echo "Gradle 8.10+ not found; run make with GRADLE=/path/to/gradle"; exit 1; }

define need_key
	@grep -q '^$(1)\.storeFile=' app/keystore.properties 2>/dev/null || { echo "app/keystore.properties has no $(1).* signing keys (see CONTRIBUTING.md)"; exit 1; }
endef

apk: check-gradle ## signed free release APK into dist/ (needs the apk.* key)
	$(call need_key,apk)
	$(GR) -Psigning=apk :app:assembleFreeRelease
	mkdir -p $(DIST) && cp app/app/build/outputs/apk/free/release/app-free-release.apk $(DIST)/muxalot-$(VERSION).apk

aab: check-gradle ## signed pro bundle for Play into dist/ (needs the play.* key)
	$(call need_key,play)
	$(GR) -Psigning=play :app:bundleProRelease
	mkdir -p $(DIST) && cp app/app/build/outputs/bundle/proRelease/app-pro-release.aab $(DIST)/muxalot-pro-$(VERSION).aab

check-clean:
	@git diff --quiet && git diff --cached --quiet && [ -z "$$(git ls-files --others --exclude-standard)" ] || { echo "working tree not clean"; exit 1; }

release: check-gradle check-clean test ## bump version, tag and push; CI then publishes the agent, the desktop .deb and the AppImage
	@[ "$$(git rev-parse --abbrev-ref HEAD)" = main ] || { echo "not on main"; exit 1; }
	@git fetch -q origin main && [ "$$(git rev-parse HEAD)" = "$$(git rev-parse origin/main)" ] || { echo "main differs from origin/main; push or pull first"; exit 1; }
	@! git rev-parse -q --verify refs/tags/$(NEXT) >/dev/null || { echo "$(NEXT) already exists"; exit 1; }
	$(GR) :app:assembleFreeDebug :app:assembleProDebug
	@echo "release $(LAST) -> $(NEXT)"
	$(RUN) git tag -a $(NEXT) -m $(NEXT)
	$(RUN) git push origin $(NEXT)

# individual releases upload onto the latest release, so HEAD must be exactly that tag
check-tag:
	@[ -n "$(TAGGED)" ] && [ "$(TAGGED)" = "$(LAST)" ] || { echo "HEAD must be exactly the latest tag ($(LAST)): git checkout $(LAST)"; exit 1; }
	@gh release view $(LAST) >/dev/null 2>&1 || { echo "no GitHub release for $(LAST) yet (CI publishes it after the tag push)"; exit 1; }

release-agent: check-tag test agent ## rebuild the agent and upload it to the latest release (local build: no attestation, install.sh verification fails)
	$(RUN) gh release upload $(LAST) $(DIST)/muxalot-agent-linux-* $(DIST)/SHA256SUMS --clobber

release-apk: check-tag apk ## build the signed APK and upload it to the latest release
	$(RUN) gh release upload $(LAST) $(DIST)/muxalot-$(VERSION).apk --clobber

release-desktop: check-tag desktop-test ## build the .deb and AppImage in an ubuntu:22.04 container and upload them (local build: no attestation; FORCE=1 replaces existing assets)
	@command -v docker >/dev/null || { echo "docker is required: the .deb must be built on Ubuntu 22.04 for its glibc floor"; exit 1; }
	@if gh release view $(LAST) --json assets -q '.assets[].name' | grep -q -e '\.deb$$' -e '\.AppImage$$' && [ -z "$(FORCE)" ]; then echo "$(LAST) already has a .deb/AppImage (CI attests them, a local build cannot); FORCE=1 replaces it with this unattested build"; exit 1; fi
	rm -f $(DIST)/muxalot-desktop_*.deb $(DIST)/muxalot-desktop_*.AppImage
	docker run --rm -v "$(CURDIR)":/src ubuntu:22.04 bash /src/desktop/packaging/container-build.sh $(VERSION) $$(id -u):$$(id -g)
	cd $(DIST) && sha256sum muxalot-desktop_*.deb > SHA256SUMS-desktop && sha256sum muxalot-desktop_*.AppImage > SHA256SUMS-appimage
	$(RUN) gh release upload $(LAST) $(DIST)/muxalot-desktop_*.deb $(DIST)/muxalot-desktop_*.AppImage $(DIST)/SHA256SUMS-desktop $(DIST)/SHA256SUMS-appimage --clobber

release-windows: check-tag windows ## cross-build and upload the Windows zip (local build: no attestation; FORCE=1 replaces an existing one)
	@if gh release view $(LAST) --json assets -q '.assets[].name' | grep -q 'windows-amd64.zip$$' && [ -z "$(FORCE)" ]; then echo "$(LAST) already has a Windows zip (CI attests it, a local build cannot); FORCE=1 replaces it with this unattested build"; exit 1; fi
	rm -f $(DIST)/muxalot-desktop_*_windows-*.zip $(DIST)/muxalot-desktop-windows-amd64.exe
	$(RUN) gh release upload $(LAST) $(DIST)/muxalot-desktop_$(VERSION)_windows-amd64.zip --clobber
