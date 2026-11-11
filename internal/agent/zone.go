package agent

// ianaZone is the IANA name for a Windows time zone ID, "" when the table does not know the ID or when daylight
// saving is switched off on the host: the clock then keeps standard time all year and no longer matches the zone the
// hub would plan with.
func ianaZone(windowsID string, daylightSavingOff bool) string {
	if daylightSavingOff {
		return ""
	}
	return windowsZones[windowsID]
}
