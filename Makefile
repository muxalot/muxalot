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

.PHONY: help version test agent apk aab release release-agent release-apk check-gradle check-clean check-tag
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

release: check-gradle check-clean test ## bump version, tag and push; CI then publishes the agent
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

release-agent: check-tag test agent ## rebuild the agent and upload it to the latest release
	$(RUN) gh release upload $(LAST) $(DIST)/muxalot-agent-linux-* $(DIST)/SHA256SUMS --clobber

release-apk: check-tag apk ## build the signed APK and upload it to the latest release
	$(RUN) gh release upload $(LAST) $(DIST)/muxalot-$(VERSION).apk --clobber
