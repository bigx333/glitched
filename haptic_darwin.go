package main

import (
	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// MyGo has no haptics, so the trackpad's are AppKit's own, called through
// the Objective-C runtime: NSHapticFeedbackManager's default performer
// plays the pattern macOS uses for a value stepping between levels. Only a
// Force Touch trackpad plays it, while a finger is on it; the Touch Bar
// has no haptic engine.

const (
	hapticGeneric     = 0 // NSHapticFeedbackPatternGeneric
	hapticLevelChange = 2 // NSHapticFeedbackPatternLevelChange
	hapticNow         = 1 // NSHapticFeedbackPerformanceTimeNow
)

var (
	selDefaultPerformer = objc.RegisterName("defaultPerformer")
	selPerform          = objc.RegisterName("performFeedbackPattern:performanceTime:")
	hapticManager       objc.Class
)

func init() {
	if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_GLOBAL|purego.RTLD_NOW); err == nil {
		hapticManager = objc.GetClass("NSHapticFeedbackManager")
	}
}

// haptic plays a level-change tick on the trackpad. It runs on the main
// thread, where the view runs.
func haptic() {
	if hapticManager == 0 {
		return
	}
	performer := objc.ID(hapticManager).Send(selDefaultPerformer)
	if performer != 0 {
		performer.Send(selPerform, hapticLevelChange, hapticNow)
	}
}

// hapticBump plays the generic pattern, a firmer bump, for something
// weighty: Full access granted.
func hapticBump() {
	if hapticManager == 0 {
		return
	}
	performer := objc.ID(hapticManager).Send(selDefaultPerformer)
	if performer != 0 {
		performer.Send(selPerform, hapticGeneric, hapticNow)
	}
}
