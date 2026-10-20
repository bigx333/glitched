package main

import "github.com/egoist/mygo"

// installMenu gives the app the menu bar of a Mac app, as MyGo's native
// example has one: the app menu, Thread with the app's commands, the Edit
// menu's roles (Undo, Cut, Copy, Paste, Select All, which the text inputs
// take), View with the panels and the theme, and Window. Their key
// equivalents are the app's shortcuts, which on macOS the menu takes
// before the view; the view's own shortcuts serve where there is none.
func (a *app) installMenu() {
	// The menu's clicks change the state through the window, as the
	// view reads it.
	do := func(fn func()) func(*mygo.MenuItem, *mygo.Window) {
		return func(*mygo.MenuItem, *mygo.Window) {
			if a.win != nil {
				a.win.Update(fn)
			}
		}
	}
	theme := func(i int) func(*mygo.MenuItem, *mygo.Window) {
		return do(func() { a.settings.theme = i })
	}
	mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu},
		{Label: "Thread", Submenu: []*mygo.MenuItem{
			{Label: "New Thread", Accelerator: "CmdOrCtrl+N", Click: do(a.newThread)},
			{Label: "Search…", Accelerator: "CmdOrCtrl+K", Click: do(a.openCommandPalette)},
			mygo.Separator(),
			{Label: "Attach Files…", Accelerator: "CmdOrCtrl+Shift+A", Click: do(a.pickFiles)},
			mygo.Separator(),
			{Label: "Settings…", Accelerator: "CmdOrCtrl+,", Click: do(func() { a.router.Push("/settings/general") })},
		}},
		{Role: mygo.RoleEditMenu},
		{Label: "View", Submenu: []*mygo.MenuItem{
			{Label: "Toggle Sidebar", Accelerator: "CmdOrCtrl+B", Click: do(func() { a.toggleSidebar(true) })},
			{Label: "Toggle Terminal", Accelerator: "CmdOrCtrl+J", Click: do(func() { a.showTerminal, a.panelsJump = !a.showTerminal, true })},
			{Label: "Toggle Diff", Accelerator: "CmdOrCtrl+D", Click: do(func() { a.showDiff, a.panelsJump = !a.showDiff, true })},
			mygo.Separator(),
			{Label: "Appearance", Submenu: []*mygo.MenuItem{
				{Label: "System", Type: mygo.MenuItemRadio, Checked: a.settings.theme == 0, Click: theme(0)},
				{Label: "Light", Type: mygo.MenuItemRadio, Checked: a.settings.theme == 1, Click: theme(1)},
				{Label: "Dark", Type: mygo.MenuItemRadio, Checked: a.settings.theme == 2, Click: theme(2)},
			}},
			mygo.Separator(),
			{Role: mygo.RoleToggleFullScreen},
		}},
		{Role: mygo.RoleWindowMenu},
	}))
}
