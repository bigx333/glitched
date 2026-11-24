//go:build unix

package skills

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// What the staging budget charges a tree covers what the filesystem gives
// it: a tree whose bytes fit but whose blocks don't is refused, and one
// taken takes no more blocks than it is charged.
func TestTheStagingBudgetCoversAllocatedBlocks(t *testing.T) {
	for _, limit := range []int{1024, 64 << 10} {
		node, _ := folders(t)
		store := NewStore(node)
		was := waitingLimit
		waitingLimit = limit
		t.Cleanup(func() { waitingLimit = was })
		p := put("plain", "readme", strings.Repeat("sample", 12))
		err := store.Put(t.Context(), p)
		var physical int64
		filepath.WalkDir(store.dir("incoming"), func(path string, _ os.DirEntry, err error) error {
			if f, err := os.Lstat(path); err == nil && path != store.dir("incoming") {
				physical += f.Sys().(*syscall.Stat_t).Blocks * 512
			}
			return nil
		})
		charged, _, _ := store.waiting()
		if physical > int64(waitingLimit) || physical > int64(charged) {
			t.Fatalf("budget=%d accepted a tree (%v) the budget charges %d of, which takes %d bytes of blocks", waitingLimit, err, charged, physical)
		}
		if limit > 1024 && err != nil {
			t.Errorf("budget=%d refused an ordinary tree: %v", limit, err)
		}
	}
}
