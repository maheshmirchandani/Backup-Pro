# Security Policy

## Reporting a vulnerability

Email mahesh.mirchandani@gmail.com with details. Encrypted via PGP preferred (key in repo root: `mm-public.asc`, to be added).

90-day coordinated disclosure window. Severe data-loss vulnerabilities get a hot-patch release; lesser issues bundle into the next regular release.

## Threat model

FlashBackup is a personal/inner-circle tool. The USB device itself is the trust boundary.

**In scope:**
- Accidental data loss (move-mode deleting unverified files)
- Silent transfer corruption (source-vs-dest hash mismatch undetected)
- Mid-run source mutation causing inconsistent backup
- Profile pattern injection through rsync subprocess args
- Terminal escape sequence injection via crafted filenames

**Out of scope:**
- Cosmic-ray bit flips, RAM tampering
- Attacker with physical USB write access after backup completed (the USB itself is the trust boundary; manifests carry HMAC integrity checksums per spec invariant #33 to detect bit-rot, NOT to defend against an adversary)
- Network attacks (FlashBackup makes no network calls at runtime)
- Confidentiality of the USB contents (lost USB exposes mirrored files; recommend APFS-encrypted destination for sticks carried outside the home)

## Sev1 / Sev2 / Sev3 / Sev4

See spec Section "Service level objectives, error budgets, and incident classification."
