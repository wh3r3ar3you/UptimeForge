# Security policy

UptimeForge holds sensitive things: VPN share links, panel and bot tokens, and agents that run as services on your servers. Reports are welcome.

## Reporting a vulnerability

Please report privately through GitHub: **Security → Report a vulnerability** on this repository (a private security advisory). Do not open a public issue.

Include what you found, how to reproduce it, and the version (`uptimeforge-hub version` or the version shown in the UI). You will get an answer within a week; a fix for a confirmed issue is released as soon as it is ready, and you are credited unless you ask not to be.

## Supported versions

Only the latest release gets security fixes.

## Scope

In scope: the hub (API, UI, auth, public status pages), the agent and its install script, and the release artifacts.
Out of scope: issues that need an admin account or the admin token to begin with (admins can change everything by design), and denial of service by an attacker who already controls an agent token.
