# Security policy

## Reporting a vulnerability

Report vulnerabilities privately. Do not open a public issue.

Use GitHub's private vulnerability reporting on this repository (Security tab → "Report a vulnerability"). Include what you found, how to reproduce it, and the version (`syntaxis version`).

You will get an acknowledgement within a few days, and a fix or mitigation plan once the issue is understood. Allow time for a fix before public disclosure.

## Scope

In scope: authentication and session handling, admin/chat separation, sandbox isolation settings, egress policy, secrets handling, approval gates, prompt-injection containment, the audit log.

Known limitations are listed in [docs/security.md](docs/security.md). They are not vulnerabilities by themselves, but improvements are welcome.

## Supported versions

While the project is pre-1.0, only the latest release receives security fixes.
