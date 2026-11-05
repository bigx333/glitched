--!strict
-- A tour of the widgets a script can use. Every value here lives in the
-- host's state: the widgets bound to a key edit it, and the summary below
-- reads it back.
return {
	state = {
		name = "Ada", plan = "Pro", seats = 3, volume = 0.6, notify = true,
		tab = 0, theme = "#8b7cf6ff", tags = { "go", "luau" }, size = "m", notes = 0,
	},

	view = function(state)
		local summary = string.format(
			"%s · %s · %d seats · volume %d%% · %s · tags %s",
			state.name, state.plan, state.seats, math.round(state.volume * 100),
			if state.notify then "notifications on" else "notifications off",
			table.concat(state.tags, ", ")
		)
		return ui.column({ gap = 14 }, {
			ui.tabs({ key = "tabs", bind = "tab", labels = { "Account", "Preferences" } }),
			if state.tab == 0
				then ui.form({ key = "account" }, {
					ui.field("Name", { ui.textInput({ bind = "name" }) }),
					ui.field("Plan", { ui.select({ bind = "plan", options = { "Free", "Pro", "Team" } }) }),
					ui.field("Seats", { ui.numberInput({ bind = "seats", min = 1, max = 50, width = 120 }) }),
					ui.field("Size", {
						ui.radioGroup({ bind = "size" }, {
							ui.radio("Small", { value = "s" }), ui.radio("Medium", { value = "m" }), ui.radio("Large", { value = "l" }),
						}),
					}),
					ui.field("Tags", { ui.tokenField({ bind = "tags", suggestions = { "rust", "zig", "swift" } }) }),
				})
				else ui.form({ key = "prefs" }, {
					ui.field("Volume", { ui.slider({ bind = "volume", min = 0, max = 1 }) }),
					ui.field("Notifications", { ui.switch({ bind = "notify" }) }),
					ui.field("Accent", { ui.colorWell({ bind = "theme", width = 60 }) }),
				}),
			ui.row({ gap = 8, alignItems = "center" }, {
				ui.badge(state.plan),
				ui.progress(state.volume, { width = 120 }),
				ui.spacer(),
				ui.menuButton("Actions", {
					items = {
						{ label = "Reset seats", onChoose = function() return { seats = 1 } end },
						{ separator = true },
						{ label = "Upgrade to Team", onChoose = function() return { plan = "Team" } end },
					},
				}),
			}),
			ui.text(summary, { key = "summary", textColor = state.theme }),
		})
	end,
}
