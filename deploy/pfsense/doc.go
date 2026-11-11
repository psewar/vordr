// Package pfsense holds no Go code: it is the home of the two shell scripts the proxy agent uses on the pfSense
// firewall (ADR-0091, ADR-0093) and of their tests, which run the scripts with /bin/sh against stubbed pfctl and pkg.
//
//   - vordr-gna: the forced command of the proxy's SSH key, run as the unprivileged user vordr-gna.
//   - vordr-gna-root: the root job, run by cron every minute; it works the queue and keeps the update window.
//   - vordr-gna-allow: the devices the window may open for.
package pfsense
