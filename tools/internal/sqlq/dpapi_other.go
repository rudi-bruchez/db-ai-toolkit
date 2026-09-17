//go:build !windows

package sqlq

import (
	"fmt"
	"runtime"
)

// dpapiUnprotect cannot exist away from Windows. The refusal happens here, at
// the moment a profile's secret is resolved, and not in Validate: a file
// holding one DPAPI profile must stay usable for its integrated and
// passwordEnv profiles, and -list-profiles must still list.
func dpapiUnprotect([]byte) ([]byte, error) {
	return nil, fmt.Errorf(
		"DPAPI is a Windows mechanism and this is %s; a profile using \"passwordDpapi\" "+
			"cannot be used here. Other profiles in the file are unaffected", runtime.GOOS)
}
