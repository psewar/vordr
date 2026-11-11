---
status: proposed
date: 2026-11-05
decision-makers: proposed by Claude (work kay asked for on 2026-11-04, "eigenständig umsetzen"); kay decides with the merge
---

# The Windows agent reports its time zone through the CLDR table

## Context and Problem Statement

The agent announces its `install.window` together with its time zone as an IANA name (`tz` in the policy summary). The
hub plans automatic installs with it ([ADR-0036](0036-automatic-install-in-rings.md)). Since
[ADR-0109](0109-the-install-button-installs-at-once.md) it also uses it to tell whether the window is closed when the
install button is pressed.

On Linux the agent reads the name from `/etc/timezone` or the `/etc/localtime` link. Windows has neither. It names
its zone by a Windows ID such as `Tokyo Standard Time`, so the Windows agent announced no zone at all. A Windows
host with a window therefore never installed automatically (`autoNoZone`). Outside its window, the button sent the
install without a reason, and the agent refused it with `rejected_outside_window`.

## Decision Drivers

* The agent reports its state as a fact ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)). The hub does not
  guess the zone from offsets.
* The wire keeps one kind of name (IANA), so the hub and older hubs need no change.
* Nothing extra runs on the host for this: no PowerShell call or network lookup at every hello.
* A wrong zone is worse than none. With no zone, the agent's own window check still holds.

## Considered Options

1. Embed the CLDR `windowsZones` mapping (territory 001) as a generated table. The agent reads the Windows ID from
   the registry and announces the mapped IANA name.
2. Ask PowerShell 7 at start (`TimeZoneInfo.TryConvertWindowsIdToIanaId`).
3. Announce the Windows ID, and let the hub map it.
4. Announce the current UTC offset instead of a zone.

## Decision Outcome

Chosen: option 1.

* `internal/agent/zone_table.go` holds the mapping. `tools/windows_zones.ps1` generates it from the ICU in .NET, which
  carries the CLDR data, and runs gofmt.
* The agent reads `TimeZoneKeyName` under `HKLM\SYSTEM\CurrentControlSet\Control\TimeZoneInformation`.
* The agent announces no zone when:
  * the ID is not in the table;
  * daylight saving is switched off (`DynamicDaylightTimeDisabled`). The clock then keeps standard time all year and
    no longer follows the zone's rules.
* The name is the territory's main zone, e.g. `Asia/Tokyo` for `Tokyo Standard Time`. CLDR maps each Windows
  zone so that the main zone has the same rules for the current era. The hub only needs the rules.

Rejected:

* **Option 2,** because it starts a process for a constant and depends on the PowerShell version on the host.
* **Option 3,** because it changes the protocol and puts a platform detail into the hub.
* **Option 4,** because an offset does not know daylight saving. The window would be wrong for half the year.

### Consequences

* Good, because Windows hosts with a window take part in automatic installs, and the button works outside the window
  there too.
* Good, because hub and protocol stay as they are. Only the agent changes.
* Bad, because the table can age. A new Windows zone stays without a name until the table is regenerated. The agent
  then announces no zone, as before this decision.
* Neutral: Home Assistant shows the main zone's name, which may name another city with the same rules.

### Confirmation

* `internal/agent/zone_test.go`:
  * `TestIanaZone` covers known IDs, an unknown one, and daylight saving switched off.
  * `TestWindowsZonesLoad` checks that every name in the table loads with the time zone data the hub embeds.
* `internal/agent/zone_windows_test.go`: `TestHostZoneMatchesTheClock` runs on Windows. The announced zone must have
  the clock's offset, now and half a year later.
