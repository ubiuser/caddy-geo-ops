# Security Policy

## Supported versions

This project follows [semantic versioning](https://semver.org/). Security fixes are
released against the **latest** published `vX.Y.Z` tag. Pin a specific version when you
build with `xcaddy` and upgrade to pick up fixes.

## Reporting a vulnerability

**Please do not open a public issue for security problems.**

Report privately via GitHub's [private vulnerability reporting](https://docs.github.com/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability)
(the **Security → Report a vulnerability** button on this repository), or by email to
**ubiuser@gmail.com**.

Please include:

- a description of the issue and its impact,
- steps to reproduce (or a proof of concept),
- affected version(s) and configuration.

You can expect an acknowledgement within a few days. Once a fix is ready we'll publish a
new release and a GitHub Security Advisory crediting the reporter (unless you prefer to
remain anonymous).

## Scope

This module exposes IP-geolocation data and reads MaxMind/DB-IP databases. Of particular
interest:

- client-IP derivation and trust of forwarding headers (spoofing),
- handling of untrusted database files,
- leakage of personal data (IPs, geolocation) — see the **Privacy / personal data**
  section in the README.
