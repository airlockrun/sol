# Security

## Reporting a vulnerability

**Preferred:** use GitHub's [Private Vulnerability Reporting](https://github.com/airlockrun/sol/security/advisories/new).

**Fallback:** email `security@airlock.run`.

**Don't** open a public issue for vulnerabilities.

## What's a vulnerability in sol

- A flaw in the sol library that, when used as documented, makes the embedding application vulnerable (memory corruption, panic on attacker-controlled input, parser confusion, sandbox escape, credential leakage, etc.).
- A flaw in the sol CLI that allows code execution beyond what the user's prompt and environment permit (e.g. a prompt that escapes the agent loop, exfiltrates credentials, or runs unauthorized commands).
- A defect in security-relevant code (tool execution gating, file access, MCP transport).

## What's not

- Bugs without a security impact — open a regular issue instead.
- Vulnerabilities in libraries that sol depends on — report to the upstream first; we'll bump once they patch.
- Bugs that are inherited from [opencode](https://github.com/anomalyco/opencode) and exist in their implementation as well — please **also** report to opencode. The fix will likely flow into sol once they ship.
- The sol CLI executing tools the user has authorized — that's the design, not a bug. Sandboxing for hostile inputs is the embedding application's responsibility.
- Misuse: the embedding application using sol in a way the docs warn against.

## What you can expect

- **Acknowledgment within 72 hours.**
- **Triage within 7 days.**
- **Fix targeted within 30 days for High/Critical, 90 days for Low/Medium.**
- Credit in the security advisory unless you ask to remain anonymous.

## Safe harbor

Good-faith research won't trigger legal action. Don't disclose publicly before we've patched (or 90 days, whichever first). Don't demand payment as a condition of disclosure.
