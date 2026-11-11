---
status: accepted
date: 2026-10-30
decision-makers: kay (close the gap), Claude (rule and threshold)
---

# Keep the OSV data when a new dump is empty or far smaller

## Context and Problem Statement

The hub vouches for every pip and npm update plan with its OSV data (ADR-0064, ADR-0070): an entry it does not flag
may be installed. Until now it took every dump it could read: `osv.Build`, then `Save`, with no look at the contents.
An empty or truncated publication, or one that lost a whole source, would have replaced the index. From then on the
hub would have signed "nothing flagged" for every plan, malware included, until the next good dump.

It came to light through a test. `TestPackageUpdateThroughTheHub`, run as a test binary outside its
package folder, found no test data, served an empty dump, and let a plan with the malware version debug 4.4.2 through.
The test was wrong. What it showed was the hub's real behaviour.

## Decision Drivers

* Fail closed: missing protection must stop updates, not wave them through.
* The daily changes of a dump must pass without anyone looking.
* No second source: the hub talks to nothing but the OSV bucket (ADR-0064).

## Considered Options

1. Leave it.
2. Refuse only a dump without entries.
3. Refuse a dump without entries, and one with less than 90 % of the entries or of the malware entries of the data in
   use.
4. Compare with a count from a second source.

## Decision Outcome

Chosen option: **3**.
* **The rule.** A refused dump does not replace the index. The hub keeps the old data and reports `implausible_dump`
  as the code of the last refresh: in its view, in the log with both counts, and in Home Assistant in the attributes
  of the "Schwachstellendaten" sensor.
* **Retries.** The hub tries again after an hour, like after any failed refresh.
* **When the data goes stale.** The last good data keeps its date. After `stale_hours` (72) it is stale, and stale
  data vouches for nothing: updates by button are then refused (design 11.4).
* **The threshold.** 90 % follows from what a dump does from day to day: it grows, and withdrawn advisories are few.
  The malware entries are counted separately, because losing them is losing the most important protection, and they
  are a small share of the whole.
* **The first dump** has nothing to compare with; for it only "not empty" applies.

### Consequences

* Good, because a broken publication can no longer make the hub vouch for everything; the old data keeps flagging
  what it flagged.
* Good, because the test helpers now stop when they find no test data, instead of testing against an empty dump.
* Bad, because a deliberate large cleanup at OSV is refused too, until someone looks. To accept it, delete
  `<state_dir>/osv/<ecosystem>.idx` and restart the hub; the next download then only has to be non-empty.
* Bad, because while a dump is refused, the hub downloads it again every hour (npm: about 220 MB each time).
* Bad, because a truncated **first** dump is not caught.

### Confirmation

* `TestImplausibleDumpKeepsTheData`:
  * empty first dump;
  * after good data: an empty dump, two GHSA entries fewer (11 of 13), and one malware entry fewer (6 of 7 malware)
    are refused. The old data keeps flagging debug 4.4.2, and there is no retry within the hour;
  * one GHSA entry fewer (12 of 13) and the full dump again are taken.
* A mutation probe, kept outside the repository: 8 of 8 mutations caught. They include each rule switched off,
  thresholds of 80 % and 95 %, and the check not called.

## Pros and Cons of the Options

### 1. Leave it

* Good, because nothing changes.
* Bad, because the protection fails open exactly when the data source breaks.

### 2. Only "not empty"

* Good, because it is trivial and never wrong.
* Bad, because a truncated dump, or one without its malware source, still passes.

### 3. Not empty, and at least 90 % of entries and of malware entries

* Good, because it catches empty, truncated and source-less dumps, and lets daily changes pass.
* Bad, because the threshold is a judgement; a real cleanup needs a person (see above).

### 4. A second source

* Good, because it could tell a real cleanup from a broken dump.
* Bad, because it means a second outside dependency for the hub, which ADR-0064 avoids.

## More Information

`internal/hub/packages.go` (`implausible`, `osvMinShare`); `design/architecture.md` 11.2.
