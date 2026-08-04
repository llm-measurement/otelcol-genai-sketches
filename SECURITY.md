# Security Policy

## Supported Versions

This project is alpha software. Security fixes are provided for the latest published
alpha release only.

## Reporting A Vulnerability

Please use
[GitHub private vulnerability reporting](https://github.com/llm-measurement/otelcol-genai-sketches/security/advisories/new).
Do not disclose a suspected vulnerability in a public issue.

Include the affected version or commit, impact, reproduction details, and any known
workaround. Reports will be acknowledged as soon as practical, then assessed and
coordinated privately through the advisory.

## Deployment Notes

- Supply `GENAI_SKETCH_SECRET` through a secret manager or protected environment, not
  through source-controlled configuration.
- Restrict collector-log access. Top-k snapshots contain stable keyed hashes while a
  secret remains active.
- Configure slices only from bounded, non-sensitive attributes because slice values
  are exported in cleartext.
- Bind diagnostic and metrics endpoints to trusted interfaces and apply normal
  network access controls outside local development.
- Build with Go 1.26.5 or newer so the generated collector includes current standard
  library security fixes.
- Pin published module and container versions, and review dependency updates before
  deployment.

Keyed hashes are pseudonymous and are not a substitute for access control,
encryption, data minimization, or differential privacy.
