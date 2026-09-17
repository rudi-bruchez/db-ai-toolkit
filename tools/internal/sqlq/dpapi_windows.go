//go:build windows

package sqlq

import (
	"fmt"
	"syscall"
	"unsafe"
)

// DPAPI through crypt32.dll, loaded lazily so that nothing new is added to the
// build. Current-user scope with no optional entropy, which is what SSMS used
// when it wrote the blob - measured against the real file, not assumed.

type dataBlob struct {
	cbData uint32
	pbData *byte
}

var (
	crypt32           = syscall.NewLazyDLL("crypt32.dll")
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	procUnprotectData = crypt32.NewProc("CryptUnprotectData")
	procLocalFree     = kernel32.NewProc("LocalFree")
)

func dpapiUnprotect(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty blob")
	}
	// Find before Call: LazyProc.Call panics when the entry point is missing,
	// and a panic here would take the JSON output with it.
	if err := procUnprotectData.Find(); err != nil {
		return nil, fmt.Errorf("CryptUnprotectData unavailable: %w", err)
	}

	in := dataBlob{cbData: uint32(len(data)), pbData: &data[0]}
	var out dataBlob
	ret, _, err := procUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0, // ppszDataDescr - not wanted
		0, // pOptionalEntropy - none was used
		0, // pvReserved
		0, // pPromptStruct
		0, // dwFlags
		uintptr(unsafe.Pointer(&out)),
	)
	if ret == 0 {
		return nil, err
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))

	clear := make([]byte, out.cbData)
	copy(clear, unsafe.Slice(out.pbData, out.cbData))
	return clear, nil
}
