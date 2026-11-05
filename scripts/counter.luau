--!strict
-- The mygoscript demo: the host keeps `count`, this script draws it and
-- says what a click does to it.
return {
	state = { count = 0 },

	view = function(state)
		return ui.column({ key = "counter", gap = 12 }, {
			ui.text("Count: " .. state.count, { key = "count", size = 28 }),
			ui.row({ gap = 8 }, {
				ui.button("Increment", { key = "inc", on = "increment" }),
				ui.button("Reset", { key = "reset", on = "reset" }),
			}),
		})
	end,

	actions = {
		increment = function(state)
			return { count = state.count + 1 }
		end,
		reset = function()
			return { count = 0 }
		end,
	},
}
