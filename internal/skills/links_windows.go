package skills

import (
	"os"
	"syscall"
)

// links is how many names the open file f has, as Windows counts them for
// its handle.
func links(f *os.File, _ os.FileInfo) (uint64, error) {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &info); err != nil {
		return 0, err
	}
	return uint64(info.NumberOfLinks), nil
}
