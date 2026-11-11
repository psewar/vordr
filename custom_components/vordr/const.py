"""Constants of the Vördr integration."""

from __future__ import annotations

from typing import Final

DOMAIN: Final = "vordr"
VERSION: Final = "0.33.4"  # keep in step with manifest.json

CONF_URL: Final = "url"
CONF_NAME: Final = "name"
CONF_CA_PEM: Final = "ca_pem"
CONF_PRIVATE_KEY: Final = "private_key"

DEFAULT_NAME: Final = "homeassistant"

# Events for automations (design/ha-integration.md, section 6).
EVENT_PROBLEM: Final = "vordr_problem"
EVENT_RUN_FINISHED: Final = "vordr_run_finished"

# Problem reasons besides the result codes, as internal/hub/view.go defines them.
REASONS: Final = (
    "offline",
    "stale",
    "check_failed",
    "reboot_pending",
    "no_report",
    "malicious_package",  # an installed pip/npm package is malware according to OSV
    "vulnerable_package",  # an installed pip/npm package has a known vulnerability
)
REASON_REBOOT_PENDING: Final = "reboot_pending"  # a reboot needed for longer than the hub's limit (ADR-0014)

# Phases while a job runs and result codes once it is done, as internal/protocol/codes.go defines them. The
# last-run sensor is an enum of exactly these.
# backing_up: an install waits for the fresh backup of its guest (ADR-0096).
PHASES: Final = ("queued", "prechecking", "downloading", "installing", "verifying", "rebooting", "backing_up")
CODES: Final = (
    "ok",
    "ok_reboot_required",
    "rejected_invalid_job",
    "rejected_scope_not_allowed",
    "rejected_outside_window",
    "rejected_rate_limited",
    "rejected_busy",
    "rejected_not_allowed",
    "rejected_unsupported",
    "precheck_disk_space",
    "precheck_package_db_broken",
    "precheck_repo_unreachable",
    "precheck_would_remove_packages",
    "precheck_version_flagged",
    "precheck_version_too_new",
    "precheck_vetting_unavailable",
    "precheck_not_from_registry",
    "failed_package_manager",
    "failed_verification",
    "failed_health",
    "failed_windows_update",
    "failed_target_auth",  # proxy instance: the appliance refused the service account
    "failed_target_tls",  # proxy instance: the appliance's certificate does not match
    "failed_target_hostkey",  # proxy instance over SSH: the appliance does not offer the pinned host key
    "failed_config_backup",  # proxy instance: the configuration backup failed
    "failed_backup",  # the backup right before the install did not come about; nothing was installed
    "timeout",
    "aborted",
    "interrupted",
)
