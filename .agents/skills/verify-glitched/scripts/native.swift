// native is what accessibility can't do for glv: find a process's window
// for screencapture, and send real pointer events (right click, hover).
//
//   native winid <pid>              the CGWindowID of pid's largest window
//   native click <pid> <x> <y> [right]  click at global point x,y (AX coordinates)
//   native move <pid> <x> <y>           move the pointer there
//
// Pointer events land on whatever is on top at that point: glv brings pid
// forward first (ax.js front), and click and move refuse when another
// window still covers the point.
import CoreGraphics
import Foundation

let args = CommandLine.arguments
func fail(_ s: String) -> Never { FileHandle.standardError.write((s + "\n").data(using: .utf8)!); exit(1) }
guard args.count >= 2 else { fail("usage: native winid PID | click X Y [right] | move X Y") }

switch args[1] {
case "winid":
    guard args.count == 3, let pid = Int(args[2]) else { fail("usage: native winid PID") }
    let list = CGWindowListCopyWindowInfo([.optionOnScreenOnly], kCGNullWindowID) as? [[String: Any]] ?? []
    let mine = list.filter { ($0["kCGWindowOwnerPID"] as? Int) == pid && ($0["kCGWindowLayer"] as? Int) == 0 }
    let area = { (w: [String: Any]) -> Double in
        let b = w["kCGWindowBounds"] as? [String: Double] ?? [:]
        return (b["Width"] ?? 0) * (b["Height"] ?? 0)
    }
    guard let w = mine.max(by: { area($0) < area($1) }), let id = w["kCGWindowNumber"] as? Int else { fail("no window for pid \(pid)") }
    print(id)
case "click", "move":
    guard args.count >= 5, let pid = Int32(args[2]), let x = Double(args[3]), let y = Double(args[4]) else { fail("usage: native click PID X Y [right]") }
    let p = CGPoint(x: x, y: y)
    var top = 0
    for _ in 0..<20 {
        let list = CGWindowListCopyWindowInfo([.optionOnScreenOnly], kCGNullWindowID) as? [[String: Any]] ?? []
        let hit = list.first { w in
            guard (w["kCGWindowLayer"] as? Int) == 0, let b = w["kCGWindowBounds"] as? [String: Double] else { return false }
            return CGRect(x: b["X"] ?? 0, y: b["Y"] ?? 0, width: b["Width"] ?? 0, height: b["Height"] ?? 0).contains(p)
        }
        top = hit?["kCGWindowOwnerPID"] as? Int ?? 0
        if top == Int(pid) { break }
        usleep(20_000)
    }
    guard top == Int(pid) else { fail("pid \(top) covers \(x),\(y), not \(pid); not clicking") }
    let src = CGEventSource(stateID: .hidSystemState)
    CGEvent(mouseEventSource: src, mouseType: .mouseMoved, mouseCursorPosition: p, mouseButton: .left)?.post(tap: .cghidEventTap)
    if args[1] == "move" { break }
    usleep(30_000)
    let right = args.count > 5 && args[5] == "right"
    let (down, up, button): (CGEventType, CGEventType, CGMouseButton) = right ? (.rightMouseDown, .rightMouseUp, .right) : (.leftMouseDown, .leftMouseUp, .left)
    CGEvent(mouseEventSource: src, mouseType: down, mouseCursorPosition: p, mouseButton: button)?.post(tap: .cghidEventTap)
    usleep(40_000)
    CGEvent(mouseEventSource: src, mouseType: up, mouseCursorPosition: p, mouseButton: button)?.post(tap: .cghidEventTap)
default:
    fail("unknown command \(args[1])")
}
