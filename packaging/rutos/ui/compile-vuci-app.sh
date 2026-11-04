#!/bin/bash
# Compiles the Vue components of this WebUI plugin into the asset layout the
# RutOS/Vuci WebUI expects: dest/<type>/<Component>_<hash>.js.gz plus a
# rewritten "view" entry in files/usr/share/vuci/menu.d/<component>.json.
#
# Contract (documented in the Teltonika SDK, reimplemented here):
#   - src/views/Index lists one component name per line (optional)
#   - each component lives in src/views/<type>/<Component>.vue
#   - Vite builds a library bundle per component; vue is external
#   - the menu file is named after the lower-cased component name
#
# Requires: node, npm (dependencies installed), and a working vue/vite setup.
set -euo pipefail

SRC_DIR="src"
DEST_DIR="dest"
TEMP_DIR=".plugin/temp"
MENU_DIRS="files/usr/share/vuci/menu.d files/usr/local/share/vuci/menu.d"
VITE_CONFIG="vite.config.js"

die() { echo "ERROR: $*" >&2; exit 1; }

# ENV_FILE is global so the EXIT trap can clean it up under "set -u".
ENV_FILE=""
trap 'rm -f "${ENV_FILE:-}"; rm -rf .plugin' EXIT

command -v npx >/dev/null 2>&1 || die "npx not found — install nodejs/npm"

components() {
	if [ -f "${SRC_DIR}/views/Index" ]; then
		grep -v '^[[:space:]]*$' "${SRC_DIR}/views/Index"
	else
		find "${SRC_DIR}/views" -name '*.vue' -exec basename {} .vue \; | sort
	fi
}

component_path() {
	local name="$1"
	find "${SRC_DIR}/views" -name "${name}.vue" -print -quit
}

app_type_of() { # src/views/<type>/<Name>.vue -> <type>
	local path="$1"
	local rel="${path#${SRC_DIR}/views/}"
	echo "${rel%%/*}"
}

build_one() {
	local name="$1" path type env_file md5 target
	path="$(component_path "$name")"
	[ -n "$path" ] || die "no .vue file found for component ${name}"
	type="$(app_type_of "$path")"

	env_file=".env.plugin${name}"
	ENV_FILE="$env_file"
	{
		echo "VITE_PLUGIN_ENTRY=${path}"
		echo "VITE_PLUGIN_FILENAME=plugin"
	} > "$env_file"
	rm -rf "$TEMP_DIR"
	npx vite build --outDir "$TEMP_DIR" --config "$VITE_CONFIG" --mode "plugin${name}" \
		|| die "vite build failed for ${name}"
	rm -f "$env_file"
	ENV_FILE=""

	md5="$(cat "${TEMP_DIR}"/plugin.* | md5sum | cut -d' ' -f1 | cut -c1-6)"
	target="${DEST_DIR}/${type}/${name}_${md5}"
	mkdir -p "${DEST_DIR}/${type}"

	if [ -f "${TEMP_DIR}/plugin.js.gz" ]; then
		cp "${TEMP_DIR}/plugin.js.gz" "${target}.js.gz"
	elif [ -f "${TEMP_DIR}/plugin.js" ]; then
		cp "${TEMP_DIR}/plugin.js" "${target}.js"
	else
		die "no JS build artefact in ${TEMP_DIR}"
	fi
	if [ -f "${TEMP_DIR}/style.css.gz" ]; then
		cp "${TEMP_DIR}/style.css.gz" "${target}.css.gz"
	elif [ -f "${TEMP_DIR}/style.css" ]; then
		cp "${TEMP_DIR}/style.css" "${target}.css"
	fi

	local menu_file lower view
	lower="$(echo "$name" | tr '[:upper:]' '[:lower:]')"
	view="${type}/${name}_${md5}"
	for dir in $MENU_DIRS; do
		menu_file="${dir}/${lower}.json"
		[ -f "$menu_file" ] || continue
		sed -i -E "s|(\"view\":[[:space:]]*\")[^\"]*\"|\1${view}\"|" "$menu_file"
	done

	rm -rf .plugin
	echo "built ${target}.js.gz (view ${view})"
}

main() {
	rm -rf "$DEST_DIR"
	mkdir -p "$DEST_DIR"
	local c
	while IFS= read -r c; do
		[ -n "$c" ] || continue
		build_one "$c"
	done < <(components)
}

main "$@"
