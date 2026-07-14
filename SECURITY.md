# Security Policy

## Supported versions

marquee is pre-1.0 and pre-alpha; it has not been released. Security fixes
land on the latest `main` and ship in the newest release. There is no
back-port channel before 1.0 — always run the newest build.

## Reporting a vulnerability

Please report vulnerabilities privately. Do not open a public issue for a
security problem.

The preferred channel is GitHub's private vulnerability reporting: on the
[repository](https://github.com/jellehuibregtse/marquee), go to the
**Security** tab and choose **Report a vulnerability**. This opens a private
GitHub Security Advisory visible only to you and the maintainers. This is the
only reporting channel — there is no security email.

In your report, please include:

- a description of the issue;
- steps to reproduce it;
- the impact you believe it has;
- the affected version or commit.

We aim to acknowledge a report within a few days. This is a good-faith
intention, not a guaranteed SLA.

## Scope

marquee is a local development tool. By default it binds a loopback address
and proxies to a local upstream; it terminates all browser traffic to the
wrapped app and injects a script into every page, so its guards are treated
as structural. The full threat model and the implemented mitigations live in
[docs/security.md](docs/security.md).

Out of scope / accepted residual risk, at a high level:

- **Exposing marquee to a network.** Doing so requires the explicit
  `--unsafe-listen` opt-in, which prints a persistent warning. Traffic
  reachable to network peers as a result of that opt-in is the operator's
  risk, not a vulnerability in the tool.
- **Same-user local-process attacks.** marquee is a single-user dev tool; an
  attacker who already runs code as the same local user is out of scope.

Reports that depend only on the above are unlikely to be treated as
vulnerabilities. When in doubt, report privately and let us decide together.

## Disclosure

We follow coordinated disclosure: report privately, and allow a reasonable
window to develop and ship a fix before any public disclosure. We will keep
you informed as the fix progresses.
