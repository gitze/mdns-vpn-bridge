-- Function API for the mdns-vpn-bridge WebUI plugin.
--
--   GET  /api/mdns_vpn_bridge_f/status   relay state from the local status endpoint
--   GET  /api/mdns_vpn_bridge_f/config   current UCI options
--   POST /api/mdns_vpn_bridge_f/actions/set       write options and restart
--   POST /api/mdns_vpn_bridge_f/actions/restart   restart the service
--
-- No shell metacharacters can reach the shell: every value is validated against
-- a whitelist before it is passed to uci.

local FunctionService = require("api/FunctionService")

local Bridge = FunctionService:new()

local CONFIG = "mdns-vpn-bridge"
local SECTION = "main"
local STATUS_URL = "http://127.0.0.1:53531/_status"
local INIT = "/etc/init.d/mdns-vpn-bridge"

local function sh(cmd)
	local f = io.popen(cmd)
	if not f then
		return ""
	end
	local out = f:read("*a") or ""
	f:close()
	return out
end

local function trim(s)
	return (s or ""):gsub("%s+$", "")
end

local function uciget(opt, default)
	local v = trim(sh("uci -q get " .. CONFIG .. "." .. SECTION .. "." .. opt))
	if v == "" then
		return default or ""
	end
	return v
end

local function is_ipv4(v)
	local a, b, c, d = v:match("^(%d+)%.(%d+)%.(%d+)%.(%d+)$")
	if not a then
		return false
	end
	for _, n in ipairs({ a, b, c, d }) do
		if tonumber(n) > 255 then
			return false
		end
	end
	return true
end

-- validate returns true/false for a whitelisted option value
local function validate(opt, value)
	if value == nil then
		return true
	end
	if opt == "enabled" then
		return value == "0" or value == "1"
	elseif opt == "lan_if" then
		return value:match("^[%w%._%-]+$") ~= nil
	elseif opt == "peer" then
		return value == "" or is_ipv4(value)
	elseif opt == "peerport" then
		local n = tonumber(value)
		return n ~= nil and n > 0 and n < 65536
	elseif opt == "lan2peer" or opt == "peer2lan" then
		return value == "query" or value == "all"
	elseif opt == "services" then
		return value:match("^[%w_%,%-%._]*$") ~= nil
	elseif opt == "stats" then
		return value:match("^%d+[smh]?$") ~= nil
	end
	return false
end

local OPTIONS = {
	"enabled",
	"lan_if",
	"peer",
	"peerport",
	"lan2peer",
	"peer2lan",
	"services",
	"stats",
}

-- GET /api/mdns_vpn_bridge_f/status
function Bridge:GET_TYPE_status()
	local raw = sh("wget -q -T 2 -O - " .. STATUS_URL .. " 2>/dev/null")
	local running = raw:match('"peer_port"') ~= nil

	local function num(pattern)
		local v = raw:match(pattern)
		return v or ""
	end

	local uptime = num('"uptime_seconds":([%d%.]+)')
	local up_txt = "-"
	if uptime ~= "" then
		local secs = math.floor(tonumber(uptime) or 0)
		if secs < 60 then
			up_txt = string.format("%ds", secs)
		elseif secs < 3600 then
			up_txt = string.format("%dm%02ds", math.floor(secs / 60), secs % 60)
		else
			up_txt = string.format("%dh%02dm", math.floor(secs / 3600), math.floor(secs / 60) % 60)
		end
	end

	local function side(section)
		local base = '"' .. section .. '":{"packets":(%d+),"bytes":(%d+),"dropped":(%d+)}'
		local p, b, d = raw:match(base)
		if not p then
			return "-"
		end
		return string.format("%s packets / %s B (dropped %s)", p, b, d)
	end

	return self:ResponseOK({
		running = running and "1" or "0",
		lan_if = num('"lan_if":"([^"]*)"'),
		lan_ip = num('"lan_ip":"([^"]*)"'),
		peer = num('"peer":"([^"]*)"'),
		peer_port = num('"peer_port":(%d+)'),
		lan2peer = num('"lan2peer":"([^"]*)"'),
		peer2lan = num('"peer2lan":"([^"]*)"'),
		uptime = up_txt,
		to_peer = side("to_peer"),
		to_lan = side("to_lan"),
	})
end

-- GET /api/mdns_vpn_bridge_f/config
function Bridge:GET_TYPE_config()
	return self:ResponseOK({
		enabled = uciget("enabled", "1"),
		lan_if = uciget("lan_if", "br-lan"),
		peer = uciget("peer", ""),
		peerport = uciget("peerport", "53530"),
		lan2peer = uciget("lan2peer", "all"),
		peer2lan = uciget("peer2lan", "all"),
		services = uciget("services", ""),
		stats = uciget("stats", "300s"),
	})
end

function Bridge:SetAction()
	local args = self.arguments or {}
	for _, opt in ipairs(OPTIONS) do
		local value = args[opt]
		if value ~= nil then
			if not validate(opt, value) then
				self:add_error(400, "invalid value for " .. opt, "uci")
				return self:ResponseError()
			end
			sh(string.format("uci set %s.%s.%s='%s'", CONFIG, SECTION, opt, value))
		end
	end
	sh("uci commit " .. CONFIG)
	os.execute(INIT .. " restart >/dev/null 2>&1")
	return self:ResponseOK({ saved = "1" })
end

function Bridge:RestartAction()
	os.execute(INIT .. " restart >/dev/null 2>&1")
	return self:ResponseOK({ restarted = "1" })
end

local set_action = Bridge:action("set", Bridge.SetAction)
for _, opt in ipairs(OPTIONS) do
	local o = set_action:option(opt)
	o.maxlength = 128
end

Bridge:action("restart", Bridge.RestartAction)

return Bridge
