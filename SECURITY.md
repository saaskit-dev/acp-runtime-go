# Security Policy

## Reporting

If you discover a security issue, do not file a public issue first.

Report it privately to the maintainers through the repository owner contact path.

## Scope

Security-sensitive areas include:

- ACP transport handling
- permission and client-authority flows
- filesystem and terminal tool surfaces
- npm publishing credentials and release workflow

## Secrets

Do not commit credentials, tokens, OTP codes, or private registry configuration into the repository.

If a token has been exposed during development or release, revoke it and rotate it immediately.
