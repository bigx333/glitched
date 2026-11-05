-- Runs once per VM, before the sandbox freezes the globals. The host puts
-- `local KINDS = {...}` (widget kind -> name of its leading argument, or
-- "") before this chunk. It defines the global `ui` that scripts build widget trees with,
-- and returns the functions the Go host calls. Every host function gets a
-- chunk compiled from Go and returns JSON.

local plugin = nil
-- handlers holds the functions of the last render's tree, which the tree
-- names as "<gen>:<n>"; an event from an older tree names an older gen.
local gen, handlers = 0, {}

local function quote(s: string): string
	if not utf8.len(s) then
		error("a string is not valid UTF-8", 0)
	end
	return '"' .. string.gsub(s, '[%c"\\]', function(ch)
		return string.format("\\u%04x", string.byte(ch))
	end) .. '"'
end

local function number(v: number, where: string): string
	if v ~= v or v == math.huge or v == -math.huge then
		error(where .. " is not a finite number", 0)
	end
	return string.format("%.17g", v)
end

local function isArray(t: { [any]: any }): boolean
	local n = #t
	if n == 0 then
		return next(t) == nil
	end
	for k in t do
		if type(k) ~= "number" or k < 1 or k > n or k % 1 ~= 0 then
			return false
		end
	end
	return true
end

local function scalar(v: any, where: string): string
	local t = type(v)
	if t == "string" then
		return quote(v)
	elseif t == "boolean" then
		return tostring(v)
	elseif t == "number" then
		return number(v, where)
	elseif t == "table" and isArray(v) then
		local parts = {}
		for i, s in v do
			if type(s) ~= "string" then
				error(where .. "[" .. i .. "] is a " .. type(s) .. "; state lists hold strings", 0)
			end
			table.insert(parts, quote(s))
		end
		return "[" .. table.concat(parts, ",") .. "]"
	end
	error(where .. " is a " .. t .. "; state holds strings, numbers, booleans and lists of strings", 0)
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

local function register(fn: any): string
	local id = gen .. ":" .. (#handlers + 1)
	table.insert(handlers, fn)
	handlers[id] = fn
	return '{"fn":' .. quote(id) .. "}"
end

local function isNode(v: any): boolean
	return type(v) == "table" and type(v.kind) == "string" and KINDS[v.kind] ~= nil
end

local encodeNode

-- value encodes a prop: functions become handler references, widgets
-- nodes, and tables lists or objects. A string under an `on…` key names
-- an action in the script's `actions` table.
local function value(v: any, where: string, key: string?): string
	local t = type(v)
	if t == "function" then
		return register(v)
	elseif t == "string" and key and string.match(key, "^on%u") then
		local action = plugin.actions and plugin.actions[v]
		if type(action) ~= "function" then
			error(where .. " names no action " .. quote(v), 0)
		end
		return register(action)
	elseif t == "string" or t == "boolean" then
		return scalar(v, where)
	elseif t == "number" then
		return number(v, where)
	elseif t == "table" then
		if isNode(v) then
			return encodeNode(v, where)
		end
		local parts = {}
		if isArray(v) then
			for i, item in v do
				table.insert(parts, value(item, where .. "[" .. i .. "]"))
			end
			return "[" .. table.concat(parts, ",") .. "]"
		end
		for k, item in v do
			if type(k) ~= "string" then
				error(where .. " mixes list items and named fields", 0)
			end
			table.insert(parts, quote(k) .. ":" .. value(item, where .. "." .. k, k))
		end
		return "{" .. table.concat(parts, ",") .. "}"
	end
	error(where .. " is a " .. t, 0)
end

function encodeNode(n: any, where: string): string
	if not isNode(n) then
		local kind = type(n) == "table" and n.kind
		if type(kind) == "string" then
			error(where .. " is an unknown widget " .. quote(kind), 0)
		end
		error(where .. " is not a widget; build one with a ui function", 0)
	end
	where = where .. "<" .. n.kind .. ">"
	local parts = { '"kind":' .. quote(n.kind) }
	local props, kids = {}, {}
	for k, v in n do
		if k == "kind" then
			continue
		elseif k == "key" then
			table.insert(parts, '"key":' .. quote(tostring(v)))
		elseif k == "children" then
			for i, child in v do
				table.insert(kids, encodeNode(child, where .. "[" .. i .. "]"))
			end
		elseif type(k) == "string" then
			table.insert(props, quote(k) .. ":" .. value(v, where .. "." .. k, k))
		else
			error(where .. " has a stray list item; put children in children", 0)
		end
	end
	table.insert(parts, '"props":{' .. table.concat(props, ",") .. "}")
	table.insert(parts, '"children":[' .. table.concat(kids, ",") .. "]")
	return "{" .. table.concat(parts, ",") .. "}"
end

-- constructor makes ui[kind]. Its arguments, in any order: a string or
-- number for the kind's leading prop (a text's text, a button's label), a
-- table of props, whose list items are children, and a widget or a list
-- of widgets as more children.
local function constructor(kind: string, lead: string)
	return function(...)
		local n: { [any]: any } = { kind = kind }
		local children = {}
		for i = 1, select("#", ...) do
			local a = select(i, ...)
			local t = type(a)
			if t == "string" or t == "number" then
				if lead == "" then
					error("ui." .. kind .. " takes no leading " .. t .. "; pass props", 2)
				end
				n[lead] = if t == "number" and lead ~= "value" then tostring(a) else a
			elseif isNode(a) then
				table.insert(children, a)
			elseif t == "table" then
				for k, v in a do
					if type(k) == "number" then
						table.insert(children, v)
					elseif k == "kind" then
						error("ui." .. kind .. " got a table of kind " .. tostring(v), 2)
					elseif k == "children" then
						for _, c in v do
							table.insert(children, c)
						end
					else
						n[k] = v
					end
				end
			elseif a ~= nil then
				error("ui." .. kind .. " got a " .. t, 2)
			end
		end
		n.children = children
		return n
	end
end

local api = {}
for kind, lead in KINDS do
	api[kind] = constructor(kind, lead)
end
ui = table.freeze(setmetatable(api, {
	__index = function(_, k)
		error("ui." .. tostring(k) .. " is not a widget", 2)
	end,
}))

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
		gen += 1
		handlers = {}
		return encodeNode(plugin.view(table.freeze(chunk())), "view()")
	end,
	act = function(chunk)
		local id, state, v = chunk()
		local fn = handlers[id]
		if string.match(id, "^%d+") ~= tostring(gen) then
			return "{}"
		end
		if type(fn) ~= "function" then
			error("no handler " .. quote(id), 0)
		end
		if type(v) == "table" then
			table.freeze(v)
		end
		return encodeState(fn(table.freeze(state), v) or {}, "handler")
	end,
}
