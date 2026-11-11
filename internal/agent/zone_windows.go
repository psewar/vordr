//go:build windows

package agent

import (
	"slices"

	"golang.org/x/sys/windows/registry"
)

// hostZone: Windows names its zone by a Windows ID in the registry ("W. Europe Standard Time"); the table from CLDR
// turns it into the IANA name the hub plans with. "" when the key is missing, the ID is unknown or daylight saving
// is switched off (then the clock no longer follows the zone's rules).
func hostZone() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\TimeZoneInformation`,
		registry.QUERY_VALUE|registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return ""
	}
	defer k.Close()
	names, err := k.ReadValueNames(0)
	if err != nil || !slices.Contains(names, "TimeZoneKeyName") {
		return ""
	}
	id, _, err := k.GetStringValue("TimeZoneKeyName")
	if err != nil {
		return ""
	}
	off := uint64(0) // the value is absent on hosts that never switched daylight saving off
	if slices.Contains(names, "DynamicDaylightTimeDisabled") {
		if off, _, err = k.GetIntegerValue("DynamicDaylightTimeDisabled"); err != nil {
			return ""
		}
	}
	return ianaZone(id, off != 0)
}
