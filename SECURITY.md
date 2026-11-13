# Security policy

## Reporting a vulnerability

Please report vulnerabilities **privately**. Do not open a public issue.

Use GitHub's private vulnerability reporting on this repository (Security tab → "Report a vulnerability"). Include what you found, how to reproduce it, and the version (`syntaxis version`).

You can expect an acknowledgement within a few days and a fix or a mitigation plan as soon as it is understood. Please allow time for a fix before public disclosure.

## Scope

In scope: authentication and session handling, admin/chat separation, sandbox isolation settings, egress policy, secrets handling, approval gates and prompt-injection containment, the audit log.

Known limitations are documented honestly in [docs/security.md](docs/security.md); they are not vulnerabilities by themselves, but improvements are welcome.

## Supported versions

Only the latest release receives security fixes while the project is pre-1.0.
