# RutOS WebUI plugin (Vue + Vite) for mdns-vpn-bridge.
#
# Build:   make            (npm install + compile-vuci-app.sh)
# Result:  dest/<type>/<Component>_<hash>.js.gz, installed into /www/views/
#
# Dependencies are installed outside the source tree (NPM_DIR) so that the
# repository stays clean and npm never writes into the package staging area.
NPM ?= npm
NODE_DIR ?= .npm

DESTDIR ?= /
FILES_DIR := files
COMPILED_DIR := dest
STAMP := $(NODE_DIR)/.installed

all: build

$(STAMP): package.json
	rm -rf "$(NODE_DIR)"
	mkdir -p "$(NODE_DIR)"
	cp package.json package-lock.json "$(NODE_DIR)/" 2>/dev/null || cp package.json "$(NODE_DIR)/"
	$(NPM) install --prefix "$(NODE_DIR)" --no-audit --no-fund
	touch "$(STAMP)"

build: $(STAMP)
	rm -rf node_modules
	ln -s "$(NODE_DIR)/node_modules" node_modules
	@$(NPM) run compile

install: build
	cp -af $(FILES_DIR)/* $(DESTDIR)
	install -d $(DESTDIR)/www/views
	cp -af $(COMPILED_DIR)/* $(DESTDIR)/www/views/

clean:
	rm -rf "$(COMPILED_DIR)" "$(NODE_DIR)" node_modules

.PHONY: all build install clean
