-- Runs once per VM, before the sandbox freezes the globals. It defines the
-- global `ui` that scripts build their widget tree with, and returns the
-- functions the Go host calls. Every host function gets a chunk compiled
-- from Go and returns JSON.

local plugin = nil

local function quote(s: string): string
	if not utf8.len(s) then
		error("a string is not valid UTF-8", 0)
	end
	return '"' .. string.gsub(s, '[%c"\\]', function(ch)
		return string.format("\\u%04x", string.byte(ch))
	end) .. '"'
end

local function scalar(v: any, where: string): string
	local t = type(v)
	if t == "string" then
		return quote(v)
	elseif t == "boolean" then
		return tostring(v)
	elseif t == "number" then
		if v ~= v or v == math.huge or v == -math.huge then
			error(where .. " is not a finite number", 0)
		end
		return string.format("%.17g", v)
	end
	error(where .. " is a " .. t .. "; state holds strings, numbers and booleans", 0)
end

local function encodeState(t: any, where: string): string
	if type(t) ~= "table" then
		error(where .. " must be a table", 0)
	end
	local parts = {}
	for k, v in t do
		if type(k) ~= "string" then
			error(where .. " has a non-string key", 0)
		end
		table.insert(parts, quote(k) .. ":" .. scalar(v, where .. "." .. k))
	end
	return "{" .. table.concat(parts, ",") .. "}"
end

local kinds = { column = true, row = true, text = true, button = true }

local function encodeNode(n: any, where: string): string
	if type(n) ~= "table" or not kinds[n.kind] then
		error(where .. " is not a widget; build one with ui.column, ui.row, ui.text or ui.button", 0)
	end
	local parts = { '"kind":' .. quote(n.kind) }
	for _, field in { "key", "text", "on" } do
		if n[field] ~= nil then
			table.insert(parts, quote(field) .. ":" .. quote(tostring(n[field])))
		end
	end
	for _, field in { "gap", "size" } do
		if n[field] ~= nil then
			table.insert(parts, quote(field) .. ":" .. scalar(n[field], where .. "." .. field))
		end
	end
	if n.children then
		local kids = {}
		for i, child in n.children do
			table.insert(kids, encodeNode(child, where .. "[" .. i .. "]"))
		end
		table.insert(parts, '"children":[' .. table.concat(kids, ",") .. "]")
	end
	return "{" .. table.concat(parts, ",") .. "}"
end

local function props(p: any?): { [string]: any }
	local out = {}
	if p then
		for k, v in p do
			out[k] = v
		end
	end
	return out
end

local function container(kind: string)
	return function(p: any, children: any?)
		if children == nil then
			p, children = nil, p
		end
		local n = props(p)
		n.kind, n.children = kind, children or {}
		return n
	end
end

ui = table.freeze({
	column = container("column"),
	row = container("row"),
	text = function(text: any, p: any?)
		local n = props(p)
		n.kind, n.text = "text", tostring(text)
		return n
	end,
	-- p.on names the action in the script's `actions` table to run on click.
	button = function(label: any, p: any?)
		local n = props(p)
		n.kind, n.text = "button", tostring(label)
		return n
	end,
})

return {
	mount = function(chunk)
		local p = chunk()
		if type(p) ~= "table" or type(p.view) ~= "function" then
			error("the script must return a table with a view function", 0)
		end
		plugin = p
		return encodeState(p.state or {}, "state")
	end,
	render = function(chunk)
		return encodeNode(plugin.view(table.freeze(chunk())), "view()")
	end,
	act = function(chunk)
		local name, state = chunk()
		local action = plugin.actions and plugin.actions[name]
		if type(action) ~= "function" then
			error("no action named " .. quote(name), 0)
		end
		return encodeState(action(table.freeze(state)) or {}, name .. "()")
	end,
}
